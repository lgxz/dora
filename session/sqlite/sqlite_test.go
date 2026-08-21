package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/session"
)

func TestStoreCommitsAndPagesCompletedTurns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("database mode = %o, want 600", info.Mode().Perm())
	}

	first := completedTurn(t, "first", "answer one", 2)
	firstID, err := store.CommitTurn(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	second := completedTurn(t, "second", "answer two", 0)
	secondID, err := store.CommitTurn(ctx, second)
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Turns) != 1 || page.Turns[0].ID != secondID || page.Turns[0].RoundCount != 0 {
		t.Fatalf("page = %#v", page)
	}
	rounds, err := store.GetRounds(ctx, firstID, session.RoundOptions{Offset: 1, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if rounds.Total != 2 || rounds.Offset != 1 || rounds.Limit != 1 || len(rounds.Rounds) != 1 ||
		rounds.Rounds[0].Assistant.ToolCalls[0].ID != "call-2" || rounds.Rounds[0].Tools[0].Content != "output-2" {
		t.Fatalf("rounds = %#v", rounds)
	}
}

func TestStoreRejectsIncompleteTurnAndUnknownSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitTurn(ctx, dora.NewTurn("system", "user")); err == nil {
		t.Fatal("expected incomplete turn error")
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 99`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("expected unsupported schema error")
	}
}

func TestStoreReturnsNotFound(t *testing.T) {
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	_, err = store.GetRounds(context.Background(), 42, session.RoundOptions{Limit: 1})
	if !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("error = %v", err)
	}
}

func TestStoreRoundTripsAssistantReasoning(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	turn := dora.NewTurn("system", "weather?")
	round := dora.Round{
		Assistant: dora.Message{
			Role:      dora.RoleAssistant,
			Content:   "checking",
			Reasoning: "let me look",
			ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{}`)}},
		},
		Tools: []dora.Message{{Role: dora.RoleTool, ToolCallID: "call-1", Content: "output"}},
	}
	if err := turn.AppendRound(round, ""); err != nil {
		t.Fatal(err)
	}
	if err := turn.Complete("sunny", ""); err != nil {
		t.Fatal(err)
	}
	id, err := store.CommitTurn(ctx, turn)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.GetRounds(ctx, id, session.RoundOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rounds) != 1 || page.Rounds[0].Assistant.Reasoning != "let me look" {
		t.Fatalf("rounds = %#v", page.Rounds)
	}
}

func TestStoreRejectsVersionTwoWithoutMigration(t *testing.T) {
	// v2 databases predate the reasoning column and are rejected rather than
	// migrated, by design.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("expected unsupported schema error")
	}
}

func TestOpenRejectsLegacyJSONSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.json")
	if err := os.WriteFile(path, []byte(`{"version":5,"messages":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), path); err == nil {
		t.Fatal("expected legacy JSON session to be rejected")
	}
}

func completedTurn(t *testing.T, user, result string, roundCount int) *dora.Turn {
	t.Helper()
	turn := dora.NewTurn("system", user)
	for index := 1; index <= roundCount; index++ {
		id := "call-" + strconv.Itoa(index)
		round := dora.Round{
			Assistant: dora.Message{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{{ID: id, Name: "echo", Input: json.RawMessage(`{}`)}}},
			Tools:     []dora.Message{{Role: dora.RoleTool, ToolCallID: id, Content: "output-" + strconv.Itoa(index)}},
		}
		if err := turn.AppendRound(round, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := turn.Complete(result, ""); err != nil {
		t.Fatal(err)
	}
	return turn
}
