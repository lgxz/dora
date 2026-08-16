package dora

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// pngBytes is a minimal valid PNG header so http.DetectContentType reports
// image/png.
var pngBytes = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}

// writeTempPNG writes a minimal PNG file and returns its path.
func writeTempPNG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type modelFunc func(context.Context, Request) (Response, error)

func (f modelFunc) Generate(ctx context.Context, request Request) (Response, error) {
	return f(ctx, request)
}

type streamingModelFunc func(context.Context, Request, func(ModelEvent)) (Response, error)

func (f streamingModelFunc) Generate(context.Context, Request) (Response, error) {
	panic("Generate must not be called for a StreamingModel")
}

func (f streamingModelFunc) GenerateStream(ctx context.Context, request Request, emit func(ModelEvent)) (Response, error) {
	return f(ctx, request, emit)
}

type stubTool struct {
	spec    ToolSpec
	execute func(context.Context, json.RawMessage) (string, error)
	images  []Image
}

func (t stubTool) Spec() ToolSpec { return t.spec }

func (t stubTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	content, err := t.execute(ctx, input)
	return ToolResult{Content: content, Images: t.images}, err
}

type testRunResult struct {
	Content      string
	Messages     []Message
	Continuation string
}

func runAgent(agent *Agent, ctx context.Context, input []Message) (testRunResult, error) {
	return runAgentObserved(agent, ctx, input, nil)
}

func runAgentObserved(agent *Agent, ctx context.Context, input []Message, observer Observer) (testRunResult, error) {
	var system, user string
	for _, message := range input {
		switch message.Role {
		case RoleSystem:
			system = message.Content
		case RoleUser:
			user = message.Content
		}
	}
	turn := NewTurn(system, user)
	err := agent.RunObserved(ctx, turn, observer)
	content, _ := turn.Result()
	return testRunResult{Content: content, Messages: turn.Messages(), Continuation: turn.Continuation()}, err
}

func TestRunReturnsFinalResponse(t *testing.T) {
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		if len(request.Messages) != 2 || request.Messages[1].Content != "hello" {
			t.Fatalf("unexpected messages: %#v", request.Messages)
		}
		return Response{Content: "hi"}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}

	input := []Message{{Role: RoleUser, Content: "hello"}}
	result, err := runAgent(agent, context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}

	if result.Content != "hi" {
		t.Fatalf("content = %q, want %q", result.Content, "hi")
	}
	want := []Message{
		{Role: RoleSystem},
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}
	if !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("messages = %#v, want %#v", result.Messages, want)
	}
}

