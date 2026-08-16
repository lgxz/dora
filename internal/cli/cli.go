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
	"strconv"
	"strings"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/internal/paths"
	"github.com/lgxz/dora/internal/progress"
	"github.com/lgxz/dora/internal/session"
	"github.com/lgxz/dora/internal/update"
	"github.com/lgxz/dora/model/registry"
	"github.com/lgxz/dora/skill"
	jobtool "github.com/lgxz/dora/tool/job"
)

const maxStdinBytes = 16 << 20

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
	SessionDir       string
	Updater          updater
}

// Run executes the dora command.
func Run(ctx context.Context, args []string, streams IO) error {
	flags := flag.NewFlagSet("dora", flag.ContinueOnError)
	flags.SetOutput(streams.Stderr)
	configPath := flags.String("config", "", "path to YAML configuration")
	thinkingFlag := flags.String("thinking", "", "override the configured model thinking mode (off|minimal|low|medium|high)")
	var maxRounds int
	var maxRoundsSet bool
	flags.Func("max-rounds", "override the maximum model-tool rounds per segment", func(value string) error {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return errors.New("must be a positive integer")
		}
		maxRounds = parsed
		maxRoundsSet = true
		return nil
	})
	var maxHistoryRounds int
	var maxHistoryRoundsSet bool
	flags.Func("max-history-rounds", "override the number of recent rounds sent to the model each iteration (0 disables compaction)", func(value string) error {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 0 {
			return errors.New("must be a non-negative integer")
		}
		maxHistoryRounds = parsed
		maxHistoryRoundsSet = true
		return nil
	})
	showVersion := flags.Bool("version", false, "print version information")
	performUpdate := flags.Bool("update", false, "update a standalone installation")
	forceUpdate := flags.Bool("force", false, "force update, bypassing the standalone-install marker and version checks")
	noSkills := flags.Bool("no-skills", false, "disable all skills")
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
	if *showVersion {
		version := streams.Version
		if version == "" {
			version = "dora dev (commit none, built unknown)"
		}
		_, err := fmt.Fprintln(streams.Stdout, version)
		return err
	}
	if *performUpdate {
		if len(flags.Args()) != 0 {
			return errors.New("-update does not accept a prompt")
		}
		updater := streams.Updater
		if updater == nil {
			updater = update.New(update.Config{
				CurrentVersion: streams.BuildVersion,
				HTTPClient:     streams.HTTPClient,
				Force:          *forceUpdate,
			})
		}
		if _, err := fmt.Fprintln(streams.Stderr, "dora: checking for updates"); err != nil {
			return err
		}
		result, err := updater.Update(ctx)
		if err != nil {
			return err
		}
		if result.Updated {
			_, err = fmt.Fprintf(streams.Stdout, "Updated dora %s -> %s\n", result.Current, result.Latest)
		} else {
			_, err = fmt.Fprintf(streams.Stdout, "dora %s is already up to date\n", result.Current)
		}
		return err
	}
	configExplicit := *configPath != ""
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
	if !configExplicit && errors.Is(err, os.ErrNotExist) {
		cfg, err = config.Default()
	}
	if err != nil {
		return err
	}
	reg, err := registry.New(registryFromConfig(cfg, streams.HTTPClient))
	if err != nil {
		return err
	}
	if *thinkingFlag != "" {
		switch *thinkingFlag {
		case "off", "minimal", "low", "medium", "high":
		default:
			return errors.New(`--thinking must be one of "off", "minimal", "low", "medium", "high"`)
		}
		value := *thinkingFlag
		reg.SetThinking(&value)
	}
	if maxRoundsSet {
		cfg.Agent.MaxRounds = maxRounds
	}
	if maxHistoryRoundsSet {
		cfg.Agent.MaxHistoryRounds = &maxHistoryRounds
	}
	sel := reg.Selection()
	backend := session.Backend{
		Provider: sel.Provider,
		Profile:  sel.Profile,
		API:      sel.API,
		Model:    sel.Model,
		BaseURL:  sel.BaseURL,
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
				"session %q belongs to %s profile %q (%s model %q at %s); use --fresh to replace it",
				sessionName,
				snapshot.Backend.Provider,
				snapshot.Backend.Profile,
				snapshot.Backend.API,
				snapshot.Backend.Model,
				snapshot.Backend.BaseURL,
			)
		}
	}

	model, err := reg.Model()
	if err != nil {
		return err
	}
	jobManager := job.New()
	var tools []dora.Tool
	if !*noSkills {
		skillDirectories, err := configuredSkillDirectories(cfg.Skills.Directories)
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
	}
	commandTools, err := buildCommandTools(cfg.Tools, sel.Vision, jobManager)
	if err != nil {
		return err
	}
	tools = append(tools, commandTools...)

	// The job tool is a regular tool like the others.
	jobTool := jobtool.New(jobManager, sel.Vision)
	tools = append(tools, jobTool)

	// File tools (read/write/edit) for precise file operations.
	tools = append(tools, buildFileTools()...)

	agent, err := dora.NewWithConfig(model, dora.AgentConfig{
		MaxRounds:        cfg.Agent.MaxRounds,
		MaxHistoryRounds: cfg.Agent.MaxHistoryRounds,
		ContextWindow:    sel.ContextWindow,
	}, tools...)
	if err != nil {
		return err
	}
	defer jobManager.Cleanup()
	var messages []dora.Message
	var continuation string
	if !*fresh {
		messages = append(messages, snapshot.Messages...)
		continuation = snapshot.Continuation
	}
	// Inject the system prompt (config override or built-in default) before
	// the user prompt. When resuming a session, the snapshot already contains
	// the system message, so only inject it for a fresh session.
	if len(messages) == 0 {
		systemPrompt := defaultSystemPrompt
		if cfg.Agent.SystemPrompt != "" {
			systemPrompt = cfg.Agent.SystemPrompt
		}
		messages = append(messages, dora.Message{Role: dora.RoleSystem, Content: systemPrompt})
	}
	messages = append(messages, dora.Message{Role: dora.RoleUser, Content: prompt})
	state := dora.State{Messages: messages, Continuation: continuation}
	var result dora.Result
	var observer dora.Observer
	if !quiet {
		renderer := progress.New(streams.Stderr, streams.TerminalProgress, streams.ColorProgress)
		if sessionName != "" {
			if *fresh && snapshot.Revision > 0 {
				renderer.FreshSession(sessionName)
			} else {
				renderer.Session(sessionName, snapshot.Revision > 0)
			}
		}
		observer = renderer
	}
	input := bufio.NewReader(streams.Stdin)
	completed := false
	for {
		result, err = agent.RunStateObserved(ctx, state, observer)
		if err == nil {
			completed = true
			break
		}
		if !errors.Is(err, dora.ErrMaxRounds) ||
			!streams.StdinIsTerminal || !streams.TerminalProgress {
			return err
		}
		state = dora.State{Messages: result.Messages, Continuation: result.Continuation}
		keepGoing, promptErr := confirmContinue(input, streams.Stderr)
		if promptErr != nil {
			return promptErr
		}
		if !keepGoing {
			break
		}
	}

	var saveErr error
	if sessionStore != nil {
		saveErr = sessionStore.Save(sessionName, snapshot.Revision, session.Snapshot{
			Backend:      backend,
			Messages:     result.Messages,
			Continuation: result.Continuation,
		})
	}
	var outputErr error
	if completed {
		outputErr = writeAnswer(streams, result.Content)
	}
	if saveErr != nil {
		return saveErr
	}
	return outputErr
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
		models := make([]registry.ModelConfig, len(p.Models))
		for j, m := range p.Models {
			models[j] = registry.ModelConfig{
				Name:          m.Name,
				Model:         m.Model,
				API:           m.API,
				Thinking:      m.Thinking,
				MaxTokens:     m.MaxTokens,
				ContextWindow: m.ContextWindow,
				Temperature:   m.Temperature,
				Vision:        m.Vision,
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
			Models:                   models,
		}
	}
	return registry.Config{
		Providers:        providers,
		SelectedProvider: cfg.Client.Provider,
		SelectedProfile:  cfg.Client.Profile,
	}
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
