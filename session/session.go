// Package session defines persistent storage for completed Dora turns.
package session

import (
	"context"
	"errors"
	"time"

	"github.com/lgxz/dora"
)

var ErrNotFound = errors.New("session turn not found")

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
// Usage is the final model call's optional provider-reported accounting.
type TurnSummary struct {
	ID          int64       `json:"id"`
	User        string      `json:"user"`
	Result      string      `json:"result"`
	RoundCount  int         `json:"rounds"`
	Usage       *dora.Usage `json:"usage,omitempty"`
	CommittedAt time.Time   `json:"committed_at"`
}

// TurnPage is a newest-first page of completed turns.
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

// Reader provides read-only access to completed turns.
type Reader interface {
	ListTurns(context.Context, ListOptions) (TurnPage, error)
	GetRounds(context.Context, int64, RoundOptions) (RoundPage, error)
}

// Store appends completed turns and provides history queries.
type Store interface {
	Reader
	CommitTurn(context.Context, *dora.Turn) (int64, error)
	Close() error
}
