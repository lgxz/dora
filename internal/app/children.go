package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/lgxz/dora"
)

type childrenKey struct{}

type childRecord struct {
	parent *childCollector
	turn   *dora.Turn
	cause  error
}

// Each prompt owns a distinct collector. Background task contexts retain this
// pointer even when another prompt starts in the same application session.
type childCollector struct {
	owner    *Session
	parentID int64
}

// RecordTask retains a terminal task Turn for saving at an application boundary.
// Ownership transfers to the collector; callers must not mutate turn afterward.
// No database operation or storage wait occurs in the task execution path.
func RecordTask(ctx context.Context, turn *dora.Turn, cause error) {
	collector, _ := ctx.Value(childrenKey{}).(*childCollector)
	if collector == nil || turn == nil {
		return
	}
	s := collector.owner
	s.recordMu.Lock()
	defer s.recordMu.Unlock()
	if !s.childrenClosed {
		s.childRecords = append(s.childRecords, childRecord{parent: collector, turn: turn, cause: cause})
	}
}

// flushChildren runs only after a prompt finishes or while closing the session.
// Save errors are diagnostic: they do not replace task results or prompt errors.
func (s *Session) flushChildren(final bool) {
	s.recordMu.Lock()
	pending := s.childRecords
	s.childRecords = nil
	if final {
		s.childrenClosed = true
	}
	s.recordMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), commitTimeout)
	defer cancel()
	for _, record := range pending {
		parentID := record.parent.parentID
		if parentID == 0 {
			s.childSaveErrors = append(s.childSaveErrors, errors.New("cannot save child because its parent was not saved"))
			continue
		}
		if _, err := s.store.CommitChild(ctx, parentID, record.turn, record.cause); err != nil {
			s.childSaveErrors = append(s.childSaveErrors, fmt.Errorf("save child of turn %d: %w", parentID, err))
		}
	}
}
