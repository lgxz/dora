package dora

import "context"

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
