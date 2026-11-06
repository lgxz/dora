package dora

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/lgxz/dora/internal/capstr"
)

const (
	compactionTriggerRatio = 0.8
	summaryTargetRatio     = 0.2
	reservedOutputTokens   = 8192
	maxCompactionAttempts  = 2

	// taskKeepRatio and tailKeepRatio bound what compaction keeps verbatim:
	// the original task (truncated beyond this) and the most recent whole
	// rounds. Together with the summary target they stay far below the
	// trigger, so a compaction always frees most of the window.
	taskKeepRatio = 0.05
	tailKeepRatio = 0.10

	// summaryWindowMarginRatio is the share of the context window kept free
	// when sizing a summary request, because input estimates are heuristic and
	// some backends reject a request whose input plus max_tokens exceeds the
	// window.
	summaryWindowMarginRatio = 0.05
	// minSummaryOutputTokens is the smallest output budget worth asking for. A
	// smaller room is still used as a best effort rather than refusing locally
	// on a heuristic the provider can judge precisely.
	minSummaryOutputTokens = 1024

	// ASCII prose and JSON average roughly four bytes per token across the
	// supported tokenizer families. Non-ASCII runes are counted individually so
	// CJK text is not underestimated by the ASCII ratio.
	estimatedASCIIBytesPerToken = 4
	// Account for provider-side role and framing tokens that are not present in
	// Message.Content.
	estimatedMessageOverheadTokens = 4
)

const compactionPrompt = `Create a concise but comprehensive summary of the conversation above. The original task and the most recent messages are kept verbatim next to your summary, so concentrate on what happened in between and what the summary must add to them.

Preserve:
- explicit requirements and constraints, including any given after the original task
- decisions already made and why they were made
- completed work, important file paths, code changes, and command results
- critical tool output, errors, and their resolutions
- the exact current state and any remaining work

Omit repetition, conversational filler, and failed attempts that produced no useful insight. The summary, together with the verbatim task and recent messages, must let another model continue the task without access to the omitted messages.`

// CompactionResult describes the capacity decision and the model-visible
// history produced by it. Token counts are predictions: they may combine
// provider-reported usage with provider-neutral local estimates.
type CompactionResult struct {
	Messages              []Message
	Compacted             bool
	PredictedTokensBefore int
	PredictedTokensAfter  int
	SummaryTokens         int
	ContextWindow         int
	TriggerTokens         int
	TargetTokens          int
	Attempts              int
	// TrimmedToolResults counts tool results shortened in the summary request
	// so it fits the context window. Only that request is affected; the
	// conversation history is never modified.
	TrimmedToolResults int
	// KeptMessages is the number of most recent messages carried over
	// verbatim after the summary when Compacted is true.
	KeptMessages int
	// TaskTruncated reports that the original task was longer than its
	// verbatim budget and was shortened in the compacted history.
	TaskTruncated bool
}

// compactOptions tunes one capacity decision.
type compactOptions struct {
	// Task is the original user request, re-attached verbatim after every
	// compaction, including repeated ones where the history no longer holds
	// it as a separate message. Nil uses the first non-system message.
	Task *Message
	// Force compacts regardless of the trigger. It is used after the provider
	// rejected a request as too large, which proves the local estimates are
	// too low: the history is then assumed to fill the window.
	Force bool
}

type summaryResult struct {
	content  string
	tokens   int
	attempts int
	trimmed  int
}

// summaryPlan is the input and output budget chosen for the summary request.
type summaryPlan struct {
	messages        []Message
	maxOutputTokens int
	trimmed         int
}

// summaryToolResultCaps are the per-result byte caps tried, largest first, when
// the history does not leave room for the summary in the context window.
var summaryToolResultCaps = []int{32 << 10, 16 << 10, 8 << 10, 4 << 10, 2 << 10, 1 << 10}

// ensureContextCapacity returns the model-visible history for the next normal
// request. It leaves history unchanged below the trigger. At or above the
// trigger it asks the active model for a summary of the middle of the
// conversation, validates the result, and atomically returns system messages,
// the original task plus that summary, and the most recent rounds verbatim.
// Failure never falls back to deleting or truncating local history.
func (a *Agent) ensureContextCapacity(ctx context.Context, history []Message, lastUsage *Usage, specs []ToolSpec) (CompactionResult, error) {
	return a.compactContext(ctx, history, lastUsage, specs, compactOptions{})
}

