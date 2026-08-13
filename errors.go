package dora

import "time"

// RetryableErrorKind classifies a retryable failure so the retry policy can
// choose an appropriate attempt limit and backoff.
type RetryableErrorKind int

const (
	// RetryableGeneric is the default kind for transient failures such as
	// server errors and network timeouts.
	RetryableGeneric RetryableErrorKind = iota
	// RetryableRateLimit marks a rate-limit (HTTP 429) failure.
	RetryableRateLimit
)

// RetryableError marks a model failure that may succeed on a later attempt,
// such as a rate limit, a transient server error, or a network timeout.
// RetryAfter optionally carries a server-suggested delay; zero means unknown.
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
	// Kind classifies the failure so the retry policy can adapt. The zero
	// value is RetryableGeneric.
	Kind RetryableErrorKind
}

// Error returns the wrapped error message.
func (e *RetryableError) Error() string { return e.Err.Error() }

// Unwrap exposes the underlying error for errors.Is and errors.As.
func (e *RetryableError) Unwrap() error { return e.Err }
