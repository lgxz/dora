package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	if page.Turns[0].Status != session.TurnStatusCompleted || page.Turns[0].Error != "" {
		t.Fatalf("turn status = %q, error = %q", page.Turns[0].Status, page.Turns[0].Error)
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

func TestOpenMemoryStoresTurnsUntilClosed(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, err := store.CommitTurn(ctx, completedTurn(t, "ephemeral", "answer", 0)); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Turns) != 1 || page.Turns[0].User != "ephemeral" {
		t.Fatalf("page = %#v", page)
	}

	other, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	otherPage, err := other.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if otherPage.Total != 0 {
		t.Fatalf("independent memory store total = %d, want 0", otherPage.Total)
	}
}

func TestStoreRejectsIncompleteTurnAndUnknownSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitTurn(ctx, dora.NewTurn("user")); err == nil {
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

func TestStoreCommitsMaximumRoundsTurn(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	cached := int64(4)
	turn := dora.NewTurn("keep working")
	round := dora.Round{
		Assistant: dora.Message{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{}`)}}},
		Tools:     []dora.Message{{Role: dora.RoleTool, ToolCallID: "call-1", Content: "partial output"}},
		Usage: &dora.Usage{
			InputTokens: 8, OutputTokens: 2, TotalTokens: 10,
			InputDetails: &dora.InputTokenDetails{CachedTokens: &cached},
		},
	}
	if err := turn.AppendRound(round, "system"); err != nil {
		t.Fatal(err)
	}
	cause := fmt.Errorf("%w (limit 1)", dora.ErrMaxRounds)
	id, err := store.CommitMaxRounds(ctx, turn, cause)
	if err != nil {
		t.Fatal(err)
	}

	turns, err := store.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns.Turns) != 1 {
		t.Fatalf("turns = %#v", turns.Turns)
	}
	summary := turns.Turns[0]
	if summary.ID != id || summary.Status != session.TurnStatusMaxRounds || summary.Error != cause.Error() || summary.Result != "" || summary.Usage != nil || summary.RoundCount != 1 {
		t.Fatalf("turn summary = %#v", summary)
	}
	rounds, err := store.GetRounds(ctx, id, session.RoundOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds.Rounds) != 1 || rounds.Rounds[0].Usage == nil || rounds.Rounds[0].Usage.TotalTokens != 10 {
		t.Fatalf("rounds = %#v", rounds)
	}
	if _, err := store.CommitMaxRounds(ctx, turn, errors.New("other")); err == nil {
		t.Fatal("expected non-max-round error to be rejected")
	}
	completed := completedTurn(t, "done", "answer", 0)
	if _, err := store.CommitMaxRounds(ctx, completed, cause); err == nil {
		t.Fatal("expected completed turn to be rejected")
	}
}

func TestStoreCommitsFailedTurn(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	turn := dora.NewTurn("keep working")
	round := dora.Round{
		Assistant: dora.Message{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{}`)}}},
		Tools:     []dora.Message{{Role: dora.RoleTool, ToolCallID: "call-1", Content: "complete output"}},
	}
	if err := turn.AppendRound(round, "provider-state"); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("model stream failed")
	id, err := store.CommitFailed(ctx, turn, cause)
	if err != nil {
		t.Fatal(err)
	}

	turns, err := store.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns.Turns) != 1 {
		t.Fatalf("turns = %#v", turns.Turns)
	}
	summary := turns.Turns[0]
	if summary.ID != id || summary.Status != session.TurnStatusFailed || summary.Error != cause.Error() || summary.Result != "" || summary.Usage != nil || summary.RoundCount != 1 {
		t.Fatalf("turn summary = %#v", summary)
	}
	rounds, err := store.GetRounds(ctx, id, session.RoundOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(rounds.Rounds) != 1 || rounds.Rounds[0].Tools[0].Content != "complete output" {
		t.Fatalf("rounds = %#v", rounds)
	}
	if _, err := store.CommitFailed(ctx, dora.NewTurn("nil cause"), nil); err == nil {
		t.Fatal("expected nil failure cause to be rejected")
	}
	completed := completedTurn(t, "done", "answer", 0)
	if _, err := store.CommitFailed(ctx, completed, cause); err == nil {
		t.Fatal("expected completed turn to be rejected")
	}
}

