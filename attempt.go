package dora

import "context"

// AttemptToolCall preserves even truncated, invalid JSON arguments as text.
type AttemptToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

// ModelAttempt is an audit record, never conversation history. Discarded
// responses cannot advance continuation state or execute tools. RoundIndex is
// the number of complete tool rounds before this call (zero based).
type ModelAttempt struct {
	RoundIndex      int               `json:"round_index"`
	Purpose         string            `json:"purpose"`
	Recovery        int               `json:"recovery"`
	Disposition     string            `json:"disposition"`
	FinishReason    FinishReason      `json:"finish_reason"`
	RawFinishReason string            `json:"raw_finish_reason,omitempty"`
	OutputBudget    int               `json:"output_budget"`
	Content         string            `json:"content"`
	Reasoning       string            `json:"reasoning,omitempty"`
	ToolCalls       []AttemptToolCall `json:"tool_calls,omitempty"`
	Usage           *Usage            `json:"usage,omitempty"`
	Error           string            `json:"error,omitempty"`
}

type attemptTurnKey struct{}
type attemptRecoveryKey struct{}
type attemptPurposeKey struct{}

func recordModelAttempt(ctx context.Context, request Request, response Response, err error) {
	turn, _ := ctx.Value(attemptTurnKey{}).(*Turn)
	if turn == nil {
		return
	}
	recovery, _ := ctx.Value(attemptRecoveryKey{}).(int)
	purpose, _ := ctx.Value(attemptPurposeKey{}).(string)
	if purpose == "" {
		purpose = "agent"
	}
	attempt := ModelAttempt{RoundIndex: len(turn.rounds), Purpose: purpose, Recovery: recovery,
		Disposition: "received", FinishReason: response.FinishReason, RawFinishReason: response.RawFinishReason,
		OutputBudget: response.OutputBudget, Content: response.Content, Reasoning: response.Reasoning, Usage: cloneUsage(response.Usage)}
	if attempt.OutputBudget == 0 && request.MaxOutputTokens != nil {
		attempt.OutputBudget = *request.MaxOutputTokens
	}
	if err != nil {
		attempt.Error = err.Error()
		attempt.Disposition = "error"
	}
	for _, call := range response.ToolCalls {
		attempt.ToolCalls = append(attempt.ToolCalls, AttemptToolCall{ID: call.ID, Name: call.Name, Input: string(call.Input)})
	}
	turn.attempts = append(turn.attempts, attempt)
}

func setAttemptDisposition(ctx context.Context, disposition string) {
	turn, _ := ctx.Value(attemptTurnKey{}).(*Turn)
	if turn != nil && len(turn.attempts) > 0 {
		turn.attempts[len(turn.attempts)-1].Disposition = disposition
	}
}

// Attempts returns defensive copies of model calls, including discarded calls.
func (t *Turn) Attempts() []ModelAttempt {
	if t == nil {
		return nil
	}
	result := append([]ModelAttempt(nil), t.attempts...)
	for i := range result {
		result[i].Usage = cloneUsage(result[i].Usage)
		result[i].ToolCalls = append([]AttemptToolCall(nil), result[i].ToolCalls...)
	}
	return result
}

// TotalUsage sums reported usage once per model attempt, including recovery and
// compaction. Missing provider usage remains unknown, not a measured zero.
func (t *Turn) TotalUsage() *Usage {
	if t == nil {
		return nil
	}
	var total *Usage
	countedRounds := make(map[int]bool)
	countedFinal := false
	for _, attempt := range t.attempts {
		total = addUsage(total, attempt.Usage)
		if attempt.Purpose == "agent" && attempt.Disposition == "tools" {
			countedRounds[attempt.RoundIndex] = true
		}
		if attempt.Purpose == "agent" && attempt.Disposition == "final" {
			countedFinal = true
		}
	}
	// Explicitly assembled history may precede calls recorded by this Agent.
	for index, round := range t.rounds {
		if !countedRounds[index] {
			total = addUsage(total, round.Usage)
		}
	}
	if !countedFinal {
		total = addUsage(total, t.usage)
	}
	return total
}

func addUsage(total, usage *Usage) *Usage {
	if usage == nil {
		return total
	}
	if total == nil {
		return cloneUsage(usage)
	}
	total.InputTokens += usage.InputTokens
	total.OutputTokens += usage.OutputTokens
	total.TotalTokens += usage.TotalTokens
	add := func(dst **int64, src *int64) {
		if src != nil {
			if *dst == nil {
				*dst = new(int64)
			}
			**dst += *src
		}
	}
	if usage.InputDetails != nil {
		if total.InputDetails == nil {
			total.InputDetails = &InputTokenDetails{}
		}
		add(&total.InputDetails.CachedTokens, usage.InputDetails.CachedTokens)
		add(&total.InputDetails.AudioTokens, usage.InputDetails.AudioTokens)
	}
	if usage.OutputDetails != nil {
		if total.OutputDetails == nil {
			total.OutputDetails = &OutputTokenDetails{}
		}
		add(&total.OutputDetails.ReasoningTokens, usage.OutputDetails.ReasoningTokens)
		add(&total.OutputDetails.AudioTokens, usage.OutputDetails.AudioTokens)
		add(&total.OutputDetails.AcceptedPredictionTokens, usage.OutputDetails.AcceptedPredictionTokens)
		add(&total.OutputDetails.RejectedPredictionTokens, usage.OutputDetails.RejectedPredictionTokens)
	}
	return total
}

// RunError is the terminal error from the latest Agent run, if any.
func (t *Turn) RunError() error {
	if t == nil {
		return nil
	}
	return t.runError
}

// FinalResponse returns a defensive copy only when the turn completed normally.
func (t *Turn) FinalResponse() (Response, bool) {
	if t == nil || !t.completed {
		return Response{}, false
	}
	r := t.finalResponse
	r.ToolCalls = cloneToolCalls(r.ToolCalls)
	r.Usage = cloneUsage(r.Usage)
	return r, true
}
