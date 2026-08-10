package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/lgxz/dora/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	info, err := os.Stdin.Stat()
	if err != nil {
		report(err)
		os.Exit(1)
	}
	stderrInfo, err := os.Stderr.Stat()
	if err != nil {
		report(err)
		os.Exit(1)
	}

	err = cli.Run(ctx, os.Args[1:], cli.IO{
		Stdin:            os.Stdin,
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
		StdinIsTerminal:  info.Mode()&os.ModeCharDevice != 0,
		TerminalProgress: stderrInfo.Mode()&os.ModeCharDevice != 0,
		ColorProgress: stderrInfo.Mode()&os.ModeCharDevice != 0 &&
			os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb",
	})
	if err != nil {
		report(err)
		os.Exit(1)
	}
}

func report(err error) {
	if strings.HasPrefix(err.Error(), "dora:") {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Fprintln(os.Stderr, "dora:", err)
}
