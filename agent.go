package dora

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"time"
)

const defaultMaxRounds = 256

// maxModelAttempts includes the initial request and all generic retries.
const maxModelAttempts = 6

// maxRateLimitAttempts bounds the number of times a single model call is
// retried after a rate-limit failure, which typically resolves with a longer
// wait.
const maxRateLimitAttempts = 5

var (
	// ErrMaxRounds indicates that a model kept requesting tools without
	// producing a final response.
	ErrMaxRounds = errors.New("maximum rounds exceeded")
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
	// maxOutputTokens is the model's hard output capacity. Zero means the model
	// does not advertise one, so request-level limits are left unclamped.
	maxOutputTokens int
	outputRecovery  OutputLimitRecovery
}

// AgentConfig controls immutable Agent behavior and safeguards. A zero
// MaxRounds uses the default limit; an empty SystemPrompt omits the system
// message for library callers.
type AgentConfig struct {
	MaxRounds    int
	SystemPrompt string
	// Nil enables one output-limit retry. A non-nil zero value disables it.
	OutputLimitRecovery *OutputLimitRecovery
}

// OutputLimitRecovery bounds retries of completed but truncated model responses.
// Each retry doubles the actual request budget, clamped to the optional cap and
// advertised model capacity. Unknown budgets cannot be increased safely.
type OutputLimitRecovery struct {
	MaxRetries      int
	MaxOutputTokens int
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
		return nil, errors.New("model is nil")
	}
	if cfg.MaxRounds < 0 {
		return nil, errors.New("MaxRounds cannot be negative")
	}
	recovery := OutputLimitRecovery{MaxRetries: 1}
	if cfg.OutputLimitRecovery != nil {
		recovery = *cfg.OutputLimitRecovery
	}
	if recovery.MaxRetries < 0 || recovery.MaxOutputTokens < 0 {
		return nil, errors.New("output recovery limits cannot be negative")
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
	maxOutputTokens := 0
	if os, ok := model.(OutputSize); ok {
		if v := os.MaxOutputTokens(); v > 0 {
			maxOutputTokens = v
		}
	}
	a := &Agent{
		model:           model,
		tools:           make(map[string]Tool, len(tools)),
		specs:           make([]ToolSpec, 0, len(tools)),
		maxRounds:       maxRounds,
		systemPrompt:    cfg.SystemPrompt,
		contextWindow:   contextWindow,
		maxOutputTokens: maxOutputTokens,
		outputRecovery:  recovery,
	}

	for _, tool := range tools {
		if tool == nil {
			return nil, errors.New("tool is nil")
		}

		spec := cloneToolSpec(tool.Spec())
		if spec.Name == "" {
			return nil, errors.New("tool name is empty")
		}
		if _, exists := a.tools[spec.Name]; exists {
			return nil, fmt.Errorf("duplicate tool %q", spec.Name)
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
func (a *Agent) RunObservedWithOptions(ctx context.Context, turn *Turn, observer Observer, opts RunOptions) (runErr error) {
	if a == nil || a.model == nil {
		return errors.New("agent is not initialized")
	}
	if turn == nil {
		return errors.New("turn is nil")
	}
	if turn.Completed() {
		return errors.New("turn is already complete")
	}
	defer func() { turn.runError = runErr }()
	ctx = context.WithValue(ctx, attemptTurnKey{}, turn)
	if opts.WorkingDirectory != "" {
		ctx = withWorkingDirectory(ctx, opts.WorkingDirectory)
	}
	if err := turn.bindSystem(a.systemPrompt); err != nil {
		return err
	}
	tools, specs := a.toolsForRun(opts)
	// modelHistory is the conversation snapshot actually visible to the model.
	// It deliberately stays separate from Turn, which retains the complete,
	// uncompressed history for persistence. After compaction, subsequent rounds
	// must continue from this snapshot so lastUsage always describes the same
	// history baseline instead of accidentally restoring messages that were not
	// present in the previous request.
	modelHistory := turn.Messages()
	// modelContinuation tracks provider-only state for modelHistory. Semantic
	// compaction replaces that history, so the old continuation must be cleared
	// at the same time instead of being read repeatedly from the complete Turn.
	modelContinuation := turn.Continuation()

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

		capacity, compactionErr := a.ensureContextCapacity(ctx, modelHistory, lastUsage, specs)
		if compactionErr != nil {
			return fmt.Errorf("compact context: %w", compactionErr)
		}
		if capacity.Compacted {
			modelContinuation = ""
			notify(observer, Update{
				Kind: UpdateInfo,
				Info: fmt.Sprintf(
					"Context compacted: %d -> %d predicted tokens",
					capacity.PredictedTokensBefore,
					capacity.PredictedTokensAfter,
				),
			})
		}
		request := Request{
			Messages:     capacity.Messages,
			Tools:        specs,
			Continuation: modelContinuation,
		}
		var response Response
		var err error
		if _, ok := a.model.(StreamingModel); ok {
			response, err = a.generateWithRecovery(ctx, request, func(event ModelEvent) {
				switch event.Kind {
				case ModelEventContentDelta:
					notify(observer, Update{Kind: UpdateContentDelta, Delta: event.Delta})
				case ModelEventReasoningDelta:
					notify(observer, Update{Kind: UpdateReasoningDelta, Delta: event.Delta})
				}
			}, observer)
		} else {
			response, err = a.generateWithRecovery(ctx, request, nil, observer)
		}
		if err != nil {
			return fmt.Errorf("generate response: %w", err)
		}
		// Anchor the next occupancy estimate on this call's real token usage.
		// Setting it after the response keeps a nil usage (providers that report
		// none) treated as a fallback to estimating the complete history.
		lastUsage = response.Usage
		modelContinuation = response.Continuation

		assistant := Message{
			Role:      RoleAssistant,
			Content:   response.Content,
			Reasoning: response.Reasoning,
			ToolCalls: cloneToolCalls(response.ToolCalls),
		}
		notify(observer, Update{Kind: UpdateMessageReceived, Message: assistant, Usage: response.Usage})

		if len(response.ToolCalls) == 0 {
			if err := turn.completeResponse(response); err != nil {
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
		// Advance the model-visible snapshot from exactly what was sent. Turn keeps
		// the full round independently, while future compaction and lastUsage stay
		// anchored to the already-compacted request history.
		modelHistory = cloneMessages(capacity.Messages)
		modelHistory = append(modelHistory, cloneMessage(assistant))
		modelHistory = append(modelHistory, cloneMessages(toolMessages)...)
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

// generateWithRetry retries the current model request, including interrupted
// partial streams. Only a complete response is committed or executes tools;
// previously emitted display deltas cannot be retracted.
func (a *Agent) generateWithRetry(ctx context.Context, request Request, emit func(ModelEvent), observer Observer) (Response, error) {
	wrapped := func(event ModelEvent) {
		if emit != nil {
			emit(event)
		}
	}

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		var response Response
		var err error
		if streaming, ok := a.model.(StreamingModel); ok {
			response, err = streaming.GenerateStream(ctx, request, wrapped)
		} else {
			response, err = a.model.Generate(ctx, request)
		}
		recordModelAttempt(ctx, request, response, err)
		if err == nil {
			return response, err
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
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
		notify(observer, Update{
			Kind: UpdateInfo,
			Info: fmt.Sprintf("Retrying model request %d/%d in %s: %v", attempt+2, limit, wait.Round(time.Millisecond), err),
		})
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
	base := time.Duration(2<<uint(attempt)) * time.Second
	if kind == RetryableRateLimit {
		base = time.Duration(5<<uint(attempt)) * time.Second
	}
	if kind != RetryableRateLimit && base > 30*time.Second {
		base = 30 * time.Second
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

// ValidateResponse rejects incomplete, empty, and ambiguous responses before
// either completing the turn or executing tools.
func ValidateResponse(response Response) error {
	switch response.FinishReason {
	case FinishOutputLimit:
		return ErrOutputLimit
	case FinishBlocked:
		return ErrModelBlocked
	case FinishStop:
		if len(response.ToolCalls) != 0 {
			return ErrInvalidModelResponse
		}
		if strings.TrimSpace(response.Content) == "" {
			return ErrEmptyResponse
		}
	case FinishToolCalls:
		if len(response.ToolCalls) == 0 {
			return ErrInvalidModelResponse
		}
		for _, call := range response.ToolCalls {
			if call.ID == "" || call.Name == "" {
				return ErrInvalidModelResponse
			}
		}
	default:
		return ErrUnknownFinishReason
	}
	return nil
}

var (
	ErrOutputLimit          = errors.New("model output budget exhausted")
	ErrEmptyResponse        = errors.New("model returned an empty final response")
	ErrUnknownFinishReason  = errors.New("model returned an unknown finish reason")
	ErrInvalidModelResponse = errors.New("model response conflicts with its finish reason")
	ErrModelBlocked         = errors.New("model response was blocked")
)

func (a *Agent) generateWithRecovery(ctx context.Context, request Request, emit func(ModelEvent), observer Observer) (Response, error) {
	for retry := 0; ; retry++ {
		attemptCtx := context.WithValue(ctx, attemptRecoveryKey{}, retry)
		response, err := a.generateWithRetry(attemptCtx, request, emit, observer)
		if err != nil {
			return response, err
		}
		if err := ctx.Err(); err != nil {
			setAttemptDisposition(ctx, "discarded")
			return response, err
		}
		err = ValidateResponse(response)
		if err == nil {
			disposition := "final"
			if response.FinishReason == FinishToolCalls {
				disposition = "tools"
			}
			setAttemptDisposition(ctx, disposition)
			return response, nil
		}
		setAttemptDisposition(ctx, "discarded")
		if !errors.Is(err, ErrOutputLimit) {
			return response, err
		}
		if ctx.Err() != nil {
			return response, ctx.Err()
		}
		budget := response.OutputBudget
		if budget <= 0 || retry >= a.outputRecovery.MaxRetries {
			return response, err
		}
		// Saturate before multiplication to avoid int overflow.
		next := int(^uint(0) >> 1)
		if budget <= next/2 {
			next = budget * 2
		}
		for _, cap := range []int{a.maxOutputTokens, a.outputRecovery.MaxOutputTokens} {
			if cap > 0 && next > cap {
				next = cap
			}
		}
		// Account for the larger output reservation without mutating history.
		occupied := estimateTokens(request.Messages) + estimateToolSpecTokens(request.Tools)
		if response.Usage != nil {
			occupied = int(response.Usage.InputTokens)
		}
		if available := a.contextWindow - occupied; next > available {
			next = available
		}
		if next <= budget {
			return response, err
		}
		request.MaxOutputTokens = &next
		notify(observer, Update{Kind: UpdateInfo, Info: fmt.Sprintf("Model output truncated; retrying from the last complete exchange with output budget %d -> %d (%d/%d)", budget, next, retry+1, a.outputRecovery.MaxRetries)})
	}
}
