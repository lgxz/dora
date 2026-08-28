package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/events"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/internal/paths"
	"github.com/lgxz/dora/internal/update"
	"github.com/lgxz/dora/model/registry"
)

const maxStdinBytes = 16 << 20
const sessionCommitTimeout = 5 * time.Second

type updater interface {
	Update(context.Context) (update.Result, error)
}

// IO contains the command's input and output streams.
type IO struct {
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	StdoutIsTerminal bool
	TerminalWidth    int
	ColorOutput      bool
	DarkBackground   bool
	Version          string
	BuildVersion     string
	StdinIsTerminal  bool
	TerminalProgress bool
	ColorProgress    bool
	HTTPClient       *http.Client
	Updater          updater
}

// Run executes the dora command.
func Run(ctx context.Context, args []string, streams IO) error {
	opts, err := parseOptions(args, streams.Stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if handled, err := handleImmediate(ctx, opts, streams); handled {
		return err
	}

	cfg, model, err := loadRuntimeConfig(opts, streams.HTTPClient)
	if err != nil {
		return err
	}
	workdir, err := resolveWorkingDirectory(opts.workdir)
	if err != nil {
		return err
	}

	// --events forces event daemon mode regardless of events.enabled.
	if opts.events {
		cfg.Events.Enabled = true
	}

	// Open the event source from the events configuration. Enabled reports
	// whether to run in event daemon mode.
	source, err := events.New(cfg.Events)
	if err != nil {
		return err
	}
	defer source.Close()
	serverMode := source.Enabled()

	var prompt string
	if !serverMode {
		prompt, err = readPrompt(opts.promptArgs, streams.Stdin, streams.StdinIsTerminal)
		if err != nil {
			opts.usage()
			return err
		}
	}

	sessionStore, err := openSession(ctx, opts.sessionPath)
	if err != nil {
		return err
	}
	defer sessionStore.Close()
	jobManager := job.New()
	extraTools, err := buildEventTools(source)
	if err != nil {
		return err
	}
	agent, err := buildAgent(cfg, agentDependencies{
		model:        model,
		jobs:         jobManager,
		history:      sessionStore,
		noSkills:     opts.noSkills,
		extraTools:   extraTools,
		systemPrompt: systemPrompt(cfg.Agent),
	})
	if err != nil {
		return err
	}
	observer := buildObserver(streams, opts.quiet, opts.reasoning, opts.color, opts.sessionPath)
	if observer != nil {
		selection := model.TextSelection()
		observer.Model(selection.Provider, selection.Profile, selection.Thinking)
	}
	// Command jobs are external processes and may outlive Dora; Task jobs are
	// in-process and are lost on exit. Silenced by --quiet.
	defer func() {
		commands, tasks := jobManager.ActiveCounts()
		if commands > 0 {
			info(observer, "background jobs are still running; they keep running after dora exits")
		}
		if tasks > 0 {
			info(observer, "background tasks are still running; they stop and lose their results when dora exits")
		}
	}()

	for {
		if serverMode {
			ev, err := source.Next(ctx)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return nil
				}
				info(observer, "receive event: %v", err)
				continue
			}
			// An event with no meaningful content does not start a turn.
			if ev.Empty() {
				continue
			}
			prompt = events.EventPrompt(ev)
		}

		if observer != nil {
			observer.Observe(dora.Update{Kind: dora.UpdateTurnStarted, Info: prompt})
		}
		turn := dora.NewTurn(prompt)
		outcome, err := runTurn(ctx, agent, turn, observer, streams, dora.RunOptions{
			WorkingDirectory: workdir,
		})
		if outcome.maxRoundsErr != nil && sessionStore != nil {
			if _, commitErr := sessionStore.CommitMaxRounds(ctx, turn, outcome.maxRoundsErr); commitErr != nil {
				return commitErr
			}
		}
		if err != nil {
			if outcome.maxRoundsErr == nil && sessionStore != nil {
				commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(ctx), sessionCommitTimeout)
				var commitErr error
				if errors.Is(err, context.Canceled) {
					_, commitErr = sessionStore.CommitCanceled(commitCtx, turn, err)
				} else {
					_, commitErr = sessionStore.CommitFailed(commitCtx, turn, err)
				}
				cancelCommit()
				if commitErr != nil {
					return commitErr
				}
			}
			if serverMode {
				info(observer, "run turn: %v", err)
				continue
			}
			return err
		}
		if !serverMode && !outcome.completed {
			return nil
		}
		if outcome.completed && sessionStore != nil {
			if _, err := sessionStore.CommitTurn(ctx, turn); err != nil {
				return err
			}
		}
		result, _ := turn.Result()
		if err := writeAnswer(streams, result); err != nil {
			return err
		}

		if !serverMode {
			return nil
		}
	}
}

func resolveWorkingDirectory(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve working directory %q: %w", path, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect working directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("working directory %q is not a directory", path)
	}
	return abs, nil
}

func writeAnswer(streams IO, content string) error {
	_, err := fmt.Fprintln(streams.Stdout, content)
	return err
}

