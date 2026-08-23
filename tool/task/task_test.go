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
