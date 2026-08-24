package dora

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

const defaultMaxRounds = 256

// maxModelAttempts bounds the number of times a single model call is retried
// after a generic retryable failure.
const maxModelAttempts = 3

// maxRateLimitAttempts bounds the number of times a single model call is
// retried after a rate-limit failure, which typically resolves with a longer
// wait.
const maxRateLimitAttempts = 5

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
	// systemPrompt is immutable Agent identity/configuration. Each Turn binds
	// a snapshot of it when the run starts.
	systemPrompt string
	// contextWindow is the model's context capacity in tokens, probed once at
	// construction and cached for compaction to consume
	// without re-asserting each round. It falls back to
	// DefaultContextWindowTokens when the model does not report a positive size.
	contextWindow int
}

// AgentConfig controls immutable Agent behavior and safeguards. A zero
// MaxRounds uses the default limit; an empty SystemPrompt omits the system
// message for library callers.
type AgentConfig struct {
	MaxRounds    int
	SystemPrompt string
}

// RunOptions controls one Agent run without mutating the Agent. ExcludeTools
// removes matching tools both from the definitions sent to the model and from
// the set of tools that may be executed during this run. WorkingDirectory is
// the base directory built-in tools use to resolve relative paths; an empty
// value preserves a directory already carried by ctx, or the process working
// directory when ctx has none.
type RunOptions struct {
	ExcludeTools     []string
	WorkingDirectory string
}

// New creates an Agent. Tool names must be non-empty and unique.
func New(model Model, tools ...Tool) (*Agent, error) {
	return NewWithConfig(model, AgentConfig{}, tools...)
}