func TestRunExecutesToolAndContinues(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			if len(request.Tools) != 1 || request.Tools[0].Name != "weather" {
				t.Fatalf("unexpected tools: %#v", request.Tools)
			}
			return Response{ToolCalls: []ToolCall{{
				ID:    "call-1",
				Name:  "weather",
				Input: json.RawMessage(`{"city":"Shanghai"}`),
			}}}, nil
		case 2:
			if len(request.Messages) != 4 {
				t.Fatalf("message count = %d, want 4", len(request.Messages))
			}
			result := request.Messages[3]
			if result.Role != RoleTool || result.ToolCallID != "call-1" || result.Content != "sunny" {
				t.Fatalf("unexpected tool result: %#v", result)
			}
			return Response{Content: "It is sunny."}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})

	tool := stubTool{
		spec: ToolSpec{
			Name:        "weather",
			Description: "Get the weather",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		execute: func(_ context.Context, input json.RawMessage) (string, error) {
			if string(input) != `{"city":"Shanghai"}` {
				t.Fatalf("input = %s", input)
			}
			return "sunny", nil
		},
	}

	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runAgent(agent, context.Background(), []Message{{Role: RoleUser, Content: "weather?"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "It is sunny." {
		t.Fatalf("content = %q", result.Content)
	}
	if calls != 2 {
		t.Fatalf("model calls = %d, want 2", calls)
	}
}

func TestRunUsesStreamingModelAndCarriesContinuation(t *testing.T) {
	var calls int
	streamReturned := false
	toolExecuted := false
	model := streamingModelFunc(func(_ context.Context, request Request, emit func(ModelEvent)) (Response, error) {
		calls++
		switch calls {
		case 1:
			emit(ModelEvent{Kind: ModelEventContentDelta, Delta: "checking"})
			emit(ModelEvent{Kind: ModelEventToolCallReady, ToolCall: ToolCall{ID: "call-1", Name: "weather"}})
			if toolExecuted {
				t.Fatal("tool executed before the streamed response completed")
			}
			streamReturned = true
			return Response{
				ToolCalls:    []ToolCall{{ID: "call-1", Name: "weather", Input: json.RawMessage(`{}`)}},
				Continuation: "resp-1",
			}, nil
		case 2:
			if request.Continuation != "resp-1" {
				t.Fatalf("continuation = %q", request.Continuation)
			}
			return Response{Content: "sunny", Continuation: "resp-2"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec: ToolSpec{Name: "weather"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			if !streamReturned {
				t.Fatal("tool executed while the model stream was active")
			}
			toolExecuted = true
			return "sunny", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	var deltas string
	result, err := runAgentObserved(agent, context.Background(), []Message{{Role: RoleUser, Content: "weather?"}}, ObserverFunc(func(update Update) {
		if update.Kind == UpdateContentDelta {
			deltas += update.Delta
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "sunny" || deltas != "checking" || !toolExecuted {
		t.Fatalf("result = %#v, deltas = %q, toolExecuted = %v", result, deltas, toolExecuted)
	}
}

func TestRunTurnCarriesContinuation(t *testing.T) {
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		if request.Continuation != "saved-state" {
			t.Fatalf("continuation = %q", request.Continuation)
		}
		return Response{Content: "done", Continuation: "next-state"}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	turn := NewTurn("", "continue")
	turn.continuation = "saved-state"
	err = agent.Run(context.Background(), turn)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := turn.Result()
	if result != "done" || turn.Continuation() != "next-state" {
		t.Fatalf("result = %q, continuation = %q", result, turn.Continuation())
	}
}

func TestNewRejectsDuplicateTools(t *testing.T) {
	tool := stubTool{
		spec: ToolSpec{Name: "duplicate"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "", nil
		},
	}
	_, err := New(modelFunc(func(context.Context, Request) (Response, error) {
		return Response{}, nil
	}), tool, tool)
	if err == nil {
		t.Fatal("expected duplicate tool error")
	}
}

func TestSetJobManagerRegistersJobToolForExecution(t *testing.T) {
	jobTool := stubTool{
		spec: ToolSpec{Name: "job"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return `{"jobs": []}`, nil
		},
	}

	// Model: first round calls the job tool, second round returns content.
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			// The job tool is always exposed (like other tools).
			found := false
			for _, spec := range request.Tools {
				if spec.Name == "job" {
					found = true
				}
			}
			if !found {
				t.Fatalf("job tool should be exposed like other tools")
			}
			return Response{ToolCalls: []ToolCall{{
				ID:    "call-1",
				Name:  "job",
				Input: json.RawMessage(`{"action":"list"}`),
			}}}, nil
		case 2:
			return Response{Content: "done"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})

	agent, err := NewWithConfig(model, AgentConfig{MaxRounds: 3}, jobTool)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runAgent(agent, context.Background(), []Message{{Role: RoleUser, Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestRunFeedsBackMissingToolError(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "missing"}}}, nil
		case 2:
			if len(request.Messages) != 4 {
				t.Fatalf("message count = %d, want 4", len(request.Messages))
			}
			result := request.Messages[3]
			if result.Role != RoleTool || result.ToolCallID != "call-1" ||
				!strings.Contains(result.Content, `tool "missing" not found`) {
				t.Fatalf("unexpected tool error message: %#v", result)
			}
			return Response{Content: "recovered"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runAgent(agent, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "recovered" || calls != 2 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}

func TestRunFeedsBackToolErrorAndCorrects(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{
				ID:    "call-1",
				Name:  "fail",
				Input: json.RawMessage(`{"bad":true}`),
			}}}, nil
		case 2:
			if len(request.Messages) != 4 {
				t.Fatalf("message count = %d, want 4", len(request.Messages))
			}
			result := request.Messages[3]
			if result.Role != RoleTool || result.ToolCallID != "call-1" ||
				!strings.Contains(result.Content, `tool "fail" failed`) {
				t.Fatalf("unexpected tool error message: %#v", result)
			}
			return Response{Content: "corrected"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec: ToolSpec{Name: "fail"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("broken")
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runAgent(agent, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "corrected" || calls != 2 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}

func TestRunFeedsBackInvalidJSONArgsAndCorrects(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{
				ID:    "call-1",
				Name:  "weather",
				Input: json.RawMessage(`not-json`),
			}}}, nil
		case 2:
			if len(request.Messages) != 4 {
				t.Fatalf("message count = %d, want 4", len(request.Messages))
			}
			result := request.Messages[3]
			if result.Role != RoleTool || result.ToolCallID != "call-1" ||
				!strings.Contains(result.Content, `arguments for tool "weather" were not valid JSON: not-json`) {
				t.Fatalf("unexpected tool error message: %#v", result)
			}
			return Response{Content: "corrected"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec: ToolSpec{Name: "weather"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			t.Fatal("tool must not execute with invalid JSON arguments")
			return "", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	result, err := runAgent(agent, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "corrected" || calls != 2 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}

func TestRunExecutesMultipleToolCallsInParallelPreservingOrder(t *testing.T) {
	// The first tool sleeps briefly while the second returns immediately. If
	// the calls run in parallel, total elapsed time is close to the slowest
	// call; if serial, it is the sum of both.
	const slowDelay = 300 * time.Millisecond

	var startedMu sync.Mutex
	var started []string
	markStarted := func(name string) {
		startedMu.Lock()
		started = append(started, name)
		startedMu.Unlock()
	}

	slow := stubTool{
		spec: ToolSpec{Name: "slow"},
		execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			markStarted("slow")
			select {
			case <-time.After(slowDelay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "slow-done", nil
		},
	}
	fast := stubTool{
		spec: ToolSpec{Name: "fast"},
		execute: func(_ context.Context, _ json.RawMessage) (string, error) {
			markStarted("fast")
			return "fast-done", nil
		},
	}

	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{
				{ID: "call-slow", Name: "slow", Input: json.RawMessage(`{}`)},
				{ID: "call-fast", Name: "fast", Input: json.RawMessage(`{}`)},
			}}, nil
		case 2:
			if len(request.Messages) != 5 {
				t.Fatalf("message count = %d, want 5", len(request.Messages))
			}
			// Tool results must appear in the model's call order.
			if request.Messages[3].ToolCallID != "call-slow" || request.Messages[3].Content != "slow-done" {
				t.Fatalf("unexpected first tool result: %#v", request.Messages[3])
			}
			if request.Messages[4].ToolCallID != "call-fast" || request.Messages[4].Content != "fast-done" {
				t.Fatalf("unexpected second tool result: %#v", request.Messages[4])
			}
			return Response{Content: "done"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})

	agent, err := New(model, slow, fast)
	if err != nil {
		t.Fatal(err)
	}

	// Record the order of Observer events for the tool calls.
	var events []string
	start := time.Now()
	result, err := runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		switch update.Kind {
		case UpdateToolStarted:
			events = append(events, "start:"+update.ToolCall.Name)
		case UpdateMessageAdded:
			if update.Message.Role == RoleTool {
				events = append(events, "added:"+update.Message.ToolCallID)
			}
		}
	}))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || calls != 2 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}

	// Both tools must have started before the slow one finished, proving the
	// calls ran concurrently.
	startedMu.Lock()
	startedSnapshot := append([]string(nil), started...)
	startedMu.Unlock()
	if len(startedSnapshot) != 2 {
		t.Fatalf("started tools = %v, want both started", startedSnapshot)
	}

	// Parallel execution should take roughly the slowest call, not the sum.
	if elapsed >= 2*slowDelay {
		t.Fatalf("elapsed = %v, want < %v (calls appear serial)", elapsed, 2*slowDelay)
	}

	// Observer events must be emitted in the model's call order.
	wantEvents := []string{
		"start:slow", "added:call-slow",
		"start:fast", "added:call-fast",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("observer events = %#v, want %#v", events, wantEvents)
	}
}

func TestRunToolStartedCarriesRealStartTime(t *testing.T) {
	// The UpdateToolStarted event is emitted after all goroutines finish, so
	// its StartedAt must reflect the real time the tool began executing rather
	// than the event delivery time. Otherwise the renderer would report ~1ms.
	const slowDelay = 300 * time.Millisecond

	slow := stubTool{
		spec: ToolSpec{Name: "slow"},
		execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			select {
			case <-time.After(slowDelay):
			case <-ctx.Done():
				return "", ctx.Err()
			}
			return "slow-done", nil
		},
	}

	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "call-slow", Name: "slow", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			return Response{Content: "done"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})

	agent, err := New(model, slow)
	if err != nil {
		t.Fatal(err)
	}

	var startedAt time.Time
	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		if update.Kind == UpdateToolStarted {
			startedAt = update.StartedAt
		}
	}))
	if err != nil {
		t.Fatal(err)
	}

	// The event is delivered after the tool finishes, so StartedAt must be
	// earlier than the delivery time by roughly the tool's sleep duration.
	delivered := time.Now()
	if startedAt.IsZero() {
		t.Fatal("UpdateToolStarted did not carry a StartedAt")
	}
	elapsed := delivered.Sub(startedAt)
	if elapsed < slowDelay/2 {
		t.Fatalf("StartedAt-based elapsed = %v, want >= ~%v (event delivered after tool finished)", elapsed, slowDelay)
	}
}

func TestRunEmitsToolFailedOnRecoverableError(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		if calls == 1 {
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "fail"}}}, nil
		}
		return Response{Content: "done"}, nil
	})
	tool := stubTool{
		spec: ToolSpec{Name: "fail"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("broken")
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	var failed []Update
	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		if update.Kind == UpdateToolFailed {
			failed = append(failed, update)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(failed) != 1 || failed[0].ToolCall.Name != "fail" || failed[0].Err == nil {
		t.Fatalf("failed updates = %#v", failed)
	}
}

func TestRunStopsAfterMaximumRounds(t *testing.T) {
	model := modelFunc(func(context.Context, Request) (Response, error) {
		return Response{ToolCalls: []ToolCall{{Name: "again"}}}, nil
	})
	tool := stubTool{
		spec: ToolSpec{Name: "again"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "ok", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	_, err = runAgent(agent, context.Background(), nil)
	if !errors.Is(err, ErrMaxRounds) {
		t.Fatalf("error = %v, want %v", err, ErrMaxRounds)
	}
}

func TestRunHonorsConfiguredMaximumRounds(t *testing.T) {
	var calls int
	model := modelFunc(func(context.Context, Request) (Response, error) {
		calls++
		return Response{ToolCalls: []ToolCall{{Name: "again"}}}, nil
	})
	tool := stubTool{
		spec: ToolSpec{Name: "again"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "ok", nil
		},
	}
	agent, err := NewWithConfig(model, AgentConfig{MaxRounds: 2}, tool)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runAgent(agent, context.Background(), nil)
	if !errors.Is(err, ErrMaxRounds) || !strings.Contains(err.Error(), "limit 2") || calls != 2 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
	if len(result.Messages) != 6 {
		t.Fatalf("messages = %#v, want resumable state", result.Messages)
	}
}

func TestRunRetriesRetryableError(t *testing.T) {
	var calls int
	model := modelFunc(func(context.Context, Request) (Response, error) {
		calls++
		if calls < 3 {
			return Response{}, &RetryableError{Err: errors.New("transient")}
		}
		return Response{Content: "recovered"}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runAgent(agent, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "recovered" || calls != 3 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}

func TestRunGivesUpAfterMaxAttempts(t *testing.T) {
	var calls int
	model := modelFunc(func(context.Context, Request) (Response, error) {
		calls++
		return Response{}, &RetryableError{Err: errors.New("persistent")}
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runAgent(agent, context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "persistent") {
		t.Fatalf("error = %v", err)
	}
	if calls != maxModelAttempts {
		t.Fatalf("calls = %d, want %d", calls, maxModelAttempts)
	}
}

func TestRunDoesNotRetryNonRetryableError(t *testing.T) {
	var calls int
	model := modelFunc(func(context.Context, Request) (Response, error) {
		calls++
		return Response{}, errors.New("bad request")
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runAgent(agent, context.Background(), nil)
	if err == nil || calls != 1 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
}

func TestRunDoesNotRetryAfterPartialStream(t *testing.T) {
	var calls int
	model := streamingModelFunc(func(_ context.Context, _ Request, emit func(ModelEvent)) (Response, error) {
		calls++
		emit(ModelEvent{Kind: ModelEventContentDelta, Delta: "partial"})
		return Response{}, &RetryableError{Err: errors.New("stream failed after content")}
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runAgent(agent, context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "stream failed after content") {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry after partial content)", calls)
	}
}

func TestRetryBackoffHonorsRetryAfter(t *testing.T) {
	delay := retryBackoff(0, 7*time.Second, RetryableGeneric)
	if delay != 7*time.Second {
		t.Fatalf("delay = %v, want 7s", delay)
	}
}

func TestRetryBackoffGrowsExponentially(t *testing.T) {
	first := retryBackoff(0, 0, RetryableGeneric)
	second := retryBackoff(1, 0, RetryableGeneric)
	if second <= first {
		t.Fatalf("second backoff %v not greater than first %v", second, first)
	}
}

func TestRetryBackoffRateLimitGrowsFasterThanGeneric(t *testing.T) {
	rateLimit := retryBackoff(0, 0, RetryableRateLimit)
	generic := retryBackoff(0, 0, RetryableGeneric)
	if rateLimit <= generic {
		t.Fatalf("rate limit backoff %v not greater than generic %v", rateLimit, generic)
	}
}

func TestRunRetriesRateLimitUpToMaxRateLimitAttempts(t *testing.T) {
	var calls int
	model := modelFunc(func(context.Context, Request) (Response, error) {
		calls++
		return Response{}, &RetryableError{Err: errors.New("rate limited"), Kind: RetryableRateLimit}
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runAgent(agent, context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("error = %v", err)
	}
	if calls != maxRateLimitAttempts {
		t.Fatalf("calls = %d, want %d", calls, maxRateLimitAttempts)
	}
}

func TestRunAttachesToolResultImagesToToolMessage(t *testing.T) {
	path := writeTempPNG(t)
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "snap", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			if len(request.Messages) != 4 {
				t.Fatalf("message count = %d, want 4", len(request.Messages))
			}
			toolMessage := request.Messages[3]
			if toolMessage.Role != RoleTool || toolMessage.ToolCallID != "call-1" {
				t.Fatalf("unexpected tool message: %#v", toolMessage)
			}
			// The tag is not stripped from the content.
			if toolMessage.Content != "captured @@"+path+"@@" {
				t.Fatalf("content = %q", toolMessage.Content)
			}
			if len(toolMessage.Images) != 1 || toolMessage.Images[0].Path != path {
				t.Fatalf("images = %#v", toolMessage.Images)
			}
			return Response{Content: "seen"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec:   ToolSpec{Name: "snap"},
		images: []Image{{Path: path}},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "captured @@" + path + "@@", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runAgent(agent, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "seen" || calls != 2 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}

func TestRunToolOutputWithoutImagesUnchanged(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "plain", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			toolMessage := request.Messages[3]
			if toolMessage.Content != "plain output" || len(toolMessage.Images) != 0 {
				t.Fatalf("tool message = %#v", toolMessage)
			}
			return Response{Content: "done"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec: ToolSpec{Name: "plain"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "plain output", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runAgent(agent, context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestRunForwardsStructuredImageWithCommandJSONOutput(t *testing.T) {
	path := writeTempPNG(t)
	jsonOutput := fmt.Sprintf(`{"exit_code":0,"stdout":"@@%s@@\n","stderr":"","timed_out":false,"truncated":false}`, path)
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "snap", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			toolMessage := request.Messages[3]
			if toolMessage.Role != RoleTool || toolMessage.ToolCallID != "call-1" {
				t.Fatalf("unexpected tool message: %#v", toolMessage)
			}
			if len(toolMessage.Images) != 1 || toolMessage.Images[0].Path != path {
				t.Fatalf("images = %#v", toolMessage.Images)
			}
			return Response{Content: "seen"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec:   ToolSpec{Name: "snap"},
		images: []Image{{Path: path}},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return jsonOutput, nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runAgent(agent, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "seen" || calls != 2 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}

func TestRunDoesNotInterpretImageTags(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.png")
	content := "captured @@" + missing + "@@"
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "snap", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			toolMessage := request.Messages[3]
			if len(toolMessage.Images) != 0 {
				t.Fatalf("images = %#v", toolMessage.Images)
			}
			if toolMessage.Content != content {
				t.Fatalf("content = %q", toolMessage.Content)
			}
			return Response{Content: "seen"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec: ToolSpec{Name: "snap"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return content, nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runAgent(agent, context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "seen" || calls != 2 {
		t.Fatalf("result = %#v, calls = %d", result, calls)
	}
}
