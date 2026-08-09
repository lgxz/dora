package dora

// UpdateKind identifies a semantic change during an Agent run.
type UpdateKind string

const (
	UpdateThinking     UpdateKind = "thinking"
	UpdateContentDelta UpdateKind = "content_delta"
	UpdateMessageAdded UpdateKind = "message_added"
	UpdateToolStarted  UpdateKind = "tool_started"
	UpdateToolFailed   UpdateKind = "tool_failed"
)

// Update describes transient run progress. Message is populated for
// UpdateMessageAdded. ToolCall is populated for tool updates.
type Update struct {
	Kind     UpdateKind
	Delta    string
	Message  Message
	ToolCall ToolCall
	Err      error
}

// Observer receives synchronous progress updates from an Agent run.
// Implementations should return quickly and must be safe for the calling
// goroutine. Updates contain defensive copies of conversation data.
type Observer interface {
	Observe(Update)
}

// ObserverFunc adapts a function to Observer.
type ObserverFunc func(Update)

// Observe implements Observer.
func (f ObserverFunc) Observe(update Update) {
	f(update)
}
