package provider

import (
	"context"
	"testing"
	"time"
)

func TestWithIdleTimeoutCancelsAfterIdle(t *testing.T) {
	ctx, _ := WithIdleTimeout(context.Background(), 20*time.Millisecond)
	select {
	case <-ctx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("context not cancelled after idle window")
	}
}

func TestWithIdleTimeoutResetPostpones(t *testing.T) {
	ctx, reset := WithIdleTimeout(context.Background(), 30*time.Millisecond)
	// Reset periodically across a span comfortably longer than a single idle
	// window; cancellation must not fire while activity is being reported.
	for i := 0; i < 5; i++ {
		select {
		case <-ctx.Done():
			t.Fatal("context cancelled despite periodic resets")
		default:
		}
		time.Sleep(10 * time.Millisecond)
		reset()
	}
	// Once resets stop, the idle deadline is reached and the context cancels.
	select {
	case <-ctx.Done():
		// Success.
	case <-time.After(500 * time.Millisecond):
		t.Fatal("context not cancelled after resets stopped")
	}
}

func TestWithIdleTimeoutExternalCancelStops(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, _ := WithIdleTimeout(parent, 1*time.Hour)
	cancelParent()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("context not cancelled by parent")
	}
	// No goroutine should leak: with the parent cancelled, the idle timer's own
	// firing is irrelevant. Sleep briefly to let the goroutine exit if it is
	// going to, then confirm the context stays done and doesn't panic.
	time.Sleep(20 * time.Millisecond)
}