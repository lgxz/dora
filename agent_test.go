package dora

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

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
}

func (t stubTool) Spec() ToolSpec { return t.spec }

func (t stubTool) Execute(ctx context.Context, input json.RawMessage) (string, error) {
	return t.execute(ctx, input)
}

func TestRunReturnsFinalResponse(t *testing.T) {
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		if len(request.Messages) != 1 || request.Messages[0].Content != "hello" {
			t.Fatalf("unexpected messages: %#v", request.Messages)
		}
		return Response{Content: "hi"}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}

	input := []Message{{Role: RoleUser, Content: "hello"}}
	result, err := agent.Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}

	if result.Content != "hi" {
		t.Fatalf("content = %q, want %q", result.Content, "hi")
	}
	want := []Message{
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
			if len(request.Messages) != 3 {
				t.Fatalf("message count = %d, want 3", len(request.Messages))
			}
			result := request.Messages[2]
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
	result, err := agent.Run(context.Background(), []Message{{Role: RoleUser, Content: "weather?"}})
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
	result, err := agent.RunObserved(context.Background(), []Message{{Role: RoleUser, Content: "weather?"}}, ObserverFunc(func(update Update) {
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

func TestRunStateResumesAndReturnsContinuation(t *testing.T) {
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
	result, err := agent.RunState(context.Background(), State{
		Messages:     []Message{{Role: RoleUser, Content: "continue"}},
		Continuation: "saved-state",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "done" || result.Continuation != "next-state" {
		t.Fatalf("result = %#v", result)
	}
}

func TestRunDoesNotMutateCallerMessages(t *testing.T) {
	inputBytes := json.RawMessage(`{"value":1}`)
	input := []Message{{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{{
			ID:    "original",
			Name:  "original",
			Input: inputBytes,
		}},
	}}

	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		request.Messages[0].ToolCalls[0].Input[0] = '['
		request.Messages[0].ToolCalls[0].Name = "changed"
		return Response{Content: "done"}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Run(context.Background(), input); err != nil {
		t.Fatal(err)
	}

	if input[0].ToolCalls[0].Name != "original" || string(inputBytes) != `{"value":1}` {
		t.Fatalf("caller input was mutated: %#v", input)
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

func TestRunReturnsMissingToolError(t *testing.T) {
	model := modelFunc(func(context.Context, Request) (Response, error) {
		return Response{ToolCalls: []ToolCall{{Name: "missing"}}}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := agent.Run(context.Background(), nil); err == nil {
		t.Fatal("expected missing tool error")
	}
}

func TestRunWrapsToolError(t *testing.T) {
	want := errors.New("broken")
	model := modelFunc(func(context.Context, Request) (Response, error) {
		return Response{ToolCalls: []ToolCall{{Name: "fail"}}}, nil
	})
	tool := stubTool{
		spec: ToolSpec{Name: "fail"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "", want
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	_, err = agent.Run(context.Background(), nil)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
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

	_, err = agent.Run(context.Background(), nil)
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
	result, err := agent.Run(context.Background(), nil)
	if !errors.Is(err, ErrMaxRounds) || !strings.Contains(err.Error(), "limit 2") || calls != 2 {
		t.Fatalf("error = %v, calls = %d", err, calls)
	}
	if len(result.Messages) != 4 {
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
	result, err := agent.Run(context.Background(), nil)
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
	_, err = agent.Run(context.Background(), nil)
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
	_, err = agent.Run(context.Background(), nil)
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
	_, err = agent.Run(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "stream failed after content") {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (no retry after partial content)", calls)
	}
}

func TestRetryBackoffHonorsRetryAfter(t *testing.T) {
	delay := retryBackoff(0, 7*time.Second)
	if delay != 7*time.Second {
		t.Fatalf("delay = %v, want 7s", delay)
	}
}

func TestRetryBackoffGrowsExponentially(t *testing.T) {
	first := retryBackoff(0, 0)
	second := retryBackoff(1, 0)
	if second <= first {
		t.Fatalf("second backoff %v not greater than first %v", second, first)
	}
}
