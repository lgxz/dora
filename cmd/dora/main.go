package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"dora/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	info, err := os.Stdin.Stat()
	if err != nil {
		fmt.Fprintln(os.Stderr, "dora:", err)
		os.Exit(1)
	}

	err = cli.Run(ctx, os.Args[1:], cli.IO{
		Stdin:           os.Stdin,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
		StdinIsTerminal: info.Mode()&os.ModeCharDevice != 0,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dora:", err)
		os.Exit(1)
	}
}
