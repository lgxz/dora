package dora

import "encoding/json"

// defaultCompactionRounds is the fixed number of most recent rounds kept when
// building the request messages for a model call. A round is an assistant
// message together with the tool messages it triggered. The threshold is a
// hard-coded internal constant (not configurable) so context compaction is
// always enabled and behaves predictably.
const defaultCompactionRounds = 32

// requestMessages returns the messages to send to the model for the current
// iteration. It keeps the leading system and user messages plus the most recent
// defaultCompactionRounds rounds, so a long tool loop does not grow the context
// without bound. When history is already within the budget it is returned
// unchanged. The retained historical rounds are compressed against the agent's
// cached contextWindow byte budget so long tool output does not overflow the
// model's context. When history is already within the round budget it is
// returned unchanged. The returned slice may share backing storage with history.
func (a *Agent) requestMessages(history []Message) []Message {
	if len(history) <= defaultCompactionRounds {
		return history
	}
	return compactMessages(history, defaultCompactionRounds, a.contextWindow)
}

// compactMessages keeps the leading system and user messages plus the most
// recent keepRounds rounds. A round is an assistant message together with the
// tool messages it triggered, so compaction never splits a round in the middle
// of its tool results. The current (last) round is retained unchanged; earlier
// retained rounds are compressed (images dropped, oversized content and tool
// input truncated) so the total text stays within the contextWindow byte
// budget. The returned slice may share backing storage with history.
func compactMessages(history []Message, keepRounds, contextWindow int) []Message {
	if keepRounds <= 0 || len(history) == 0 {
		return history
	}

	// Find the index of the first assistant message. Everything before it
	// (system and user messages) is always retained.
	firstAssistant := -1
	for i, message := range history {
		if message.Role == RoleAssistant {
			firstAssistant = i
			break
		}
	}
	if firstAssistant < 0 {
		return history
	}

	// Count rounds backwards from the end. Each assistant message starts a
	// round; tool messages belong to the preceding assistant round.
	roundStart := firstAssistant
	rounds := 0
	for i := len(history) - 1; i >= firstAssistant; i-- {
		if history[i].Role == RoleAssistant {
			rounds++
			if rounds == keepRounds {
				roundStart = i
				break
			}
		}
	}

	// If there are fewer rounds than keepRounds, nothing is dropped.
	if rounds < keepRounds {
		return history
	}

	// The last round is the current one and is retained unchanged. Estimate its
	// text size (images are ignored as they consume no text context) and
	// subtract it from the budget.
	currentStart := roundStart
	for i := len(history) - 1; i >= roundStart; i-- {
		if history[i].Role == RoleAssistant {
			currentStart = i
			break
		}
	}

	// Count historical messages (before the current round) to divide the
	// remaining budget among them.
	historicalCount := currentStart - roundStart

	// Compute the per-message budget for historical rounds. A non-positive
	// contextWindow disables budget-based truncation; compactRound then still
	// drops images so multimodal history stays lean but otherwise returns the
	// round intact (matching the pre-budget behavior).
	perMessage := 0
	if contextWindow > 0 {
		currentBytes := 0
		for _, message := range history[currentStart:] {
			currentBytes += len(message.Content)
			for _, call := range message.ToolCalls {
				currentBytes += len(call.Input)
			}
		}
		remaining := contextWindow - currentBytes
		if remaining <= 0 || historicalCount == 0 {
			// The current round already consumes the budget; keep only it.
			kept := make([]Message, 0, firstAssistant+len(history)-currentStart)
			kept = append(kept, history[:firstAssistant]...)
			kept = append(kept, history[currentStart:]...)
			return kept
		}
		perMessage = remaining / historicalCount
	}

	// Keep the leading system/user prefix plus the retained rounds. The last
	// round is the current one and is retained unchanged; earlier rounds are
	// compressed against the per-message budget.
	kept := make([]Message, 0, firstAssistant+len(history)-roundStart)
	kept = append(kept, history[:firstAssistant]...)
	for i := roundStart; i < len(history); {
		roundEnd := i + 1
		for roundEnd < len(history) && history[roundEnd].Role != RoleAssistant {
			roundEnd++
		}
		if roundEnd == len(history) {
			kept = append(kept, history[i:roundEnd]...)
		} else {
			kept = append(kept, compactRound(history[i:roundEnd], perMessage)...)
		}
		i = roundEnd
	}
	return kept
}

// compactRound compresses a historical round (an assistant message plus its
// tool messages): images are dropped and oversized content or tool call input
// is truncated to at most maxMessageBytes. The current round is never passed
// here. A non-positive maxMessageBytes keeps the round intact.
func compactRound(round []Message, maxMessageBytes int) []Message {
	compacted := make([]Message, len(round))
	for i, message := range round {
		message.Images = nil
		if maxMessageBytes > 0 {
			message.Content = truncateString(message.Content, maxMessageBytes)
			for j := range message.ToolCalls {
				message.ToolCalls[j].Input = compactJSON(message.ToolCalls[j].Input, maxMessageBytes)
			}
		}
		compacted[i] = message
	}
	return compacted
}

// truncateString truncates s to at most max bytes, keeping the beginning and
// the end and marking the omitted middle. It is UTF-8 safe.
func truncateString(s string, max int) string {
	if len(s) <= max {
		return s
	}
	runes := []rune(s)
	half := max / 2
	return string(runes[:half]) + "... [truncated] ..." + string(runes[len(runes)-half:])
}

// compactJSON truncates oversized string values inside a JSON document while
// keeping it valid JSON. Non-JSON input is returned unchanged.
func compactJSON(raw json.RawMessage, max int) json.RawMessage {
	if len(raw) <= max {
		return raw
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return raw
	}
	compactValue(&value, max)
	encoded, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return encoded
}

// compactValue recursively truncates oversized string values in a JSON value.
func compactValue(value *any, max int) {
	switch v := (*value).(type) {
	case string:
		if len(v) > max {
			*value = truncateString(v, max)
		}
	case []any:
		for i := range v {
			compactValue(&v[i], max)
		}
	case map[string]any:
		for key := range v {
			item := v[key]
			compactValue(&item, max)
			v[key] = item
		}
	}
}