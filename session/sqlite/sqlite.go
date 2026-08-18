// Package sqlite stores completed Dora turns in a SQLite database.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/session"
	_ "modernc.org/sqlite"
)

const schemaVersion = 2

// Store is a SQLite-backed session store.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens or creates a SQLite session database at path.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite session path is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve sqlite session path: %w", err)
	}
	if err := ensureFile(absolute); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", absolute)
	if err != nil {
		return nil, fmt.Errorf("open sqlite session: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: db, path: absolute}
	if err := store.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close closes the SQLite database.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// CommitTurn atomically appends one completed turn and all of its rounds.
func (s *Store) CommitTurn(ctx context.Context, turn *dora.Turn) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("sqlite session is not initialized")
	}
	if turn == nil || !turn.Completed() {
		return 0, errors.New("cannot commit an incomplete turn")
	}
	result, _ := turn.Result()
	rounds := turn.Rounds()
	committedAt := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin session transaction: %w", err)
	}
	defer tx.Rollback()

	inserted, err := tx.ExecContext(ctx, `
INSERT INTO turns (
    system, user, result, round_count, committed_at
) VALUES (?, ?, ?, ?, ?)`,
		turn.System(), turn.User(), result, len(rounds), committedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("insert turn: %w", err)
	}
	turnID, err := inserted.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read inserted turn ID: %w", err)
	}
	for roundIndex, round := range rounds {
		if err := insertMessage(ctx, tx, turnID, roundIndex, 0, round.Assistant); err != nil {
			return 0, err
		}
		for toolIndex, message := range round.Tools {
			if err := insertMessage(ctx, tx, turnID, roundIndex, toolIndex+1, message); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit turn: %w", err)
	}
	return turnID, nil
}

// ListTurns returns completed turns newest first.
func (s *Store) ListTurns(ctx context.Context, options session.ListOptions) (session.TurnPage, error) {
	if s == nil || s.db == nil {
		return session.TurnPage{}, errors.New("sqlite session is not initialized")
	}
	if options.Offset < 0 || options.Limit <= 0 {
		return session.TurnPage{}, errors.New("turn list offset must be non-negative and limit must be positive")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM turns`).Scan(&total); err != nil {
		return session.TurnPage{}, fmt.Errorf("count turns: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, user, result, round_count, committed_at
FROM turns
ORDER BY id DESC
LIMIT ? OFFSET ?`, options.Limit, options.Offset)
	if err != nil {
		return session.TurnPage{}, fmt.Errorf("list turns: %w", err)
	}
	defer rows.Close()
	page := session.TurnPage{Total: total, Offset: options.Offset, Limit: options.Limit, Turns: []session.TurnSummary{}}
	for rows.Next() {
		var summary session.TurnSummary
		var committedAt string
		if err := rows.Scan(&summary.ID, &summary.User, &summary.Result, &summary.RoundCount, &committedAt); err != nil {
			return session.TurnPage{}, fmt.Errorf("scan turn summary: %w", err)
		}
		summary.CommittedAt, err = parseTime(committedAt)
		if err != nil {
			return session.TurnPage{}, err
		}
		page.Turns = append(page.Turns, summary)
	}
	if err := rows.Err(); err != nil {
		return session.TurnPage{}, fmt.Errorf("list turns: %w", err)
	}
	return page, nil
}

// GetRounds returns a chronological page of complete rounds from one turn.
func (s *Store) GetRounds(ctx context.Context, id int64, options session.RoundOptions) (session.RoundPage, error) {
	if s == nil || s.db == nil {
		return session.RoundPage{}, errors.New("sqlite session is not initialized")
	}
	if id <= 0 {
		return session.RoundPage{}, errors.New("turn ID must be positive")
	}
	if options.Offset < 0 || options.Limit <= 0 {
		return session.RoundPage{}, errors.New("round offset must be non-negative and limit must be positive")
	}
	page := session.RoundPage{Offset: options.Offset, Limit: options.Limit, Rounds: []dora.Round{}}
	err := s.db.QueryRowContext(ctx, `SELECT round_count FROM turns WHERE id = ?`, id).Scan(&page.Total)
	if errors.Is(err, sql.ErrNoRows) {
		return session.RoundPage{}, fmt.Errorf("%w: %d", session.ErrNotFound, id)
	}
	if err != nil {
		return session.RoundPage{}, fmt.Errorf("get turn %d round count: %w", id, err)
	}
	if options.Offset >= page.Total {
		return page, nil
	}

	rows, err := s.db.QueryContext(ctx, `
SELECT round_index, position, role, content, tool_calls_json, tool_call_id
FROM messages
WHERE turn_id = ? AND round_index >= ? AND round_index < ?
ORDER BY round_index, position`, id, options.Offset, options.Offset+options.Limit)
	if err != nil {
		return session.RoundPage{}, fmt.Errorf("get turn %d rounds: %w", id, err)
	}
	defer rows.Close()
	currentIndex := -1
	expectedRound := options.Offset
	for rows.Next() {
		var roundIndex, position int
		var role string
		var content, callsJSON, callID sql.NullString
		if err := rows.Scan(&roundIndex, &position, &role, &content, &callsJSON, &callID); err != nil {
			return session.RoundPage{}, fmt.Errorf("scan turn %d message: %w", id, err)
		}
		message, err := decodeMessage(role, content.String, callsJSON.String, callID.String)
		if err != nil {
			return session.RoundPage{}, fmt.Errorf("decode turn %d round %d position %d: %w", id, roundIndex, position, err)
		}
		if roundIndex != currentIndex {
			if roundIndex != expectedRound || position != 0 {
				return session.RoundPage{}, fmt.Errorf("decode turn %d: expected round %d position 0, got round %d position %d", id, expectedRound, roundIndex, position)
			}
			page.Rounds = append(page.Rounds, dora.Round{Assistant: message})
			currentIndex = roundIndex
			expectedRound++
		} else {
			expectedPosition := len(page.Rounds[len(page.Rounds)-1].Tools) + 1
			if position != expectedPosition {
				return session.RoundPage{}, fmt.Errorf("decode turn %d round %d: expected position %d, got %d", id, roundIndex, expectedPosition, position)
			}
			page.Rounds[len(page.Rounds)-1].Tools = append(page.Rounds[len(page.Rounds)-1].Tools, message)
		}
	}
	if err := rows.Err(); err != nil {
		return session.RoundPage{}, fmt.Errorf("get turn %d rounds: %w", id, err)
	}
	expectedCount := min(options.Limit, page.Total-options.Offset)
	if len(page.Rounds) != expectedCount {
		return session.RoundPage{}, fmt.Errorf("decode turn %d: loaded %d rounds, want %d", id, len(page.Rounds), expectedCount)
	}
	validator := dora.NewTurn("", "")
	for index, round := range page.Rounds {
		if err := validator.AppendRound(round, ""); err != nil {
			return session.RoundPage{}, fmt.Errorf("decode turn %d round %d: %w", id, options.Offset+index, err)
		}
	}
	return page, nil
}

