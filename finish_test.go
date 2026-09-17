package dora

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestOutputLimitRecoveryDiscardsPartialCallsAndPreservesHistory(t *testing.T) {
	var requests []Request
	executed := 0
	m := modelFunc(func(_ context.Context, r Request) (Response, error) {
		requests = append(requests, r)
		switch len(requests) {
		case 1:
			return Response{FinishReason: FinishOutputLimit, RawFinishReason: "length", OutputBudget: 32768,
				Reasoning: "unfinished reasoning", Content: "partial answer", Continuation: "discard-me",
				ToolCalls: []ToolCall{{ID: "partial", Name: "run", Input: json.RawMessage(`{"x":`)}},
				Usage:     &Usage{InputTokens: 100, OutputTokens: 32768, TotalTokens: 32868}}, nil
		case 2:
			return Response{FinishReason: FinishToolCalls, OutputBudget: 65536,
				ToolCalls: []ToolCall{{ID: "valid", Name: "run", Input: json.RawMessage(`{}`)}},
				Usage:     &Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120}}, nil
		default:
			return Response{FinishReason: FinishStop, Content: "done", Reasoning: "finished", OutputBudget: 32768,
				Usage: &Usage{InputTokens: 200, OutputTokens: 10, TotalTokens: 210}}, nil
		}
	})
	a, err := New(m, stubTool{spec: ToolSpec{Name: "run"}, execute: func(context.Context, json.RawMessage) (string, error) { executed++; return "ok", nil }})
	if err != nil {
		t.Fatal(err)
	}
	turn := NewTurn("solve")
	if err := a.Run(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 3 || executed != 1 {
		t.Fatalf("calls=%d tools=%d", len(requests), executed)
	}
	if !reflect.DeepEqual(requests[0].Messages, requests[1].Messages) || requests[1].Continuation != "" {
		t.Fatal("discarded response leaked into history")
	}
	if requests[1].MaxOutputTokens == nil || *requests[1].MaxOutputTokens != 65536 || requests[2].MaxOutputTokens != nil {
		t.Fatal("incorrect recovery budgets")
	}
	attempts := turn.Attempts()
	if len(attempts) != 3 || attempts[0].Disposition != "discarded" || attempts[1].Recovery != 1 || attempts[1].Disposition != "tools" || attempts[2].RoundIndex != 1 {
		t.Fatalf("attempts=%+v", attempts)
	}
	if _, err := json.Marshal(attempts); err != nil {
		t.Fatal("partial tool arguments must remain serializable", err)
	}
	if turn.TotalUsage().TotalTokens != 33198 {
		t.Fatalf("usage=%+v", turn.TotalUsage())
	}
	attempts[0].Usage.TotalTokens = 0
	attempts[0].ToolCalls[0].Input = "changed"
	if turn.Attempts()[0].Usage.TotalTokens != 32868 || turn.Attempts()[0].ToolCalls[0].Input == "changed" {
		t.Fatal("mutable audit records")
	}
	final, ok := turn.FinalResponse()
	if !ok || final.Reasoning != "finished" || final.FinishReason != FinishStop {
		t.Fatalf("final=%+v", final)
	}
}

func TestOutputLimitRecoveryStops(t *testing.T) {
	for _, tc := range []struct {
		name                                              string
		budget, hard, cap, retries, wantCalls, wantBudget int
	}{
		{"default", 32768, 0, 0, 1, 2, 65536},
		{"hard limit", 32768, 32768, 0, 1, 1, 0},
		{"clamped", 32768, 48000, 0, 1, 2, 48000},
		{"configured cap", 32768, 0, 40000, 1, 2, 40000},
		{"unknown budget", 0, 0, 0, 1, 1, 0},
		{"disabled", 32768, 0, 0, 0, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			model := modelFunc(func(_ context.Context, r Request) (Response, error) {
				calls++
				if calls == 2 && (r.MaxOutputTokens == nil || *r.MaxOutputTokens != tc.wantBudget) {
					t.Fatalf("budget=%v", r.MaxOutputTokens)
				}
				budget := tc.budget
				if r.MaxOutputTokens != nil {
					budget = *r.MaxOutputTokens
				}
				return Response{FinishReason: FinishOutputLimit, OutputBudget: budget, Content: "partial"}, nil
			})
			a, err := NewWithConfig(model, AgentConfig{OutputLimitRecovery: &OutputLimitRecovery{MaxRetries: tc.retries, MaxOutputTokens: tc.cap}})
			if err != nil {
				t.Fatal(err)
			}
			a.maxOutputTokens = tc.hard
			turn := NewTurn("solve")
			err = a.Run(context.Background(), turn)
			if !errors.Is(err, ErrOutputLimit) || turn.Completed() || calls != tc.wantCalls || turn.RunError() == nil {
				t.Fatalf("error=%v complete=%v calls=%d", err, turn.Completed(), calls)
			}
		})
	}
}

func TestFinishReasonStrictValidation(t *testing.T) {
	for _, tc := range []struct {
		name string
		r    Response
		want error
	}{
		{"missing", Response{Content: "looks done"}, ErrUnknownFinishReason},
		{"unknown", Response{FinishReason: FinishUnknown, Content: "done"}, ErrUnknownFinishReason},
		{"empty stop", Response{FinishReason: FinishStop, Reasoning: "thinking", Content: " \n"}, ErrEmptyResponse},
		{"blocked", Response{FinishReason: FinishBlocked}, ErrModelBlocked},
		{"stop with calls", Response{FinishReason: FinishStop, Content: "done", ToolCalls: []ToolCall{{ID: "x", Name: "run"}}}, ErrInvalidModelResponse},
		{"missing calls", Response{FinishReason: FinishToolCalls}, ErrInvalidModelResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			a, _ := New(modelFunc(func(context.Context, Request) (Response, error) { calls++; return tc.r, nil }))
			turn := NewTurn("test")
			err := a.Run(context.Background(), turn)
			if !errors.Is(err, tc.want) || turn.Completed() || calls != 1 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestRecoveryHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	a, _ := New(modelFunc(func(context.Context, Request) (Response, error) {
		calls++
		cancel()
		return Response{FinishReason: FinishOutputLimit, OutputBudget: 32768}, nil
	}))
	turn := NewTurn("test")
	if err := a.Run(ctx, turn); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
