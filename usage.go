package dora

// Occupancy estimates how much of a model context budget a dora.Usage consumes,
// using the reported input tokens. It is the consumption-side accounting exit
// that compactor and future "retain rounds by usage" logic rely on.
//
// It is nil safe: an absent usage yields 0 (no reported occupancy). It is a
// pure, provider-neutral function: it does not read any agent, turn, or
// persistence state, does not run a tokenizer, and does not convert tokens to
// bytes. Negative or zero token counts are passed through verbatim.
func Occupancy(u *Usage) int {
	if u == nil {
		return 0
	}
	return int(u.InputTokens)
}

// OccupancyTotal is the same accounting but includes output tokens, for callers
// that want the full round's footprint treated as ongoing occupancy. As with
// Occupancy it is nil safe, pure, and provider-neutral; negative or zero values
// are passed through verbatim.
func OccupancyTotal(u *Usage) int {
	if u == nil {
		return 0
	}
	return int(u.TotalTokens)
}