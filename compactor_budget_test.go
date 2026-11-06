package dora

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

// A summary that outgrows its budget is cut off by the output limit. That must
// lead to a retry asking for a shorter one, not to an immediate failure.
func TestSummaryCutOffByOutputLimitIsRetried(t *testing.T) {
	for name, firstContent := range map[string]string{
		"partial text":                 "the summary starts and is cut o",
		"empty after reasoning budget": "",
	} {
		t.Run(name, func(t *testing.T) {
			var requests []Request
			a := &Agent{
				model: modelFunc(func(_ context.Context, request Request) (Response, error) {
					requests = append(requests, request)
					if len(requests) == 1 {
						return Response{FinishReason: FinishOutputLimit, Content: firstContent}, nil
					}
					return Response{FinishReason: FinishStop, Content: "short enough"}, nil
				}),
				contextWindow: 100_000,
			}
			summary, err := a.generateContextSummary(context.Background(), []Message{{Role: RoleUser, Content: "task"}}, 2000)
			if err != nil {
				t.Fatal(err)
			}
			if summary.content != "short enough" || summary.attempts != 2 || len(requests) != 2 {
				t.Fatalf("summary = %#v after %d requests", summary, len(requests))
			}
			retry := requests[1].Messages[len(requests[1].Messages)-1].Content
			if !strings.Contains(retry, "substantially more concise") || !strings.Contains(retry, "at most approximately 1000 tokens") {
				t.Fatalf("retry instruction = %q, want a stricter request for half the budget", retry)
			}
			for i, request := range requests {
				if request.MaxOutputTokens == nil || *request.MaxOutputTokens != 2000 {
					t.Fatalf("request %d output limit = %v, want the hard cap to stay 2000", i, request.MaxOutputTokens)
				}
			}
		})
	}
}

func TestSummaryCutOffTwiceFailsWithOutputLimitError(t *testing.T) {
	calls := 0
	a := &Agent{
		model: modelFunc(func(context.Context, Request) (Response, error) {
			calls++
			return Response{FinishReason: FinishOutputLimit, Content: "cut"}, nil
		}),
		contextWindow: 100_000,
	}
	summary, err := a.generateContextSummary(context.Background(), []Message{{Role: RoleUser, Content: "task"}}, 2000)
	if !errors.Is(err, ErrOutputLimit) || !strings.Contains(err.Error(), "output limit") {
		t.Fatalf("error = %v, want one wrapping ErrOutputLimit", err)
	}
	if calls != maxCompactionAttempts || summary.attempts != maxCompactionAttempts {
		t.Fatalf("calls = %d, attempts = %d, want %d", calls, summary.attempts, maxCompactionAttempts)
	}
}

func TestSummaryOtherInvalidResponsesStillFailImmediately(t *testing.T) {
	calls := 0
	a := &Agent{
		model: modelFunc(func(context.Context, Request) (Response, error) {
			calls++
			return Response{FinishReason: FinishBlocked}, nil
		}),
		contextWindow: 100_000,
	}
	_, err := a.generateContextSummary(context.Background(), []Message{{Role: RoleUser, Content: "task"}}, 2000)
	if !errors.Is(err, ErrModelBlocked) || calls != 1 {
		t.Fatalf("error = %v after %d calls, want an immediate ErrModelBlocked", err, calls)
	}
}

func capturingAgent(window int, captured *[]Request) *Agent {
	return &Agent{
		model: modelFunc(func(_ context.Context, request Request) (Response, error) {
			*captured = append(*captured, request)
			return Response{FinishReason: FinishStop, Content: "summary"}, nil
		}),
		contextWindow: window,
	}
}

