package cli

import (
	"bufio"
	"context"
	"errors"

	"github.com/lgxz/dora"
)

type turnOutcome struct {
	completed    bool
	maxRoundsErr error
}

func runTurn(ctx context.Context, agent *dora.Agent, turn *dora.Turn, observer dora.Observer, streams IO, opts dora.RunOptions) (turnOutcome, error) {
	input := bufio.NewReader(streams.Stdin)
	for {
		err := agent.RunObservedWithOptions(ctx, turn, observer, opts)
		if err == nil {
			return turnOutcome{completed: true}, nil
		}
		if !errors.Is(err, dora.ErrMaxRounds) ||
			!streams.StdinIsTerminal || !streams.TerminalProgress {
			outcome := turnOutcome{}
			if errors.Is(err, dora.ErrMaxRounds) {
				outcome.maxRoundsErr = err
			}
			return outcome, err
		}
		maxRoundsErr := err
		keepGoing, confirmErr := confirmContinue(input, streams.Stderr)
		if confirmErr != nil {
			return turnOutcome{maxRoundsErr: maxRoundsErr}, confirmErr
		}
		if !keepGoing {
			return turnOutcome{maxRoundsErr: maxRoundsErr}, nil
		}
	}
}
