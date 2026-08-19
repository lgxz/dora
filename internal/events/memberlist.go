package events

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"github.com/hashicorp/memberlist"
)

// memberlistTransport receives and sends events over memberlist gossip. It
// implements memberlist.Delegate and delivers inbound events through a bounded
// channel so the gossip receive loop never blocks. Outbound broadcasts are
// queued and delivered via gossip.
type memberlistTransport struct {
	list  *memberlist.Memberlist
	msgs  chan Event
	queue *memberlist.TransmitLimitedQueue
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
	t.queue = &memberlist.TransmitLimitedQueue{
		RetransmitMult: memberlist.DefaultLANConfig().RetransmitMult,
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
	t.queue.NumNodes = func() int { return list.NumMembers() }
	return t, nil
}

// Send delivers an event into the cluster. An empty Receiver broadcasts via
// gossip; a non-empty Receiver targets that node with a reliable unicast.
func (t *memberlistTransport) Send(ev Event) error {
	encoded, err := encodeEvent(ev)
	if err != nil {
		return fmt.Errorf("send event: encode: %w", err)
	}

	if ev.Receiver == "" {
		t.queue.QueueBroadcast(&eventBroadcast{msg: encoded})
		return nil
	}

	for _, n := range t.list.Members() {
		if n.Name == ev.Receiver {
			if err := t.list.SendReliable(n, encoded); err != nil {
				return fmt.Errorf("send event to %q: %w", ev.Receiver, err)
			}
			return nil
		}
	}
	return fmt.Errorf("send event: receiver %q not found", ev.Receiver)
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

// GetBroadcasts implements memberlist.Delegate, returning queued gossip
// broadcasts for the memberlist layer to deliver.
func (t *memberlistTransport) GetBroadcasts(overhead, limit int) [][]byte {
	return t.queue.GetBroadcasts(overhead, limit)
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

// eventBroadcast is a single gossip broadcast message. Each message is unique,
// so it never invalidates another.
type eventBroadcast struct {
	msg []byte
}

func (b *eventBroadcast) Invalidates(other memberlist.Broadcast) bool { return false }

func (b *eventBroadcast) Message() []byte { return b.msg }

func (b *eventBroadcast) Finished() {}
