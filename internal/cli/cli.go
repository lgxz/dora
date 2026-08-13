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
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/config"
	"github.com/lgxz/dora/internal/paths"
	"github.com/lgxz/dora/internal/progress"
	"github.com/lgxz/dora/internal/session"
	"github.com/lgxz/dora/internal/update"
	"github.com/lgxz/dora/model/openai"
	"github.com/lgxz/dora/model/openairesponses"
	"github.com/lgxz/dora/skill"
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
	modelName := flags.String("model", "", "override the configured model name")
	baseURL := flags.String("base-url", "", "override the configured model base URL")
	thinkingFlag := flags.String("thinking", "", "override the configured model thinking mode (off|minimal|low|medium|high)")
	visionFlag := flags.Bool("vision", false, "enable image understanding (requires a vision-capable model)")
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
	var commandSkillDirectories stringListFlag
	flags.Var(&commandSkillDirectories, "skills-dir", "add a skill parent directory (repeatable)")
	var imagePaths stringListFlag
	flags.Var(&imagePaths, "image", "attach an image file to the prompt (repeatable)")
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
	visionSet := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "vision" {
			visionSet = true
		}
	})
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
	if *modelName != "" {
		cfg.Model.Name = *modelName
	}
	if *baseURL != "" {
		cfg.Model.BaseURL = *baseURL
	}
	if *thinkingFlag != "" {
		switch *thinkingFlag {
		case "off", "minimal", "low", "medium", "high":
		default:
			return errors.New(`--thinking must be one of "off", "minimal", "low", "medium", "high"`)
		}
		value := *thinkingFlag
		cfg.Model.Thinking = &value
	}
	if visionSet {
		cfg.Model.Vision = *visionFlag
	}
	if maxRoundsSet {
		cfg.Agent.MaxRounds = maxRounds
	}
	if maxHistoryRoundsSet {
		cfg.Agent.MaxHistoryRounds = maxHistoryRounds
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
		reasoningEffort, thinking := mapChatThinking(cfg.Model)
		model, err = openai.New(openai.Config{
			BaseURL:           cfg.Model.BaseURL,
			APIKey:            cfg.Model.APIKey,
			Model:             cfg.Model.Name,
			HTTPClient:        streams.HTTPClient,
			ConnectTimeout:    time.Duration(cfg.Model.ConnectTimeoutSeconds) * time.Second,
			StreamIdleTimeout: time.Duration(cfg.Model.StreamIdleTimeoutSeconds) * time.Second,
			Timeout:           time.Duration(cfg.Model.TimeoutSeconds) * time.Second,
			MaxTokens:         cfg.Model.MaxTokens,
			Temperature:       cfg.Model.Temperature,
			ReasoningEffort:   reasoningEffort,
			Thinking:          thinking,
		})
	case "responses":
		reasoning := mapResponsesThinking(cfg.Model)
		model, err = openairesponses.New(openairesponses.Config{
			BaseURL:           cfg.Model.BaseURL,
			APIKey:            cfg.Model.APIKey,
			Model:             cfg.Model.Name,
			HTTPClient:        streams.HTTPClient,
			ConnectTimeout:    time.Duration(cfg.Model.ConnectTimeoutSeconds) * time.Second,
			StreamIdleTimeout: time.Duration(cfg.Model.StreamIdleTimeoutSeconds) * time.Second,
			Timeout:           time.Duration(cfg.Model.TimeoutSeconds) * time.Second,
			MaxTokens:         cfg.Model.MaxTokens,
			Temperature:       cfg.Model.Temperature,
			Reasoning:         reasoning,
		})
	}
	if err != nil {
		return err
	}
	var tools []dora.Tool
	if !*noSkills {
		additionalSkillDirectories := append([]string(nil), cfg.Skills.Directories...)
		additionalSkillDirectories = append(additionalSkillDirectories, commandSkillDirectories...)
		skillDirectories, err := configuredSkillDirectories(additionalSkillDirectories)
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
	commandTools, err := buildCommandTools(cfg.Tools, cfg.Model.Vision)
	if err != nil {
		return err
	}
	tools = append(tools, commandTools...)

	agent, err := dora.NewWithConfig(model, dora.AgentConfig{
		MaxRounds:        cfg.Agent.MaxRounds,
		MaxHistoryRounds: cfg.Agent.MaxHistoryRounds,
		ContextWindow:    cfg.Agent.ContextWindow,
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
	if len(imagePaths) > 0 && !cfg.Model.Vision {
		return errors.New("--image requires a vision-capable model; enable it with --vision or model.vision: true")
	}
	imageRefs := make([]dora.Image, 0, len(imagePaths))
	for _, path := range imagePaths {
		imageRefs = append(imageRefs, dora.Image{Path: path})
	}
	messages = append(messages, dora.Message{Role: dora.RoleUser, Content: prompt, Images: imageRefs})
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

// mapChatThinking maps the config model.thinking value onto the Chat
// Completions reasoning_effort and thinking controls according to the provider
// policy. Values a provider does not support are ignored (not sent).
func mapChatThinking(model config.Model) (*string, *openai.ThinkingControl) {
	if model.Thinking == nil {
		return nil, nil
	}
	value := *model.Thinking
	if value == "off" {
		switch model.Provider {
		case "deepseek":
			return nil, openai.NewThinkingControl("disabled")
		default:
			// openai/trust do not support off on Chat Completions; ignore.
			return nil, nil
		}
	}
	if value == "minimal" && model.Provider == "deepseek" {
		// DeepSeek does not support minimal on Chat Completions; ignore.
		return nil, nil
	}
	return &value, nil
}

// mapResponsesThinking maps the config model.thinking value onto the Responses
// API reasoning control according to the provider policy. Values a provider
// does not support are ignored (not sent).
func mapResponsesThinking(model config.Model) *openairesponses.ReasoningControl {
	if model.Thinking == nil {
		return nil
	}
	value := *model.Thinking
	if value == "off" {
		return openairesponses.NewReasoningControl("none")
	}
	if value == "minimal" && model.Provider == "deepseek" {
		// DeepSeek does not support minimal on the Responses API; ignore.
		return nil
	}
	return openairesponses.NewReasoningControl(value)
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

type stringListFlag []string

func (values *stringListFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func configuredSkillDirectories(additional []string) ([]string, error) {
	defaultDirectory, err := paths.SkillsDir()
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