func (a *Agent) compactContext(ctx context.Context, history []Message, lastUsage *Usage, specs []ToolSpec, opts compactOptions) (CompactionResult, error) {
	predictedBefore := a.predictedTokens(history, lastUsage, specs)
	result := CompactionResult{
		Messages:              history,
		PredictedTokensBefore: predictedBefore,
		PredictedTokensAfter:  predictedBefore,
		ContextWindow:         a.contextWindow,
		TriggerTokens:         a.compactionTrigger(),
		TargetTokens:          a.summaryTarget(),
	}
	if len(history) == 0 || (!opts.Force && predictedBefore < result.TriggerTokens) {
		return result, nil
	}
	head := firstNonSystem(history)
	if head >= len(history) {
		return result, nil
	}

	// The occupancy estimate includes the output reserve and the tool schemas;
	// the summary request carries neither. Never go below the local estimate
	// of the history itself.
	baseInput := predictedBefore - a.outputReserve() - estimateToolSpecTokens(specs)
	if local := estimateTokens(history); baseInput < local {
		baseInput = local
	}
	if opts.Force {
		if full := a.contextWindow - a.outputReserve() - estimateToolSpecTokens(specs); baseInput < full {
			baseInput = full
		}
	}

	// Everything before the verbatim tail (system prompt, task and middle) is summarized; the tail
	// itself is not sent to the summarizer, so it is removed from the input
	// estimate.
	tailStart := a.verbatimTailStart(history, head)
	middle, tail := history[:tailStart], history[tailStart:]
	middleInput := baseInput - estimateTokens(tail)
	if local := estimateTokens(middle); middleInput < local {
		middleInput = local
	}
	summary, err := a.generateContextSummaryFrom(ctx, middle, result.TargetTokens, middleInput)
	result.SummaryTokens = summary.tokens
	result.Attempts = summary.attempts
	result.TrimmedToolResults = summary.trimmed
	if err != nil {
		return result, err
	}

	task := history[head]
	if opts.Task != nil {
		task = *opts.Task
	}
	task, result.TaskTruncated = a.capTask(task)
	compacted := make([]Message, 0, head+1+len(tail))
	for _, message := range history[:head] {
		compacted = append(compacted, cloneMessage(message))
	}
	compacted = append(compacted, compactedUserMessage(task, summary.content))
	compacted = append(compacted, cloneMessages(tail)...)
	result.PredictedTokensAfter = a.predictedTokens(compacted, nil, specs)
	if result.PredictedTokensAfter >= a.contextWindow {
		return result, fmt.Errorf(
			"request does not fit after compaction: %d >= %d tokens",
			result.PredictedTokensAfter,
			a.contextWindow,
		)
	}
	result.Messages = compacted
	result.Compacted = true
	result.KeptMessages = len(tail)
	return result, nil
}

// firstNonSystem returns the index of the first message that is not a leading
// system message, which is the task (or its compacted replacement).
func firstNonSystem(history []Message) int {
	for i, message := range history {
		if message.Role != RoleSystem {
			return i
		}
	}
	return len(history)
}

// verbatimTailStart returns the index where the verbatim tail begins. The tail
// is made of whole rounds (an assistant message and the tool results that
// answer it), newest first, while they fit tailKeepRatio of the window, so a
// tool call is never separated from its result. The oldest round is never
// part of the tail: something must remain to summarize, and a single round
// larger than the budget is summarized rather than kept.
func (a *Agent) verbatimTailStart(history []Message, head int) int {
	var starts []int
	for i := head + 1; i < len(history); i++ {
		if history[i].Role == RoleAssistant {
			starts = append(starts, i)
		}
	}
	budget := int(float64(a.contextWindow) * tailKeepRatio)
	tailStart := len(history)
	for k := len(starts) - 1; k >= 1; k-- {
		if estimateTokens(history[starts[k]:]) > budget {
			break
		}
		tailStart = starts[k]
	}
	return tailStart
}

// capTask returns the task shortened to taskKeepRatio of the window, keeping
// its beginning and end. The summarizer still sees the complete task.
func (a *Agent) capTask(task Message) (Message, bool) {
	task = cloneMessage(task)
	budget := int(float64(a.contextWindow) * taskKeepRatio)
	if budget < 1 || estimateTextTokens(task.Content) <= budget {
		return task, false
	}
	limit := budget * estimatedASCIIBytesPerToken
	for range 8 {
		task.Content = capstr.HeadTail(task.Content, limit, limit*3/4,
			"\n... [the task was %d bytes in total; the middle is omitted here] ...\n")
		if estimateTextTokens(task.Content) <= budget || limit <= 64 {
			break
		}
		limit /= 2
	}
	return task, true
}

// compactedUserMessage joins the verbatim task and the summary of what
// followed into the single user message that opens the compacted history.
func compactedUserMessage(task Message, summary string) Message {
	return Message{
		Role:   RoleUser,
		Images: task.Images,
		Content: "Original task (verbatim):\n\n" + task.Content +
			"\n\n---\n\nSummary of the work so far (older messages were compacted; any recent messages follow unchanged):\n\n" + summary,
	}
}

