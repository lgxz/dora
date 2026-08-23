// Package task implements a dora.Tool that runs an instruction in a fresh
// Agent turn and returns the final result to the calling model.
package task

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/lgxz/dora"
)

// Name is the tool name exposed to models. Callers can use it when excluding
// the tool from nested Agent runs.
const Name = "task"

// Runner executes one instruction in an independent conversation context.
type Runner func(context.Context, string) (string, error)

// Tool delegates instructions to an injected Runner.
type Tool struct {
	runner Runner
}

// New creates a task tool. Runner is invoked only by Execute, so it may close
// over an Agent that is assigned after the tool itself is constructed.
func New(runner Runner) *Tool {
	return &Tool{runner: runner}
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        Name,
		Description: "Run an instruction in a fresh, independent conversation context and return the final result. The subagent can use the other available tools but cannot call task recursively. Context isolation does not isolate the filesystem, process, working directory, or permissions.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "instruction": {"type": "string", "description": "A complete, self-contained instruction for the subagent"}
  },
  "required": ["instruction"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	instruction, err := decodeInstruction(raw)
	if err != nil {
		return dora.ToolResult{}, err
	}
	if t.runner == nil {
		return dora.ToolResult{}, errors.New("task: no runner configured")
	}
	result, err := t.runner(ctx, instruction)
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("task: %w", err)
	}
	return dora.ToolResult{Content: result}, nil
}

func decodeInstruction(raw json.RawMessage) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var input struct {
		Instruction string `json:"instruction"`
	}
	if err := decoder.Decode(&input); err != nil {
		return "", fmt.Errorf("task: decode input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return "", errors.New("task: input must contain one JSON value")
		}
		return "", fmt.Errorf("task: decode input: %w", err)
	}
	instruction := strings.TrimSpace(input.Instruction)
	if instruction == "" {
		return "", errors.New("task: instruction is required")
	}
	return instruction, nil
}

var _ dora.Tool = (*Tool)(nil)
