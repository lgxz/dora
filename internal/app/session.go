// Package app provides the reusable application runtime shared by Dora's
// protocol and command-line frontends.
package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/job"
	"github.com/lgxz/dora/session"
)

const commitTimeout = 5 * time.Second

// terminalSaveAttempts bounds how often a failing terminal save is tried. The
// writer is safe to retry: a failed save rolls back and advances no counters.
// Retrying rides out brief contention (another process holding the session
// file) before a finished run is reported as unsaved.
const terminalSaveAttempts = 3

// terminalSaveBackoff is the wait before the second attempt; it doubles for
// each further one. It is a variable so tests do not have to sleep.
var terminalSaveBackoff = 200 * time.Millisecond

// ErrSessionBusy reports an attempt to start a second prompt while one is
// already running in the same application session.
var ErrSessionBusy = errors.New("session already has an active prompt")

// PersistenceError reports that a terminal Turn could not be committed. A
// long-running frontend should stop rather than discard that Turn and continue.
// The PromptResult returned with it is still filled in: when the model
// finished, Completed and Content carry the answer, so the frontend can
// deliver it before reporting that it could not be saved.
type PersistenceError struct {
	err error
}

func (e *PersistenceError) Error() string { return e.err.Error() }
func (e *PersistenceError) Unwrap() error { return e.err }

// IsPersistenceError reports whether err came from committing a terminal Turn.
func IsPersistenceError(err error) bool {
	var target *PersistenceError
	return errors.As(err, &target)
}

// ContinueFunc decides whether a Turn stopped by ErrMaxRounds should run
// another segment. A nil callback declines continuation.
type ContinueFunc func(context.Context, *dora.Turn, error) (bool, error)

// PromptOptions controls one prompt without changing session state.
type PromptOptions struct {
	Observer dora.Observer
	Continue ContinueFunc
}

// PromptResult describes the terminal state of one prompt. Completed is false
// only when continuation after ErrMaxRounds was declined, or when the run
// failed or was canceled. It reports whether the model produced a final answer,
// independently of whether that Turn could be saved.
type PromptResult struct {
	Turn      *dora.Turn
	Content   string
	Completed bool
}

// Session owns the state shared by prompts in one frontend conversation.
// Agent is immutable; Store supplies the history tool and persists terminal
// Turns; Jobs tracks command and nested Task work for this session.
type Session struct {
	agent            *dora.Agent
	store            session.Store
	jobs             *job.Manager
	workingDirectory string

	mu              sync.Mutex
	active          context.CancelFunc
	done            chan struct{}
	closed          bool
	recordMu        sync.Mutex
	childRecords    []childRecord
	childrenClosed  bool
	childSaveErrors []error
}

// NewSession creates an application session from already assembled runtime
// dependencies. Each frontend session should receive its own Store and Jobs.
func NewSession(agent *dora.Agent, store session.Store, jobs *job.Manager, workingDirectory string) (*Session, error) {
	if agent == nil {
		return nil, errors.New("application session agent is nil")
	}
	if store == nil {
		return nil, errors.New("application session store is nil")
	}
	if jobs == nil {
		return nil, errors.New("application session job manager is nil")
	}
	return &Session{
		agent:            agent,
		store:            store,
		jobs:             jobs,
		workingDirectory: workingDirectory,
	}, nil
}

