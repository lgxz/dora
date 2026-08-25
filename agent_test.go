package dora

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

func (t stubTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	content, err := t.execute(ctx, input)
	return ToolResult{Content: content}, err
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
	var user string
	for _, message := range input {
		switch message.Role {
		case RoleUser:
			user = message.Content
		}
	}
	turn := NewTurn(user)
	err := agent.RunObserved(ctx, turn, observer)
	content, _ := turn.Result()
	return testRunResult{Content: content, Messages: turn.Messages(), Continuation: turn.Continuation()}, err
}

func TestRunReturnsFinalResponse(t *testing.T) {
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		if len(request.Messages) != 2 || request.Messages[0].Role != RoleSystem || request.Messages[0].Content != "system" || request.Messages[1].Content != "hello" {
			t.Fatalf("unexpected messages: %#v", request.Messages)
		}
		return Response{Content: "hi"}, nil
	})
	agent, err := NewWithConfig(model, AgentConfig{SystemPrompt: "system"})
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
		{Role: RoleSystem, Content: "system"},
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi"},
	}
	if !reflect.DeepEqual(result.Messages, want) {
		t.Fatalf("messages = %#v, want %#v", result.Messages, want)
	}
}

func TestRunObservedEmitsUsagePerRound(t *testing.T) {
	// Two rounds: a tool round then a final round. Each must emit an
	// UpdateMessageReceived observer event carrying the assistant message and
	// the Response.Usage, including the nil (no usage) case without panicking.
	inputUsage := &Usage{InputTokens: 4, OutputTokens: 2, TotalTokens: 6}
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		if len(request.Messages) == 1 {
			return Response{Content: "use tool", ToolCalls: []ToolCall{{ID: "call-1", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)}}, Usage: inputUsage}, nil
		}
		return Response{Content: "done", Usage: nil}, nil
	})
	tool := stubTool{
		spec:    ToolSpec{Name: "bash"},
		execute: func(context.Context, json.RawMessage) (string, error) { return "ok", nil },
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	var messageReceives []Update
	observer := ObserverFunc(func(update Update) {
		if update.Kind == UpdateMessageReceived {
			messageReceives = append(messageReceives, update)
		}
	})

	input := []Message{{Role: RoleUser, Content: "hello"}}
	if _, err := runAgentObserved(agent, context.Background(), input, observer); err != nil {
		t.Fatal(err)
	}
	if len(messageReceives) != 2 {
		t.Fatalf("messageReceives = %#v, want 2 updates", messageReceives)
	}
	if messageReceives[0].Usage == inputUsage || messageReceives[0].Usage == nil || *messageReceives[0].Usage != *inputUsage {
		t.Fatalf("round 1 usage = %#v, want defensive copy of %#v", messageReceives[0].Usage, inputUsage)
	}
	if messageReceives[1].Usage != nil {
		t.Fatalf("round 2 usage = %#v, want nil", messageReceives[1].Usage)
	}
	for _, update := range messageReceives {
		if update.Message.Role != RoleAssistant {
			t.Fatalf("message_received role = %#v, want RoleAssistant", update.Message.Role)
		}
	}
}

