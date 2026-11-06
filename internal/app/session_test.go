package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
		return dora.Response{FinishReason: dora.FinishStop, Content: "done"}, nil
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
			return dora.Response{FinishReason: dora.FinishToolCalls, ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "inspect", Input: json.RawMessage(`{}`)}}}, nil
		}
		return dora.Response{FinishReason: dora.FinishStop, Content: "done"}, nil
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
		return dora.Response{FinishReason: dora.FinishToolCalls, ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "inspect", Input: json.RawMessage(`{}`)}}}, nil
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

// The commit of a completed turn must survive a cancellation that races the
// transition from the run loop to the store, not only the explicitly canceled
// terminal paths. The kernel's last context check happens before the final
// message notification, so canceling from the observer deterministically lands
// in that window.
func TestPromptPersistsCompletedTurnWhenContextCancelsMidRun(t *testing.T) {
	application, store := newTestSession(t, modelFunc(func(_ context.Context, _ dora.Request) (dora.Response, error) {
		return dora.Response{FinishReason: dora.FinishStop, Content: "done"}, nil
	}), dora.AgentConfig{})

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observer := dora.ObserverFunc(func(update dora.Update) {
		if update.Kind == dora.UpdateMessageReceived && len(update.Message.ToolCalls) == 0 {
			cancel()
		}
	})
	result, err := application.Prompt(runCtx, "cancel mid-run", PromptOptions{Observer: observer})
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

func TestPromptRejectsConcurrentRun(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	application, _ := newTestSession(t, modelFunc(func(_ context.Context, _ dora.Request) (dora.Response, error) {
		close(started)
		<-release
		return dora.Response{FinishReason: dora.FinishStop, Content: "done"}, nil
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

func twoToolRoundsThenStop(onCall func(call int, request dora.Request)) modelFunc {
	var calls int
	return func(_ context.Context, request dora.Request) (dora.Response, error) {
		calls++
		if onCall != nil {
			onCall(calls, request)
		}
		if calls <= 2 {
			return dora.Response{FinishReason: dora.FinishToolCalls, ToolCalls: []dora.ToolCall{{
				ID: "call-" + string(rune('0'+calls)), Name: "inspect", Input: json.RawMessage(`{}`),
			}}}, nil
		}
		return dora.Response{FinishReason: dora.FinishStop, Content: "done"}, nil
	}
}

func TestPromptSavesEachRoundBeforeTheNextModelRequest(t *testing.T) {
	var store *sqlitesession.Store
	model := twoToolRoundsThenStop(func(call int, _ dora.Request) {
		page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
		if err != nil {
			t.Errorf("call %d: %v", call, err)
			return
		}
		if call == 1 {
			if page.Total != 0 {
				t.Errorf("turn saved before any round completed: %+v", page)
			}
			return
		}
		if page.Total != 1 || page.Turns[0].Status != session.TurnStatusRunning || page.Turns[0].RoundCount != call-1 {
			t.Errorf("model call %d saw %+v, want one running turn with %d rounds", call, page, call-1)
		}
	})
	var application *Session
	application, store = newTestSession(t, model, dora.AgentConfig{})

	result, err := application.Prompt(context.Background(), "inspect twice", PromptOptions{})
	if err != nil || !result.Completed || result.Content != "done" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusCompleted || page.Turns[0].Result != "done" || page.Turns[0].RoundCount != 2 {
		t.Fatalf("final turns = %+v, want a single completed turn with 2 rounds", page)
	}
}

type failingSyncStore struct{ session.Store }

func (s failingSyncStore) BeginTurn(turn *dora.Turn) session.TurnWriter {
	return failingSyncWriter{s.Store.BeginTurn(turn)}
}

type failingSyncWriter struct{ session.TurnWriter }

func (failingSyncWriter) Sync(context.Context) error { return errors.New("disk is full") }

func TestPromptWarnsOnceWhenProgressSaveFailsAndFinishStillSavesEverything(t *testing.T) {
	real, err := sqlitesession.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := dora.NewWithConfig(twoToolRoundsThenStop(nil), dora.AgentConfig{}, stubTool{})
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewSession(agent, failingSyncStore{real}, job.New(), "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })

	var warnings []string
	observer := dora.ObserverFunc(func(update dora.Update) {
		if update.Kind == dora.UpdateInfo && strings.Contains(update.Info, "could not save turn progress") {
			warnings = append(warnings, update.Info)
		}
	})
	result, err := application.Prompt(context.Background(), "inspect twice", PromptOptions{Observer: observer})
	if err != nil || !result.Completed {
		t.Fatalf("a failing progress save must not fail the run: result = %#v, err = %v", result, err)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "disk is full") {
		t.Fatalf("warnings = %q, want exactly one mentioning the cause", warnings)
	}
	page, err := real.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusCompleted || page.Turns[0].RoundCount != 2 {
		t.Fatalf("turns = %+v, want Finish to store the complete turn", page)
	}
}

// finishFault makes the terminal save fail: the first failures calls to
// TurnWriter.Finish return an error (every call when failures is negative).
type finishFault struct {
	session.Store
	failures int
	calls    int
}

func (s *finishFault) BeginTurn(turn *dora.Turn) session.TurnWriter {
	return &finishFaultWriter{TurnWriter: s.Store.BeginTurn(turn), fault: s}
}

type finishFaultWriter struct {
	session.TurnWriter
	fault *finishFault
}

func (w *finishFaultWriter) Finish(ctx context.Context, status session.TurnStatus, cause error) (int64, error) {
	w.fault.calls++
	if w.fault.failures < 0 || w.fault.calls <= w.fault.failures {
		return 0, errors.New("database is locked")
	}
	return w.TurnWriter.Finish(ctx, status, cause)
}

func newFaultSession(t *testing.T, model dora.Model, failures int) (*Session, *sqlitesession.Store, *finishFault) {
	t.Helper()
	real, err := sqlitesession.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := dora.NewWithConfig(model, dora.AgentConfig{}, stubTool{})
	if err != nil {
		t.Fatal(err)
	}
	fault := &finishFault{Store: real, failures: failures}
	application, err := NewSession(agent, fault, job.New(), "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	previous := terminalSaveBackoff
	terminalSaveBackoff = 0
	t.Cleanup(func() { terminalSaveBackoff = previous })
	return application, real, fault
}

// A finished run whose terminal save keeps failing must still hand back the
// answer, together with the persistence error.
func TestPromptReturnsTheAnswerWhenTheFinalSaveFails(t *testing.T) {
	application, real, fault := newFaultSession(t, twoToolRoundsThenStop(nil), -1)

	result, err := application.Prompt(context.Background(), "inspect twice", PromptOptions{})
	if !IsPersistenceError(err) {
		t.Fatalf("err = %v, want a persistence error", err)
	}
	if !result.Completed || result.Content != "done" || result.Turn == nil || !result.Turn.Completed() {
		t.Fatalf("result = %#v, want the completed answer despite the failed save", result)
	}
	if fault.calls != terminalSaveAttempts {
		t.Fatalf("terminal save attempts = %d, want %d", fault.calls, terminalSaveAttempts)
	}
	// The work done before the failure was saved round by round and stays in the
	// store, marked as still running because it was never finalized.
	page, err := real.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusRunning || page.Turns[0].RoundCount != 2 {
		t.Fatalf("stored turns = %+v, want the two saved rounds of an unfinished turn", page)
	}
}

func TestPromptRetriesATransientFinalSaveFailure(t *testing.T) {
	application, real, fault := newFaultSession(t, twoToolRoundsThenStop(nil), 2)

	result, err := application.Prompt(context.Background(), "inspect twice", PromptOptions{})
	if err != nil || !result.Completed || result.Content != "done" {
		t.Fatalf("result = %#v, err = %v; a brief lock must not fail the prompt", result, err)
	}
	if fault.calls != 3 {
		t.Fatalf("terminal save attempts = %d, want 3", fault.calls)
	}
	page, err := real.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusCompleted || page.Turns[0].RoundCount != 2 {
		t.Fatalf("stored turns = %+v, want one completed turn", page)
	}
}

func TestPromptRetriesTheFinalSaveOfAFailedTurnToo(t *testing.T) {
	boom := errors.New("model exploded")
	application, real, fault := newFaultSession(t, modelFunc(func(context.Context, dora.Request) (dora.Response, error) {
		return dora.Response{}, boom
	}), 1)

	result, err := application.Prompt(context.Background(), "inspect", PromptOptions{})
	if !errors.Is(err, boom) || IsPersistenceError(err) {
		t.Fatalf("err = %v, want the run error, not a persistence error", err)
	}
	if result.Completed || fault.calls != 2 {
		t.Fatalf("completed = %v, save attempts = %d, want false and 2", result.Completed, fault.calls)
	}
	page, err := real.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Turns[0].Status != session.TurnStatusFailed {
		t.Fatalf("stored turns = %+v, want one failed turn", page)
	}
}

// A run that did not produce an answer still reports none when saving fails.
func TestPromptDoesNotInventAnAnswerWhenAFailedTurnCannotBeSaved(t *testing.T) {
	boom := errors.New("model exploded")
	application, _, _ := newFaultSession(t, modelFunc(func(context.Context, dora.Request) (dora.Response, error) {
		return dora.Response{}, boom
	}), -1)

	result, err := application.Prompt(context.Background(), "inspect", PromptOptions{})
	if !IsPersistenceError(err) || result.Completed || result.Content != "" {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
}
