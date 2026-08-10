package dora

import (
	"context"
	"errors"
	"fmt"
)

const defaultMaxRounds = 64

var (
	// ErrMaxRounds indicates that a model kept requesting tools without
	// producing a final response.
	ErrMaxRounds = errors.New("dora: maximum rounds exceeded")
)

// Agent runs the model-tool loop. It is immutable after construction and does
// not retain conversation state between Run calls.
type Agent struct {
	model     Model
	tools     map[string]Tool
	specs     []ToolSpec
	maxRounds int
}

// AgentConfig controls safeguards for the model-tool loop. A zero
// MaxRounds uses the default limit.
type AgentConfig struct {
	MaxRounds int
}

// New creates an Agent. Tool names must be non-empty and unique.
func New(model Model, tools ...Tool) (*Agent, error) {
	return NewWithConfig(model, AgentConfig{}, tools...)
}

// NewWithConfig creates an Agent with explicit loop safeguards.
func NewWithConfig(model Model, cfg AgentConfig, tools ...Tool) (*Agent, error) {
	if model == nil {
		return nil, errors.New("dora: model is nil")
	}
	if cfg.MaxRounds < 0 {
		return nil, errors.New("dora: MaxRounds cannot be negative")
	}
	maxRounds := cfg.MaxRounds
	if maxRounds == 0 {
		maxRounds = defaultMaxRounds
	}

	a := &Agent{
		model:     model,
		tools:     make(map[string]Tool, len(tools)),
		specs:     make([]ToolSpec, 0, len(tools)),
		maxRounds: maxRounds,
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
	return a.RunObserved(ctx, messages, nil)
}

// RunState resumes a conversation with optional opaque model continuation.
func (a *Agent) RunState(ctx context.Context, state State) (Result, error) {
	return a.RunStateObserved(ctx, state, nil)
}

// RunObserved is Run with synchronous progress notifications. The observer is
// optional and cannot modify the Agent's conversation history.
func (a *Agent) RunObserved(ctx context.Context, messages []Message, observer Observer) (Result, error) {
	return a.RunStateObserved(ctx, State{Messages: messages}, observer)
}

// RunStateObserved is RunState with synchronous progress notifications.
func (a *Agent) RunStateObserved(ctx context.Context, state State, observer Observer) (Result, error) {
	if a == nil || a.model == nil {
		return Result{}, errors.New("dora: agent is not initialized")
	}

	history := cloneMessages(state.Messages)
	continuation := state.Continuation

	for range a.maxRounds {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		notify(observer, Update{Kind: UpdateThinking})

		request := Request{
			Messages:     cloneMessages(history),
			Tools:        cloneToolSpecs(a.specs),
			Continuation: continuation,
		}
		var response Response
		var err error
		if streaming, ok := a.model.(StreamingModel); ok {
			response, err = streaming.GenerateStream(ctx, request, func(event ModelEvent) {
				if event.Kind == ModelEventContentDelta {
					notify(observer, Update{Kind: UpdateContentDelta, Delta: event.Delta})
				}
			})
		} else {
			response, err = a.model.Generate(ctx, request)
		}
		if err != nil {
			return Result{}, fmt.Errorf("dora: generate response: %w", err)
		}
		continuation = response.Continuation

		assistant := Message{
			Role:      RoleAssistant,
			Content:   response.Content,
			ToolCalls: cloneToolCalls(response.ToolCalls),
		}
		history = append(history, assistant)
		notify(observer, Update{Kind: UpdateMessageAdded, Message: assistant})

		if len(response.ToolCalls) == 0 {
			return Result{
				Content:      response.Content,
				Messages:     cloneMessages(history),
				Continuation: continuation,
			}, nil
		}

		for _, call := range response.ToolCalls {
			notify(observer, Update{Kind: UpdateToolStarted, ToolCall: call})
			tool, ok := a.tools[call.Name]
			if !ok {
				err := fmt.Errorf("dora: tool %q not found", call.Name)
				notify(observer, Update{Kind: UpdateToolFailed, ToolCall: call, Err: err})
				return Result{}, err
			}

			output, err := tool.Execute(ctx, cloneBytes(call.Input))
			if err != nil {
				err = fmt.Errorf("dora: execute tool %q: %w", call.Name, err)
				notify(observer, Update{Kind: UpdateToolFailed, ToolCall: call, Err: err})
				return Result{}, err
			}

			toolMessage := Message{
				Role:       RoleTool,
				Content:    output,
				ToolCallID: call.ID,
			}
			history = append(history, toolMessage)
			notify(observer, Update{Kind: UpdateMessageAdded, Message: toolMessage})
		}
	}

	return Result{
		Messages:     cloneMessages(history),
		Continuation: continuation,
	}, fmt.Errorf("%w (limit %d)", ErrMaxRounds, a.maxRounds)
}

func notify(observer Observer, update Update) {
	if observer == nil {
		return
	}
	update.Message = cloneMessage(update.Message)
	update.ToolCall = cloneToolCall(update.ToolCall)
	observer.Observe(update)
}

func cloneMessage(message Message) Message {
	message.ToolCalls = cloneToolCalls(message.ToolCalls)
	return message
}

func cloneToolCall(call ToolCall) ToolCall {
	call.Input = cloneBytes(call.Input)
	return call
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
