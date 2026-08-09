package dora

// Result contains the final assistant text and the complete conversation. The
// returned messages can be passed to a later Run call to continue the session.
type Result struct {
	Content  string
	Messages []Message
}
