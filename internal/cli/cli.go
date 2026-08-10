package cli

import (
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

	"dora"
	"dora/internal/config"
	"dora/internal/paths"
	"dora/internal/progress"
	"dora/internal/session"
	"dora/model/openai"
	"dora/model/openairesponses"
	"dora/skill"
	bashtool "dora/tool/bash"
	powershelltool "dora/tool/powershell"
)

const maxStdinBytes = 16 << 20

// IO contains the command's input and output streams.
type IO struct {
	Stdin            io.Reader
	Stdout           io.Writer
	Stderr           io.Writer
	StdinIsTerminal  bool
	TerminalProgress bool
	ColorProgress    bool
	HTTPClient       *http.Client
	SessionDir       string
}

// Run executes the dora command.
func Run(ctx context.Context, args []string, streams IO) error {
	flags := flag.NewFlagSet("dora", flag.ContinueOnError)
	flags.SetOutput(streams.Stderr)
	configPath := flags.String("config", "", "path to YAML configuration")
	modelName := flags.String("model", "", "override the configured model name")
	baseURL := flags.String("base-url", "", "override the configured model base URL")
	var quiet bool
	flags.BoolVar(&quiet, "quiet", false, "hide run progress")
	flags.BoolVar(&quiet, "q", false, "hide run progress (shorthand)")
	var sessionName string
	flags.StringVar(&sessionName, "session", "", "continue a named session")
	flags.StringVar(&sessionName, "s", "", "continue a named session (shorthand)")
	fresh := flags.Bool("fresh", false, "start fresh and replace the named session on success")
	flags.Usage = func() {
		fmt.Fprintf(streams.Stderr, "Usage: dora [options] <prompt>\n")
		fmt.Fprintf(streams.Stderr, "       command | dora [options] [instruction]\n\nOptions:\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *configPath == "" {
		defaultConfig, err := paths.ConfigFile()
		if err != nil {
			return err
		}
		*configPath = defaultConfig
	}
	if *fresh && sessionName == "" {
		return errors.New("--fresh requires --session or -s")
	}

	prompt, err := readPrompt(flags.Args(), streams.Stdin, streams.StdinIsTerminal)
	if err != nil {
		flags.Usage()
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if *modelName != "" {
		cfg.Model.Name = *modelName
	}
	if *baseURL != "" {
		cfg.Model.BaseURL = *baseURL
	}
	backend := session.Backend{
		Provider: cfg.Model.Provider,
		API:      cfg.Model.API,
		Model:    cfg.Model.Name,
		BaseURL:  strings.TrimRight(cfg.Model.BaseURL, "/"),
	}

	var sessionStore *session.Store
	var snapshot session.Snapshot
	if sessionName != "" {
		sessionDir := streams.SessionDir
		if sessionDir == "" {
			sessionDir, err = paths.SessionsDir()
			if err != nil {
				return err
			}
		}
		sessionStore, err = session.New(sessionDir)
		if err != nil {
			return err
		}
		snapshot, err = sessionStore.Load(sessionName)
		if *fresh && errors.Is(err, session.ErrUnsupportedVersion) {
			revision, revisionErr := sessionStore.Revision(sessionName)
			if revisionErr != nil {
				return revisionErr
			}
			snapshot = session.Snapshot{Revision: revision}
			err = nil
		}
		if err != nil {
			return err
		}
		if snapshot.Revision > 0 && !*fresh && snapshot.Backend != backend {
			return fmt.Errorf(
				"session %q belongs to %s %s model %q at %s; use --fresh to replace it",
				sessionName,
				snapshot.Backend.Provider,
				snapshot.Backend.API,
				snapshot.Backend.Model,
				snapshot.Backend.BaseURL,
			)
		}
	}

	var model dora.Model
	switch cfg.Model.API {
	case "chat_completions":
		model, err = openai.New(openai.Config{
			BaseURL:    cfg.Model.BaseURL,
			APIKey:     cfg.Model.APIKey,
			Model:      cfg.Model.Name,
			HTTPClient: streams.HTTPClient,
		})
	case "responses":
		model, err = openairesponses.New(openairesponses.Config{
			BaseURL:    cfg.Model.BaseURL,
			APIKey:     cfg.Model.APIKey,
			Model:      cfg.Model.Name,
			HTTPClient: streams.HTTPClient,
		})
	}
	if err != nil {
		return err
	}
	var tools []dora.Tool
	skillDirectories, err := configuredSkillDirectories(*configPath, cfg.Skills.Directories)
	if err != nil {
		return err
	}
	if len(skillDirectories) > 0 {
		skills, err := skill.New(skill.Config{Directories: skillDirectories})
		if errors.Is(err, skill.ErrNoSkills) {
			skills = nil
			err = nil
		}
		if err != nil {
			return err
		}
		if skills != nil {
			tools = append(tools, skills)
		}
	}
	if cfg.Tools.Bash.Enabled {
		bash, err := bashtool.New(bashtool.Config{
			Timeout: time.Duration(cfg.Tools.Bash.TimeoutSeconds) * time.Second,
		})
		if errors.Is(err, bashtool.ErrUnavailable) {
			bash = nil
			err = nil
		}
		if err != nil {
			return err
		}
		if bash != nil {
			tools = append(tools, bash)
		}
	}
	if cfg.Tools.PowerShell.Enabled {
		powershell, err := powershelltool.New(powershelltool.Config{
			Timeout: time.Duration(cfg.Tools.PowerShell.TimeoutSeconds) * time.Second,
		})
		if errors.Is(err, powershelltool.ErrUnavailable) {
			powershell = nil
			err = nil
		}
		if err != nil {
			return err
		}
		if powershell != nil {
			tools = append(tools, powershell)
		}
	}

	agent, err := dora.NewWithConfig(model, dora.AgentConfig{
		MaxModelCalls: cfg.Agent.MaxModelCalls,
	}, tools...)
	if err != nil {
		return err
	}
	var messages []dora.Message
	var continuation string
	if !*fresh {
		messages = append(messages, snapshot.Messages...)
		continuation = snapshot.Continuation
	}
	messages = append(messages, dora.Message{Role: dora.RoleUser, Content: prompt})
	state := dora.State{Messages: messages, Continuation: continuation}
	var result dora.Result
	if quiet {
		result, err = agent.RunState(ctx, state)
	} else {
		renderer := progress.New(streams.Stderr, streams.TerminalProgress, streams.ColorProgress)
		if sessionName != "" {
			if *fresh && snapshot.Revision > 0 {
				renderer.FreshSession(sessionName)
			} else {
				renderer.Session(sessionName, snapshot.Revision > 0)
			}
		}
		result, err = agent.RunStateObserved(ctx, state, renderer)
	}
	if err != nil {
		return err
	}

	var saveErr error
	if sessionStore != nil {
		saveErr = sessionStore.Save(sessionName, snapshot.Revision, session.Snapshot{
			Backend:      backend,
			Messages:     result.Messages,
			Continuation: result.Continuation,
		})
	}
	_, outputErr := fmt.Fprintln(streams.Stdout, result.Content)
	if saveErr != nil {
		return saveErr
	}
	return outputErr
}

func configuredSkillDirectories(configPath string, additional []string) ([]string, error) {
	defaultDirectory, err := paths.SkillsDir(configPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(defaultDirectory)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect default skill directory %q: %w", defaultDirectory, err)
	}

	directories := make([]string, 0, len(additional)+1)
	seen := make(map[string]struct{}, len(additional)+1)
	if err == nil {
		if !info.IsDir() {
			return nil, fmt.Errorf("default skill path %q is not a directory", defaultDirectory)
		}
		directories = append(directories, defaultDirectory)
		seen[defaultDirectory] = struct{}{}
	}
	for _, directory := range additional {
		absolute, err := filepath.Abs(directory)
		if err != nil {
			return nil, fmt.Errorf("resolve skill directory %q: %w", directory, err)
		}
		if _, exists := seen[absolute]; exists {
			continue
		}
		seen[absolute] = struct{}{}
		directories = append(directories, absolute)
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
