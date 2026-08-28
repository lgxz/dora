package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/lgxz/dora/internal/update"
)

type options struct {
	configPath   string
	model        string
	thinking     string
	maxRounds    int
	maxRoundsSet bool
	showVersion  bool
	update       bool
	forceUpdate  bool
	setup        bool
	noSkills     bool
	quiet        bool
	color        string
	reasoning    bool
	events       bool
	acp          bool
	sessionPath  string
	workdir      string
	promptArgs   []string
	usage        func()
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("dora", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.configPath, "config", "", "path to YAML configuration")
	flags.StringVar(&opts.model, "model", "", "override the configured model as PROVIDER/PROFILE (profile may be empty)")
	flags.StringVar(&opts.thinking, "thinking", "", "override the configured model thinking mode (off|minimal|low|medium|high|xhigh|max)")
	flags.Func("max-rounds", "override the maximum model-tool rounds per segment", func(value string) error {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return errors.New("must be a positive integer")
		}
		opts.maxRounds = parsed
		opts.maxRoundsSet = true
		return nil
	})
	flags.BoolVar(&opts.showVersion, "version", false, "print version information")
	flags.BoolVar(&opts.update, "update", false, "update a standalone installation")
	flags.BoolVar(&opts.forceUpdate, "force", false, "force update, bypassing the standalone-install marker and version checks")
	flags.BoolVar(&opts.setup, "setup", false, "interactively configure a model provider and API key")
	flags.BoolVar(&opts.noSkills, "no-skills", false, "disable all skills")
	flags.BoolVar(&opts.quiet, "quiet", false, "hide run progress, only print the final result to stdout.")
	flags.Func("color", "progress color mode: auto, always, or never (default auto)", func(value string) error {
		switch value {
		case "auto", "always", "never":
			opts.color = value
			return nil
		default:
			return errors.New("must be auto, always, or never")
		}
	})
	flags.BoolVar(&opts.reasoning, "reasoning", false, "stream captured model reasoning (slower on slow terminals)")
	flags.BoolVar(&opts.events, "events", false, "enable event daemon mode even when events.enabled is unset")
	flags.BoolVar(&opts.acp, "acp", false, "serve Agent Client Protocol v1 over stdin/stdout")
	flags.StringVar(&opts.sessionPath, "session", "", "SQLite file used to store and query saved turns")
	flags.StringVar(&opts.workdir, "workdir", "", "working directory used to resolve relative tool paths")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Dora - A tiny, extensible, and efficient LLM agent.\n\n")
		fmt.Fprintf(stderr, "Usage: dora [options] <prompt>\n")
		fmt.Fprintf(stderr, "Examples:\n")
		fmt.Fprintf(stderr, "  $ dora -quiet What's your name?\n\n")
		fmt.Fprintf(stderr, "Options:\n")
		flags.PrintDefaults()
	}
	opts.usage = flags.Usage
	opts.color = "auto"
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	opts.promptArgs = flags.Args()
	return opts, nil
}

func handleImmediate(ctx context.Context, opts options, streams IO) (bool, error) {
	if opts.setup {
		if opts.showVersion || opts.update {
			return true, errors.New("--setup cannot be combined with --version or --update")
		}
		if len(opts.promptArgs) != 0 {
			return true, errors.New("--setup does not accept a prompt")
		}
		return true, runSetup(opts, streams)
	}
	if opts.showVersion {
		version := streams.Version
		if version == "" {
			version = "dora dev (commit none, built unknown)"
		}
		_, err := fmt.Fprintln(streams.Stdout, version)
		return true, err
	}
	if !opts.update {
		return false, nil
	}
	if len(opts.promptArgs) != 0 {
		return true, errors.New("-update does not accept a prompt")
	}
	service := streams.Updater
	if service == nil {
		service = update.New(update.Config{
			CurrentVersion: streams.BuildVersion,
			HTTPClient:     streams.HTTPClient,
			Force:          opts.forceUpdate,
		})
	}
	if _, err := fmt.Fprintln(streams.Stderr, "dora: checking for updates"); err != nil {
		return true, err
	}
	result, err := service.Update(ctx)
	if err != nil {
		return true, err
	}
	if result.Updated {
		_, err = fmt.Fprintf(streams.Stdout, "Updated dora %s -> %s\n", result.Current, result.Latest)
	} else {
		_, err = fmt.Fprintf(streams.Stdout, "dora %s is already up to date\n", result.Current)
	}
	return true, err
}
