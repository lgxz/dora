package dora

// State is the complete provider-neutral conversation plus opaque model state
// needed to resume providers with richer response protocols.
type State struct {
	Messages     []Message
	Continuation string
}

// Result contains the assistant text and the complete conversation. The
// returned state can be passed to a later RunState call to resume it exactly.
// When a run returns ErrMaxRounds, Result contains the partial state accumulated
// through the final completed round and Content is empty.
type Result struct {
	Content      string
	Messages     []Message
	Continuation string
}
