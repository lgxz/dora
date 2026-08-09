package dora

import "context"

// Model produces the next assistant response for a conversation.
type Model interface {
	Generate(context.Context, Request) (Response, error)
}

// Request contains the complete conversation and the tools available to the
// model for the next response.
type Request struct {
	Messages []Message
	Tools    []ToolSpec
}

// Response is either a final assistant response, one or more tool calls, or
// both. Tool calls are executed before the model is invoked again.
type Response struct {
	Content   string
	ToolCalls []ToolCall
}
