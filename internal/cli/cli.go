package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"

	"dora"
	"dora/internal/config"
	"dora/model/openai"
)

const maxStdinBytes = 16 << 20

// IO contains the command's input and output streams.
type IO struct {
	Stdin           io.Reader
	Stdout          io.Writer
	Stderr          io.Writer
	StdinIsTerminal bool
	HTTPClient      *http.Client
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

	model, err := openai.New(openai.Config{
		BaseURL:    cfg.Model.BaseURL,
		APIKey:     cfg.Model.APIKey,
		Model:      cfg.Model.Name,
		HTTPClient: streams.HTTPClient,
	})
	if err != nil {
		return err
	}
	agent, err := dora.New(model)
	if err != nil {
		return err
	}
	result, err := agent.Run(ctx, []dora.Message{{Role: dora.RoleUser, Content: prompt}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(streams.Stdout, result.Content)
	return err
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