func TestRunEmitsToolFinishedOnSuccess(t *testing.T) {
	// A tool that executes successfully must emit one UpdateToolFinished
	// carrying the tool result message (RoleTool), the correct ToolCall, and no
	// error, distinct from the failure path which carries a non-nil Err.
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		if calls == 1 {
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)}}}, nil
		}
		return Response{Content: "done"}, nil
	})
	tool := stubTool{
		spec:    ToolSpec{Name: "bash"},
		execute: func(context.Context, json.RawMessage) (string, error) { return "ok", nil },
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	var finished []Update
	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		if update.Kind == UpdateToolFinished {
			finished = append(finished, update)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(finished) != 1 {
		t.Fatalf("finished updates = %#v, want 1", finished)
	}
	got := finished[0]
	if got.Err != nil {
		t.Fatalf("finished err = %#v, want nil", got.Err)
	}
	if got.ToolCall.ID != "call-1" || got.ToolCall.Name != "bash" {
		t.Fatalf("finished tool call = %#v, want call-1/bash", got.ToolCall)
	}
	if got.Message.Role != RoleTool || got.Message.Content != "ok" || got.Message.ToolCallID != "call-1" {
		t.Fatalf("finished message = %#v, want the tool result", got.Message)
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

func TestRunObservedWithOptionsExcludesToolFromModelAndExecution(t *testing.T) {
	var modelCalls int
	var taskCalls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		modelCalls++
		switch modelCalls {
		case 1:
			if len(request.Tools) != 1 || request.Tools[0].Name != "read" {
				t.Fatalf("tools = %#v, want only read", request.Tools)
			}
			// Even if a provider returns a call for an excluded tool, execution
			// must use the same filtered tool set as the model request.
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "task", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			result := request.Messages[len(request.Messages)-1]
			if !strings.Contains(result.Content, `tool "task" not found`) {
				t.Fatalf("tool result = %#v", result)
			}
			return Response{Content: "recovered"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	readTool := stubTool{spec: ToolSpec{Name: "read"}, execute: func(context.Context, json.RawMessage) (string, error) {
		return "read", nil
	}}
	taskTool := stubTool{spec: ToolSpec{Name: "task"}, execute: func(context.Context, json.RawMessage) (string, error) {
		taskCalls++
		return "nested", nil
	}}
	agent, err := New(model, readTool, taskTool)
	if err != nil {
		t.Fatal(err)
	}
	turn := NewTurn("delegate")
	if err := agent.RunObservedWithOptions(context.Background(), turn, nil, RunOptions{ExcludeTools: []string{"task"}}); err != nil {
		t.Fatal(err)
	}
	result, _ := turn.Result()
	if result != "recovered" || taskCalls != 0 {
		t.Fatalf("result = %q, task calls = %d", result, taskCalls)
	}
}

func TestRunObservedWithOptionsProvidesWorkingDirectoryToTools(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, _ Request) (Response, error) {
		calls++
		if calls == 1 {
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "inspect", Input: json.RawMessage(`{}`)}}}, nil
		}
		return Response{Content: "done"}, nil
	})
	tool := stubTool{
		spec: ToolSpec{Name: "inspect"},
		execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
			if got := WorkingDirectory(ctx); got != "/tmp/project" {
				t.Fatalf("working directory = %q, want /tmp/project", got)
			}
			return "ok", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.RunObservedWithOptions(context.Background(), NewTurn("inspect"), nil, RunOptions{
		WorkingDirectory: "/tmp/project",
	}); err != nil {
		t.Fatal(err)
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

func TestRunForwardsReasoningDeltasAndHistory(t *testing.T) {
	var calls int
	model := streamingModelFunc(func(_ context.Context, _ Request, emit func(ModelEvent)) (Response, error) {
		calls++
		switch calls {
		case 1:
			emit(ModelEvent{Kind: ModelEventReasoningDelta, Delta: "think "})
			emit(ModelEvent{Kind: ModelEventReasoningDelta, Delta: "hard"})
			emit(ModelEvent{Kind: ModelEventContentDelta, Delta: "checking"})
			return Response{
				Reasoning: "think hard",
				Content:   "checking",
				ToolCalls: []ToolCall{{ID: "call-1", Name: "weather", Input: json.RawMessage(`{}`)}},
			}, nil
		case 2:
			return Response{Content: "sunny", Reasoning: "done thinking"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{spec: ToolSpec{Name: "weather"}, execute: func(context.Context, json.RawMessage) (string, error) {
		return "sunny", nil
	}}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	var reasonings []string
	var reasoning string
	result, err := runAgentObserved(agent, context.Background(), []Message{{Role: RoleUser, Content: "weather?"}}, ObserverFunc(func(update Update) {
		switch update.Kind {
		case UpdateReasoningDelta:
			reasoning += update.Delta
		case UpdateMessageReceived:
			if update.Message.Role == RoleAssistant {
				reasonings = append(reasonings, update.Message.Reasoning)
			}
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "sunny" || reasoning != "think hard" {
		t.Fatalf("result = %#v, reasoning = %q", result, reasoning)
	}
	// The tool round's assistant message keeps its reasoning in the history
	// that subsequent model calls see.
	if len(reasonings) != 2 || reasonings[0] != "think hard" || reasonings[1] != "done thinking" {
		t.Fatalf("assistant reasonings = %#v", reasonings)
	}
	if result.Messages[1].Reasoning != "think hard" {
		t.Fatalf("round assistant reasoning = %q", result.Messages[1].Reasoning)
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
	turn := NewTurn("continue")
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
			if len(request.Messages) != 3 {
				t.Fatalf("message count = %d, want 3", len(request.Messages))
			}
			result := request.Messages[2]
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
			if len(request.Messages) != 3 {
				t.Fatalf("message count = %d, want 3", len(request.Messages))
			}
			result := request.Messages[2]
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
			if len(request.Messages) != 3 {
				t.Fatalf("message count = %d, want 3", len(request.Messages))
			}
			result := request.Messages[2]
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
			if len(request.Messages) != 4 {
				t.Fatalf("message count = %d, want 4", len(request.Messages))
			}
			// Tool results must appear in the model's call order.
			if request.Messages[2].ToolCallID != "call-slow" || request.Messages[2].Content != "slow-done" {
				t.Fatalf("unexpected first tool result: %#v", request.Messages[2])
			}
			if request.Messages[3].ToolCallID != "call-fast" || request.Messages[3].Content != "fast-done" {
				t.Fatalf("unexpected second tool result: %#v", request.Messages[3])
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
		case UpdateToolFinished:
			events = append(events, "added:"+update.ToolCall.ID)
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

	// Started events retain model call order, while finished events arrive in
	// completion order so the fast tool is visible without waiting for slow.
	wantEvents := []string{
		"start:slow", "start:fast",
		"added:call-fast", "added:call-slow",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("observer events = %#v, want %#v", events, wantEvents)
	}
}

func TestRunToolStartedCarriesRealStartTime(t *testing.T) {
	// The UpdateToolStarted event is emitted immediately before its goroutine is
	// launched, so StartedAt should closely match event delivery time.
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

	var startedAt, deliveredAt time.Time
	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		if update.Kind == UpdateToolStarted {
			startedAt = update.StartedAt
			deliveredAt = time.Now()
		}
	}))
	if err != nil {
		t.Fatal(err)
	}

	if startedAt.IsZero() {
		t.Fatal("UpdateToolStarted did not carry a StartedAt")
	}
	elapsed := deliveredAt.Sub(startedAt)
	if elapsed < 0 || elapsed >= slowDelay/2 {
		t.Fatalf("UpdateToolStarted delivered after %v, want immediate delivery", elapsed)
	}
}

func TestRunEmitsToolFailedOnRecoverableError(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		if calls == 1 {
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "fail", Input: json.RawMessage(`{"args":true}`)}}}, nil
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

	var finished []Update
	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		if update.Kind == UpdateToolFinished {
			finished = append(finished, update)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(finished) != 1 {
		t.Fatalf("finished updates = %#v, want 1", finished)
	}
	got := finished[0]
	if got.ToolCall.Name != "fail" {
		t.Fatalf("finished tool call = %#v, want fail", got.ToolCall)
	}
	// A failed tool must still carry its error message together with the non-nil
	// error: both are reported on the same event. With valid input the message
	// is the execute-failure text persisted to the conversation.
	if got.Err == nil {
		t.Fatalf("finished err = %#v, want non-nil", got.Err)
	}
	if got.Message.Role != RoleTool || !strings.Contains(got.Message.Content, `tool "fail" failed`) {
		t.Fatalf("finished message = %#v, want the tool error message", got.Message)
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
	if len(result.Messages) != 5 {
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

func TestRunToolOutputWithoutImagesUnchanged(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "call-1", Name: "plain", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			toolMessage := request.Messages[2]
			if toolMessage.Content != "plain output" {
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
			toolMessage := request.Messages[2]
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

// contextSizeModel is a Model stub that also reports a context size, used to
// verify Agent caches the probed ContextSize value at construction.
type contextSizeModel struct {
	modelFunc
	size int
}

func (m contextSizeModel) ContextSize() int { return m.size }

type outputSizeModel struct {
	modelFunc
	size int
}

func (m outputSizeModel) MaxOutputTokens() int { return m.size }

func TestAgentCapturesContextWindow(t *testing.T) {
	model := contextSizeModel{modelFunc: modelFunc(noopGenerate), size: 4096}
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	if agent.contextWindow != 4096 {
		t.Fatalf("contextWindow = %d, want 4096", agent.contextWindow)
	}
}

func TestAgentFallsBackToDefaultContextWindow(t *testing.T) {
	// modelFunc does not implement ContextSize; the agent must use the default.
	agent, err := New(modelFunc(noopGenerate))
	if err != nil {
		t.Fatal(err)
	}
	if agent.contextWindow != DefaultContextWindowTokens {
		t.Fatalf("contextWindow = %d, want %d", agent.contextWindow, DefaultContextWindowTokens)
	}
}

func TestAgentFallsBackWhenContextSizeNonPositive(t *testing.T) {
	for _, size := range []int{0, -1} {
		model := contextSizeModel{modelFunc: modelFunc(noopGenerate), size: size}
		agent, err := New(model)
		if err != nil {
			t.Fatal(err)
		}
		if agent.contextWindow != DefaultContextWindowTokens {
			t.Fatalf("contextWindow = %d, want %d", agent.contextWindow, DefaultContextWindowTokens)
		}
	}
}

func TestAgentCapturesMaxOutputTokens(t *testing.T) {
	model := outputSizeModel{modelFunc: modelFunc(noopGenerate), size: 8192}
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	if agent.maxOutputTokens != 8192 {
		t.Fatalf("maxOutputTokens = %d, want 8192", agent.maxOutputTokens)
	}
}

func TestAgentIgnoresNonPositiveMaxOutputTokens(t *testing.T) {
	for _, size := range []int{0, -1} {
		agent, err := New(outputSizeModel{modelFunc: modelFunc(noopGenerate), size: size})
		if err != nil {
			t.Fatal(err)
		}
		if agent.maxOutputTokens != 0 {
			t.Fatalf("maxOutputTokens = %d, want unknown", agent.maxOutputTokens)
		}
	}
}

func noopGenerate(context.Context, Request) (Response, error) {
	return Response{Content: "done"}, nil
}
