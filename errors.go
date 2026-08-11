package dora

import "time"

// RetryableError marks a model failure that may succeed on a later attempt,
// such as a rate limit, a transient server error, or a network timeout.
// RetryAfter optionally carries a server-suggested delay; zero means unknown.
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

// Error returns the wrapped error message.
func (e *RetryableError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying error for errors.Is and errors.As.
func (e *RetryableError) Unwrap() error { return e.Err }
