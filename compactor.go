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
	// provider-tokenizer differences plus tool-schema and image tokens that the
	// provider-neutral estimate cannot see, so a session does not brush against
	// the provider's hard limit.
	budgetSafetyRatio = 0.9
	// ASCII prose and JSON average roughly four bytes per token across the
	// supported tokenizer families. Non-ASCII runes are counted individually so
	// CJK text is not underestimated by the ASCII ratio.
	estimatedASCIIBytesPerToken = 4
	// Account for provider-side role and framing tokens that are not present in
	// Message.Content.
	estimatedMessageOverheadTokens = 4
)

// requestMessages returns the messages to send to the model for the current
// iteration. It always keeps the leading system and user messages, and retains
// the most recent rounds that fit within the agent's cached contextWindow
// budget (see dynamicRetainedRounds). lastUsage is the real token usage of the
// *previously completed* model call in the same run, used as the occupancy
// baseline when estimating the current input size; only tool results produced
// after that response are added to it. When lastUsage is nil the estimate falls
// back to a token estimate of the whole history. When the whole history
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
// The current occupancy is the previous model call's real total_tokens. That
// already includes both its input and generated assistant response, so only the
// tool result messages produced after the response are estimated and added.
// When lastUsage is nil (no usage reported), the entire history is estimated in
// tokens instead. Both paths therefore compare token occupancy with the token
// capacity reported by ContextSize.
func (a *Agent) dynamicRetainedRounds(history []Message, lastUsage *Usage) (keepRounds int, keepAll bool) {
	prefix, rounds := splitRounds(history)
	if len(rounds) == 0 {
		// No assistant history yet; nothing to compact.
		return 0, true
	}
	budget := int(float64(a.contextWindow) * budgetSafetyRatio)

	var occupancy int
	if lastUsage != nil {
		// The assistant response is already included in total_tokens. Tool result
		// messages are the only conversation content added after that response.
		occupancy = TotalTokens(lastUsage) + estimateToolResultTokens(rounds[len(rounds)-1])
	} else {
		occupancy = estimateTokens(prefix) + estimateTokensRounds(rounds)
	}
	if occupancy <= budget {
		// Budget holds the entire history; retain everything unchanged.
		return len(rounds), true
	}

	// Greedily retain the newest rounds (each kept whole) until the prefix plus
	// the retained rounds reaches the budget. Always retain at least
	// minRetainedRounds and never more than maxRetainedRounds.
	acc := estimateTokens(prefix)
	kept := 0
	for i := len(rounds) - 1; i >= 0 && kept < maxRetainedRounds; i-- {
		if acc > budget {
			break
		}
		acc += estimateTokens(rounds[i])
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

// estimateTokens estimates provider-neutral context usage for a message set.
// ASCII content uses the common four-bytes-per-token heuristic; every
// non-ASCII rune counts as one token. A small per-message allowance covers role
// and framing tokens. Images and provider-specific tool schema overhead remain
// outside this estimate.
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

func estimateTokensRounds(rounds [][]Message) int {
	total := 0
	for _, round := range rounds {
		total += estimateTokens(round)
	}
	return total
}

// estimateToolResultTokens counts only messages created after the previous
// model response. The assistant message at the start of the round is already
// represented in that response's total_tokens.
func estimateToolResultTokens(round []Message) int {
	total := 0
	for _, message := range round {
		if message.Role == RoleTool {
			total += estimateTokens([]Message{message})
		}
	}
	return total
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

// compactMessages keeps the leading system and user messages plus the most
// recent keepRounds rounds. A round is an assistant message together with the
// tool messages it triggered, so compaction never splits a round in the middle
// of its tool results. The current (last) round is retained unchanged; earlier
// retained rounds are compressed (images dropped, oversized content and tool
// input truncated) so the estimated usage stays within the contextWindow token
// budget. The returned slice may share backing storage with history.
func compactMessages(history []Message, keepRounds, contextWindow int) []Message {
	if keepRounds <= 0 || len(history) == 0 {
		return history
	}

	// Use splitRounds to locate the leading prefix (always retained) and the
	// rounds. Each round is a contiguous slice of an assistant message plus the
	// tool messages it triggered, so a round is never split in the middle.
	prefix, rounds := splitRounds(history)

	// No assistant history (whole history is the prefix) or fewer rounds than
	// keepRounds: nothing is dropped.
	if len(rounds) == 0 || len(rounds) < keepRounds {
		return history
	}

	// Retain the most recent keepRounds rounds. The very last round is the
	// current one and is kept unchanged; every earlier retained round is a
	// historical round compressed against the per-message budget below.
	currentRound := rounds[len(rounds)-1]
	historicalRounds := rounds[len(rounds)-keepRounds : len(rounds)-1]

	// Count historical messages (all retained rounds except the current one) to
	// divide the remaining budget among them.
	historicalCount := 0
	for _, round := range historicalRounds {
		historicalCount += len(round)
	}

	// Compute the per-message budget for historical rounds. A non-positive
	// contextWindow disables budget-based truncation; compactRound then still
	// drops images so multimodal history stays lean but otherwise returns the
	// round intact (matching the pre-budget behavior).
	perMessage := 0
	if contextWindow > 0 {
		// The current round is retained unchanged. Estimate its text size
		// (images are ignored as they consume no text context) and subtract it
		// from the budget.
		remaining := contextWindow - estimateTokens(currentRound)
		if remaining <= 0 || historicalCount == 0 {
			// The current round already consumes the budget; keep only it.
			kept := make([]Message, 0, len(prefix)+len(currentRound))
			kept = append(kept, prefix...)
			kept = append(kept, currentRound...)
			return kept
		}
		perMessage = remaining / historicalCount
	}

	// Keep the leading system/user prefix, the compacted historical rounds, and
	// the current round unchanged.
	kept := make([]Message, 0, len(prefix)+len(currentRound)+historicalCount)
	kept = append(kept, prefix...)
	for _, round := range historicalRounds {
		kept = append(kept, compactRound(round, perMessage)...)
	}
	kept = append(kept, currentRound...)
	return kept
}

// compactRound compresses a historical round (an assistant message plus its
// tool messages): images are dropped and oversized content or tool call input
// is truncated to at most maxMessageTokens estimated tokens. The current round
// is never passed here. A non-positive maxMessageTokens keeps the round intact.
func compactRound(round []Message, maxMessageTokens int) []Message {
	compacted := make([]Message, len(round))
	for i, message := range round {
		message.Images = nil
		if maxMessageTokens > 0 {
			message.Content = truncateString(message.Content, maxMessageTokens)
			for j := range message.ToolCalls {
				message.ToolCalls[j].Input = compactJSON(message.ToolCalls[j].Input, maxMessageTokens)
			}
		}
		compacted[i] = message
	}
	return compacted
}

// truncateString truncates s to at most max estimated tokens, keeping the
// beginning and the end and marking the omitted middle. It is UTF-8 safe.
func truncateString(s string, max int) string {
	if estimateTextTokens(s) <= max {
		return s
	}
	const marker = "... [truncated] ..."
	markerTokens := estimateTextTokens(marker)
	if max <= markerTokens {
		return tokenPrefix(s, max)
	}
	runes := []rune(s)
	contentBudget := max - markerTokens
	leftBudget := contentBudget / 2
	rightBudget := contentBudget - leftBudget
	left := tokenPrefixRunes(runes, leftBudget)
	right := tokenSuffixRunes(runes[len(left):], rightBudget)
	return string(left) + marker + string(right)
}

func tokenPrefix(s string, max int) string {
	return string(tokenPrefixRunes([]rune(s), max))
}

func tokenPrefixRunes(runes []rune, max int) []rune {
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if estimateTextTokens(string(runes[:mid])) <= max {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return runes[:low]
}

func tokenSuffixRunes(runes []rune, max int) []rune {
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if estimateTextTokens(string(runes[len(runes)-mid:])) <= max {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return runes[len(runes)-low:]
}

// compactJSON truncates oversized string values inside a JSON document while
// keeping it valid JSON. Non-JSON input is returned unchanged.
func compactJSON(raw json.RawMessage, max int) json.RawMessage {
	if estimateTextTokens(string(raw)) <= max {
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