func TestStoreCommitsCanceledTurn(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "history.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	turn := dora.NewTurn("keep working")
	round := dora.Round{
		Assistant: dora.Message{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{}`)}}},
		Tools:     []dora.Message{{Role: dora.RoleTool, ToolCallID: "call-1", Content: "complete output"}},
	}
	if err := turn.AppendRound(round, "provider-state"); err != nil {
		t.Fatal(err)
	}
	cause := fmt.Errorf("dora: generate response: %w", context.Canceled)
	id, err := store.CommitCanceled(ctx, turn, cause)
	if err != nil {
		t.Fatal(err)
	}

	turns, err := store.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns.Turns) != 1 {
		t.Fatalf("turns = %#v", turns.Turns)
	}
	summary := turns.Turns[0]
	if summary.ID != id || summary.Status != session.TurnStatusCanceled || summary.Error != cause.Error() || summary.Result != "" || summary.Usage != nil || summary.RoundCount != 1 {
		t.Fatalf("turn summary = %#v", summary)
	}
	if _, err := store.CommitCanceled(ctx, dora.NewTurn("other error"), errors.New("other")); err == nil {
		t.Fatal("expected non-cancellation error to be rejected")
	}
	completed := completedTurn(t, "done", "answer", 0)
	if _, err := store.CommitCanceled(ctx, completed, cause); err == nil {
		t.Fatal("expected completed turn to be rejected")
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

func TestStoreRoundTripsAssistantReasoningAndUsage(t *testing.T) {
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

	turn := dora.NewTurn("weather?")
	cached := int64(7)
	round := dora.Round{
		Assistant: dora.Message{
			Role:      dora.RoleAssistant,
			Content:   "checking",
			Reasoning: "let me look",
			ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{}`)}},
		},
		Tools: []dora.Message{{Role: dora.RoleTool, ToolCallID: "call-1", Content: "output"}},
		Usage: &dora.Usage{
			InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
			InputDetails: &dora.InputTokenDetails{CachedTokens: &cached},
		},
	}
	if err := turn.AppendRound(round, ""); err != nil {
		t.Fatal(err)
	}
	reasoning := int64(3)
	completeTurnWithResponse(t, turn, dora.Response{
		Content: "sunny",
		Usage: &dora.Usage{
			InputTokens: 15, OutputTokens: 4, TotalTokens: 19,
			OutputDetails: &dora.OutputTokenDetails{ReasoningTokens: &reasoning},
		},
	})
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
	if usage := page.Rounds[0].Usage; usage == nil || usage.TotalTokens != 12 || usage.InputDetails == nil || usage.InputDetails.CachedTokens == nil || *usage.InputDetails.CachedTokens != 7 {
		t.Fatalf("round usage = %#v", usage)
	}
	turns, err := store.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(turns.Turns) != 1 || turns.Turns[0].Usage == nil || turns.Turns[0].Usage.TotalTokens != 19 || turns.Turns[0].Usage.OutputDetails == nil || turns.Turns[0].Usage.OutputDetails.ReasoningTokens == nil || *turns.Turns[0].Usage.OutputDetails.ReasoningTokens != 3 {
		t.Fatalf("turn summary = %#v", turns.Turns)
	}
}

func TestStoreRejectsVersionFiveWithoutMigration(t *testing.T) {
	// v5 databases do not support failed turns and are rejected rather than
	// migrated, by design.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 5`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("expected unsupported schema error")
	}
}

func TestStoreRejectsEarlierVersionSixDefinition(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE turns (
    id INTEGER PRIMARY KEY,
    system TEXT NOT NULL,
    user TEXT NOT NULL,
    result TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('completed', 'max_rounds', 'failed')),
    error TEXT,
    round_count INTEGER NOT NULL,
    usage_json TEXT,
    committed_at TEXT NOT NULL
);
CREATE TABLE messages (
    turn_id INTEGER NOT NULL,
    round_index INTEGER NOT NULL,
    position INTEGER NOT NULL,
    role TEXT NOT NULL,
    content TEXT,
    reasoning TEXT,
    tool_calls_json TEXT,
    tool_call_id TEXT,
    usage_json TEXT
);
PRAGMA user_version = 6;`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "version 6 definition") {
		t.Fatalf("error = %v, want unsupported version 6 definition", err)
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
	turn := dora.NewTurn(user)
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
	completeTurnWithSystem(t, turn, result)
	return turn
}

type finalModel string

func (m finalModel) Generate(context.Context, dora.Request) (dora.Response, error) {
	return dora.Response{Content: string(m)}, nil
}

type finalResponseModel struct {
	response dora.Response
}

func (m finalResponseModel) Generate(context.Context, dora.Request) (dora.Response, error) {
	return m.response, nil
}

func completeTurnWithSystem(t *testing.T, turn *dora.Turn, result string) {
	t.Helper()
	agent, err := dora.NewWithConfig(finalModel(result), dora.AgentConfig{SystemPrompt: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Run(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
}

func completeTurnWithResponse(t *testing.T, turn *dora.Turn, response dora.Response) {
	t.Helper()
	agent, err := dora.NewWithConfig(finalResponseModel{response: response}, dora.AgentConfig{SystemPrompt: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if err := agent.Run(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
}
