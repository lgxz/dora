package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExecuteRunsTrimmedInstruction(t *testing.T) {
	var got string
	tool := New(func(_ context.Context, instruction string) (string, error) {
		got = instruction
		return "result", nil
	})
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"instruction":"  inspect this  "}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "inspect this" || result.Content != "result" {
		t.Fatalf("instruction = %q, result = %#v", got, result)
	}
}

func TestExecuteRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"instruction":"   "}`,
		`{"instruction":"go","extra":true}`,
		`{"instruction":"go"} {}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := New(func(context.Context, string) (string, error) {
				t.Fatal("runner must not be called")
				return "", nil
			}).Execute(context.Background(), json.RawMessage(raw))
			if err == nil {
				t.Fatal("expected input error")
			}
		})
	}
}

func TestExecutePropagatesContextAndWrapsError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tool := New(func(got context.Context, _ string) (string, error) {
		if !errors.Is(got.Err(), context.Canceled) {
			t.Fatalf("context error = %v", got.Err())
		}
		return "", context.Canceled
	})
	_, err := tool.Execute(ctx, json.RawMessage(`{"instruction":"go"}`))
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "task:") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteRequiresRunner(t *testing.T) {
	_, err := New(nil).Execute(context.Background(), json.RawMessage(`{"instruction":"go"}`))
	if err == nil || !strings.Contains(err.Error(), "no runner configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteStartsBackgroundTask(t *testing.T) {
	tool := New(func(context.Context, string) (string, error) {
		t.Fatal("foreground runner must not be called by Execute")
		return "", nil
	})
	var instruction string
	tool.SetBackgroundStarter(func(got string) (string, error) {
		instruction = got
		return "task_0", nil
	})
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"instruction":" inspect ","background":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if instruction != "inspect" || !strings.Contains(result.Content, `"job_id":"task_0"`) || !strings.Contains(result.Content, `"status":"running"`) {
		t.Fatalf("instruction = %q, result = %q", instruction, result.Content)
	}
}

func TestExecuteBackgroundRequiresStarter(t *testing.T) {
	tool := New(func(context.Context, string) (string, error) { return "", nil })
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"instruction":"go","background":true}`))
	if err == nil || !strings.Contains(err.Error(), "no background starter configured") {
		t.Fatalf("error = %v", err)
	}
}
