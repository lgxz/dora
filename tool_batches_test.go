package dora

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type exclusiveStub struct {
	stubTool
	exclusive bool
}

func (t exclusiveStub) Exclusive() bool { return t.exclusive }

// eventLog records what tools and the Observer saw, in the order it happened.
type eventLog struct {
	mu      sync.Mutex
	entries []string
}

func (l *eventLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, fmt.Sprintf(format, args...))
}

func (l *eventLog) index(entry string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	for i, e := range l.entries {
		if e == entry {
			return i
		}
	}
	return -1
}

func (l *eventLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

// before fails the test unless entry a was logged before entry b.
func (l *eventLog) before(t *testing.T, a, b string) {
	t.Helper()
	ia, ib := l.index(a), l.index(b)
	if ia < 0 || ib < 0 || ia >= ib {
		t.Fatalf("want %q before %q, got indexes %d and %d in %v", a, b, ia, ib, l.snapshot())
	}
}

// loggingTool logs start and end around an optional body.
func loggingTool(name string, log *eventLog, exclusive bool, body func(context.Context)) Tool {
	return exclusiveStub{
		exclusive: exclusive,
		stubTool: stubTool{
			spec: ToolSpec{Name: name},
			execute: func(ctx context.Context, _ json.RawMessage) (string, error) {
				log.add("start:%s", name)
				if body != nil {
					body(ctx)
				}
				log.add("end:%s", name)
				return name + "-result", nil
			},
		},
	}
}

// meet blocks until both peers have arrived, or fails the test after a timeout.
// It only returns in time when the two calls really run concurrently.
func meet(t *testing.T, arrived *atomic.Int32, want int32) {
	t.Helper()
	arrived.Add(1)
	deadline := time.Now().Add(3 * time.Second)
	for arrived.Load() < want {
		if time.Now().After(deadline) {
			t.Error("calls that should run concurrently did not overlap")
			return
		}
		time.Sleep(time.Millisecond)
	}
}

func toolCalls(names ...string) []ToolCall {
	calls := make([]ToolCall, len(names))
	for i, name := range names {
		calls[i] = ToolCall{ID: fmt.Sprintf("call-%d", i), Name: name, Input: json.RawMessage(`{}`)}
	}
	return calls
}

// runOneResponse runs an Agent whose model answers with calls and then with
// "done", and returns the Observer events and the tool messages the model saw.
func runOneResponse(t *testing.T, tools []Tool, calls []ToolCall, log *eventLog) []Message {
	t.Helper()
	var requests int
	var seen []Message
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		requests++
		if requests == 1 {
			return Response{FinishReason: FinishToolCalls, ToolCalls: calls}, nil
		}
		seen = request.Messages
		return Response{FinishReason: FinishStop, Content: "done"}, nil
	})
	agent, err := New(model, tools...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		switch update.Kind {
		case UpdateToolStarted:
			log.add("started:%s", update.ToolCall.Name)
		case UpdateToolFinished:
			log.add("finished:%s", update.ToolCall.Name)
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	return seen
}

func TestExclusiveToolActsAsBarrier(t *testing.T) {
	log := &eventLog{}
	var first, last atomic.Int32
	tools := []Tool{
		loggingTool("p1", log, false, func(context.Context) { meet(t, &first, 2) }),
		loggingTool("p2", log, false, func(context.Context) { meet(t, &first, 2) }),
		loggingTool("ex", log, true, func(context.Context) { time.Sleep(20 * time.Millisecond) }),
		loggingTool("p3", log, false, func(context.Context) { meet(t, &last, 2) }),
		loggingTool("p4", log, false, func(context.Context) { meet(t, &last, 2) }),
	}
	messages := runOneResponse(t, tools, toolCalls("p1", "p2", "ex", "p3", "p4"), log)

	// Earlier calls finish before the exclusive one starts; later calls wait.
	log.before(t, "end:p1", "start:ex")
	log.before(t, "end:p2", "start:ex")
	log.before(t, "end:ex", "start:p3")
	log.before(t, "end:ex", "start:p4")

	// Tool messages stay in the model's call order.
	var got []string
	for _, m := range messages {
		if m.Role == RoleTool {
			got = append(got, m.ToolCallID+"="+m.Content)
		}
	}
	want := []string{"call-0=p1-result", "call-1=p2-result", "call-2=ex-result", "call-3=p3-result", "call-4=p4-result"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tool messages = %v, want %v", got, want)
	}
}

func TestConsecutiveExclusiveToolsRunOneAtATimeInOrder(t *testing.T) {
	log := &eventLog{}
	var active, maxActive atomic.Int32
	body := func(delay time.Duration) func(context.Context) {
		return func(context.Context) {
			n := active.Add(1)
			for {
				old := maxActive.Load()
				if n <= old || maxActive.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(delay)
			active.Add(-1)
		}
	}
	tools := []Tool{
		loggingTool("e1", log, true, body(60*time.Millisecond)), // slowest, must still go first
		loggingTool("e2", log, true, body(10*time.Millisecond)),
		loggingTool("e3", log, true, body(0)),
	}
	runOneResponse(t, tools, toolCalls("e3", "e1", "e2"), log)

	if maxActive.Load() != 1 {
		t.Fatalf("%d exclusive calls overlapped", maxActive.Load())
	}
	var order []string
	for _, e := range log.snapshot() {
		if strings.HasPrefix(e, "start:") {
			order = append(order, strings.TrimPrefix(e, "start:"))
		}
	}
	if want := []string{"e3", "e1", "e2"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("start order = %v, want the model's call order %v", order, want)
	}
}

func TestExclusiveFalseKeepsRunningConcurrently(t *testing.T) {
	log := &eventLog{}
	var arrived atomic.Int32
	tools := []Tool{
		loggingTool("a", log, false, func(context.Context) { meet(t, &arrived, 2) }),
		loggingTool("b", log, false, func(context.Context) { meet(t, &arrived, 2) }),
	}
	runOneResponse(t, tools, toolCalls("a", "b"), log)
}

func TestObserverEventsFollowBatches(t *testing.T) {
	log := &eventLog{}
	tools := []Tool{
		loggingTool("r", log, false, nil),
		loggingTool("w", log, true, nil),
		loggingTool("s", log, false, nil),
	}
	runOneResponse(t, tools, toolCalls("r", "w", "s"), log)

	// Started events keep the model's order, and a call is reported started
	// only when it really begins, i.e. after the earlier batch has finished.
	log.before(t, "started:r", "started:w")
	log.before(t, "started:w", "started:s")
	log.before(t, "finished:r", "started:w")
	log.before(t, "finished:w", "started:s")
}

func TestBarrierSurvivesFailingAndPanickingCalls(t *testing.T) {
	log := &eventLog{}
	panicking := exclusiveStub{exclusive: true, stubTool: stubTool{
		spec: ToolSpec{Name: "boom"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			panic("kaboom")
		},
	}}
	tools := []Tool{
		loggingTool("before", log, false, nil),
		panicking,
		loggingTool("after", log, false, nil),
	}
	calls := toolCalls("before", "boom", "missing", "after")
	calls = append(calls, ToolCall{ID: "call-bad", Name: "after", Input: json.RawMessage(`{`)})
	messages := runOneResponse(t, tools, calls, log)

	var toolMessages []Message
	for _, m := range messages {
		if m.Role == RoleTool {
			toolMessages = append(toolMessages, m)
		}
	}
	if len(toolMessages) != len(calls) {
		t.Fatalf("got %d tool messages for %d calls", len(toolMessages), len(calls))
	}
	for i, m := range toolMessages {
		if m.ToolCallID != calls[i].ID {
			t.Fatalf("tool message %d answers %q, want %q", i, m.ToolCallID, calls[i].ID)
		}
	}
	log.before(t, "end:before", "finished:boom")
	if log.index("end:after") < 0 {
		t.Fatalf("a call after the panicking exclusive one never ran: %v", log.snapshot())
	}
}

func TestPlanToolBatches(t *testing.T) {
	tools := map[string]Tool{
		"r": exclusiveStub{stubTool: stubTool{spec: ToolSpec{Name: "r"}}},
		"x": exclusiveStub{exclusive: true, stubTool: stubTool{spec: ToolSpec{Name: "x"}}},
		"p": stubTool{spec: ToolSpec{Name: "p"}}, // does not implement ExclusiveTool at all
	}
	cases := []struct {
		names []string
		want  [][]int
	}{
		{nil, nil},
		{[]string{"r", "p", "unknown"}, [][]int{{0, 1, 2}}},
		{[]string{"x"}, [][]int{{0}}},
		{[]string{"x", "x", "x"}, [][]int{{0}, {1}, {2}}},
		{[]string{"r", "x", "p"}, [][]int{{0}, {1}, {2}}},
		{[]string{"r", "p", "x", "r", "p", "x", "x", "p"}, [][]int{{0, 1}, {2}, {3, 4}, {5}, {6}, {7}}},
	}
	for _, tc := range cases {
		if got := planToolBatches(toolCalls(tc.names...), tools); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("planToolBatches(%v) = %v, want %v", tc.names, got, tc.want)
		}
	}
}
