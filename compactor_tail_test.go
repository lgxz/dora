package dora

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// testRound returns an assistant message with one tool call and its result.
// size is the tool result length in bytes (about size/4 tokens).
func testRound(id string, size int) []Message {
	return []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: id, Name: "run", Input: json.RawMessage(`{}`)}}},
		{Role: RoleTool, ToolCallID: id, Content: id + ":" + strings.Repeat("x", size)},
	}
}

func testHistory(task string, rounds ...[]Message) []Message {
	history := []Message{
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: task},
	}
	for _, round := range rounds {
		history = append(history, round...)
	}
	return history
}

// nearFull is a usage anchor that puts a 10k window above its 80% trigger.
var nearFull = &Usage{TotalTokens: 9_000}

func TestCompactionKeepsTaskAndRecentRoundsVerbatim(t *testing.T) {
	var requests []Request
	a := capturingAgent(10_000, &requests)
	// About 417 tokens per round; the tail budget is 1000, so two rounds fit.
	history := testHistory("fix the parser",
		testRound("c1", 1600), testRound("c2", 1600), testRound("c3", 1600), testRound("c4", 1600), testRound("c5", 1600))
	before := cloneMessages(history)

	result, err := a.ensureContextCapacity(context.Background(), history, nearFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.KeptMessages != 4 {
		t.Fatalf("compacted = %v, kept = %d; want true, 4", result.Compacted, result.KeptMessages)
	}
	want := []Message{history[0]}
	if len(result.Messages) != 6 || !reflect.DeepEqual(result.Messages[0], want[0]) {
		t.Fatalf("compacted layout = %#v", result.Messages)
	}
	head := result.Messages[1]
	if head.Role != RoleUser || !strings.HasPrefix(head.Content, "Original task (verbatim):\n\nfix the parser\n") ||
		!strings.HasSuffix(head.Content, "summary") {
		t.Fatalf("head message = %q", head.Content)
	}
	if !reflect.DeepEqual(result.Messages[2:], history[len(history)-4:]) {
		t.Fatalf("tail is not the last two rounds verbatim: %#v", result.Messages[2:])
	}
	// The summarizer sees everything before the tail, and nothing of it.
	summaryInput := requests[0].Messages
	if got, want := len(summaryInput), len(history)-4+1; got != want {
		t.Fatalf("summary request messages = %d, want %d", got, want)
	}
	for _, message := range summaryInput {
		if strings.HasPrefix(message.Content, "c4:") || strings.HasPrefix(message.Content, "c5:") {
			t.Fatalf("the summary request contains the verbatim tail: %q", message.Content[:3])
		}
	}
	if !reflect.DeepEqual(history, before) {
		t.Fatal("history was modified")
	}
}

func TestCompactionTailKeepsToolCallsWithTheirResults(t *testing.T) {
	var requests []Request
	a := capturingAgent(10_000, &requests)
	// The newest round has two results; its size alone nearly fills the tail
	// budget, so only whole rounds may ever be kept.
	last := []Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{
			{ID: "d1", Name: "run", Input: json.RawMessage(`{}`)},
			{ID: "d2", Name: "run", Input: json.RawMessage(`{}`)},
		}},
		{Role: RoleTool, ToolCallID: "d1", Content: strings.Repeat("a", 1600)},
		{Role: RoleTool, ToolCallID: "d2", Content: strings.Repeat("b", 1600)},
	}
	history := testHistory("task", testRound("c1", 1600), testRound("c2", 1600), last)

	result, err := a.ensureContextCapacity(context.Background(), history, nearFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The last round is ~830 tokens; adding c2 would exceed the budget.
	if !result.Compacted || result.KeptMessages != 3 {
		t.Fatalf("compacted = %v, kept = %d; want true, 3", result.Compacted, result.KeptMessages)
	}
	assertToolPairsIntact(t, result.Messages)
}

func assertToolPairsIntact(t *testing.T, messages []Message) {
	t.Helper()
	pending := map[string]bool{}
	for _, message := range messages {
		switch message.Role {
		case RoleAssistant:
			if len(pending) != 0 {
				t.Fatalf("assistant message while results are pending: %v", pending)
			}
			for _, call := range message.ToolCalls {
				pending[call.ID] = true
			}
		case RoleTool:
			if !pending[message.ToolCallID] {
				t.Fatalf("tool result %q has no preceding call", message.ToolCallID)
			}
			delete(pending, message.ToolCallID)
		}
	}
	if len(pending) != 0 {
		t.Fatalf("tool calls without results: %v", pending)
	}
}

