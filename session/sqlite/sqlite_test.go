package sqlite

import (
	"bytes"
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

func TestOpenAppliesConnectionPragmas(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	memory, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	for name, db := range map[string]*sql.DB{"file": store.db, "memory": memory.db} {
		var foreignKeys, busyTimeout int
		if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
			t.Fatalf("%s: foreign_keys = %d, err = %v", name, foreignKeys, err)
		}
		if err := db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil || busyTimeout != 5000 {
			t.Fatalf("%s: busy_timeout = %d, err = %v", name, busyTimeout, err)
		}
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
	cause := fmt.Errorf("generate response: %w", context.Canceled)
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
			Images:    []dora.Image{{URL: "https://example.com/chart.png"}},
			ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{}`)}},
		},
		Tools: []dora.Message{{Role: dora.RoleTool, ToolCallID: "call-1", Content: "output", Images: []dora.Image{{Path: "/tmp/scan.png"}}}},
		Usage: &dora.Usage{
			InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
			InputDetails: &dora.InputTokenDetails{CachedTokens: &cached},
		},
	}
	if err := turn.AppendRound(round, ""); err != nil {
		t.Fatal(err)
	}
	reasoning := int64(3)
	completeTurnWithResponse(t, turn, dora.Response{FinishReason: dora.FinishStop,
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
	if images := page.Rounds[0].Assistant.Images; len(images) != 1 || images[0].URL != "https://example.com/chart.png" {
		t.Fatalf("assistant images = %#v", images)
	}
	if images := page.Rounds[0].Tools[0].Images; len(images) != 1 || images[0].Path != "/tmp/scan.png" {
		t.Fatalf("tool images = %#v", images)
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
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "version 6") {
		t.Fatalf("error = %v, want unsupported version 6", err)
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
	return dora.Response{FinishReason: dora.FinishStop, Content: string(m)}, nil
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

func TestStorePreservesArgumentBytesAcrossReopen(t *testing.T) {
	for _, status := range []string{"completed", "failed", "canceled", "max_rounds"} {
		t.Run(status, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "history.sqlite")
			store, err := Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { store.Close() }()
			inputs := [][]byte{[]byte(`{"command":`), []byte(" \n{\"x\": 1}\t"), nil, []byte("\x00\xff"), []byte(`{"文本":"你好"}`)}
			turn := dora.NewTurn("test")
			for i, raw := range inputs {
				id := strconv.Itoa(i)
				if err := turn.AppendRound(dora.Round{
					Assistant: dora.Message{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{{ID: id, Name: "echo", Input: raw}}},
					Tools:     []dora.Message{{Role: dora.RoleTool, ToolCallID: id, Content: "tool result"}},
				}, ""); err != nil {
					t.Fatal(err)
				}
			}
			var id int64
			switch status {
			case "completed":
				completeTurnWithSystem(t, turn, "done")
				id, err = store.CommitTurn(ctx, turn)
			case "failed":
				id, err = store.CommitFailed(ctx, turn, errors.New("failed"))
			case "canceled":
				id, err = store.CommitCanceled(ctx, turn, context.Canceled)
			case "max_rounds":
				id, err = store.CommitMaxRounds(ctx, turn, dora.ErrMaxRounds)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store, err = Open(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			page, err := store.GetRounds(ctx, id, session.RoundOptions{Limit: len(inputs)})
			if err != nil {
				t.Fatal(err)
			}
			if len(page.Rounds) != len(inputs) {
				t.Fatalf("round count = %d", len(page.Rounds))
			}
			for i, raw := range inputs {
				if got := page.Rounds[i].Assistant.ToolCalls[0].Input; !bytes.Equal(got, raw) {
					t.Fatalf("round %d: got %q, want %q", i, got, raw)
				}
			}
		})
	}
}

func TestStoreRejectsVersionEightWithoutMigration(t *testing.T) {
	// v8 databases have no images_json column and are rejected rather than
	// migrated, by design.
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 8`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "version 8") {
		t.Fatalf("expected version rejection, got %v", err)
	}
}

func TestStoreRejectsVersionSixWithoutMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "history.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 6`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "version 6") {
		t.Fatalf("expected version rejection, got %v", err)
	}
}

