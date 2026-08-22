package dora

import "context"

// DefaultContextWindowBytes is the fallback context budget (message-content
// bytes) used when a Model does not report its own context size. It matches the
// internal/config default so the agent behaves predictably out of the box.
const DefaultContextWindowBytes = 1 << 20

// ContextSize is optionally implemented by models that can report their
// approximate context capacity in content bytes. It is a pure capability,
// leaving the Model and StreamingModel contracts unchanged; models that do not
// implement it keep working and fall back to DefaultContextWindowBytes.
type ContextSize interface {
	ContextSize() int
}

// Model produces the next assistant response for a conversation.
type Model interface {
	Generate(context.Context, Request) (Response, error)
}

// StreamingModel is optionally implemented by models that can expose response
// progress while generating. Generate remains the compatibility baseline.
type StreamingModel interface {
	Model
	GenerateStream(context.Context, Request, func(ModelEvent)) (Response, error)
}

// ModelEventKind identifies an incremental model response event.
type ModelEventKind string

const (
	ModelEventContentDelta   ModelEventKind = "content_delta"
	ModelEventReasoningDelta ModelEventKind = "reasoning_delta"
	ModelEventToolCallReady  ModelEventKind = "tool_call_ready"
)

// ModelEvent reports content as it arrives or a tool call whose arguments have
// finished streaming. ReasoningDelta carries the model's chain-of-thought,
// which typically streams before the content. The Agent currently waits for
// the complete response before executing any reported tool call.
type ModelEvent struct {
	Kind     ModelEventKind
	Delta    string
	ToolCall ToolCall
}

// Request contains the complete conversation and the tools available to the
// model for the next response.
type Request struct {
	Messages     []Message
	Tools        []ToolSpec
	Continuation string
}

// Response is either a final assistant response, one or more tool calls, or
// both. Tool calls are executed before the model is invoked again. Reasoning
// holds the chain-of-thought that reasoning models emit alongside Content; it
// is for display and persistence only and is resent to a provider solely
// according to the adapter's provider-specific policy.
type Response struct {
	Content      string
	Reasoning    string
	ToolCalls    []ToolCall
	Continuation string
}
