package provider

import (
	"context"
	"time"
)

// WithIdleTimeout returns a context that is cancelled when no activity is
// reported within idle for the given duration. The returned reset function
// must be called on each unit of activity to postpone the deadline.
func WithIdleTimeout(ctx context.Context, idle time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	timer := time.NewTimer(idle)
	reset := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(idle)
	}
	go func() {
		select {
		case <-timer.C:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, reset
}
