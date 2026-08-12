package dora

// requestMessages returns the messages to send to the model for the current
// iteration. When compaction is enabled it keeps only the most recent rounds
// (an assistant message plus its tool messages) plus any leading system and
// user messages, so a long tool loop does not grow the context without bound.
// The returned slice may share backing storage with history; the caller is
// responsible for cloning it before exposing it outside the Agent.
func (a *Agent) requestMessages(history []Message) []Message {
	// When compaction is disabled, or the history is already small enough that
	// it cannot exceed the round budget, send it unchanged.
	if a.maxHistoryRounds <= 0 || len(history) <= a.maxHistoryRounds {
		return history
	}
	return compactMessages(history, a.maxHistoryRounds)
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