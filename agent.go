package dora

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"
)

const defaultMaxRounds = 256

// maxModelAttempts bounds the number of times a single model call is retried
// after a retryable failure.
const maxModelAttempts = 3

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
			Tools:        a.specs,
			Continuation: continuation,
		}
		var response Response
		var err error
		if _, ok := a.model.(StreamingModel); ok {
			response, err = a.generateWithRetry(ctx, request, func(event ModelEvent) {
				if event.Kind == ModelEventContentDelta {
					notify(observer, Update{Kind: UpdateContentDelta, Delta: event.Delta})
				}
			})
		} else {
			response, err = a.generateWithRetry(ctx, request, nil)
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

		// Execute all tool calls in parallel, but preserve the model's call
		// order for results and Observer events. Each goroutine performs the
		// unknown-tool check, JSON validation, and Execute; the results are
		// collected by index. Observer events are emitted serially by the main
		// goroutine after all goroutines finish, so the progress renderer's
		// non-concurrent map is never written concurrently.
		type toolResult struct {
			output string
			err    error
			// startedAt records the real time the tool began executing, so the
			// progress renderer can report an accurate duration even though the
			// UpdateToolStarted event is emitted after all goroutines finish.
			startedAt time.Time
			// invalidJSON marks a call whose arguments were not valid JSON, so
			// the main goroutine can emit the dedicated recovery message.
			invalidJSON bool
		}
		results := make([]toolResult, len(response.ToolCalls))
		var wg sync.WaitGroup
		for i, call := range response.ToolCalls {
			wg.Add(1)
			go func(i int, call ToolCall) {
				defer wg.Done()
				tool, ok := a.tools[call.Name]
				if !ok {
					results[i].err = fmt.Errorf("tool %q not found", call.Name)
					return
				}
				if !json.Valid(call.Input) {
					results[i].err = fmt.Errorf("arguments are not valid JSON: %s", call.Input)
					results[i].invalidJSON = true
					return
				}
				results[i].startedAt = time.Now()
				output, err := tool.Execute(ctx, cloneBytes(call.Input))
				if err != nil {
					results[i].err = fmt.Errorf("execute tool %q: %w", call.Name, err)
					return
				}
				results[i].output = output
			}(i, call)
		}
		wg.Wait()

		for i, call := range response.ToolCalls {
			notify(observer, Update{Kind: UpdateToolStarted, ToolCall: call, StartedAt: results[i].startedAt})

			result := results[i]
			if result.err != nil {
				if result.invalidJSON {
					notify(observer, Update{Kind: UpdateToolFailed, ToolCall: call, Err: result.err})
					history = append(history, Message{
						Role:       RoleTool,
						ToolCallID: call.ID,
						Content:    fmt.Sprintf("Error: the arguments for tool %q were not valid JSON: %s. Please provide valid JSON.", call.Name, call.Input),
					})
					continue
				}
				notify(observer, Update{Kind: UpdateToolFailed, ToolCall: call, Err: result.err})
				feedToolError(&history, call, result.err)
				continue
			}

			toolMessage := Message{
				Role:       RoleTool,
				Content:    result.output,
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

// generateWithRetry invokes the model, retrying retryable failures with
// exponential backoff and jitter. A stream is only retried when it failed
// before emitting any content; once partial content has been emitted the
// error is surfaced directly to avoid duplicate or partial output.
func (a *Agent) generateWithRetry(ctx context.Context, request Request, emit func(ModelEvent)) (Response, error) {
	var emitted bool
	wrapped := func(event ModelEvent) {
		emitted = true
		if emit != nil {
			emit(event)
		}
	}

	for attempt := 0; ; attempt++ {
		var response Response
		var err error
		if streaming, ok := a.model.(StreamingModel); ok {
			response, err = streaming.GenerateStream(ctx, request, wrapped)
		} else {
			response, err = a.model.Generate(ctx, request)
		}
		if err == nil || attempt >= maxModelAttempts-1 || emitted {
			return response, err
		}
		var retryable *RetryableError
		if !errors.As(err, &retryable) {
			return response, err
		}
		wait := retryBackoff(attempt, retryable.RetryAfter)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return response, ctx.Err()
		}
	}
}

// retryBackoff computes the delay before the next attempt: the server's
// RetryAfter when provided, otherwise exponential backoff with jitter.
func retryBackoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	base := time.Duration(1<<uint(attempt)) * time.Second
	jitter := time.Duration(rand.Int63n(int64(base) / 2))
	return base + jitter
}

func notify(observer Observer, update Update) {
	if observer == nil {
		return
	}
	update.Message = cloneMessage(update.Message)
	update.ToolCall = cloneToolCall(update.ToolCall)
	observer.Observe(update)
}

// feedToolError appends a RoleTool message describing a failed tool call so the
// model can correct itself, and is used instead of aborting the run. The error
// is correlated to the original call via call.ID.
func feedToolError(history *[]Message, call ToolCall, err error) {
	*history = append(*history, Message{
		Role:       RoleTool,
		ToolCallID: call.ID,
		Content:    fmt.Sprintf("Error: tool %q failed: %v. Please correct your arguments and try again.", call.Name, err),
	})
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

func cloneToolSpec(spec ToolSpec) ToolSpec {
	spec.InputSchema = cloneBytes(spec.InputSchema)
	return spec
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