// With too little room left in the window, the output budget shrinks to what
// fits instead of asking for 20% of the window regardless.
func TestSummaryOutputBudgetShrinksToRemainingWindow(t *testing.T) {
	var requests []Request
	const window = 10_000
	a := capturingAgent(window, &requests)
	history := []Message{{Role: RoleUser, Content: strings.Repeat("x", 4*8000)}} // ~8000 tokens
	target := a.summaryTarget()                                                  // 2000

	result, err := a.ensureContextCapacity(context.Background(), history, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || len(requests) != 1 {
		t.Fatalf("compacted = %v after %d requests", result.Compacted, len(requests))
	}
	got := *requests[0].MaxOutputTokens
	margin := int(float64(window) * summaryWindowMarginRatio)
	input := estimateTokens(history) + estimateTextTokens(summaryPrompt(target, 1)) + estimatedMessageOverheadTokens
	if want := window - margin - input; got != want {
		t.Fatalf("output budget = %d, want the remaining room %d", got, want)
	}
	if got >= target || got < minSummaryOutputTokens {
		t.Fatalf("output budget %d should be below the target %d and not below %d", got, target, minSummaryOutputTokens)
	}
	if result.TrimmedToolResults != 0 {
		t.Fatalf("nothing to shorten, but TrimmedToolResults = %d", result.TrimmedToolResults)
	}
}

// The calibrated baseline (real reported usage) is used, not just the local
// heuristic, and a request that cannot fit still goes out with a floor budget.
func TestSummaryBudgetUsesReportedUsageAndKeepsAFloor(t *testing.T) {
	var requests []Request
	a := capturingAgent(20_000, &requests)
	history := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "run", Input: json.RawMessage(`{}`)}}},
		{Role: RoleTool, ToolCallID: "c1", Content: "ok"},
	}
	usage := &Usage{TotalTokens: 18_000}

	result, err := a.ensureContextCapacity(context.Background(), history, usage, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted {
		t.Fatal("expected compaction")
	}
	if got := *requests[0].MaxOutputTokens; got != minSummaryOutputTokens {
		t.Fatalf("output budget = %d, want the floor %d when the window is nearly full", got, minSummaryOutputTokens)
	}
}