func TestStorePersistsDiscardedAttemptsAndTotalUsage(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "attempts.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reasonTokens := int64(32768)
	response := dora.Response{FinishReason: dora.FinishOutputLimit, RawFinishReason: "length", OutputBudget: 32768, Reasoning: "unfinished",
		ToolCalls: []dora.ToolCall{{ID: "partial", Name: "bash", Input: json.RawMessage(`{`)}},
		Usage:     &dora.Usage{InputTokens: 100, OutputTokens: 32768, TotalTokens: 32868, OutputDetails: &dora.OutputTokenDetails{ReasoningTokens: &reasonTokens}}}
	agent, err := dora.NewWithConfig(finalResponseModel{response: response}, dora.AgentConfig{OutputLimitRecovery: &dora.OutputLimitRecovery{}})
	if err != nil {
		t.Fatal(err)
	}
	turn := dora.NewTurn("solve")
	cause := agent.Run(ctx, turn)
	if !errors.Is(cause, dora.ErrOutputLimit) {
		t.Fatal(cause)
	}
	id, err := store.CommitFailed(ctx, turn, cause)
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.GetAttempts(ctx, id, session.RoundOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Attempts) != 1 || page.Attempts[0].Reasoning != "unfinished" || page.Attempts[0].ToolCalls[0].Input != "{" || page.Attempts[0].Disposition != "discarded" {
		t.Fatalf("page=%+v", page)
	}
	turns, err := store.ListTurns(ctx, session.ListOptions{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	saved := turns.Turns[0]
	if saved.Status != session.TurnStatusFailed || saved.Usage != nil || saved.TotalUsage.TotalTokens != 32868 {
		t.Fatalf("saved=%+v", saved)
	}
	empty, err := store.GetAttempts(ctx, id, session.RoundOptions{Offset: 1, Limit: 1})
	if err != nil || empty.Total != 1 || len(empty.Attempts) != 0 {
		t.Fatalf("page=%+v err=%v", empty, err)
	}
}

func TestChildTurnsAreLinkedAndIncludedInList(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "family.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CommitChild(ctx, 1, completedTurn(t, "orphan", "answer", 0), nil); err == nil {
		t.Fatal("accepted self-referencing child without a parent")
	}
	parentID, err := store.CommitTurn(ctx, completedTurn(t, "parent", "parent answer", 1))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		status session.TurnStatus
		cause  error
	}{
		{session.TurnStatusCompleted, nil}, {session.TurnStatusFailed, errors.New("failed")},
		{session.TurnStatusCanceled, context.Canceled}, {session.TurnStatusMaxRounds, dora.ErrMaxRounds},
	}
	var ids []int64
	for _, tc := range cases {
		child := dora.NewTurn("child")
		if tc.cause == nil {
			child = completedTurn(t, "child", "child answer", 1)
		}
		id, err := store.CommitChild(ctx, parentID, child, tc.cause)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := store.CommitChild(ctx, parentID+1000, completedTurn(t, "orphan", "answer", 0), nil); err == nil {
		t.Fatal("accepted missing parent")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Read one row per page so parent/child relations survive pagination.
	for offset := 0; offset < len(ids)+1; offset++ {
		page, err := store.ListTurns(ctx, session.ListOptions{Offset: offset, Limit: 1})
		if err != nil || page.Total != len(ids)+1 || len(page.Turns) != 1 {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		row := page.Turns[0]
		if offset == len(ids) {
			if row.ID != parentID || row.ParentTurnID != nil {
				t.Fatalf("parent=%+v", row)
			}
			continue
		}
		i := len(ids) - 1 - offset
		if row.ID != ids[i] || row.ParentTurnID == nil || *row.ParentTurnID != parentID || row.Status != cases[i].status {
			t.Fatalf("child=%+v", row)
		}
		rounds, err := store.GetRounds(ctx, row.ID, session.RoundOptions{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		if cases[i].cause == nil && (row.Result != "child answer" || len(rounds.Rounds) != 1) {
			t.Fatalf("child=%+v rounds=%+v", row, rounds)
		}
	}
	empty, err := store.ListTurns(ctx, session.ListOptions{Offset: len(ids) + 1, Limit: 1})
	if err != nil || empty.Total != len(ids)+1 || len(empty.Turns) != 0 {
		t.Fatalf("page=%+v err=%v", empty, err)
	}

}

func TestStoreRejectsVersionNine(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 9`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "version 9") {
		t.Fatalf("error=%v", err)
	}
}

func appendTestRound(t *testing.T, turn *dora.Turn, number int) {
	t.Helper()
	id := "call-" + strconv.Itoa(number)
	round := dora.Round{
		Assistant: dora.Message{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{{ID: id, Name: "echo", Input: json.RawMessage(`{}`)}}},
		Tools:     []dora.Message{{Role: dora.RoleTool, ToolCallID: id, Content: "output-" + strconv.Itoa(number)}},
	}
	if err := turn.AppendRound(round, ""); err != nil {
		t.Fatal(err)
	}
}

func listAll(t *testing.T, store *Store) []session.TurnSummary {
	t.Helper()
	page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	return page.Turns
}

func TestTurnWriterSavesRoundsIncrementallyThenFinishes(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	turn := dora.NewTurn("long task")
	writer := store.BeginTurn(turn)
	if err := writer.Sync(ctx); err != nil || writer.ID() != 0 || len(listAll(t, store)) != 0 {
		t.Fatalf("Sync without rounds: err=%v id=%d turns=%d (must not create a row)", err, writer.ID(), len(listAll(t, store)))
	}

	appendTestRound(t, turn, 1)
	if err := writer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	id := writer.ID()
	if id == 0 {
		t.Fatal("first Sync with a round did not create the turn")
	}
	turns := listAll(t, store)
	if len(turns) != 1 || turns[0].ID != id || turns[0].Status != session.TurnStatusRunning ||
		turns[0].RoundCount != 1 || turns[0].Result != "" || turns[0].Error != "" || turns[0].Usage != nil {
		t.Fatalf("running turn = %+v", turns)
	}
	rounds, err := store.GetRounds(ctx, id, session.RoundOptions{Limit: 10})
	if err != nil || rounds.Total != 1 || len(rounds.Rounds) != 1 || rounds.Rounds[0].Tools[0].Content != "output-1" {
		t.Fatalf("rounds = %+v, err = %v", rounds, err)
	}

	// A Sync with nothing new changes nothing.
	if err := writer.Sync(ctx); err != nil || writer.ID() != id {
		t.Fatalf("idempotent Sync: err=%v id=%d", err, writer.ID())
	}

	appendTestRound(t, turn, 2)
	if err := writer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if turns := listAll(t, store); len(turns) != 1 || turns[0].RoundCount != 2 {
		t.Fatalf("after second round = %+v", turns)
	}

	completeTurnWithSystem(t, turn, "all done")
	finishedID, err := writer.Finish(ctx, session.TurnStatusCompleted, nil)
	if err != nil || finishedID != id {
		t.Fatalf("Finish = %d, %v; want %d", finishedID, err, id)
	}
	turns = listAll(t, store)
	if len(turns) != 1 || turns[0].Status != session.TurnStatusCompleted || turns[0].Result != "all done" ||
		turns[0].Error != "" || turns[0].RoundCount != 2 {
		t.Fatalf("completed turn = %+v (must be the same row, not a duplicate)", turns)
	}
	var system string
	if err := store.db.QueryRow(`SELECT system FROM turns WHERE id = ?`, id).Scan(&system); err != nil || system != "system" {
		t.Fatalf("system = %q, err = %v", system, err)
	}
	rounds, err = store.GetRounds(ctx, id, session.RoundOptions{Limit: 10})
	if err != nil || len(rounds.Rounds) != 2 || rounds.Rounds[1].Tools[0].Content != "output-2" {
		t.Fatalf("rounds after finish = %+v, err = %v", rounds, err)
	}
	if _, err := writer.Finish(ctx, session.TurnStatusCompleted, nil); err == nil {
		t.Fatal("second Finish succeeded")
	}
	if got := len(listAll(t, store)); got != 1 {
		t.Fatalf("turns after second Finish = %d", got)
	}
}

func TestTurnWriterFinishWithoutSyncCreatesOneTerminalRow(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	turn := completedTurn(t, "never synced", "answer", 2)
	writer := store.BeginTurn(turn)
	id, err := writer.Finish(ctx, session.TurnStatusCompleted, nil)
	if err != nil || id == 0 || writer.ID() != id {
		t.Fatalf("Finish = %d, %v", id, err)
	}
	turns := listAll(t, store)
	if len(turns) != 1 || turns[0].Status != session.TurnStatusCompleted || turns[0].RoundCount != 2 {
		t.Fatalf("turns = %+v", turns)
	}
}

func TestTurnWriterRunningRowSurvivesInterruption(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "crash.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	turn := dora.NewTurn("will be interrupted")
	writer := store.BeginTurn(turn)
	appendTestRound(t, turn, 1)
	appendTestRound(t, turn, 2)
	if err := writer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	// Close without Finish, as if the process had been killed after round two.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	turns := listAll(t, reopened)
	if len(turns) != 1 || turns[0].Status != session.TurnStatusRunning || turns[0].RoundCount != 2 {
		t.Fatalf("interrupted turn = %+v", turns)
	}
	rounds, err := reopened.GetRounds(ctx, turns[0].ID, session.RoundOptions{Limit: 10})
	if err != nil || len(rounds.Rounds) != 2 {
		t.Fatalf("rounds = %+v, err = %v", rounds, err)
	}
}

func TestTurnWriterFinishAfterSyncKeepsRoundsOnce(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	turn := dora.NewTurn("fails later")
	writer := store.BeginTurn(turn)
	appendTestRound(t, turn, 1)
	if err := writer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	appendTestRound(t, turn, 2)
	cause := errors.New("model exploded")
	id, err := writer.Finish(ctx, session.TurnStatusFailed, cause)
	if err != nil {
		t.Fatal(err)
	}
	turns := listAll(t, store)
	if len(turns) != 1 || turns[0].ID != id || turns[0].Status != session.TurnStatusFailed ||
		turns[0].Error != cause.Error() || turns[0].Result != "" || turns[0].RoundCount != 2 {
		t.Fatalf("failed turn = %+v", turns)
	}
	var messages int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM messages WHERE turn_id = ?`, id).Scan(&messages); err != nil || messages != 4 {
		t.Fatalf("messages = %d (want 2 rounds x assistant+tool, no duplicates), err = %v", messages, err)
	}
}

func TestTurnWriterRepairsFailedSyncOnNextSave(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	turn := dora.NewTurn("flaky storage")
	writer := store.BeginTurn(turn)
	appendTestRound(t, turn, 1)

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := writer.Sync(canceled); err == nil {
		t.Fatal("Sync with a canceled context succeeded")
	}
	if writer.ID() != 0 || len(listAll(t, store)) != 0 {
		t.Fatalf("failed Sync left state behind: id=%d turns=%d", writer.ID(), len(listAll(t, store)))
	}

	appendTestRound(t, turn, 2)
	if err := writer.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if turns := listAll(t, store); len(turns) != 1 || turns[0].RoundCount != 2 || turns[0].Status != session.TurnStatusRunning {
		t.Fatalf("after repair = %+v (both rounds must be saved)", turns)
	}
}

func TestTurnWriterRejectsInvalidFinishWithoutCreatingRows(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	cases := []struct {
		name   string
		status session.TurnStatus
		cause  error
	}{
		{"running is not terminal", session.TurnStatusRunning, nil},
		{"completed needs a completed turn", session.TurnStatusCompleted, nil},
		{"failed needs a cause", session.TurnStatusFailed, nil},
		{"canceled needs context.Canceled", session.TurnStatusCanceled, errors.New("other")},
		{"max rounds needs ErrMaxRounds", session.TurnStatusMaxRounds, errors.New("other")},
		{"unknown status", session.TurnStatus("bogus"), errors.New("x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn := dora.NewTurn("incomplete")
			appendTestRound(t, turn, 1)
			if _, err := store.BeginTurn(turn).Finish(ctx, tc.status, tc.cause); err == nil {
				t.Fatal("Finish succeeded")
			}
			if got := len(listAll(t, store)); got != 0 {
				t.Fatalf("rows after rejected Finish = %d", got)
			}
		})
	}
}

// onlyTool is a minimal dora.Tool for driving a real Agent run.
type onlyTool struct{}

func (onlyTool) Spec() dora.ToolSpec { return dora.ToolSpec{Name: "echo"} }
func (onlyTool) Execute(context.Context, json.RawMessage) (dora.ToolResult, error) {
	return dora.ToolResult{Content: "echoed"}, nil
}

type modelFunc func(context.Context, dora.Request) (dora.Response, error)

func (f modelFunc) Generate(ctx context.Context, request dora.Request) (dora.Response, error) {
	return f(ctx, request)
}

func TestAgentRunSavesEachRoundBeforeNextModelRequest(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	turn := dora.NewTurn("multi round")
	writer := store.BeginTurn(turn)
	calls := 0
	model := modelFunc(func(ctx context.Context, _ dora.Request) (dora.Response, error) {
		calls++
		turns := listAll(t, store)
		if calls == 1 {
			if len(turns) != 0 {
				t.Fatalf("turn saved before any round completed: %+v", turns)
			}
			return dora.Response{FinishReason: dora.FinishToolCalls, ToolCalls: []dora.ToolCall{{ID: "c1", Name: "echo", Input: json.RawMessage(`{}`)}}}, nil
		}
		// Rounds finished so far are already durable at this point.
		if len(turns) != 1 || turns[0].Status != session.TurnStatusRunning || turns[0].RoundCount != calls-1 {
			t.Fatalf("model call %d saw turns %+v", calls, turns)
		}
		attempts, err := store.GetAttempts(ctx, turns[0].ID, session.RoundOptions{Limit: 10})
		if err != nil || attempts.Total != calls-1 {
			t.Fatalf("model call %d saw attempts %+v, err = %v", calls, attempts, err)
		}
		if calls == 2 {
			return dora.Response{FinishReason: dora.FinishToolCalls, ToolCalls: []dora.ToolCall{{ID: "c2", Name: "echo", Input: json.RawMessage(`{}`)}}}, nil
		}
		return dora.Response{FinishReason: dora.FinishStop, Content: "finished"}, nil
	})
	agent, err := dora.New(model, onlyTool{})
	if err != nil {
		t.Fatal(err)
	}
	err = agent.RunObservedWithOptions(ctx, turn, nil, dora.RunOptions{
		OnRound: func(*dora.Turn) {
			if err := writer.Sync(ctx); err != nil {
				t.Errorf("Sync: %v", err)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := writer.Finish(ctx, session.TurnStatusCompleted, nil)
	if err != nil {
		t.Fatal(err)
	}
	turns := listAll(t, store)
	if len(turns) != 1 || turns[0].ID != id || turns[0].Status != session.TurnStatusCompleted ||
		turns[0].Result != "finished" || turns[0].RoundCount != 2 {
		t.Fatalf("final turn = %+v", turns)
	}
	attempts, err := store.GetAttempts(ctx, id, session.RoundOptions{Limit: 10})
	if err != nil || attempts.Total != 3 || attempts.Attempts[2].Disposition != "final" {
		t.Fatalf("final attempts = %+v, err = %v", attempts, err)
	}
}

func TestStoreRejectsVersionTenWithoutMigration(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.sqlite")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`PRAGMA user_version = 10`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, path); err == nil || !strings.Contains(err.Error(), "version 10") {
		t.Fatalf("error=%v", err)
	}
}

func TestRunningStatusConstraints(t *testing.T) {
	ctx := context.Background()
	store, err := OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insert := func(status, errorText, result string) error {
		_, err := store.db.Exec(`
INSERT INTO turns (system, user, result, status, error, round_count, committed_at)
VALUES ('', 'u', ?, ?, NULLIF(?, ''), 0, '2026-01-01T00:00:00Z')`, result, status, errorText)
		return err
	}
	if err := insert("running", "", ""); err != nil {
		t.Fatalf("plain running row rejected: %v", err)
	}
	if err := insert("running", "some error", ""); err == nil {
		t.Fatal("running row with an error accepted")
	}
	if err := insert("running", "", "stray result"); err == nil {
		t.Fatal("running row with a result accepted")
	}
}