// NewWithConfig creates an Agent with explicit immutable configuration.
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
	contextWindow := DefaultContextWindowTokens
	if cs, ok := model.(ContextSize); ok {
		if v := cs.ContextSize(); v > 0 {
			contextWindow = v
		}
	}
	a := &Agent{
		model:         model,
		tools:         make(map[string]Tool, len(tools)),
		specs:         make([]ToolSpec, 0, len(tools)),
		maxRounds:     maxRounds,
		systemPrompt:  cfg.SystemPrompt,
		contextWindow: contextWindow,
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

// Run invokes the model on turn until it returns a final response without tool
// calls. The turn owns all mutable run state.
func (a *Agent) Run(ctx context.Context, turn *Turn) error {
	return a.RunObserved(ctx, turn, nil)
}

// RunObserved is Run with synchronous progress notifications.
func (a *Agent) RunObserved(ctx context.Context, turn *Turn, observer Observer) error {
	return a.RunObservedWithOptions(ctx, turn, observer, RunOptions{})
}

// RunObservedWithOptions is RunObserved with per-run tool selection. The
// Agent remains immutable, so the same Agent may run independent Turns with
// different options concurrently.
func (a *Agent) RunObservedWithOptions(ctx context.Context, turn *Turn, observer Observer, opts RunOptions) error {
	if a == nil || a.model == nil {
		return errors.New("dora: agent is not initialized")
	}
	if turn == nil {
		return errors.New("dora: turn is nil")
	}
	if turn.Completed() {
		return errors.New("dora: turn is already complete")
	}
	if opts.WorkingDirectory != "" {
		ctx = withWorkingDirectory(ctx, opts.WorkingDirectory)
	}
	if err := turn.bindSystem(a.systemPrompt); err != nil {
		return err
	}
	tools, specs := a.toolsForRun(opts)

	// lastUsage is the real token usage of the most recently completed model call
	// in this run. It stays local to the loop (never stored on the immutable
	// Agent) so the compactor can anchor its occupancy estimate on the previous
	// round's true total_tokens without carrying conversation state across runs.
	var lastUsage *Usage
	for range a.maxRounds {
		if err := ctx.Err(); err != nil {
			return err
		}
		notify(observer, Update{Kind: UpdateThinking})

		request := Request{
			Messages:     a.requestMessages(turn.Messages(), lastUsage),
			Tools:        specs,
			Continuation: turn.Continuation(),
		}
		var response Response
		var err error
		if _, ok := a.model.(StreamingModel); ok {
			response, err = a.generateWithRetry(ctx, request, func(event ModelEvent) {
				switch event.Kind {
				case ModelEventContentDelta:
					notify(observer, Update{Kind: UpdateContentDelta, Delta: event.Delta})
				case ModelEventReasoningDelta:
					notify(observer, Update{Kind: UpdateReasoningDelta, Delta: event.Delta})
				}
			})
		} else {
			response, err = a.generateWithRetry(ctx, request, nil)
		}
		if err != nil {
			return fmt.Errorf("dora: generate response: %w", err)
		}
		// Anchor the next occupancy estimate on this call's real token usage.
		// Setting it after the response keeps a nil usage (providers that report
		// none) treated as a fallback to estimating the complete history.
		lastUsage = response.Usage

		assistant := Message{
			Role:      RoleAssistant,
			Content:   response.Content,
			Reasoning: response.Reasoning,
			ToolCalls: cloneToolCalls(response.ToolCalls),
		}
		notify(observer, Update{Kind: UpdateMessageReceived, Message: assistant, Usage: response.Usage})

		if len(response.ToolCalls) == 0 {
			if err := turn.completeWithUsage(response.Content, response.Continuation, response.Usage); err != nil {
				return err
			}
			return nil
		}

		// Execute all tool calls in parallel. Started events are emitted in the
		// model's call order before any finished event; finished events are then
		// emitted as results arrive, so a fast tool is displayed without waiting
		// for slower tools. Observer calls remain serialized on this goroutine,
		// while tool messages are stored by index to preserve model call order.
		type toolExecution struct {
			result ToolResult
			err    error
			// invalidJSON marks a call whose arguments were not valid JSON, so
			// the main goroutine can emit the dedicated recovery message.
			invalidJSON bool
		}
		type completedTool struct {
			index     int
			execution toolExecution
		}
		completed := make(chan completedTool, len(response.ToolCalls))
		toolMessages := make([]Message, len(response.ToolCalls))
		for i, call := range response.ToolCalls {
			startedAt := time.Now()
			notify(observer, Update{Kind: UpdateToolStarted, ToolCall: call, StartedAt: startedAt})
			go func(i int, call ToolCall) {
				var execution toolExecution
				tool, ok := tools[call.Name]
				if !ok {
					execution.err = fmt.Errorf("tool %q not found", call.Name)
					completed <- completedTool{index: i, execution: execution}
					return
				}
				if !json.Valid(call.Input) {
					execution.err = fmt.Errorf("arguments are not valid JSON: %s", call.Input)
					execution.invalidJSON = true
					completed <- completedTool{index: i, execution: execution}
					return
				}
				result, err := tool.Execute(ctx, cloneBytes(call.Input))
				if err != nil {
					execution.err = fmt.Errorf("execute tool %q: %w", call.Name, err)
				} else {
					execution.result = result
				}
				completed <- completedTool{index: i, execution: execution}
			}(i, call)
		}

		for range response.ToolCalls {
			done := <-completed
			call := response.ToolCalls[done.index]
			result := done.execution
			var toolMessage Message
			if result.err != nil {
				// A failed tool still reports its error message on the finish
				// event: that message is the one appended to toolMessages and
				// later persisted to the conversation, so the Observer sees the
				// exact same text the model will.
				if result.invalidJSON {
					toolMessage = Message{
						Role:       RoleTool,
						ToolCallID: call.ID,
						Content:    fmt.Sprintf("Error: the arguments for tool %q were not valid JSON: %s. Please provide valid JSON.", call.Name, call.Input),
					}
				} else {
					toolMessage = toolErrorMessage(call, result.err)
				}
			} else {
				toolMessage = Message{
					Role:       RoleTool,
					Content:    result.result.Content,
					ToolCallID: call.ID,
				}
			}
			toolMessages[done.index] = toolMessage
			notify(observer, Update{Kind: UpdateToolFinished, ToolCall: call, Message: toolMessage, Err: result.err})
		}
		if err := turn.AppendRound(Round{Assistant: assistant, Tools: toolMessages, Usage: response.Usage}, response.Continuation); err != nil {
			return err
		}
	}

	return fmt.Errorf("%w (limit %d)", ErrMaxRounds, a.maxRounds)
}

func (a *Agent) toolsForRun(opts RunOptions) (map[string]Tool, []ToolSpec) {
	if len(opts.ExcludeTools) == 0 {
		return a.tools, a.specs
	}
	excluded := make(map[string]struct{}, len(opts.ExcludeTools))
	for _, name := range opts.ExcludeTools {
		excluded[name] = struct{}{}
	}
	tools := make(map[string]Tool, len(a.tools))
	for name, tool := range a.tools {
		if _, skip := excluded[name]; !skip {
			tools[name] = tool
		}
	}
	specs := make([]ToolSpec, 0, len(a.specs))
	for _, spec := range a.specs {
		if _, skip := excluded[spec.Name]; !skip {
			specs = append(specs, spec)
		}
	}
	return tools, specs
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
		if err == nil || emitted {
			return response, err
		}
		var retryable *RetryableError
		if !errors.As(err, &retryable) {
			return response, err
		}
		limit := maxModelAttempts
		if retryable.Kind == RetryableRateLimit {
			limit = maxRateLimitAttempts
		}
		if attempt >= limit-1 {
			return response, err
		}
		wait := retryBackoff(attempt, retryable.RetryAfter, retryable.Kind)
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return response, ctx.Err()
		}
	}
}

