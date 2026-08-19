package events

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/hashicorp/memberlist"
)

// memberlistTransport receives events over memberlist gossip. It implements
// memberlist.Delegate and delivers inbound events through a bounded channel so
// the gossip receive loop never blocks.
type memberlistTransport struct {
	list  *memberlist.Memberlist
	msgs  chan Event
	done  chan struct{}
	close chan struct{}
}

const (
	defaultBind       = "0.0.0.0:8848"
	defaultBufferSize = 256
)

// newMemberlistTransport starts dora as the first node of a memberlist
// cluster, bound to host:port.
func newMemberlistTransport(cfg MemberlistConfig) (*memberlistTransport, error) {
	if cfg.Bind == "" {
		cfg.Bind = defaultBind
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = defaultBufferSize
	}

	host, port, err := net.SplitHostPort(cfg.Bind)
	if err != nil {
		return nil, fmt.Errorf("parse bind address %q: %w", cfg.Bind, err)
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("parse bind port %q: %w", port, err)
	}

	t := &memberlistTransport{
		msgs:  make(chan Event, cfg.BufferSize),
		done:  make(chan struct{}),
		close: make(chan struct{}),
	}

	conf := memberlist.DefaultLANConfig()
	conf.BindAddr = host
	conf.BindPort = portNum
	conf.Delegate = t
	// Use the configured name, falling back to the bind address, so multiple
	// dora nodes on the same host do not collide on the hostname-based default.
	if cfg.Name != "" {
		conf.Name = cfg.Name
	} else {
		conf.Name = cfg.Bind
	}
	// Memberlist emits its own logs to stderr by default; silence them to keep
	// the CLI output clean, since dora reports its own progress.
	conf.LogOutput = discardWriter{}

	list, err := memberlist.Create(conf)
	if err != nil {
		return nil, fmt.Errorf("create memberlist: %w", err)
	}
	t.list = list
	return t, nil
}

// Next blocks until an event is available or ctx is canceled.
func (t *memberlistTransport) Next(ctx context.Context) (Event, error) {
	select {
	case <-ctx.Done():
		return Event{}, ctx.Err()
	case ev := <-t.msgs:
		return ev, nil
	case <-t.done:
		return Event{}, fmt.Errorf("memberlist transport closed")
	}
}

// Close shuts down the memberlist node and releases resources.
func (t *memberlistTransport) Close() error {
	select {
	case <-t.close:
		return nil
	default:
		close(t.close)
	}
	err := t.list.Shutdown()
	close(t.done)
	return err
}

// NodeMeta implements memberlist.Delegate. It returns empty metadata, since
// dora does not attach node metadata in phase one.
func (t *memberlistTransport) NodeMeta(limit int) []byte {
	return nil
}

// NotifyMsg implements memberlist.Delegate. It decodes inbound events and
// enqueues them without blocking: when the buffer is full the newest event is
// dropped to keep the gossip receive loop responsive.
func (t *memberlistTransport) NotifyMsg(buf []byte) {
	ev, err := decodeEvent(buf)
	if err != nil {
		// Ignore messages that are not valid events (for example future
		// message kinds or malformed payloads).
		return
	}
	select {
	case t.msgs <- ev:
	default:
		// Buffer full: drop the newest event rather than block the receive loop.
	}
}

// GetBroadcasts implements memberlist.Delegate. Phase one does not broadcast,
// so it always returns nil.
func (t *memberlistTransport) GetBroadcasts(overhead, limit int) [][]byte {
	return nil
}

// LocalState implements memberlist.Delegate with no state.
func (t *memberlistTransport) LocalState(join bool) []byte {
	return nil
}

// MergeRemoteState implements memberlist.Delegate as a no-op.
func (t *memberlistTransport) MergeRemoteState(buf []byte, join bool) {}

// discardWriter discards all writes. It keeps memberlist logging silent.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) {
	return len(p), nil
}
