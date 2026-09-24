package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/session"
	sqlitesession "github.com/lgxz/dora/session/sqlite"
)

func TestChildrenKeepOriginalParentAcrossPrompts(t *testing.T) {
	var firstContext context.Context
	application, store := newTestSession(t, modelFunc(func(ctx context.Context, _ dora.Request) (dora.Response, error) {
		if firstContext == nil {
			firstContext = context.WithoutCancel(ctx)
		} else {
			// Simulate a late background completion from the earlier prompt, plus
			// concurrent foreground completions belonging to the current prompt.
			old := dora.NewTurn("old child")
			_ = old.Complete("old answer", "")
			RecordTask(firstContext, old, nil)
			var wg sync.WaitGroup
			for i := 0; i < 5; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					child := dora.NewTurn("new child")
					_ = child.Complete("new answer", "")
					RecordTask(ctx, child, nil)
				}()
			}
			wg.Wait()
		}
		return dora.Response{FinishReason: dora.FinishStop, Content: "answer"}, nil
	}), dora.AgentConfig{})
	for _, prompt := range []string{"first", "second"} {
		if _, err := application.Prompt(context.Background(), prompt, PromptOptions{}); err != nil {
			t.Fatal(err)
		}
	}

	saved, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil || saved.Total != 8 {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	parents := map[string]int64{}
	for _, row := range saved.Turns {
		if row.ParentTurnID == nil {
			parents[row.User] = row.ID
		}
	}
	counts := map[string]int{}
	for _, row := range saved.Turns {
		if row.ParentTurnID == nil {
			continue
		}
		name := "second"
		if row.User == "old child" {
			name = "first"
		}
		if *row.ParentTurnID != parents[name] {
			t.Fatalf("wrong parent: %+v", row)
		}
		counts[name]++
	}
	if counts["first"] != 1 || counts["second"] != 5 {
		t.Fatalf("counts=%v", counts)
	}

}

type failingChildStore struct{ session.Store }

func (s failingChildStore) CommitChild(context.Context, int64, *dora.Turn, error) (int64, error) {
	return 0, errors.New("child write failed")
}

func TestChildSaveFailureDoesNotChangeParentResult(t *testing.T) {
	application, _ := newTestSession(t, modelFunc(func(ctx context.Context, _ dora.Request) (dora.Response, error) {
		child := dora.NewTurn("child")
		_ = child.Complete("answer", "")
		RecordTask(ctx, child, nil)
		return dora.Response{FinishReason: dora.FinishStop, Content: "parent answer"}, nil
	}), dora.AgentConfig{})
	application.store = failingChildStore{application.store}
	result, err := application.Prompt(context.Background(), "parent", PromptOptions{})
	if err != nil || result.Content != "parent answer" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if err := application.Close(); err == nil {
		t.Fatal("missing storage diagnostic")
	}
}

func TestCloseCancelsAndSavesBackgroundChild(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "session.sqlite")
	store, err := sqlitesession.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	jobs := job.New()
	started := make(chan struct{})
	agent, err := dora.New(modelFunc(func(ctx context.Context, _ dora.Request) (dora.Response, error) {
		jobs.StartTaskContext(ctx, "task", "background", func(childCtx context.Context) (string, error) {
			close(started)
			<-childCtx.Done()
			RecordTask(childCtx, dora.NewTurn("background"), childCtx.Err())
			return "", childCtx.Err()
		})
		<-started
		return dora.Response{FinishReason: dora.FinishStop, Content: "parent answer"}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewSession(agent, store, jobs, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Prompt(ctx, "parent", PromptOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlitesession.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	saved, err := store.ListTurns(ctx, session.ListOptions{Limit: 10})
	if err != nil || saved.Total != 2 {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
	child, parent := saved.Turns[0], saved.Turns[1]
	if parent.ParentTurnID != nil || child.ParentTurnID == nil || *child.ParentTurnID != parent.ID || child.Status != session.TurnStatusCanceled {
		t.Fatalf("saved=%+v", saved)
	}
}
