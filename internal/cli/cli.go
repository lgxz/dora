package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"dora"
	"dora/internal/config"
	"dora/internal/progress"
	"dora/internal/session"
	"dora/model/openai"
	"dora/model/openairesponses"
	bashtool "dora/tool/bash"
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
	defaultConfig, err := config.DefaultPath()
	if err != nil {
		return err
	}
	configPath := flags.String("config", defaultConfig, "path to YAML configuration")
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

	var sessionStore *session.Store
	var snapshot session.Snapshot
	if sessionName != "" {
		sessionDir := streams.SessionDir
		if sessionDir == "" {
			sessionDir, err = session.DefaultDir()
			if err != nil {
				return err
			}
		}
		sessionStore, err = session.New(sessionDir)
		if err != nil {
			return err
		}
		snapshot, err = sessionStore.Load(sessionName)
		if err != nil {
			return err
		}
	}

	var model dora.Model
	switch cfg.Model.Provider {
	case "openai-compatible":
		model, err = openai.New(openai.Config{
			BaseURL:    cfg.Model.BaseURL,
			APIKey:     cfg.Model.APIKey,
			Model:      cfg.Model.Name,
			HTTPClient: streams.HTTPClient,
		})
	case "openai-responses":
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
	if cfg.Tools.Bash.Enabled {
		bash, err := bashtool.New(bashtool.Config{
			WorkingDir: cfg.Tools.Bash.WorkingDir,
			Timeout:    time.Duration(cfg.Tools.Bash.TimeoutSeconds) * time.Second,
		})
		if err != nil {
			return err
		}
		tools = append(tools, bash)
	}

	agent, err := dora.New(model, tools...)
	if err != nil {
		return err
	}
	var messages []dora.Message
	if !*fresh {
		messages = append(messages, snapshot.Messages...)
	}
	messages = append(messages, dora.Message{Role: dora.RoleUser, Content: prompt})
	var result dora.Result
	if quiet {
		result, err = agent.Run(ctx, messages)
	} else {
		renderer := progress.New(streams.Stderr, streams.TerminalProgress, streams.ColorProgress)
		if sessionName != "" {
			if *fresh && snapshot.Revision > 0 {
				renderer.FreshSession(sessionName)
			} else {
				renderer.Session(sessionName, snapshot.Revision > 0)
			}
		}
		result, err = agent.RunObserved(ctx, messages, renderer)
	}
	if err != nil {
		return err
	}

	var saveErr error
	if sessionStore != nil {
		saveErr = sessionStore.Save(sessionName, snapshot.Revision, result.Messages)
	}
	_, outputErr := fmt.Fprintln(streams.Stdout, result.Content)
	if saveErr != nil {
		return saveErr
	}
	return outputErr
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