// retryBackoff computes the delay before the next attempt: the server's
// RetryAfter when provided, otherwise exponential backoff with jitter. Rate
// limit failures use a longer base so they wait longer between attempts.
func retryBackoff(attempt int, retryAfter time.Duration, kind RetryableErrorKind) time.Duration {
	if retryAfter > 0 {
		return retryAfter
	}
	base := time.Duration(1<<uint(attempt)) * time.Second
	if kind == RetryableRateLimit {
		base = time.Duration(5<<uint(attempt)) * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(base) / 2))
	return base + jitter
}

func notify(observer Observer, update Update) {
	if observer == nil {
		return
	}
	update.Message = cloneMessage(update.Message)
	update.ToolCall = cloneToolCall(update.ToolCall)
	update.Usage = cloneUsage(update.Usage)
	observer.Observe(update)
}

// toolErrorMessage describes a failed tool call so the model can correct it.
func toolErrorMessage(call ToolCall, err error) Message {
	return Message{
		Role:       RoleTool,
		ToolCallID: call.ID,
		Content:    fmt.Sprintf("Error: tool %q failed: %v. Please correct your arguments and try again.", call.Name, err),
	}
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

func cloneUsage(usage *Usage) *Usage {
	if usage == nil {
		return nil
	}
	cloned := *usage
	if usage.InputDetails != nil {
		input := *usage.InputDetails
		input.CachedTokens = cloneInt64(input.CachedTokens)
		input.AudioTokens = cloneInt64(input.AudioTokens)
		cloned.InputDetails = &input
	}
	if usage.OutputDetails != nil {
		output := *usage.OutputDetails
		output.ReasoningTokens = cloneInt64(output.ReasoningTokens)
		output.AudioTokens = cloneInt64(output.AudioTokens)
		output.AcceptedPredictionTokens = cloneInt64(output.AcceptedPredictionTokens)
		output.RejectedPredictionTokens = cloneInt64(output.RejectedPredictionTokens)
		cloned.OutputDetails = &output
	}
	return &cloned
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
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