// A single huge tool result must not make the summary request itself overflow
// the window. It is shortened in the request only.
func TestOversizedToolResultIsShortenedOnlyInSummaryRequest(t *testing.T) {
	var requests []Request
	a := capturingAgent(20_000, &requests)
	huge := "HEAD-OF-OUTPUT\n" + strings.Repeat("line of tool output\n", 12_000) + "TAIL-OF-OUTPUT" // ~60k tokens
	history := []Message{
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: "original task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "run", Input: json.RawMessage(`{}`)}}},
		{Role: RoleTool, ToolCallID: "c1", Content: huge},
	}
	before := cloneMessages(history)

	result, err := a.ensureContextCapacity(context.Background(), history, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.TrimmedToolResults != 1 {
		t.Fatalf("compacted = %v, trimmed = %d", result.Compacted, result.TrimmedToolResults)
	}
	request := requests[0]
	tool := request.Messages[3].Content
	if len(tool) >= len(huge)/2 || !strings.HasPrefix(tool, "HEAD-OF-OUTPUT") || !strings.HasSuffix(tool, "TAIL-OF-OUTPUT") ||
		!strings.Contains(tool, "omitted from this summary request") {
		t.Fatalf("tool result in the summary request: %d bytes, %q ... %q", len(tool), tool[:20], tool[len(tool)-20:])
	}
	if *request.MaxOutputTokens != result.TargetTokens {
		t.Fatalf("with room made, the output budget should be the target %d, got %d", result.TargetTokens, *request.MaxOutputTokens)
	}
	margin := int(float64(20_000) * summaryWindowMarginRatio)
	if estimated := estimateTokens(request.Messages) + *request.MaxOutputTokens; estimated > 20_000-margin {
		t.Fatalf("summary request still estimated at %d tokens including output, over %d", estimated, 20_000-margin)
	}
	if !reflect.DeepEqual(history, before) {
		t.Fatal("the caller's history was modified")
	}
}

func TestShortenToolResults(t *testing.T) {
	messages := []Message{
		{Role: RoleUser, Content: strings.Repeat("u", 5000)}, // not a tool result: untouched
		{Role: RoleTool, ToolCallID: "a", Content: strings.Repeat("界", 3000)},
		{Role: RoleTool, ToolCallID: "b", Content: "short"},
	}
	before := cloneMessages(messages)

	got, count := shortenToolResults(messages, 1024)
	if count != 1 {
		t.Fatalf("shortened %d results, want 1", count)
	}
	if got[0].Content != messages[0].Content || got[2].Content != "short" {
		t.Fatal("messages that should stay were changed")
	}
	if len(got[1].Content) > 1024+200 || !utf8.ValidString(got[1].Content) || !strings.Contains(got[1].Content, "9000 bytes in total") {
		t.Fatalf("shortened result: %d bytes, valid UTF-8 = %v", len(got[1].Content), utf8.ValidString(got[1].Content))
	}
	if !reflect.DeepEqual(messages, before) {
		t.Fatal("the input slice was modified")
	}

	same, count := shortenToolResults(messages[2:], 1024)
	if count != 0 || &same[0] != &messages[2] {
		t.Fatal("with nothing to shorten the input should be returned as is")
	}
}

// When no cap can make room, the request is still sent (the provider judges
// with exact counts) with the floor budget and the most aggressive shortening.
func TestSummaryRequestIsStillSentWhenNothingFits(t *testing.T) {
	var requests []Request
	a := capturingAgent(2000, &requests)
	history := []Message{
		{Role: RoleUser, Content: strings.Repeat("x", 4*3000)}, // not shortenable
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "run", Input: json.RawMessage(`{}`)}}},
		{Role: RoleTool, ToolCallID: "c1", Content: strings.Repeat("y", 10_000)},
	}
	summary, err := a.generateContextSummary(context.Background(), history, 400)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || summary.trimmed != 1 {
		t.Fatalf("requests = %d, trimmed = %d", len(requests), summary.trimmed)
	}
	if got := *requests[0].MaxOutputTokens; got != 400 { // floor = min(target, 1024)
		t.Fatalf("output budget = %d, want the target 400 as the floor", got)
	}
	if len(requests[0].Messages[2].Content) > 1024+200 {
		t.Fatalf("tool result not shortened to the smallest cap: %d bytes", len(requests[0].Messages[2].Content))
	}
}

// The run-level view: the full turn is kept, the model sees a summary, and the
// observer is told that tool results were shortened for the summary request.
func TestAgentRunReportsShortenedToolResults(t *testing.T) {
	calls := 0
	var summaryTool string
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		if calls == 1 {
			for _, m := range request.Messages {
				if m.Role == RoleTool {
					summaryTool = m.Content
				}
			}
			return Response{FinishReason: FinishStop, Content: "task and result summarized"}, nil
		}
		return Response{FinishReason: FinishStop, Content: "done"}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	agent.contextWindow = 20_000

	huge := strings.Repeat("tool output line\n", 16_000)
	turn := NewTurn("original task")
	if err := turn.AppendRound(Round{
		Assistant: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "run", Input: json.RawMessage(`{}`)}}},
		Tools:     []Message{{Role: RoleTool, ToolCallID: "c1", Content: huge}},
	}, ""); err != nil {
		t.Fatal(err)
	}

	var info string
	err = agent.RunObserved(context.Background(), turn, ObserverFunc(func(update Update) {
		if update.Kind == UpdateInfo && strings.HasPrefix(update.Info, "Context compacted:") {
			info = update.Info
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info, "1 large tool result was shortened in the summary request only") {
		t.Fatalf("info = %q", info)
	}
	if len(summaryTool) >= len(huge)/2 {
		t.Fatalf("summary request carried %d of %d bytes", len(summaryTool), len(huge))
	}
	if got := turn.Messages()[2].Content; got != huge {
		t.Fatalf("the persisted tool result was altered (%d bytes)", len(got))
	}
}
