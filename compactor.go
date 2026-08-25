package dora

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

const (
	compactionTriggerRatio = 0.8
	compactionTargetRatio  = 0.2
	reservedOutputTokens   = 8192
	maxCompactionAttempts  = 2

	// ASCII prose and JSON average roughly four bytes per token across the
	// supported tokenizer families. Non-ASCII runes are counted individually so
	// CJK text is not underestimated by the ASCII ratio.
	estimatedASCIIBytesPerToken = 4
	// Account for provider-side role and framing tokens that are not present in
	// Message.Content.
	estimatedMessageOverheadTokens = 4
)

const compactionPrompt = `Create a concise but comprehensive replacement summary of the conversation above.

Preserve:
- the original task, explicit requirements, and constraints
- decisions already made and why they were made
- completed work, important file paths, code changes, and command results
- critical tool output, errors, and their resolutions
- the exact current state and any remaining work

Omit repetition, conversational filler, and failed attempts that produced no useful insight. The summary must let another model continue the task without access to the original messages.`

// ensureContextCapacity returns the model-visible history for the next normal
// request. It leaves history unchanged below the trigger. At or above the
// trigger it asks the active model for a replacement summary, validates the
// result, and atomically returns system messages plus that summary. Failure
// never falls back to deleting or truncating local history.
func (a *Agent) ensureContextCapacity(ctx context.Context, history []Message, lastUsage *Usage, specs []ToolSpec) ([]Message, bool, error) {
	if len(history) == 0 || a.predictedTokens(history, lastUsage, specs) < a.compactionTrigger() {
		return history, false, nil
	}

	summary, err := a.generateContextSummary(ctx, history, a.compactionTarget())
	if err != nil {
		return history, false, err
	}

	compacted := make([]Message, 0, 2)
	for _, message := range history {
		if message.Role != RoleSystem {
			break
		}
		compacted = append(compacted, cloneMessage(message))
	}
	compacted = append(compacted, Message{
		Role:    RoleUser,
		Content: "Conversation summary:\n\n" + summary,
	})
	if predicted := a.predictedTokens(compacted, nil, specs); predicted >= a.contextWindow {
		return history, false, fmt.Errorf(
			"request does not fit after compaction: %d >= %d tokens",
			predicted,
			a.contextWindow,
		)
	}
	return compacted, true, nil
}

func (a *Agent) compactionTrigger() int {
	trigger := int(float64(a.contextWindow) * compactionTriggerRatio)
	if trigger < 1 {
		return 1
	}
	return trigger
}

func (a *Agent) compactionTarget() int {
	target := int(float64(a.contextWindow) * compactionTargetRatio)
	if target < 1 {
		return 1
	}
	return target
}

func (a *Agent) outputReserve() int {
	reserve := reservedOutputTokens
	// Keep small context models usable while preserving the 80/20 policy.
	if maximum := a.contextWindow / 5; reserve > maximum {
		reserve = maximum
	}
	return reserve
}

// predictedTokens estimates the next request including its output reserve.
// Reported total_tokens already covers the preceding request, its tool schema,
// and the assistant response, so only tool results created afterwards are
// added. Without reported usage the complete history and tool schema are
// estimated locally.
func (a *Agent) predictedTokens(history []Message, lastUsage *Usage, specs []ToolSpec) int {
	occupied := TotalTokens(lastUsage)
	if occupied > 0 {
		occupied += estimateTrailingToolResultTokens(history)
	} else {
		occupied = estimateTokens(history) + estimateToolSpecTokens(specs)
	}
	return occupied + a.outputReserve()
}

func (a *Agent) generateContextSummary(ctx context.Context, history []Message, targetTokens int) (string, error) {
	lastError := errors.New("summary was empty")
	for attempt := 0; attempt < maxCompactionAttempts; attempt++ {
		strictness := ""
		if attempt > 0 {
			strictness = " The previous summary was too long; be substantially more concise."
		}
		prompt := fmt.Sprintf(
			"%s%s The replacement summary must be at most approximately %d tokens. Return only the summary.",
			compactionPrompt,
			strictness,
			targetTokens,
		)
		messages := cloneMessages(history)
		messages = append(messages, Message{Role: RoleUser, Content: prompt})
		response, err := a.generateWithRetry(ctx, Request{Messages: messages}, nil)
		if err != nil {
			return "", fmt.Errorf("generate summary: %w", err)
		}
		if len(response.ToolCalls) > 0 {
			return "", errors.New("summary response unexpectedly requested tools")
		}
		summary := strings.TrimSpace(response.Content)
		if summary == "" {
			lastError = errors.New("summary was empty")
			continue
		}
		summaryTokens := estimateTextTokens(summary)
		if response.Usage != nil && int(response.Usage.OutputTokens) > summaryTokens {
			summaryTokens = int(response.Usage.OutputTokens)
		}
		if summaryTokens > targetTokens {
			lastError = fmt.Errorf("summary exceeds target: %d > %d tokens", summaryTokens, targetTokens)
			continue
		}
		return summary, nil
	}
	return "", lastError
}

func estimateTokens(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += estimatedMessageOverheadTokens
		total += estimateTextTokens(message.Content)
		total += estimateTextTokens(message.ToolCallID)
		for _, call := range message.ToolCalls {
			total += estimatedMessageOverheadTokens
			total += estimateTextTokens(call.ID)
			total += estimateTextTokens(call.Name)
			total += estimateTextTokens(string(call.Input))
		}
	}
	return total
}

func estimateToolSpecTokens(specs []ToolSpec) int {
	total := 0
	for _, spec := range specs {
		total += estimatedMessageOverheadTokens
		total += estimateTextTokens(spec.Name)
		total += estimateTextTokens(spec.Description)
		total += estimateTextTokens(string(spec.InputSchema))
	}
	return total
}

// estimateTrailingToolResultTokens counts messages created after the previous
// assistant response. That assistant response is already included in the
// provider-reported total_tokens baseline.
func estimateTrailingToolResultTokens(history []Message) int {
	firstTool := len(history)
	for firstTool > 0 && history[firstTool-1].Role == RoleTool {
		firstTool--
	}
	return estimateTokens(history[firstTool:])
}

func estimateTextTokens(text string) int {
	asciiBytes := 0
	nonASCII := 0
	for _, r := range text {
		if r < 0x80 {
			asciiBytes++
		} else {
			nonASCII++
		}
	}
	return (asciiBytes+estimatedASCIIBytesPerToken-1)/estimatedASCIIBytesPerToken + nonASCII
}