func (a *Agent) compactionTrigger() int {
	trigger := int(float64(a.contextWindow) * compactionTriggerRatio)
	if trigger < 1 {
		return 1
	}
	return trigger
}

func (a *Agent) summaryTarget() int {
	target := int(float64(a.contextWindow) * summaryTargetRatio)
	if target < 1 {
		target = 1
	}
	if a.maxOutputTokens > 0 && target > a.maxOutputTokens {
		target = a.maxOutputTokens
	}
	return target
}

func (a *Agent) outputReserve() int {
	reserve := reservedOutputTokens
	// Keep small context models usable while preserving the 80/20 policy.
	if maximum := a.contextWindow / 5; reserve > maximum {
		reserve = maximum
	}
	return reserve
}

// predictedTokens estimates the next request including its output reserve.
// Reported total_tokens already covers the preceding request, its tool schema,
// and the assistant response, so only tool results created afterwards are
// added. Without reported usage the complete history and tool schema are
// estimated locally.
func (a *Agent) predictedTokens(history []Message, lastUsage *Usage, specs []ToolSpec) int {
	occupied := TotalTokens(lastUsage)
	if occupied > 0 {
		occupied += estimateTrailingToolResultTokens(history)
	} else {
		occupied = estimateTokens(history) + estimateToolSpecTokens(specs)
	}
	return occupied + a.outputReserve()
}

func (a *Agent) generateContextSummary(ctx context.Context, history []Message, targetTokens int) (summaryResult, error) {
	return a.generateContextSummaryFrom(ctx, history, targetTokens, estimateTokens(history))
}

// summaryPrompt builds the instruction for one attempt. The retry asks for half
// the budget: models cannot count tokens, so a stricter request needs a visibly
// smaller number to change the outcome.
func summaryPrompt(limitTokens, attempt int) string {
	strictness := ""
	if attempt > 0 {
		strictness = " The previous summary was too long; be substantially more concise."
		limitTokens /= 2
		if limitTokens < 1 {
			limitTokens = 1
		}
	}
	return fmt.Sprintf(
		"%s%s The replacement summary must be at most approximately %d tokens. Return only the summary.",
		compactionPrompt,
		strictness,
		limitTokens,
	)
}

// planSummaryRequest sizes the summary request against the context window.
// baseInput is the calibrated estimate of the history's tokens. When the
// history plus the summary budget would not fit, it first shortens oversized
// tool results in a copy used only for this request, from large caps to small,
// then lowers the output budget to the room that remains. It never fails
// locally: with no room to gain it sends the request anyway and lets the
// provider decide with exact counts.
func (a *Agent) planSummaryRequest(history []Message, targetTokens, baseInput int) summaryPlan {
	plan := summaryPlan{messages: history, maxOutputTokens: targetTokens}
	if a.contextWindow <= 0 {
		return plan
	}
	promptTokens := estimateTextTokens(summaryPrompt(targetTokens, 1)) + estimatedMessageOverheadTokens
	margin := int(float64(a.contextWindow) * summaryWindowMarginRatio)
	room := func(input int) int { return a.contextWindow - margin - input }

	if room(baseInput+promptTokens) >= targetTokens {
		return plan
	}

	localInput := estimateTokens(history)
	bestInput := baseInput + promptTokens
	for _, limit := range summaryToolResultCaps {
		shortened, count := shortenToolResults(history, limit)
		if count == 0 {
			continue
		}
		input := baseInput - (localInput - estimateTokens(shortened)) + promptTokens
		if input < bestInput {
			plan.messages, plan.trimmed, bestInput = shortened, count, input
		}
		if room(input) >= targetTokens {
			break
		}
	}

	if available := room(bestInput); available < targetTokens {
		floor := minSummaryOutputTokens
		if floor > targetTokens {
			floor = targetTokens
		}
		if available < floor {
			available = floor
		}
		plan.maxOutputTokens = available
	}
	return plan
}

// shortenToolResults returns a copy of messages in which every tool result
// longer than maxBytes keeps its head and tail around a marker, and how many
// were shortened. The input is not modified.
func shortenToolResults(messages []Message, maxBytes int) ([]Message, int) {
	var shortened []Message
	count := 0
	for i, message := range messages {
		if message.Role != RoleTool || len(message.Content) <= maxBytes {
			continue
		}
		if shortened == nil {
			shortened = make([]Message, len(messages))
			copy(shortened, messages)
		}
		shortened[i].Content = capstr.HeadTail(message.Content, maxBytes, maxBytes*3/4,
			"\n... [%d bytes in total; the middle is omitted from this summary request to fit the context window] ...\n")
		count++
	}
	if count == 0 {
		return messages, 0
	}
	return shortened, count
}

