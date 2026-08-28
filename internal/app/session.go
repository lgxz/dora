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

// ErrSessionBusy reports an attempt to start a second prompt while one is
// already running in the same application session.
var ErrSessionBusy = errors.New("dora: session already has an active prompt")

// PersistenceError reports that a terminal Turn could not be committed. A
// long-running frontend should stop rather than discard that Turn and continue.
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
// only when continuation after ErrMaxRounds was declined.
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

	mu     sync.Mutex
	active context.CancelFunc
	done   chan struct{}
	closed bool
}

// NewSession creates an application session from already assembled runtime
// dependencies. Each frontend session should receive its own Store and Jobs.
func NewSession(agent *dora.Agent, store session.Store, jobs *job.Manager, workingDirectory string) (*Session, error) {
	if agent == nil {
		return nil, errors.New("dora: application session agent is nil")
	}
	if store == nil {
		return nil, errors.New("dora: application session store is nil")
	}
	if jobs == nil {
		return nil, errors.New("dora: application session job manager is nil")
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
		return PromptResult{}, errors.New("dora: application session is nil")
	}
	runCtx, finish, err := s.beginPrompt(ctx)
	if err != nil {
		return PromptResult{}, err
	}
	defer finish()

	turn := dora.NewTurn(prompt)
	for {
		runErr := s.agent.RunObservedWithOptions(runCtx, turn, options.Observer, dora.RunOptions{
			WorkingDirectory: s.workingDirectory,
		})
		if runErr == nil {
			if _, err := s.store.CommitTurn(runCtx, turn); err != nil {
				return PromptResult{Turn: turn}, &PersistenceError{err: err}
			}
			content, _ := turn.Result()
			return PromptResult{Turn: turn, Content: content, Completed: true}, nil
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
			commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(runCtx), commitTimeout)
			_, commitErr := s.store.CommitMaxRounds(commitCtx, turn, runErr)
			cancelCommit()
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

		commitCtx, cancelCommit := context.WithTimeout(context.WithoutCancel(runCtx), commitTimeout)
		var commitErr error
		if errors.Is(runErr, context.Canceled) {
			_, commitErr = s.store.CommitCanceled(commitCtx, turn, runErr)
		} else {
			_, commitErr = s.store.CommitFailed(commitCtx, turn, runErr)
		}
		cancelCommit()
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
		return nil, nil, errors.New("dora: application session is closed")
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
	return s.close(false)
}

// Shutdown closes the session and also cancels all background jobs. Protocol
// frontends use it when the remote client explicitly releases the session.
func (s *Session) Shutdown() error {
	return s.close(true)
}

func (s *Session) close(cancelJobs bool) error {
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
	if cancelJobs {
		s.jobs.CancelAll()
	}
	if err := s.store.Close(); err != nil {
		return fmt.Errorf("close application session: %w", err)
	}
	return nil
}
