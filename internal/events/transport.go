package events

import (
	"context"
)

// Transport is a pluggable event transport. Phase two adds sending; a
// Transport must be safe for a single serial consumer of Next, and Send is
// called from a single goroutine (the agent run loop) as well.
type Transport interface {
	// Next returns the next event, blocking until one is available or the
	// context is canceled.
	Next(ctx context.Context) (Event, error)
	// Send delivers an event into the cluster. An empty Receiver broadcasts;
	// a non-empty Receiver targets that node.
	Send(ev Event) error
	// Close releases transport resources.
	Close() error
}
