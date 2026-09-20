package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

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
	// timeout and process supervisors normally request shutdown with SIGTERM.
	// Cancel the run so the application can persist its terminal Turn before exit.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
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
	return fmt.Sprintf("%s (commit %s, built %s)", version, commit, date)
}

func report(err error) {
	if strings.HasPrefix(err.Error(), "E:") {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Fprintln(os.Stderr, "E:", err)
}
