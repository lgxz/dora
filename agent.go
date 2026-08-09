package dora

import (
	"context"
	"errors"
	"fmt"
)

const maxModelCalls = 16

var (
	// ErrMaxModelCalls indicates that a model kept requesting tools without
	// producing a final response.
	ErrMaxModelCalls = errors.New("dora: maximum model calls exceeded")
)

// Agent runs the model-tool loop. It is immutable after construction and does
// not retain conversation state between Run calls.
type Agent struct {
	model Model
	tools map[string]Tool
	specs []ToolSpec
}

// New creates an Agent. Tool names must be non-empty and unique.
func New(model Model, tools ...Tool) (*Agent, error) {
	if model == nil {
		return nil, errors.New("dora: model is nil")
	}

	a := &Agent{
		model: model,
		tools: make(map[string]Tool, len(tools)),
		specs: make([]ToolSpec, 0, len(tools)),
	}

	for _, tool := range tools {
		if tool == nil {
			return nil, errors.New("dora: tool is nil")
		}

		spec := cloneToolSpec(tool.Spec())
		if spec.Name == "" {
			return nil, errors.New("dora: tool name is empty")
		}
		if _, exists := a.tools[spec.Name]; exists {
			return nil, fmt.Errorf("dora: duplicate tool %q", spec.Name)
		}

		a.tools[spec.Name] = tool
		a.specs = append(a.specs, spec)
	}

	return a, nil
}

// Run invokes the model until it returns a response without tool calls.
// Messages supplied by the caller are copied and never modified.
func (a *Agent) Run(ctx context.Context, messages []Message) (Result, error) {
	if a == nil || a.model == nil {
		return Result{}, errors.New("dora: agent is not initialized")
	}

	history := cloneMessages(messages)

	for range maxModelCalls {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}

		response, err := a.model.Generate(ctx, Request{
			Messages: cloneMessages(history),
			Tools:    cloneToolSpecs(a.specs),
		})
		if err != nil {
			return Result{}, fmt.Errorf("dora: generate response: %w", err)
		}

		assistant := Message{
			Role:      RoleAssistant,
			Content:   response.Content,
			ToolCalls: cloneToolCalls(response.ToolCalls),
		}
		history = append(history, assistant)

		if len(response.ToolCalls) == 0 {
			return Result{
				Content:  response.Content,
				Messages: cloneMessages(history),
			}, nil
		}

		for _, call := range response.ToolCalls {
			tool, ok := a.tools[call.Name]
			if !ok {
				return Result{}, fmt.Errorf("dora: tool %q not found", call.Name)
			}

			output, err := tool.Execute(ctx, cloneBytes(call.Input))
			if err != nil {
				return Result{}, fmt.Errorf("dora: execute tool %q: %w", call.Name, err)
			}

			history = append(history, Message{
				Role:       RoleTool,
				Content:    output,
				ToolCallID: call.ID,
			})
		}
	}

	return Result{}, ErrMaxModelCalls
}

func cloneMessages(messages []Message) []Message {
	if messages == nil {
		return nil
	}
	cloned := make([]Message, len(messages))
	for i, message := range messages {
		cloned[i] = message
		cloned[i].ToolCalls = cloneToolCalls(message.ToolCalls)
	}
	return cloned
}

func cloneToolCalls(calls []ToolCall) []ToolCall {
	if calls == nil {
		return nil
	}
	cloned := make([]ToolCall, len(calls))
	for i, call := range calls {
		cloned[i] = call
		cloned[i].Input = cloneBytes(call.Input)
	}
	return cloned
}

func cloneToolSpecs(specs []ToolSpec) []ToolSpec {
	if specs == nil {
		return nil
	}
	cloned := make([]ToolSpec, len(specs))
	for i, spec := range specs {
		cloned[i] = cloneToolSpec(spec)
	}
	return cloned
}

func cloneToolSpec(spec ToolSpec) ToolSpec {
	spec.InputSchema = cloneBytes(spec.InputSchema)
	return spec
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