func (a *Agent) generateContextSummaryFrom(ctx context.Context, history []Message, targetTokens, baseInput int) (summaryResult, error) {
	var result summaryResult
	plan := a.planSummaryRequest(history, targetTokens, baseInput)
	result.trimmed = plan.trimmed
	limit := plan.maxOutputTokens
	lastError := errors.New("summary was empty")
	replanned := false
	for attempt := 0; attempt < maxCompactionAttempts; attempt++ {
		result.attempts = attempt + 1
		messages := cloneMessages(plan.messages)
		messages = append(messages, Message{Role: RoleUser, Content: summaryPrompt(limit, attempt)})
		response, err := a.generateWithRetry(context.WithValue(ctx, attemptPurposeKey{}, "compaction"), Request{
			Messages:        messages,
			MaxOutputTokens: &limit,
		}, nil, nil)
		if err != nil && !replanned && errors.Is(err, ErrContextLength) {
			// The summary request itself was too large: the estimate was too
			// low. Plan once more assuming a larger input, which shortens more
			// tool results and lowers the output budget, and repeat this
			// attempt.
			replanned = true
			pessimistic := baseInput + baseInput/2
			if pessimistic < a.contextWindow {
				pessimistic = a.contextWindow
			}
			plan = a.planSummaryRequest(history, targetTokens, pessimistic)
			result.trimmed = plan.trimmed
			limit = plan.maxOutputTokens
			attempt--
			continue
		}
		if err != nil {
			return result, fmt.Errorf("generate summary: %w", err)
		}
		setAttemptDisposition(ctx, "discarded")
		if err := ValidateResponse(response); err != nil {
			switch {
			case errors.Is(err, ErrEmptyResponse):
				// Handled below together with other empty summaries.
			case errors.Is(err, ErrOutputLimit):
				// A summary that outgrows its budget is cut off by the output
				// limit, so this is the usual way a too-long summary shows up.
				// Retry asking for a shorter one instead of giving up.
				result.tokens = 0
				lastError = fmt.Errorf("summary was cut off at the %d token output limit: %w", limit, ErrOutputLimit)
				continue
			default:
				return result, fmt.Errorf("generate summary: %w", err)
			}
		}
		if len(response.ToolCalls) > 0 {
			return result, errors.New("summary response unexpectedly requested tools")
		}
		result.content = strings.TrimSpace(response.Content)
		if result.content == "" {
			result.tokens = 0
			lastError = errors.New("summary was empty")
			continue
		}
		result.tokens = estimateTextTokens(result.content)
		if response.Usage != nil && int(response.Usage.OutputTokens) > result.tokens {
			result.tokens = int(response.Usage.OutputTokens)
		}
		if result.tokens > limit {
			lastError = fmt.Errorf("summary exceeds target: %d > %d tokens", result.tokens, limit)
			continue
		}
		setAttemptDisposition(ctx, "summary")
		return result, nil
	}
	return result, lastError
}

func estimateTokens(messages []Message) int {
	total := 0
	for _, message := range messages {
		total += estimatedMessageOverheadTokens
		total += estimateTextTokens(message.Content)
		total += estimateTextTokens(message.ToolCallID)
		for _, call := range message.ToolCalls {
			total += estimatedMessageOverheadTokens
			total += estimateTextTokens(call.ID)
			total += estimateTextTokens(call.Name)
			total += estimateTextTokens(string(call.Input))
		}
	}
	return total
}

func estimateToolSpecTokens(specs []ToolSpec) int {
	total := 0
	for _, spec := range specs {
		total += estimatedMessageOverheadTokens
		total += estimateTextTokens(spec.Name)
		total += estimateTextTokens(spec.Description)
		total += estimateTextTokens(string(spec.InputSchema))
	}
	return total
}

// estimateTrailingToolResultTokens counts messages created after the previous
// assistant response. That assistant response is already included in the
// provider-reported total_tokens baseline.
func estimateTrailingToolResultTokens(history []Message) int {
	firstTool := len(history)
	for firstTool > 0 && history[firstTool-1].Role == RoleTool {
		firstTool--
	}
	return estimateTokens(history[firstTool:])
}

func estimateTextTokens(text string) int {
	asciiBytes := 0
	nonASCII := 0
	for _, r := range text {
		if r < 0x80 {
			asciiBytes++
		} else {
			nonASCII++
		}
	}
	return (asciiBytes+estimatedASCIIBytesPerToken-1)/estimatedASCIIBytesPerToken + nonASCII
}
