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

// ToolResult is the successful output of a tool invocation. Content and
// Images are forwarded together in the tool message sent to the model.
type ToolResult struct {
	Content string
	Images  []Image
}

// ToolSpec describes a tool and the JSON object accepted by Execute.
type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

// ToolCall is a model request to execute a named tool.
type ToolCall struct {
	ID    string
	Name  string
	Input json.RawMessage
}
