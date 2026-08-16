package cli

import (
	"bufio"
	"context"
	"errors"

	"github.com/lgxz/dora"
)

func runTurn(ctx context.Context, agent *dora.Agent, turn *dora.Turn, observer dora.Observer, streams IO) (bool, error) {
	input := bufio.NewReader(streams.Stdin)
	for {
		err := agent.RunObserved(ctx, turn, observer)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, dora.ErrMaxRounds) ||
			!streams.StdinIsTerminal || !streams.TerminalProgress {
			return false, err
		}
		keepGoing, err := confirmContinue(input, streams.Stderr)
		if err != nil {
			return false, err
		}
		if !keepGoing {
			return false, nil
		}
	}
}