func TestCompactionNeverKeepsTheOldestRoundVerbatim(t *testing.T) {
	var requests []Request
	a := capturingAgent(10_000, &requests)
	// Both rounds are small enough for the tail budget, yet at least one round
	// must remain for the summary.
	history := testHistory("task", testRound("c1", 400), testRound("c2", 400))

	result, err := a.ensureContextCapacity(context.Background(), history, nearFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.KeptMessages != 2 {
		t.Fatalf("kept = %d, want 2 (only the newest round)", result.KeptMessages)
	}
	if !reflect.DeepEqual(result.Messages[2:], history[len(history)-2:]) {
		t.Fatalf("tail = %#v", result.Messages[2:])
	}
	assertToolPairsIntact(t, result.Messages)
}

func TestCompactionSummarizesAnOversizedNewestRound(t *testing.T) {
	var requests []Request
	a := capturingAgent(10_000, &requests)
	// The newest round is far larger than the tail budget: it is summarized
	// (shortened in the request only), never kept.
	history := testHistory("task", testRound("c1", 400), testRound("c2", 12_000))

	result, err := a.ensureContextCapacity(context.Background(), history, nearFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || result.KeptMessages != 0 || len(result.Messages) != 2 {
		t.Fatalf("kept = %d, messages = %d; want a summary only", result.KeptMessages, len(result.Messages))
	}
}

func TestCompactionTruncatesAHugeTaskButSummarizerSeesAllOfIt(t *testing.T) {
	var requests []Request
	a := capturingAgent(10_000, &requests)
	task := "START-OF-TASK\n" + strings.Repeat("requirement line\n", 4_000) + "END-OF-TASK" // ~17k tokens
	history := testHistory(task, testRound("c1", 400), testRound("c2", 400))

	result, err := a.ensureContextCapacity(context.Background(), history, nearFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Compacted || !result.TaskTruncated {
		t.Fatalf("compacted = %v, truncated = %v", result.Compacted, result.TaskTruncated)
	}
	head := result.Messages[1].Content
	if !strings.Contains(head, "START-OF-TASK") || !strings.Contains(head, "END-OF-TASK") ||
		!strings.Contains(head, "the middle is omitted here") {
		t.Fatalf("head does not keep the task's two ends: %.80q", head)
	}
	if limit := int(10_000 * taskKeepRatio); estimateTextTokens(head) > limit+400 {
		t.Fatalf("head message still %d tokens", estimateTextTokens(head))
	}
	if !strings.Contains(requests[0].Messages[1].Content, "END-OF-TASK") ||
		len(requests[0].Messages[1].Content) != len(task) {
		t.Fatal("the summary request should carry the complete task")
	}
}

func TestCompactionKeepsTaskImages(t *testing.T) {
	var requests []Request
	a := capturingAgent(10_000, &requests)
	history := testHistory("look at this", testRound("c1", 400), testRound("c2", 400))
	history[1].Images = []Image{{URL: "https://example.test/a.png"}}

	result, err := a.ensureContextCapacity(context.Background(), history, nearFull, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Messages[1].Images; len(got) != 1 || got[0].URL != "https://example.test/a.png" {
		t.Fatalf("images = %#v", got)
	}
}

// After one compaction the first message is the compacted one. A second
// compaction must re-attach the original task rather than nest the old head.
func TestRepeatedCompactionReattachesTheOriginalTask(t *testing.T) {
	var requests []Request
	a := capturingAgent(10_000, &requests)
	task := Message{Role: RoleUser, Content: "the real task"}
	history := testHistory(task.Content, testRound("c1", 1600), testRound("c2", 1600), testRound("c3", 1600))
	options := compactOptions{Task: &task}

	first, err := a.compactContext(context.Background(), history, nearFull, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	next := append(cloneMessages(first.Messages), testRound("c4", 1600)...)
	next = append(next, testRound("c5", 1600)...)
	second, err := a.compactContext(context.Background(), next, nearFull, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	head := second.Messages[1].Content
	if !strings.HasPrefix(head, "Original task (verbatim):\n\nthe real task\n") {
		t.Fatalf("head = %q", head)
	}
	if strings.Count(head, "Original task (verbatim)") != 1 {
		t.Fatalf("the previous head was nested into the new one: %q", head)
	}
	assertToolPairsIntact(t, second.Messages)
}

func TestForcedCompactionIgnoresTheTriggerAndAssumesAFullWindow(t *testing.T) {
	var requests []Request
	a := capturingAgent(20_000, &requests)
	// Locally estimated far below the trigger, but the provider said no. The
	// big result is only ~3k tokens by the heuristic.
	history := testHistory("task", testRound("c1", 12_000), testRound("c2", 400))

	plain, err := a.compactContext(context.Background(), history, nil, nil, compactOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Compacted || len(requests) != 0 {
		t.Fatal("without Force a small history must not be compacted")
	}

	forced, err := a.compactContext(context.Background(), history, nil, nil, compactOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if !forced.Compacted {
		t.Fatal("Force should compact")
	}
	if forced.TrimmedToolResults == 0 {
		t.Fatal("a full window must be assumed so large tool results are shortened in the request")
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d", len(requests))
	}
}

func TestForcedCompactionOfEmptyOrSystemOnlyHistoryDoesNothing(t *testing.T) {
	a := capturingAgent(1000, new([]Request))
	for _, history := range [][]Message{nil, {{Role: RoleSystem, Content: "s"}}} {
		result, err := a.compactContext(context.Background(), history, nil, nil, compactOptions{Force: true})
		if err != nil || result.Compacted {
			t.Fatalf("compacted = %v, err = %v", result.Compacted, err)
		}
	}
}

func TestSummaryRequestOverflowIsReplannedOnce(t *testing.T) {
	var requests []Request
	calls := 0
	a := &Agent{
		model: modelFunc(func(_ context.Context, request Request) (Response, error) {
			calls++
			requests = append(requests, request)
			if calls == 1 {
				return Response{}, &ContextLengthError{Err: errors.New("too large")}
			}
			return Response{FinishReason: FinishStop, Content: "summary"}, nil
		}),
		contextWindow: 20_000,
	}
	// By the heuristic this fits without trimming (~3.5k tokens).
	history := testHistory("task", testRound("c1", 14_000))

	summary, err := a.generateContextSummaryFrom(context.Background(), history, nil, 4000, estimateTokens(history))
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || summary.attempts != 1 {
		t.Fatalf("calls = %d, attempts = %d; want 2, 1 (a replan is not an attempt)", calls, summary.attempts)
	}
	if summary.trimmed == 0 || len(requests[1].Messages[3].Content) >= len(requests[0].Messages[3].Content) {
		t.Fatalf("the second request should carry shortened tool results (trimmed = %d)", summary.trimmed)
	}
}

func TestSummaryRequestOverflowTwiceFails(t *testing.T) {
	calls := 0
	a := &Agent{
		model: modelFunc(func(context.Context, Request) (Response, error) {
			calls++
			return Response{}, &ContextLengthError{Err: errors.New("too large")}
		}),
		contextWindow: 20_000,
	}
	_, err := a.generateContextSummaryFrom(context.Background(), testHistory("task", testRound("c1", 400)), nil, 4000, 500)
	if !errors.Is(err, ErrContextLength) || calls != 2 {
		t.Fatalf("err = %v, calls = %d; want ErrContextLength after 2 calls", err, calls)
	}
}

func TestContextLengthErrorMatchesSentinel(t *testing.T) {
	cause := errors.New("provider text")
	err := fmt.Errorf("wrapped: %w", &ContextLengthError{Err: cause})
	if !errors.Is(err, ErrContextLength) || !errors.Is(err, cause) {
		t.Fatalf("errors.Is failed for %v", err)
	}
	if (&ContextLengthError{Err: cause}).Error() != "provider text" {
		t.Fatal("message changed")
	}
}

// overflowAgent builds an agent whose first normal request is rejected as too
// large. Summary requests (identified by the missing tools) succeed.
func overflowAgent(t *testing.T, rejectAll bool, log *[]string, normal *[]Request) *Agent {
	t.Helper()
	rejected := false
	tool := stubTool{
		spec:    ToolSpec{Name: "run"},
		execute: func(context.Context, json.RawMessage) (string, error) { return "ok", nil },
	}
	agent, err := New(modelFunc(func(_ context.Context, request Request) (Response, error) {
		if isSummaryRequest(request) {
			*log = append(*log, "summary")
			return Response{FinishReason: FinishStop, Content: "the summary"}, nil
		}
		*log = append(*log, "normal")
		*normal = append(*normal, request)
		if rejectAll || !rejected {
			rejected = true
			return Response{}, &ContextLengthError{Err: errors.New("maximum context length exceeded")}
		}
		return Response{FinishReason: FinishStop, Content: "done"}, nil
	}), tool)
	if err != nil {
		t.Fatal(err)
	}
	agent.contextWindow = 100_000
	return agent
}

func TestAgentCompactsAndRetriesOnceAfterProviderOverflow(t *testing.T) {
	var log []string
	var normal []Request
	agent := overflowAgent(t, false, &log, &normal)
	turn := seededCompactionTurn(t)
	var infos []string
	err := agent.RunObserved(context.Background(), turn, ObserverFunc(func(update Update) {
		if update.Kind == UpdateInfo {
			infos = append(infos, update.Info)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"normal", "summary", "normal"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("model calls = %v, want %v", log, want)
	}
	if len(normal[0].Messages) != 3 || len(normal[1].Messages) != 1 ||
		!strings.HasPrefix(normal[1].Messages[0].Content, "Original task (verbatim):\n\noriginal task") {
		t.Fatalf("requests = %#v", normal)
	}
	found := false
	for _, info := range infos {
		found = found || strings.HasPrefix(info, "Provider reported the context window exceeded; context compacted:")
	}
	if !found {
		t.Fatalf("no overflow notice in %v", infos)
	}
	if result, ok := turn.Result(); !ok || result != "done" {
		t.Fatalf("result = %q, %v", result, ok)
	}
	// The turn keeps the complete, uncompacted history.
	if got := len(turn.Messages()); got != 4 {
		t.Fatalf("turn messages = %d, want 4", got)
	}
}

func TestAgentGivesUpWhenTheRetryOverflowsToo(t *testing.T) {
	var log []string
	var normal []Request
	agent := overflowAgent(t, true, &log, &normal)
	err := agent.Run(context.Background(), seededCompactionTurn(t))
	if !errors.Is(err, ErrContextLength) {
		t.Fatalf("err = %v, want ErrContextLength", err)
	}
	if want := []string{"normal", "summary", "normal"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("model calls = %v; the retry must happen exactly once", log)
	}
}

func TestAgentDoesNotRecompactARoundThatWasJustCompacted(t *testing.T) {
	var log []string
	var normal []Request
	agent := overflowAgent(t, true, &log, &normal)
	agent.contextWindow = 200 // the seeded turn exceeds the 80% trigger
	err := agent.Run(context.Background(), seededCompactionTurn(t))
	if !errors.Is(err, ErrContextLength) {
		t.Fatalf("err = %v", err)
	}
	if want := []string{"summary", "normal"}; !reflect.DeepEqual(log, want) {
		t.Fatalf("model calls = %v, want %v", log, want)
	}
}

func TestAgentReportsAFailedCompactionAfterOverflow(t *testing.T) {
	calls := 0
	tool := stubTool{spec: ToolSpec{Name: "run"}, execute: func(context.Context, json.RawMessage) (string, error) { return "ok", nil }}
	agent, err := New(modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		if isSummaryRequest(request) {
			return Response{}, errors.New("summarizer down")
		}
		return Response{}, &ContextLengthError{Err: errors.New("too long")}
	}), tool)
	if err != nil {
		t.Fatal(err)
	}
	agent.contextWindow = 100_000
	err = agent.Run(context.Background(), seededCompactionTurn(t))
	if err == nil || !strings.Contains(err.Error(), "summarizer down") || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("err = %v, want both causes", err)
	}
}

func TestAgentDoesNotCompactForOtherModelErrors(t *testing.T) {
	calls := 0
	agent, err := New(modelFunc(func(context.Context, Request) (Response, error) {
		calls++
		return Response{}, errors.New("boom")
	}))
	if err != nil {
		t.Fatal(err)
	}
	agent.contextWindow = 100_000
	if err := agent.Run(context.Background(), seededCompactionTurn(t)); err == nil || calls != 1 {
		t.Fatalf("err = %v, calls = %d", err, calls)
	}
}
