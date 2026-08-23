package dora

import "time"

// UpdateKind identifies a semantic change during an Agent run.
type UpdateKind string

const (
	UpdateThinking        UpdateKind = "thinking"
	UpdateContentDelta    UpdateKind = "content_delta"
	UpdateReasoningDelta  UpdateKind = "reasoning_delta"
	UpdateMessageReceived UpdateKind = "message_received"
	UpdateToolStarted     UpdateKind = "tool_started"
	UpdateToolFinished    UpdateKind = "tool_finished"
	UpdateInfo            UpdateKind = "info"
	UpdateTurnStarted     UpdateKind = "turn_started"
)

// Update describes transient run progress. Message is populated for
// UpdateMessageReceived (the complete assistant message). ToolCall is
// populated for UpdateToolStarted and UpdateToolFinished. StartedAt is
// populated for UpdateToolStarted and carries the real time the tool began
// executing, which may differ from when the event is delivered.
// UpdateToolFinished carries both the tool's result/error Message and an
// optional Err that is non-nil only on the failure path, so consumers can
// render the failure and still read the message that was persisted. Info
// carries the text for UpdateInfo and UpdateTurnStarted (the latter reports
// the prompt that starts a new turn).
type Update struct {
	Kind      UpdateKind
	Delta     string
	Message   Message
	ToolCall  ToolCall
	StartedAt time.Time
	Err       error
	Info      string
	Usage     *Usage // populated for UpdateMessageReceived; nil when the provider reports none
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
