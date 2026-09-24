// Package sqlite stores Dora turns in a SQLite database.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/session"
	_ "modernc.org/sqlite"
)

const schemaVersion = 10

// connectionPragmas travels in the DSN rather than in one-time statements so
// the driver applies it to every connection it creates, including a pool
// connection recreated after an error.
const connectionPragmas = "_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"

// fileDSN encodes path as a file: URI carrying connectionPragmas. Reserved URI
// characters in path are percent-escaped, and a Windows drive path gains the
// empty authority slash (file:///C:/...).
func fileDSN(path string) string {
	slash := filepath.ToSlash(path)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	uri := url.URL{Scheme: "file", Path: slash, RawQuery: connectionPragmas}
	return uri.String()
}

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
	return open(ctx, fileDSN(absolute), absolute)
}

// OpenMemory opens an ephemeral SQLite session database. Its contents live
// only for the lifetime of the returned Store and are discarded on Close.
func OpenMemory(ctx context.Context) (*Store, error) {
	return open(ctx, "file::memory:?"+connectionPragmas, ":memory:")
}

func open(ctx context.Context, dsn, path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite session: %w", err)
	}
	// An in-memory SQLite database belongs to one connection. Keeping the pool
	// at exactly one connection also preserves the existing serialized access
	// behavior for file-backed sessions.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db, path: path}
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
	return s.commitTurn(ctx, 0, turn, session.TurnStatusCompleted, result, "", turn.Usage())
}

// CommitMaxRounds atomically appends an incomplete turn stopped by the round limit.
func (s *Store) CommitMaxRounds(ctx context.Context, turn *dora.Turn, cause error) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("sqlite session is not initialized")
	}
	if turn == nil || turn.Completed() {
		return 0, errors.New("cannot commit a completed turn as max rounds")
	}
	if !errors.Is(cause, dora.ErrMaxRounds) {
		return 0, errors.New("max-round turn requires ErrMaxRounds")
	}
	return s.commitTurn(ctx, 0, turn, session.TurnStatusMaxRounds, "", cause.Error(), nil)
}

// CommitFailed atomically appends an incomplete turn stopped by an error. Only
// complete assistant/tool rounds are stored; any partial streamed model output
// is deliberately absent from Turn and therefore is not persisted.
func (s *Store) CommitFailed(ctx context.Context, turn *dora.Turn, cause error) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("sqlite session is not initialized")
	}
	if turn == nil || turn.Completed() {
		return 0, errors.New("cannot commit a completed turn as failed")
	}
	if cause == nil {
		return 0, errors.New("failed turn requires an error")
	}
	return s.commitTurn(ctx, 0, turn, session.TurnStatusFailed, "", cause.Error(), nil)
}

// CommitCanceled atomically appends an incomplete turn stopped by context
// cancellation. Only complete assistant/tool rounds are stored.
func (s *Store) CommitCanceled(ctx context.Context, turn *dora.Turn, cause error) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("sqlite session is not initialized")
	}
	if turn == nil || turn.Completed() {
		return 0, errors.New("cannot commit a completed turn as canceled")
	}
	if !errors.Is(cause, context.Canceled) {
		return 0, errors.New("canceled turn requires context.Canceled")
	}
	return s.commitTurn(ctx, 0, turn, session.TurnStatusCanceled, "", cause.Error(), nil)
}

