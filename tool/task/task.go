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

// BackgroundStarter starts one instruction outside the calling tool context
// and returns the job ID used to manage it.
type BackgroundStarter func(string) (string, error)

// Tool delegates instructions to an injected Runner.
type Tool struct {
	runner            Runner
	backgroundStarter BackgroundStarter
}

// New creates a task tool. Runner is invoked only by Execute, so it may close
// over an Agent that is assigned after the tool itself is constructed.
func New(runner Runner) *Tool {
	return &Tool{runner: runner}
}

// SetBackgroundStarter enables background execution. It must be called before
// the Tool is exposed to an Agent and is not safe to mutate during execution.
func (t *Tool) SetBackgroundStarter(starter BackgroundStarter) {
	t.backgroundStarter = starter
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        Name,
		Description: "Run an instruction in a fresh, independent conversation context and return the final result.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "instruction": {"type": "string", "description": "A complete, self-contained instruction for the subagent"},
    "background": {"type": "boolean", "description": "Run in the background and immediately return a job_id. Default false"}
  },
  "required": ["instruction"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	input, err := decodeInput(raw)
	if err != nil {
		return dora.ToolResult{}, err
	}
	if t.runner == nil {
		return dora.ToolResult{}, errors.New("task: no runner configured")
	}
	if input.Background {
		if t.backgroundStarter == nil {
			return dora.ToolResult{}, errors.New("task: no background starter configured")
		}
		jobID, err := t.backgroundStarter(input.Instruction)
		if err != nil {
			return dora.ToolResult{}, fmt.Errorf("task: start background: %w", err)
		}
		content, _ := json.Marshal(struct {
			JobID   string `json:"job_id"`
			Status  string `json:"status"`
			Message string `json:"message"`
		}{
			JobID:   jobID,
			Status:  "running",
			Message: "Task is running in the background. Use the job tool to check status before Dora exits.",
		})
		return dora.ToolResult{Content: string(content)}, nil
	}
	result, err := t.runner(ctx, input.Instruction)
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("task: %w", err)
	}
	return dora.ToolResult{Content: result}, nil
}

type input struct {
	Instruction string `json:"instruction"`
	Background  bool   `json:"background,omitempty"`
}

func decodeInput(raw json.RawMessage) (input, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return input{}, fmt.Errorf("task: decode input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input{}, errors.New("task: input must contain one JSON value")
		}
		return input{}, fmt.Errorf("task: decode input: %w", err)
	}
	value.Instruction = strings.TrimSpace(value.Instruction)
	if value.Instruction == "" {
		return input{}, errors.New("task: instruction is required")
	}
	return value, nil
}

var _ dora.Tool = (*Tool)(nil)
