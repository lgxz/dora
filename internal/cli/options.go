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
	noSkills     bool
	quiet        bool
	reasoning    bool
	events       bool
	sessionPath  string
	promptArgs   []string
	usage        func()
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	flags := flag.NewFlagSet("dora", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.configPath, "config", "", "path to YAML configuration")
	flags.StringVar(&opts.model, "m", "", "override the configured model as PROVIDER/PROFILE (profile may be empty)")
	flags.StringVar(&opts.model, "model", "", "override the configured model as PROVIDER/PROFILE (profile may be empty)")
	flags.StringVar(&opts.thinking, "thinking", "", "override the configured model thinking mode (off|minimal|low|medium|high)")
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
	flags.BoolVar(&opts.noSkills, "no-skills", false, "disable all skills")
	flags.BoolVar(&opts.quiet, "quiet", false, "hide run progress")
	flags.BoolVar(&opts.quiet, "q", false, "hide run progress (shorthand)")
	flags.BoolVar(&opts.reasoning, "reasoning", false, "stream captured model reasoning (slower on slow terminals)")
	flags.BoolVar(&opts.events, "events", false, "enable event daemon mode even when events.enabled is unset")
	flags.StringVar(&opts.sessionPath, "session", "", "SQLite file used to store and query completed turns")
	flags.StringVar(&opts.sessionPath, "s", "", "SQLite session file (shorthand)")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "Usage: dora [options] <prompt>\n")
		fmt.Fprintf(stderr, "       command | dora [options] [instruction]\n\nOptions:\n")
		flags.PrintDefaults()
	}
	opts.usage = flags.Usage
	if err := flags.Parse(args); err != nil {
		return options{}, err
	}
	opts.promptArgs = flags.Args()
	return opts, nil
}

func handleImmediate(ctx context.Context, opts options, streams IO) (bool, error) {
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
