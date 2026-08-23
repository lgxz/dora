package jobtool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora/internal/job"
)

func TestResultReturnsContent(t *testing.T) {
	tool := New(job.New())
	result := tool.result(`{"stdout":"hello"}`)
	if result.Content != `{"stdout":"hello"}` {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestPollTaskReturnsPersistentResultWithoutKind(t *testing.T) {
	manager := job.New()
	background := manager.StartTask("task", "inspect", func(context.Context) (string, error) {
		return "analysis", nil
	})
	tool := New(manager)
	input := json.RawMessage(`{"action":"poll","job_id":"` + background.ID + `","wait_seconds":1}`)
	for range 2 {
		result, err := tool.Execute(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Content, `"job_id":"task_0"`) ||
			!strings.Contains(result.Content, `"status":"done"`) ||
			!strings.Contains(result.Content, `"result":"analysis"`) ||
			strings.Contains(result.Content, `"kind"`) {
			t.Fatalf("result = %q", result.Content)
		}
	}
}

func TestPollTaskFailure(t *testing.T) {
	manager := job.New()
	background := manager.StartTask("task", "fail", func(context.Context) (string, error) {
		return "", context.DeadlineExceeded
	})
	result, err := New(manager).Execute(context.Background(), json.RawMessage(`{"action":"poll","job_id":"`+background.ID+`","wait_seconds":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"status":"failed"`) || !strings.Contains(result.Content, `"error":"context deadline exceeded"`) {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestListUsesIDsWithoutKind(t *testing.T) {
	manager := job.New()
	background := manager.StartTask("task", "wait", func(ctx context.Context) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
			return "done", nil
		}
	})
	defer manager.Kill(background.ID)
	result, err := New(manager).Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Content, `"job_id":"task_0"`) || strings.Contains(result.Content, `"kind"`) {
		t.Fatalf("result = %q", result.Content)
	}
}
