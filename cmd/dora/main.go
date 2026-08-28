package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/lgxz/dora/internal/cli"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	info, err := os.Stdin.Stat()
	if err != nil {
		report(err)
		os.Exit(1)
	}
	stdoutInfo, err := os.Stdout.Stat()
	if err != nil {
		report(err)
		os.Exit(1)
	}
	stderrInfo, err := os.Stderr.Stat()
	if err != nil {
		report(err)
		os.Exit(1)
	}

	stdoutIsTerminal := stdoutInfo.Mode()&os.ModeCharDevice != 0
	terminalWidth := 0
	if stdoutIsTerminal {
		terminalWidth, _, _ = term.GetSize(int(os.Stdout.Fd()))
	}
	colorOutput := stdoutIsTerminal && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb"
	darkBackground := colorOutput && termenv.HasDarkBackground()
	err = cli.Run(ctx, os.Args[1:], cli.IO{
		Stdin:            os.Stdin,
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
		StdoutIsTerminal: stdoutIsTerminal,
		TerminalWidth:    terminalWidth,
		ColorOutput:      colorOutput,
		DarkBackground:   darkBackground,
		Version:          versionString(),
		BuildVersion:     version,
		StdinIsTerminal:  info.Mode()&os.ModeCharDevice != 0,
		TerminalProgress: stderrInfo.Mode()&os.ModeCharDevice != 0,
		ColorProgress: stderrInfo.Mode()&os.ModeCharDevice != 0 &&
			os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
		ReadSecret: func() (string, error) {
			value, err := term.ReadPassword(int(os.Stdin.Fd()))
			return string(value), err
		},
	})
	if err != nil {
		report(err)
		os.Exit(1)
	}
}

func versionString() string {
	return fmt.Sprintf("dora %s (commit %s, built %s)", version, commit, date)
}

func report(err error) {
	if strings.HasPrefix(err.Error(), "dora:") {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Fprintln(os.Stderr, "dora:", err)
}
