package dora

import "encoding/json"

// Compaction tuning constants. These are internal and not configurable so
// context compaction is always enabled and behaves deterministically (given the
// same history and usage the same number of rounds is retained).
const (
	// defaultCompactionRounds is the absolute upper bound on the number of most
	// recent rounds retained. It doubles as the historical fixed retention
	// value, so retention never exceeds the old fixed budget and only ever
	// becomes more generous when context is plentier.
	defaultCompactionRounds = 32
	// minRetainedRounds is the absolute floor on retained rounds so a single
	// oversized round cannot collapse the visible history to nothing.
	minRetainedRounds = 8
	// maxRetainedRounds is the absolute ceiling on retained rounds; it equals
	// defaultCompactionRounds, keeping the old fixed value as a safety valve.
	maxRetainedRounds = defaultCompactionRounds
	// budgetSafetyRatio is the maximum fraction of the context window occupied
	// before the compactor starts dropping history. It stays below 1 to absorb
	// the token-vs-byte estimation error (plus images and header fields that the
	// byte estimate ignores) so a session does not brush against the provider's
	// hard limit.
	budgetSafetyRatio = 0.9
)

// requestMessages returns the messages to send to the model for the current
// iteration. It always keeps the leading system and user messages, and retains
// the most recent rounds that fit within the agent's cached contextWindow
// budget (see dynamicRetainedRounds). lastUsage is the real token usage of the
// *previously completed* model call in the same run, used as the occupancy
// baseline when estimating the current input size; when it is nil the estimate
// falls back to a pure byte count of the whole history. When the whole history
// already fits within the budget it is returned unchanged. The returned slice
// may share backing storage with history.
func (a *Agent) requestMessages(history []Message, lastUsage *Usage) []Message {
	keepRounds, keepAll := a.dynamicRetainedRounds(history, lastUsage)
	if keepAll {
		return history
	}
	return compactMessages(history, keepRounds, a.contextWindow)
}

// dynamicRetainedRounds decides how many of the most recent rounds to retain
// based on the current context occupancy. It returns the number of rounds to
// keep, bounded by minRetainedRounds and maxRetainedRounds, and keepAll, true
// when the whole history fits within the budget (in which case requestMessages
// returns it unchanged).
//
// The current occupancy is the previous round's real total_tokens (the model's
// own accounting of the context that produced the last response, covering
// everything up to and including that round's input) plus a byte estimate of
// the newest round added since then. This mixes token and byte units and is an
// upper-bound approximation, documented as such. When lastUsage is nil (no
// usage reported) it falls back to a pure byte estimate of the entire history,
// matching the pre-usage behavior.
func (a *Agent) dynamicRetainedRounds(history []Message, lastUsage *Usage) (keepRounds int, keepAll bool) {
	prefix, rounds := splitRounds(history)
	if len(rounds) == 0 {
		// No assistant history yet; nothing to compact.
		return 0, true
	}
	budget := int(float64(a.contextWindow) * budgetSafetyRatio)

	var occupancy int
	if lastUsage != nil {
		// Baseline: the previous round's real total footprint, plus the newest
		// round's bytes added since that call.
		occupancy = TotalTokens(lastUsage) + estimateBytes(rounds[len(rounds)-1])
	} else {
		occupancy = estimateBytes(prefix) + estimateBytesRounds(rounds)
	}
	if occupancy <= budget {
		// Budget holds the entire history; retain everything unchanged.
		return len(rounds), true
	}

	// Greedily retain the newest rounds (each kept whole) until the prefix plus
	// the retained rounds reaches the budget. Always retain at least
	// minRetainedRounds and never more than maxRetainedRounds.
	acc := estimateBytes(prefix)
	kept := 0
	for i := len(rounds) - 1; i >= 0 && kept < maxRetainedRounds; i-- {
		if acc > budget {
			break
		}
		acc += estimateBytes(rounds[i])
		kept++
	}
	if kept < minRetainedRounds {
		kept = minRetainedRounds
	}
	return kept, false
}

// splitRounds splits history into the leading prefix (the system and user
// messages before the first assistant message, always retained) and the rounds.
// Each round is a contiguous slice starting with an assistant message and
// including the tool messages it triggered. If there is no assistant message the
// whole history is the prefix and no rounds are returned.
func splitRounds(history []Message) (prefix []Message, rounds [][]Message) {
	firstAssistant := -1
	for i, message := range history {
		if message.Role == RoleAssistant {
			firstAssistant = i
			break
		}
	}
	if firstAssistant < 0 {
		return history, nil
	}
	for i := firstAssistant; i < len(history); {
		j := i + 1
		for j < len(history) && history[j].Role != RoleAssistant {
			j++
		}
		rounds = append(rounds, history[i:j])
		i = j
	}
	return history[:firstAssistant], rounds
}

// estimateBytes sums the text bytes of a message set: each Content plus each
// tool call's Input. Images and header fields are ignored, matching the existing
// contextWindow byte accounting (a known limitation of the token-vs-byte
// approximation documented on the compactor).
func estimateBytes(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += len(message.Content)
		for _, call := range message.ToolCalls {
			total += len(call.Input)
		}
	}
	return total
}

func estimateBytesRounds(rounds [][]Message) int {
	total := 0
	for _, round := range rounds {
		total += estimateBytes(round)
	}
	return total
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