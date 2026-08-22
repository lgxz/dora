package dora

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
// unchanged. The returned slice may share backing storage with history.
func (a *Agent) requestMessages(history []Message) []Message {
	if len(history) <= defaultCompactionRounds {
		return history
	}
	return compactMessages(history, defaultCompactionRounds)
}

// compactMessages keeps the leading system and user messages plus the most
// recent keepRounds rounds. A round is an assistant message together with the
// tool messages it triggered, so compaction never splits a round in the middle
// of its tool results. Each retained round is passed through compactRound so
// per-round compression can be added later. The returned slice may share
// backing storage with history.
func compactMessages(history []Message, keepRounds int) []Message {
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

	// Keep the leading system/user prefix plus the retained rounds, each
	// processed by compactRound.
	kept := make([]Message, 0, firstAssistant+len(history)-roundStart)
	kept = append(kept, history[:firstAssistant]...)
	for i := roundStart; i < len(history); {
		roundEnd := i + 1
		for roundEnd < len(history) && history[roundEnd].Role != RoleAssistant {
			roundEnd++
		}
		kept = append(kept, compactRound(history[i:roundEnd])...)
		i = roundEnd
	}
	return kept
}

// compactRound processes a single round (an assistant message plus its tool
// messages). It currently returns the round unchanged; per-round compression
// such as truncating long tool output or limiting images can be added here.
func compactRound(round []Message) []Message {
	return round
}