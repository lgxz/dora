package dora

import (
	"context"
	"encoding/json"
)

// Tool exposes one capability to a model.
type Tool interface {
	Spec() ToolSpec
	Execute(context.Context, json.RawMessage) (ToolResult, error)
}

// ExclusiveTool is an optional interface for a Tool whose calls change state
// that other calls in the same model response can observe or depend on, such as
// files or processes.
//
// The Agent runs the tool calls of one response concurrently, except around an
// exclusive call: it starts only after every earlier call in the model's order
// has finished, and no later call starts until it has finished. Calls that do
// not implement the interface, or return false, keep running concurrently with
// their neighbours, so a Tool must otherwise be safe for concurrent use.
type ExclusiveTool interface {
	Tool
	// Exclusive reports whether calls must not overlap with any other call of
	// the same response.
	Exclusive() bool
}

// ToolResult is the successful output of a tool invocation.
type ToolResult struct {
	Content string
}

// ToolSpec describes a tool and the JSON object accepted by Execute.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolCall is a model request to execute a named tool.
type ToolCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}
