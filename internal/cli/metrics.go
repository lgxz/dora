package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lgxz/dora"
)

// aggregateTurnUsage sums every provider-reported model call retained by the
// turn. Nil calls are skipped; if no call reported usage, the aggregate is nil.
func aggregateTurnUsage(turn *dora.Turn) *dora.Usage {
	if turn == nil {
		return nil
	}
	usages := make([]*dora.Usage, 0, len(turn.Rounds())+1)
	for _, round := range turn.Rounds() {
		usages = append(usages, round.Usage)
	}
	usages = append(usages, turn.Usage())
	return aggregateUsages(usages)
}

func aggregateUsages(usages []*dora.Usage) *dora.Usage {
	var total *dora.Usage
	var cachedTokens, inputAudioTokens int64
	var reasoningTokens, outputAudioTokens int64
	var acceptedPredictionTokens, rejectedPredictionTokens int64
	var cachedTokensReported, inputAudioTokensReported bool
	var reasoningTokensReported, outputAudioTokensReported bool
	var acceptedPredictionTokensReported, rejectedPredictionTokensReported bool
	for _, usage := range usages {
		if usage == nil {
			continue
		}
		if total == nil {
			total = &dora.Usage{}
		}
		total.InputTokens += usage.InputTokens
		total.OutputTokens += usage.OutputTokens
		total.TotalTokens += usage.TotalTokens
		if usage.InputDetails != nil && usage.InputDetails.CachedTokens != nil {
			cachedTokens += *usage.InputDetails.CachedTokens
			cachedTokensReported = true
		}
		if usage.InputDetails != nil && usage.InputDetails.AudioTokens != nil {
			inputAudioTokens += *usage.InputDetails.AudioTokens
			inputAudioTokensReported = true
		}
		if usage.OutputDetails != nil && usage.OutputDetails.ReasoningTokens != nil {
			reasoningTokens += *usage.OutputDetails.ReasoningTokens
			reasoningTokensReported = true
		}
		if usage.OutputDetails != nil && usage.OutputDetails.AudioTokens != nil {
			outputAudioTokens += *usage.OutputDetails.AudioTokens
			outputAudioTokensReported = true
		}
		if usage.OutputDetails != nil && usage.OutputDetails.AcceptedPredictionTokens != nil {
			acceptedPredictionTokens += *usage.OutputDetails.AcceptedPredictionTokens
			acceptedPredictionTokensReported = true
		}
		if usage.OutputDetails != nil && usage.OutputDetails.RejectedPredictionTokens != nil {
			rejectedPredictionTokens += *usage.OutputDetails.RejectedPredictionTokens
			rejectedPredictionTokensReported = true
		}
	}
	if total != nil && (cachedTokensReported || inputAudioTokensReported) {
		total.InputDetails = &dora.InputTokenDetails{}
		if cachedTokensReported {
			total.InputDetails.CachedTokens = &cachedTokens
		}
		if inputAudioTokensReported {
			total.InputDetails.AudioTokens = &inputAudioTokens
		}
	}
	if total != nil && (reasoningTokensReported || outputAudioTokensReported || acceptedPredictionTokensReported || rejectedPredictionTokensReported) {
		total.OutputDetails = &dora.OutputTokenDetails{}
		if reasoningTokensReported {
			total.OutputDetails.ReasoningTokens = &reasoningTokens
		}
		if outputAudioTokensReported {
			total.OutputDetails.AudioTokens = &outputAudioTokens
		}
		if acceptedPredictionTokensReported {
			total.OutputDetails.AcceptedPredictionTokens = &acceptedPredictionTokens
		}
		if rejectedPredictionTokensReported {
			total.OutputDetails.RejectedPredictionTokens = &rejectedPredictionTokens
		}
	}
	return total
}

// writeMetricsFile writes the aggregate as the existing dora.Usage JSON shape.
// A JSON null records that no provider reported usage. The containing directory
// must already exist; requested metrics are never silently discarded.
func writeMetricsFile(path string, turn *dora.Turn) error {
	if path == "" {
		return nil
	}
	encoded, err := json.MarshalIndent(aggregateTurnUsage(turn), "", "  ")
	if err != nil {
		return fmt.Errorf("encode metrics: %w", err)
	}
	encoded = append(encoded, '\n')

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open metrics file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set metrics file permissions: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return fmt.Errorf("write metrics file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close metrics file: %w", err)
	}
	return nil
}
