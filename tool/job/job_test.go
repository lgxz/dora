package jobtool

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/lgxz/dora/internal/job"
)

func TestPollTaskReturnsPersistentResult(t *testing.T) {
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
		if want := "[job task_0: done]\nanalysis"; result.Content != want {
			t.Fatalf("result = %q, want %q", result.Content, want)
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
	if want := "[job task_0: failed]\nerror: context deadline exceeded"; result.Content != want {
		t.Fatalf("result = %q, want %q", result.Content, want)
	}
}

func TestListShowsIDAndStatus(t *testing.T) {
	manager := job.New()
	background := manager.StartTask("task", "wait", func(ctx context.Context) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
			return "done", nil
		}
	})
	defer func() { _, _ = manager.Kill(background.ID) }()
	result, err := New(manager).Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "task_0 running" {
		t.Fatalf("result = %q", result.Content)
	}
}

func TestKillReturnsCurrentState(t *testing.T) {
	manager := job.New()
	started := make(chan struct{})
	release := make(chan struct{})
	background := manager.StartTask("task", "ignore cancellation", func(context.Context) (string, error) {
		close(started)
		<-release
		return "late", nil
	})
	<-started

	result, err := New(manager).Execute(context.Background(), json.RawMessage(`{"action":"kill","job_id":"`+background.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "job task_0: cancelling" {
		t.Fatalf("kill result = %q", result.Content)
	}
	close(release)
	_, _ = manager.Poll(background.ID, time.Second)
}

func TestKillCompletedJobReturnsTerminalState(t *testing.T) {
	manager := job.New()
	background := manager.StartTask("task", "done", func(context.Context) (string, error) {
		return "done", nil
	})
	_, _ = manager.Poll(background.ID, time.Second)

	result, err := New(manager).Execute(context.Background(), json.RawMessage(`{"action":"kill","job_id":"`+background.ID+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "job task_0: done" {
		t.Fatalf("kill result = %q", result.Content)
	}
}