func (s *Store) initialize(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return fmt.Errorf("enable sqlite foreign keys: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `PRAGMA busy_timeout = 5000`); err != nil {
		return fmt.Errorf("set sqlite busy timeout: %w", err)
	}
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read sqlite schema version: %w", err)
	}
	if version != 0 && version != schemaVersion {
		return fmt.Errorf("unsupported sqlite session schema version %d", version)
	}
	if version == schemaVersion {
		return s.validateSchema(ctx)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin sqlite schema transaction: %w", err)
	}
	defer tx.Rollback()
	for _, statement := range schemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create sqlite session schema: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `PRAGMA user_version = 2`); err != nil {
		return fmt.Errorf("write sqlite schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite session schema: %w", err)
	}
	return s.validateSchema(ctx)
}

func (s *Store) validateSchema(ctx context.Context) error {
	queries := []string{
		`SELECT id, system, user, result, round_count, committed_at FROM turns LIMIT 0`,
		`SELECT turn_id, round_index, position, role, content, tool_calls_json, tool_call_id FROM messages LIMIT 0`,
	}
	for _, query := range queries {
		rows, err := s.db.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("validate sqlite session schema: %w", err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("validate sqlite session schema: %w", err)
		}
	}
	return nil
}

var schemaStatements = []string{
	`CREATE TABLE turns (
        id INTEGER PRIMARY KEY,
        system TEXT NOT NULL,
        user TEXT NOT NULL,
        result TEXT NOT NULL,
        round_count INTEGER NOT NULL CHECK (round_count >= 0),
        committed_at TEXT NOT NULL
    )`,
	`CREATE TABLE messages (
        turn_id INTEGER NOT NULL,
        round_index INTEGER NOT NULL,
        position INTEGER NOT NULL,
        role TEXT NOT NULL,
        content TEXT,
        tool_calls_json TEXT,
        tool_call_id TEXT,
        PRIMARY KEY (turn_id, round_index, position),
        FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE,
        CHECK ((position = 0 AND role = 'assistant') OR (position > 0 AND role = 'tool'))
    )`,
}

type toolCallRecord struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func insertMessage(ctx context.Context, tx *sql.Tx, turnID int64, roundIndex, position int, message dora.Message) error {
	calls, err := encodeToolCalls(message.ToolCalls)
	if err != nil {
		return fmt.Errorf("encode turn %d round %d position %d tool calls: %w", turnID, roundIndex, position, err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO messages (turn_id, round_index, position, role, content, tool_calls_json, tool_call_id)
VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
		turnID, roundIndex, position, string(message.Role), message.Content, calls, message.ToolCallID,
	)
	if err != nil {
		return fmt.Errorf("insert turn %d round %d position %d: %w", turnID, roundIndex, position, err)
	}
	return nil
}

func encodeToolCalls(calls []dora.ToolCall) (string, error) {
	if len(calls) == 0 {
		return "", nil
	}
	records := make([]toolCallRecord, len(calls))
	for i, call := range calls {
		if !json.Valid(call.Input) {
			return "", fmt.Errorf("tool call %d has invalid JSON input", i)
		}
		records[i] = toolCallRecord{ID: call.ID, Name: call.Name, Input: append(json.RawMessage(nil), call.Input...)}
	}
	encoded, err := json.Marshal(records)
	return string(encoded), err
}

func decodeMessage(role, content, callsJSON, callID string) (dora.Message, error) {
	message := dora.Message{Role: dora.Role(role), Content: content, ToolCallID: callID}
	if callsJSON != "" {
		var records []toolCallRecord
		if err := json.Unmarshal([]byte(callsJSON), &records); err != nil {
			return dora.Message{}, err
		}
		message.ToolCalls = make([]dora.ToolCall, len(records))
		for i, record := range records {
			message.ToolCalls[i] = dora.ToolCall{ID: record.ID, Name: record.Name, Input: append(json.RawMessage(nil), record.Input...)}
		}
	}
	return message, nil
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("decode committed time %q: %w", value, err)
	}
	return parsed, nil
}

func ensureFile(path string) error {
	info, err := os.Stat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("sqlite session %q is not a regular file", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect sqlite session: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("create sqlite session: %w", err)
	}
	return file.Close()
}
