package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/session"
	sqlitesession "github.com/lgxz/dora/session/sqlite"
)

type modelFunc func(context.Context, dora.Request) (dora.Response, error)

func (f modelFunc) Generate(ctx context.Context, request dora.Request) (dora.Response, error) {
	return f(ctx, request)
}

type stubTool struct{}

func (stubTool) Spec() dora.ToolSpec { return dora.ToolSpec{Name: "inspect"} }

func (stubTool) Execute(context.Context, json.RawMessage) (dora.ToolResult, error) {
	return dora.ToolResult{Content: "ok"}, nil
}

func newTestSession(t *testing.T, model dora.Model, cfg dora.AgentConfig) (*Session, *sqlitesession.Store) {
	t.Helper()
	store, err := sqlitesession.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := dora.NewWithConfig(model, cfg, stubTool{})
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewSession(agent, store, job.New(), "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	return application, store
}

func TestPromptCompletesAndPersistsTurn(t *testing.T) {
	application, store := newTestSession(t, modelFunc(func(_ context.Context, _ dora.Request) (dora.Response, error) {
		return dora.Response{Content: "done"}, nil
	}), dora.AgentConfig{})

	result, err := application.Prompt(context.Background(), "inspect", PromptOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || result.Content != "done" {
		t.Fatalf("result = %#v", result)
	}
	page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusCompleted {
		t.Fatalf("turns = %#v", page)
	}
}

func TestPromptContinuesAfterMaxRounds(t *testing.T) {
	var calls int
	application, store := newTestSession(t, modelFunc(func(_ context.Context, _ dora.Request) (dora.Response, error) {
		calls++
		if calls == 1 {
			return dora.Response{ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "inspect", Input: json.RawMessage(`{}`)}}}, nil
		}
		return dora.Response{Content: "done"}, nil
	}), dora.AgentConfig{MaxRounds: 1})

	continued := 0
	result, err := application.Prompt(context.Background(), "inspect", PromptOptions{
		Continue: func(context.Context, *dora.Turn, error) (bool, error) {
			continued++
			return true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Completed || result.Content != "done" || continued != 1 {
		t.Fatalf("result = %#v, continued = %d", result, continued)
	}
	page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusCompleted {
		t.Fatalf("turns = %#v", page)
	}
}

func TestPromptDeclinesContinuationAndPersistsMaxRounds(t *testing.T) {
	application, store := newTestSession(t, modelFunc(func(_ context.Context, _ dora.Request) (dora.Response, error) {
		return dora.Response{ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "inspect", Input: json.RawMessage(`{}`)}}}, nil
	}), dora.AgentConfig{MaxRounds: 1})

	result, err := application.Prompt(context.Background(), "inspect", PromptOptions{
		Continue: func(context.Context, *dora.Turn, error) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Completed {
		t.Fatalf("result = %#v", result)
	}
	page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusMaxRounds {
		t.Fatalf("turns = %#v", page)
	}
}

func TestCancelStopsActivePromptAndPersistsCancellation(t *testing.T) {
	started := make(chan struct{})
	application, store := newTestSession(t, modelFunc(func(ctx context.Context, _ dora.Request) (dora.Response, error) {
		close(started)
		<-ctx.Done()
		return dora.Response{}, ctx.Err()
	}), dora.AgentConfig{})

	done := make(chan error, 1)
	go func() {
		_, err := application.Prompt(context.Background(), "wait", PromptOptions{})
		done <- err
	}()
	<-started
	if !application.Cancel() {
		t.Fatal("Cancel returned false")
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Prompt error = %v", err)
	}
	page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusCanceled {
		t.Fatalf("turns = %#v", page)
	}
}

func TestPromptRejectsConcurrentRun(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	application, _ := newTestSession(t, modelFunc(func(_ context.Context, _ dora.Request) (dora.Response, error) {
		close(started)
		<-release
		return dora.Response{Content: "done"}, nil
	}), dora.AgentConfig{})

	done := make(chan error, 1)
	go func() {
		_, err := application.Prompt(context.Background(), "first", PromptOptions{})
		done <- err
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := application.Prompt(ctx, "second", PromptOptions{})
	if !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("Prompt error = %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
