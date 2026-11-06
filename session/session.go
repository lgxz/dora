// Package session defines persistent storage for Dora turns, saved
// incrementally while they run and finalized when they stop.
package session

import (
	"context"
	"errors"
	"time"

	"github.com/lgxz/dora"
)

var ErrNotFound = errors.New("session turn not found")

// TurnStatus describes the state of a saved turn: still running, or why it stopped.
type TurnStatus string

const (
	// TurnStatusRunning marks a turn whose completed rounds are saved but which
	// has not reached a terminal state. A row that stays running was interrupted
	// by a crash or forced exit (or belongs to a process still working on it).
	TurnStatusRunning   TurnStatus = "running"
	TurnStatusCompleted TurnStatus = "completed"
	TurnStatusMaxRounds TurnStatus = "max_rounds"
	TurnStatusFailed    TurnStatus = "failed"
	TurnStatusCanceled  TurnStatus = "canceled"
)

// ListOptions selects a page of turns. Offset zero starts at the newest turn.
type ListOptions struct {
	Offset int
	Limit  int
}

// RoundOptions selects a chronological page of tool rounds within one turn.
type RoundOptions struct {
	Offset int
	Limit  int
}

// TurnSummary is the compact representation returned by history listings.
// Usage is the final model call's optional provider-reported accounting and is
// nil for turns stopped at the maximum-round limit, by an error, or by
// cancellation.
type TurnSummary struct {
	ID           int64       `json:"id"`
	ParentTurnID *int64      `json:"parent_turn_id,omitempty"`
	User         string      `json:"user"`
	Result       string      `json:"result"`
	Status       TurnStatus  `json:"status"`
	Error        string      `json:"error,omitempty"`
	RoundCount   int         `json:"rounds"`
	Usage        *dora.Usage `json:"usage,omitempty"`
	TotalUsage   *dora.Usage `json:"total_usage,omitempty"`
	CommittedAt  time.Time   `json:"committed_at"`
}

// TurnPage is a newest-first page of saved turns.
type TurnPage struct {
	Total  int           `json:"total"`
	Offset int           `json:"offset"`
	Limit  int           `json:"limit"`
	Turns  []TurnSummary `json:"turns"`
}

// RoundPage is a chronological page of complete rounds from one turn. Each
// round carries the optional usage reported for its assistant model call.
type RoundPage struct {
	Total  int          `json:"total"`
	Offset int          `json:"offset"`
	Limit  int          `json:"limit"`
	Rounds []dora.Round `json:"rounds"`
}

// Reader provides read-only access to saved turns, including child turns.
type Reader interface {
	ListTurns(context.Context, ListOptions) (TurnPage, error)
	GetRounds(context.Context, int64, RoundOptions) (RoundPage, error)
}

// TurnWriter incrementally persists one main Turn. It is used from a single
// goroutine: Sync from the Agent's round callback, then Finish once.
//
// Sync and Finish are idempotent about content: each saves every round and
// model attempt the writer has not saved yet, so a failed Sync is repaired by
// the next Sync or by Finish without losing or duplicating anything.
type TurnWriter interface {
	// ID returns the saved turn's ID, or zero before the first successful save.
	ID() int64
	// Sync saves the complete rounds and finalized model attempts added since the
	// previous save. The first save with at least one round creates the turn with
	// status running. Sync is a no-op when there is nothing new to save.
	Sync(ctx context.Context) error
	// Finish saves any remaining rounds and attempts and records the terminal
	// status in one transaction, returning the turn ID. status must be one of the
	// four terminal statuses and cause must match it (nil for completed).
	Finish(ctx context.Context, status TurnStatus, cause error) (int64, error)
}

// Store saves Dora turns and provides saved-turn history queries. Main turns
// are written through a TurnWriter; a child turn is saved once, after its
// parent exists, by CommitChild.
type Store interface {
	Reader
	// BeginTurn returns a writer for turn. It performs no storage work; the turn
	// is created on the first save.
	BeginTurn(turn *dora.Turn) TurnWriter
	CommitChild(context.Context, int64, *dora.Turn, error) (int64, error)
	Close() error
}

// AttemptPage is a chronological page of model calls, including discarded calls.
type AttemptPage struct {
	Total    int                 `json:"total"`
	Offset   int                 `json:"offset"`
	Limit    int                 `json:"limit"`
	Attempts []dora.ModelAttempt `json:"attempts"`
}

// AttemptReader exposes the audit stream separately from conversation rounds.
type AttemptReader interface {
	GetAttempts(context.Context, int64, RoundOptions) (AttemptPage, error)
}
