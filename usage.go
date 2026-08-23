package dora

// InputTokens estimates how much of a model context budget a dora.Usage
// consumes, using the reported input tokens. It is the consumption-side
// accounting exit that compactor and future "retain rounds by usage" logic rely
// on.
//
// It is nil safe: an absent usage yields 0 (no reported input). It is a pure,
// provider-neutral function: it does not read any agent, turn, or persistence
// state, does not run a tokenizer, and does not convert tokens to bytes.
// Negative or zero token counts are passed through verbatim.
func InputTokens(u *Usage) int {
	if u == nil {
		return 0
	}
	return int(u.InputTokens)
}

// TotalTokens is the same accounting but includes output tokens, for callers
// that want the full round's footprint treated as ongoing usage. As with
// InputTokens it is nil safe, pure, and provider-neutral; negative or zero
// values are passed through verbatim.
func TotalTokens(u *Usage) int {
	if u == nil {
		return 0
	}
	return int(u.TotalTokens)
}