func (s *Store) commitTurn(ctx context.Context, parentID int64, turn *dora.Turn, status session.TurnStatus, result, errorText string, usage *dora.Usage) (int64, error) {
	usageJSON, err := encodeUsage(usage)
	if err != nil {
		return 0, fmt.Errorf("encode final usage: %w", err)
	}
	totalUsageJSON, err := encodeUsage(turn.TotalUsage())
	if err != nil {
		return 0, fmt.Errorf("encode total usage: %w", err)
	}
	rounds := turn.Rounds()
	committedAt := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin session transaction: %w", err)
	}
	defer tx.Rollback()

	inserted, err := tx.ExecContext(ctx, `
INSERT INTO turns (
    parent_turn_id, system, user, result, status, error, round_count, usage_json, total_usage_json, committed_at
) VALUES (NULLIF(?, 0), ?, ?, ?, ?, NULLIF(?, ''), ?, NULLIF(?, ''), NULLIF(?, ''), ?)`,
		parentID, turn.System(), turn.User(), result, status, errorText, len(rounds), usageJSON, totalUsageJSON, committedAt.Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("insert turn: %w", err)
	}
	turnID, err := inserted.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("read inserted turn ID: %w", err)
	}
	for index, attempt := range turn.Attempts() {
		encoded, err := json.Marshal(attempt)
		if err != nil {
			return 0, fmt.Errorf("encode model attempt: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO model_attempts (turn_id, attempt_index, record_json) VALUES (?, ?, ?)`, turnID, index, string(encoded)); err != nil {
			return 0, fmt.Errorf("insert model attempt: %w", err)
		}
	}
	for roundIndex, round := range rounds {
		if err := insertMessage(ctx, tx, turnID, roundIndex, 0, round.Assistant, round.Usage); err != nil {
			return 0, err
		}
		for toolIndex, message := range round.Tools {
			if err := insertMessage(ctx, tx, turnID, roundIndex, toolIndex+1, message, nil); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit turn: %w", err)
	}
	return turnID, nil
}

// ListTurns returns saved turns newest first.
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
SELECT id, parent_turn_id, user, result, status, error, round_count, usage_json, total_usage_json, committed_at
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
		var parent sql.NullInt64
		var errorText, usageJSON, totalUsageJSON sql.NullString
		var committedAt string
		if err := rows.Scan(&summary.ID, &parent, &summary.User, &summary.Result, &summary.Status, &errorText, &summary.RoundCount, &usageJSON, &totalUsageJSON, &committedAt); err != nil {
			return session.TurnPage{}, fmt.Errorf("scan turn summary: %w", err)
		}
		if parent.Valid {
			summary.ParentTurnID = &parent.Int64
		}
		summary.Error = errorText.String
		summary.TotalUsage, err = decodeUsage(totalUsageJSON.String)
		if err != nil {
			return session.TurnPage{}, fmt.Errorf("decode total usage: %w", err)
		}
		summary.Usage, err = decodeUsage(usageJSON.String)
		if err != nil {
			return session.TurnPage{}, fmt.Errorf("decode turn %d final usage: %w", summary.ID, err)
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
SELECT round_index, position, role, content, reasoning, images_json, tool_calls_json, tool_call_id, usage_json
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
		var content, reasoning, imagesJSON, callsJSON, callID, usageJSON sql.NullString
		if err := rows.Scan(&roundIndex, &position, &role, &content, &reasoning, &imagesJSON, &callsJSON, &callID, &usageJSON); err != nil {
			return session.RoundPage{}, fmt.Errorf("scan turn %d message: %w", id, err)
		}
		message, err := decodeMessage(role, content.String, reasoning.String, imagesJSON.String, callsJSON.String, callID.String)
		if err != nil {
			return session.RoundPage{}, fmt.Errorf("decode turn %d round %d position %d: %w", id, roundIndex, position, err)
		}
		if roundIndex != currentIndex {
			if roundIndex != expectedRound || position != 0 {
				return session.RoundPage{}, fmt.Errorf("decode turn %d: expected round %d position 0, got round %d position %d", id, expectedRound, roundIndex, position)
			}
			usage, err := decodeUsage(usageJSON.String)
			if err != nil {
				return session.RoundPage{}, fmt.Errorf("decode turn %d round %d usage: %w", id, roundIndex, err)
			}
			page.Rounds = append(page.Rounds, dora.Round{Assistant: message, Usage: usage})
			currentIndex = roundIndex
			expectedRound++
		} else {
			if usageJSON.Valid {
				return session.RoundPage{}, fmt.Errorf("decode turn %d round %d: tool position %d has usage", id, roundIndex, position)
			}
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
	validator := dora.NewTurn("")
	for index, round := range page.Rounds {
		if err := validator.AppendRound(round, ""); err != nil {
			return session.RoundPage{}, fmt.Errorf("decode turn %d round %d: %w", id, options.Offset+index, err)
		}
	}
	return page, nil
}

func (s *Store) initialize(ctx context.Context) error {
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
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("write sqlite schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit sqlite session schema: %w", err)
	}
	return s.validateSchema(ctx)
}

func (s *Store) validateSchema(ctx context.Context) error {
	queries := []string{
		`SELECT turn_id, attempt_index, record_json FROM model_attempts LIMIT 0`,
		`SELECT id, parent_turn_id, system, user, result, status, error, round_count, usage_json, total_usage_json, committed_at FROM turns LIMIT 0`,
		`SELECT turn_id, round_index, position, role, content, reasoning, images_json, tool_calls_json, tool_call_id, usage_json FROM messages LIMIT 0`,
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
	var turnsDefinition string
	if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'turns'`).Scan(&turnsDefinition); err != nil {
		return fmt.Errorf("validate sqlite session schema: read turns definition: %w", err)
	}
	if !strings.Contains(turnsDefinition, "'canceled'") {
		return fmt.Errorf("unsupported sqlite session schema version %d definition", schemaVersion)
	}
	return nil
}

var schemaStatements = []string{
	`CREATE TABLE turns (
        id INTEGER PRIMARY KEY,
        parent_turn_id INTEGER REFERENCES turns(id) CHECK (parent_turn_id != id),
        system TEXT NOT NULL,
        user TEXT NOT NULL,
        result TEXT NOT NULL,
		status TEXT NOT NULL CHECK (status IN ('completed', 'max_rounds', 'failed', 'canceled')),
		error TEXT,
        round_count INTEGER NOT NULL CHECK (round_count >= 0),
		usage_json TEXT,
		total_usage_json TEXT,
		committed_at TEXT NOT NULL,
		CHECK ((status = 'completed' AND error IS NULL) OR (status IN ('max_rounds', 'failed', 'canceled') AND error IS NOT NULL)),
		CHECK (status = 'completed' OR (result = '' AND usage_json IS NULL))
    )`,
	`CREATE INDEX turns_parent ON turns(parent_turn_id)`,
	`CREATE TABLE model_attempts (
        turn_id INTEGER NOT NULL,
        attempt_index INTEGER NOT NULL CHECK (attempt_index >= 0),
        record_json TEXT NOT NULL,
        PRIMARY KEY (turn_id, attempt_index),
        FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE
    )`,
	`CREATE TABLE messages (
        turn_id INTEGER NOT NULL,
        round_index INTEGER NOT NULL,
        position INTEGER NOT NULL,
        role TEXT NOT NULL,
        content TEXT,
        reasoning TEXT,
        images_json TEXT,
        tool_calls_json TEXT,
        tool_call_id TEXT,
		usage_json TEXT,
        PRIMARY KEY (turn_id, round_index, position),
        FOREIGN KEY (turn_id) REFERENCES turns(id) ON DELETE CASCADE,
		CHECK ((position = 0 AND role = 'assistant') OR (position > 0 AND role = 'tool')),
		CHECK (position = 0 OR usage_json IS NULL)
    )`,
}

type toolCallRecord struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Bytes are base64-encoded by encoding/json, preserving even malformed JSON
	// and invalid UTF-8 without validation or normalization.
	Input []byte `json:"input_bytes"`
}

func insertMessage(ctx context.Context, tx *sql.Tx, turnID int64, roundIndex, position int, message dora.Message, usage *dora.Usage) error {
	calls, err := encodeToolCalls(message.ToolCalls)
	if err != nil {
		return fmt.Errorf("encode turn %d round %d position %d tool calls: %w", turnID, roundIndex, position, err)
	}
	images, err := encodeImages(message.Images)
	if err != nil {
		return fmt.Errorf("encode turn %d round %d position %d images: %w", turnID, roundIndex, position, err)
	}
	usageJSON, err := encodeUsage(usage)
	if err != nil {
		return fmt.Errorf("encode turn %d round %d position %d usage: %w", turnID, roundIndex, position, err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO messages (turn_id, round_index, position, role, content, reasoning, images_json, tool_calls_json, tool_call_id, usage_json)
VALUES (?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''))`,
		turnID, roundIndex, position, string(message.Role), message.Content, message.Reasoning, images, calls, message.ToolCallID, usageJSON,
	)
	if err != nil {
		return fmt.Errorf("insert turn %d round %d position %d: %w", turnID, roundIndex, position, err)
	}
	return nil
}

func encodeUsage(usage *dora.Usage) (string, error) {
	if usage == nil {
		return "", nil
	}
	encoded, err := json.Marshal(usage)
	return string(encoded), err
}

func decodeUsage(value string) (*dora.Usage, error) {
	if value == "" {
		return nil, nil
	}
	var usage dora.Usage
	if err := json.Unmarshal([]byte(value), &usage); err != nil {
		return nil, err
	}
	return &usage, nil
}

func encodeToolCalls(calls []dora.ToolCall) (string, error) {
	if len(calls) == 0 {
		return "", nil
	}
	records := make([]toolCallRecord, len(calls))
	for i, call := range calls {
		records[i] = toolCallRecord{ID: call.ID, Name: call.Name, Input: append([]byte(nil), call.Input...)}
	}
	encoded, err := json.Marshal(records)
	return string(encoded), err
}

func encodeImages(images []dora.Image) (string, error) {
	if len(images) == 0 {
		return "", nil
	}
	encoded, err := json.Marshal(images)
	return string(encoded), err
}

func decodeImages(value string) ([]dora.Image, error) {
	if value == "" {
		return nil, nil
	}
	var images []dora.Image
	if err := json.Unmarshal([]byte(value), &images); err != nil {
		return nil, err
	}
	return images, nil
}

func decodeMessage(role, content, reasoning, imagesJSON, callsJSON, callID string) (dora.Message, error) {
	images, err := decodeImages(imagesJSON)
	if err != nil {
		return dora.Message{}, err
	}
	message := dora.Message{Role: dora.Role(role), Content: content, Reasoning: reasoning, Images: images, ToolCallID: callID}
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

// GetAttempts returns audit records independently of model-visible tool rounds.
func (s *Store) GetAttempts(ctx context.Context, id int64, options session.RoundOptions) (session.AttemptPage, error) {
	if s == nil || s.db == nil {
		return session.AttemptPage{}, errors.New("sqlite session is not initialized")
	}
	if id <= 0 {
		return session.AttemptPage{}, errors.New("turn ID must be positive")
	}
	if options.Offset < 0 || options.Limit <= 0 {
		return session.AttemptPage{}, errors.New("attempt offset must be non-negative and limit positive")
	}
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM turns WHERE id = ?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return session.AttemptPage{}, fmt.Errorf("%w: %d", session.ErrNotFound, id)
		}
		return session.AttemptPage{}, fmt.Errorf("get turn %d attempts: %w", id, err)
	}
	page := session.AttemptPage{Offset: options.Offset, Limit: options.Limit, Attempts: []dora.ModelAttempt{}}
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_attempts WHERE turn_id = ?`, id).Scan(&page.Total); err != nil {
		return session.AttemptPage{}, fmt.Errorf("count turn %d attempts: %w", id, err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT record_json FROM model_attempts WHERE turn_id = ? ORDER BY attempt_index LIMIT ? OFFSET ?`, id, options.Limit, options.Offset)
	if err != nil {
		return session.AttemptPage{}, fmt.Errorf("get turn %d attempts: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var encoded string
		if err := rows.Scan(&encoded); err != nil {
			return session.AttemptPage{}, fmt.Errorf("scan turn %d attempt: %w", id, err)
		}
		var attempt dora.ModelAttempt
		if err := json.Unmarshal([]byte(encoded), &attempt); err != nil {
			return session.AttemptPage{}, fmt.Errorf("decode turn %d attempt %d: %w", id, options.Offset+len(page.Attempts), err)
		}
		page.Attempts = append(page.Attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return session.AttemptPage{}, fmt.Errorf("get turn %d attempts: %w", id, err)
	}
	return page, nil
}

// CommitChild records a terminal child without changing the parent transcript or usage.
func (s *Store) CommitChild(ctx context.Context, parentID int64, turn *dora.Turn, cause error) (int64, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("sqlite session is not initialized")
	}
	if parentID <= 0 || turn == nil {
		return 0, errors.New("child requires a parent ID and turn")
	}
	status := session.TurnStatusCompleted
	result, complete := turn.Result()
	errorText := ""
	if !complete {
		if cause == nil {
			return 0, errors.New("incomplete child requires an error")
		}
		errorText = cause.Error()
		switch {
		case errors.Is(cause, dora.ErrMaxRounds):
			status = session.TurnStatusMaxRounds
		case errors.Is(cause, context.Canceled):
			status = session.TurnStatusCanceled
		default:
			status = session.TurnStatusFailed
		}
	}
	return s.commitTurn(ctx, parentID, turn, status, result, errorText, turn.Usage())
}
