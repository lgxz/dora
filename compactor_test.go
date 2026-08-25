package dora

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestEnsureContextCapacityKeepsHistoryBelowTrigger(t *testing.T) {
	calls := 0
	a := &Agent{
		model: modelFunc(func(context.Context, Request) (Response, error) {
			calls++
			return Response{Content: "unexpected"}, nil
		}),
		contextWindow: 1000,
	}
	history := []Message{{Role: RoleUser, Content: "small request"}}

	result, err := a.ensureContextCapacity(context.Background(), history, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Compacted || calls != 0 {
		t.Fatalf("compacted = %v, calls = %d; want unchanged", result.Compacted, calls)
	}
	if result.PredictedTokensBefore != result.PredictedTokensAfter {
		t.Fatalf("before = %d, after = %d", result.PredictedTokensBefore, result.PredictedTokensAfter)
	}
	if result.Attempts != 0 || result.SummaryTokens != 0 {
		t.Fatalf("unexpected summary stats: %#v", result)
	}
	if &result.Messages[0] != &history[0] {
		t.Fatal("history below the trigger should be returned directly")
	}
}

func TestEnsureContextCapacitySummarizesAtomically(t *testing.T) {
	var request Request
	a := &Agent{
		model: modelFunc(func(_ context.Context, got Request) (Response, error) {
			request = got
			return Response{Content: "requirements and completed work"}, nil
		}),
		contextWindow: 200,
	}
	history := []Message{
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: "original task"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "c1", Name: "run", Input: json.RawMessage(`{}`)}}},
		{Role: RoleTool, ToolCallID: "c1", Content: strings.Repeat("x", 800)},
	}
	before := cloneMessages(history)

	result, err := a.ensureContextCapacity(context.Background(), history, nil, []ToolSpec{{Name: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted {
		t.Fatal("expected context compaction")
	}
	if len(request.Tools) != 0 || request.Continuation != "" {
		t.Fatalf("summary request leaked tools or continuation: %#v", request)
	}
	if len(request.Messages) != len(history)+1 {
		t.Fatalf("summary request messages = %d, want %d", len(request.Messages), len(history)+1)
	}
	if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "Return only the summary") {
		t.Fatalf("missing summary instruction: %q", request.Messages[len(request.Messages)-1].Content)
	}
	if len(result.Messages) != 2 || result.Messages[0].Role != RoleSystem || result.Messages[1].Role != RoleUser {
		t.Fatalf("compacted history = %#v", result.Messages)
	}
	if !strings.Contains(result.Messages[1].Content, "requirements and completed work") {
		t.Fatalf("summary message = %q", result.Messages[1].Content)
	}
	if result.PredictedTokensAfter >= result.PredictedTokensBefore {
		t.Fatalf("compaction stats = %#v", result)
	}
	if result.SummaryTokens == 0 || result.Attempts != 1 {
		t.Fatalf("summary stats = %#v", result)
	}
	if !reflect.DeepEqual(history, before) {
		t.Fatal("compaction mutated the original history")
	}
}

func TestGenerateContextSummaryRetriesOversizedOutput(t *testing.T) {
	calls := 0
	a := &Agent{
		model: modelFunc(func(_ context.Context, request Request) (Response, error) {
			calls++
			if calls == 1 {
				return Response{
					Content: "too long",
					Usage:   &Usage{OutputTokens: 21},
				}, nil
			}
			if !strings.Contains(request.Messages[len(request.Messages)-1].Content, "substantially more concise") {
				t.Fatal("retry did not strengthen the summary instruction")
			}
			return Response{Content: "short"}, nil
		}),
		contextWindow: 100,
	}

	summary, err := a.generateContextSummary(
		context.Background(),
		[]Message{{Role: RoleUser, Content: "task"}},
		20,
	)
	if err != nil {
		t.Fatal(err)
	}
	if summary.content != "short" || summary.tokens == 0 || summary.attempts != 2 || calls != 2 {
		t.Fatalf("summary = %#v, calls = %d", summary, calls)
	}
}

func TestGenerateContextSummaryRejectsToolCalls(t *testing.T) {
	a := &Agent{
		model: modelFunc(func(context.Context, Request) (Response, error) {
			return Response{ToolCalls: []ToolCall{{ID: "c1", Name: "run"}}}, nil
		}),
		contextWindow: 100,
	}

	_, err := a.generateContextSummary(
		context.Background(),
		[]Message{{Role: RoleUser, Content: "task"}},
		20,
	)
	if err == nil || !strings.Contains(err.Error(), "unexpectedly requested tools") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnsureContextCapacityRejectsRequestThatStillDoesNotFit(t *testing.T) {
	a := &Agent{
		model: modelFunc(func(context.Context, Request) (Response, error) {
			return Response{Content: "short summary"}, nil
		}),
		contextWindow: 100,
	}
	history := []Message{{Role: RoleUser, Content: strings.Repeat("x", 400)}}
	specs := []ToolSpec{{Description: strings.Repeat("schema", 100)}}

	result, err := a.ensureContextCapacity(
		context.Background(),
		history,
		nil,
		specs,
	)
	if err == nil || !strings.Contains(err.Error(), "does not fit after compaction") {
		t.Fatalf("error = %v", err)
	}
	if result.Compacted || !reflect.DeepEqual(result.Messages, history) {
		t.Fatal("failed capacity check changed history")
	}
	if result.PredictedTokensAfter == result.PredictedTokensBefore || result.SummaryTokens == 0 || result.Attempts != 1 {
		t.Fatalf("failed compaction stats = %#v", result)
	}
}

func TestReportedUsageAddsOnlyTrailingToolResults(t *testing.T) {
	a := &Agent{contextWindow: 100}
	history := []Message{
		{Role: RoleUser, Content: "task"},
		{Role: RoleAssistant, Content: strings.Repeat("assistant", 1000)},
		{Role: RoleTool, ToolCallID: "c1", Content: "small"},
	}
	usage := &Usage{TotalTokens: 50}

	got := a.predictedTokens(history, usage, nil)
	want := 50 + estimateTokens(history[2:]) + a.outputReserve()
	if got != want {
		t.Fatalf("predicted tokens = %d, want %d", got, want)
	}
}

func TestPredictionWithoutUsageIncludesToolSchemas(t *testing.T) {
	a := &Agent{contextWindow: 1000}
	history := []Message{{Role: RoleUser, Content: "task"}}
	specs := []ToolSpec{{
		Name:        "search",
		Description: strings.Repeat("description", 40),
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}}

	withoutTools := a.predictedTokens(history, nil, nil)
	withTools := a.predictedTokens(history, nil, specs)
	if withTools <= withoutTools {
		t.Fatalf("tool schema was not counted: without=%d with=%d", withoutTools, withTools)
	}
}

func TestSmallContextScalesOutputReserve(t *testing.T) {
	a := &Agent{contextWindow: 100}
	if got := a.outputReserve(); got != 20 {
		t.Fatalf("output reserve = %d, want 20", got)
	}
	if got := a.compactionTrigger(); got != 80 {
		t.Fatalf("trigger = %d, want 80", got)
	}
	if got := a.compactionTarget(); got != 20 {
		t.Fatalf("target = %d, want 20", got)
	}
}

func TestAgentRunCompactsAndClearsContinuation(t *testing.T) {
	calls := 0
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			if len(request.Tools) != 0 || request.Continuation != "" {
				t.Fatalf("summary request = %#v", request)
			}
			return Response{
				Content:      "original task and tool result preserved",
				Continuation: "summary-continuation-must-be-ignored",
			}, nil
		case 2:
			if request.Continuation != "" {
				t.Fatalf("normal request retained stale continuation %q", request.Continuation)
			}
			if len(request.Tools) != 1 {
				t.Fatalf("normal request tools = %#v", request.Tools)
			}
			if len(request.Messages) != 1 || !strings.HasPrefix(request.Messages[0].Content, "Conversation summary:") {
				t.Fatalf("normal request messages = %#v", request.Messages)
			}
			return Response{Content: "done", Continuation: "new-continuation"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec: ToolSpec{Name: "run"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "unused", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	agent.contextWindow = 200

	turn := seededCompactionTurn(t)
	before := turn.Messages()
	var compacted bool
	err = agent.RunObserved(context.Background(), turn, ObserverFunc(func(update Update) {
		if update.Kind == UpdateInfo && strings.HasPrefix(update.Info, "Context compacted:") {
			compacted = true
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !compacted {
		t.Fatal("observer did not receive compaction update")
	}
	if turn.Continuation() != "new-continuation" {
		t.Fatalf("continuation = %q", turn.Continuation())
	}
	after := turn.Messages()
	if len(after) != len(before)+1 {
		t.Fatalf("persisted messages = %d, want %d", len(after), len(before)+1)
	}
	for i := range before {
		if !reflect.DeepEqual(after[i], before[i]) {
			t.Fatalf("persisted message %d changed: %#v -> %#v", i, before[i], after[i])
		}
	}
}

func TestAgentRunLeavesTurnUntouchedWhenSummaryFails(t *testing.T) {
	calls := 0
	model := modelFunc(func(context.Context, Request) (Response, error) {
		calls++
		return Response{}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	agent.contextWindow = 200
	turn := seededCompactionTurn(t)
	before := turn.Messages()

	err = agent.Run(context.Background(), turn)
	if err == nil || !strings.Contains(err.Error(), "compact context") {
		t.Fatalf("error = %v", err)
	}
	if calls != maxCompactionAttempts {
		t.Fatalf("summary calls = %d, want %d", calls, maxCompactionAttempts)
	}
	if turn.Completed() {
		t.Fatal("turn completed after failed compaction")
	}
	if !reflect.DeepEqual(turn.Messages(), before) {
		t.Fatal("failed compaction mutated the turn")
	}
}

func TestSummaryStreamingDeltasAreNotObserved(t *testing.T) {
	calls := 0
	model := streamingModelFunc(func(_ context.Context, request Request, emit func(ModelEvent)) (Response, error) {
		calls++
		if len(request.Tools) == 0 {
			emit(ModelEvent{Kind: ModelEventContentDelta, Delta: "hidden summary delta"})
			return Response{Content: "concise state"}, nil
		}
		emit(ModelEvent{Kind: ModelEventContentDelta, Delta: "visible final delta"})
		return Response{Content: "done"}, nil
	})
	tool := stubTool{
		spec: ToolSpec{Name: "run"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "unused", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	agent.contextWindow = 200
	turn := seededCompactionTurn(t)
	var deltas []string

	err = agent.RunObserved(context.Background(), turn, ObserverFunc(func(update Update) {
		if update.Kind == UpdateContentDelta {
			deltas = append(deltas, update.Delta)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || !reflect.DeepEqual(deltas, []string{"visible final delta"}) {
		t.Fatalf("calls = %d, observed deltas = %#v", calls, deltas)
	}
}

func TestEstimateTextTokens(t *testing.T) {
	if got := estimateTextTokens("abcdefgh"); got != 2 {
		t.Fatalf("ASCII tokens = %d, want 2", got)
	}
	if got := estimateTextTokens("你好啊"); got != 3 {
		t.Fatalf("CJK tokens = %d, want 3", got)
	}
}

func seededCompactionTurn(t *testing.T) *Turn {
	t.Helper()
	turn := NewTurn("original task")
	err := turn.AppendRound(Round{
		Assistant: Message{
			Role:      RoleAssistant,
			ToolCalls: []ToolCall{{ID: "c1", Name: "run", Input: json.RawMessage(`{}`)}},
		},
		Tools: []Message{{
			Role:       RoleTool,
			ToolCallID: "c1",
			Content:    strings.Repeat("tool output ", 200),
		}},
	}, "old-continuation")
	if err != nil {
		t.Fatal(err)
	}
	return turn
}