// Prompt runs and persists one independent Dora Turn. Previous Turns remain
// available through the session's history tool rather than being inserted into
// the model request automatically.
func (s *Session) Prompt(ctx context.Context, prompt string, options PromptOptions) (PromptResult, error) {
	if s == nil {
		return PromptResult{}, errors.New("application session is nil")
	}
	runCtx, finish, err := s.beginPrompt(ctx)
	if err != nil {
		return PromptResult{}, err
	}
	defer finish()
	collector := &childCollector{owner: s}
	runCtx = context.WithValue(runCtx, childrenKey{}, collector)
	defer s.flushChildren(false)

	turn := dora.NewTurn(prompt)
	writer := s.store.BeginTurn(turn)
	saveWarned := false
	runOptions := dora.RunOptions{
		WorkingDirectory: s.workingDirectory,
		// Save every completed round as soon as it exists, so a crash or forced
		// exit keeps the work done so far. A failed save is only a warning: the
		// writer retries whatever is missing on the next save, and Finish stores
		// the rest, which is where a persistent storage failure becomes an error.
		// A canceled run is left to the terminal save, on its detached context.
		OnRound: func(*dora.Turn) {
			if runCtx.Err() != nil {
				return
			}
			syncCtx, cancelSync := context.WithTimeout(runCtx, commitTimeout)
			defer cancelSync()
			if err := writer.Sync(syncCtx); err != nil && !saveWarned {
				saveWarned = true
				if options.Observer != nil {
					options.Observer.Observe(dora.Update{
						Kind: dora.UpdateInfo,
						Info: fmt.Sprintf("Warning: could not save turn progress: %v", err),
					})
				}
			}
		},
	}
	// saveTerminal records the terminal state on a detached context, so a cancellation
	// racing the run-to-store transition cannot discard a Turn.
	saveTerminal := func(status session.TurnStatus, cause error) (id int64, err error) {
		backoff := terminalSaveBackoff
		for attempt := 1; ; attempt++ {
			commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(runCtx), commitTimeout)
			id, err = writer.Finish(commitCtx, status, cause)
			cancelCommit()
			if err == nil || attempt == terminalSaveAttempts {
				return id, err
			}
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	for {
		runErr := s.agent.RunObservedWithOptions(runCtx, turn, options.Observer, runOptions)
		if runErr == nil {
			// A completed turn is the one result that must not be lost, so the
			// answer is returned even when saving it fails.
			parentID, commitErr := saveTerminal(session.TurnStatusCompleted, nil)
			collector.parentID = parentID
			content, _ := turn.Result()
			result := PromptResult{Turn: turn, Content: content, Completed: true}
			if commitErr != nil {
				return result, &PersistenceError{err: commitErr}
			}
			return result, nil
		}

		if errors.Is(runErr, dora.ErrMaxRounds) {
			keepGoing := false
			var continueErr error
			if options.Continue != nil {
				keepGoing, continueErr = options.Continue(runCtx, turn, runErr)
			}
			if keepGoing && continueErr == nil {
				continue
			}
			parentID, commitErr := saveTerminal(session.TurnStatusMaxRounds, runErr)
			collector.parentID = parentID
			if commitErr != nil {
				return PromptResult{Turn: turn}, &PersistenceError{err: commitErr}
			}
			if continueErr != nil {
				return PromptResult{Turn: turn}, continueErr
			}
			if options.Continue != nil {
				return PromptResult{Turn: turn}, nil
			}
			return PromptResult{Turn: turn}, runErr
		}

		status := session.TurnStatusFailed
		if errors.Is(runErr, context.Canceled) {
			status = session.TurnStatusCanceled
		}
		parentID, commitErr := saveTerminal(status, runErr)
		collector.parentID = parentID
		if commitErr != nil {
			return PromptResult{Turn: turn}, &PersistenceError{err: commitErr}
		}
		return PromptResult{Turn: turn}, runErr
	}
}

func (s *Session) beginPrompt(parent context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, errors.New("application session is closed")
	}
	if s.active != nil {
		return nil, nil, ErrSessionBusy
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	s.active = cancel
	s.done = done
	return ctx, func() {
		cancel()
		s.mu.Lock()
		s.active = nil
		s.done = nil
		close(done)
		s.mu.Unlock()
	}, nil
}

// Cancel interrupts the active prompt, if any. It returns whether there was an
// active prompt to cancel.
func (s *Session) Cancel() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	cancel := s.active
	s.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// ActiveCounts reports active command and nested Task jobs.
func (s *Session) ActiveCounts() (commands, tasks int) {
	if s == nil || s.jobs == nil {
		return 0, 0
	}
	return s.jobs.ActiveCounts()
}

// Close cancels an active prompt and closes the history store. It is safe to
// call more than once.
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.active
	done := s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	// Background Tasks do not survive session closure. Give their canceled
	// Turns a chance to reach the collector before closing the store.
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), commitTimeout)
	if err := s.jobs.StopTasks(cleanupCtx); err != nil {
		s.childSaveErrors = append(s.childSaveErrors, err)
	}
	cancelCleanup()
	s.flushChildren(true)
	if err := s.store.Close(); err != nil {
		s.childSaveErrors = append(s.childSaveErrors, fmt.Errorf("close application session: %w", err))
	}
	return errors.Join(s.childSaveErrors...)
}