// registryFromConfig converts the resolved provider catalog into the
// registry's neutral input types.
func registryFromConfig(cfg config.Config, httpClient *http.Client) registry.Config {
	providers := make([]registry.ProviderConfig, len(cfg.Providers))
	for i, p := range cfg.Providers {
		profiles := make([]registry.Profile, len(p.Profiles))
		for j, m := range p.Profiles {
			profiles[j] = registry.Profile{
				Name:             m.Name,
				Model:            m.Model,
				API:              m.API,
				Thinking:         m.Thinking,
				PreserveThinking: m.PreserveThinking,
				MaxTokens:        m.MaxTokens,
				MaxOutputTokens:  m.MaxOutputTokens,
				ContextWindow:    m.ContextWindow,
				Temperature:      m.Temperature,
				Capabilities:     m.Capabilities,
			}
		}
		providers[i] = registry.ProviderConfig{
			Name:                     p.Name,
			BaseURL:                  p.BaseURL,
			APIKey:                   p.APIKey,
			API:                      p.API,
			TimeoutSeconds:           p.TimeoutSeconds,
			ConnectTimeoutSeconds:    p.ConnectTimeoutSeconds,
			StreamIdleTimeoutSeconds: p.StreamIdleTimeoutSeconds,
			HTTPClient:               httpClient,
			Profiles:                 profiles,
		}
	}
	return registry.Config{Providers: providers}
}

func confirmContinue(input *bufio.Reader, output io.Writer) (bool, error) {
	for {
		if _, err := fmt.Fprint(output, "dora: maximum rounds reached; continue? [y/N] "); err != nil {
			return false, err
		}
		answer, err := input.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, fmt.Errorf("read continuation response: %w", err)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "y", "yes":
			return true, nil
		case "", "n", "no":
			return false, nil
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if _, err := fmt.Fprintln(output, "Please answer yes or no."); err != nil {
			return false, err
		}
	}
}

// configuredSkillDirectories resolves the skill parent directories to load.
// When nothing is configured, it uses the dora home skill directory followed
// by ~/.agents/skills/ (silently skipping any that do not exist). When any
// directories are configured, only those are used. Configured paths must be
// absolute or start with ~/; all resulting paths are deduplicated.
func configuredSkillDirectories(configured []string) ([]string, error) {
	// resolveDir validates a user-supplied path and expands a leading ~ home.
	resolveDir := func(directory string) (string, error) {
		if strings.HasPrefix(directory, "~") {
			if directory != "~" && !strings.HasPrefix(directory, "~/") {
				return "", fmt.Errorf("skill directory %q must be an absolute path or start with ~/", directory)
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("find home directory for skill directory %q: %w", directory, err)
			}
			if directory == "~" {
				return home, nil
			}
			return filepath.Join(home, directory[2:]), nil
		}
		if !filepath.IsAbs(directory) {
			return "", fmt.Errorf("skill directory %q must be an absolute path or start with ~/", directory)
		}
		return filepath.Clean(directory), nil
	}

	if len(configured) == 0 {
		directories := make([]string, 0, 2)
		seen := make(map[string]struct{})
		addDefaultIfExists := func(directory string) error {
			info, err := os.Stat(directory)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return fmt.Errorf("inspect skill directory %q: %w", directory, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("skill path %q is not a directory", directory)
			}
			directory = filepath.Clean(directory)
			if _, exists := seen[directory]; exists {
				return nil
			}
			seen[directory] = struct{}{}
			directories = append(directories, directory)
			return nil
		}
		defaultDirectory, err := paths.SkillsDir()
		if err != nil {
			return nil, err
		}
		if err := addDefaultIfExists(defaultDirectory); err != nil {
			return nil, err
		}
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("find home directory: %w", err)
		}
		if err := addDefaultIfExists(filepath.Join(home, ".agents", "skills")); err != nil {
			return nil, err
		}
		return directories, nil
	}

	directories := make([]string, 0, len(configured))
	seen := make(map[string]struct{}, len(configured))
	for _, directory := range configured {
		resolved, err := resolveDir(directory)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[resolved]; exists {
			continue
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("inspect skill directory %q: %w", resolved, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("skill path %q is not a directory", resolved)
		}
		seen[resolved] = struct{}{}
		directories = append(directories, resolved)
	}
	return directories, nil
}

func readPrompt(args []string, stdin io.Reader, stdinIsTerminal bool) (string, error) {
	instruction := strings.TrimSpace(strings.Join(args, " "))
	if stdinIsTerminal {
		if instruction == "" {
			return "", errors.New("prompt is required")
		}
		return instruction, nil
	}

	input, err := io.ReadAll(io.LimitReader(stdin, maxStdinBytes+1))
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	if len(input) > maxStdinBytes {
		return "", errors.New("stdin exceeds 16 MiB")
	}
	piped := strings.TrimSpace(string(input))
	switch {
	case instruction != "" && piped != "":
		return instruction + "\n\n" + piped, nil
	case instruction != "":
		return instruction, nil
	case piped != "":
		return piped, nil
	default:
		return "", errors.New("prompt is required")
	}
}
