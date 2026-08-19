package events

import (
	"context"
)

// Transport is a pluggable, receive-only event source. Phase one only needs to
// receive events; sending is deferred to a later phase. A Transport must be
// safe for a single serial consumer of Next.
type Transport interface {
	// Next returns the next event, blocking until one is available or the
	// context is canceled.
	Next(ctx context.Context) (Event, error)
	// Close releases transport resources.
	Close() error
}
