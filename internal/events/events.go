package events

import (
	"context"
	"fmt"
)

// MemberlistConfig configures the memberlist transport.
type MemberlistConfig struct {
	// Bind is the "host:port" bind address. Empty uses "0.0.0.0:8848".
	Bind string `yaml:"bind,omitempty"`
	// Name is the memberlist node name. Empty derives a name from the bind
	// address.
	Name string `yaml:"name,omitempty"`
	// BufferSize bounds the number of queued events before they are dropped.
	// Zero uses a sensible default.
	BufferSize int `yaml:"-"`
}

// Transports configures the available event transports. Phase one supports
// only memberlist; a future phase may add more and route between them.
type Transports struct {
	Memberlist MemberlistConfig `yaml:"memberlist,omitempty"`
}

// Config is the complete event source configuration. The CLI passes it through
// unchanged; New selects and wires the concrete transport(s) internally.
type Config struct {
	// Enabled turns on event daemon mode. When false, New returns a disabled
	// Events with no transport.
	Enabled    bool       `yaml:"enabled,omitempty"`
	Transports Transports `yaml:"transports,omitempty"`
}

// Events is the facade the CLI depends on. It owns a concrete Transport and
// hides which transport (and future routing) backs it.
type Events struct {
	enabled   bool
	transport Transport
}

// New constructs the event source from the given configuration. When enabled,
// it selects and wires the concrete transport(s) internally; when disabled it
// returns a no-op Events. Phase one always selects the memberlist transport; a
// future phase may dispatch across multiple transports.
func New(cfg Config) (*Events, error) {
	if !cfg.Enabled {
		return &Events{}, nil
	}
	transport, err := newMemberlistTransport(cfg.Transports.Memberlist)
	if err != nil {
		return nil, fmt.Errorf("open events: %w", err)
	}
	return &Events{enabled: true, transport: transport}, nil
}

// Enabled reports whether event daemon mode is active.
func (e *Events) Enabled() bool {
	return e != nil && e.enabled
}

// Next returns the next received event, blocking until one is available or ctx
// is canceled.
func (e *Events) Next(ctx context.Context) (Event, error) {
	if e.transport == nil {
		return Event{}, fmt.Errorf("events: not enabled")
	}
	return e.transport.Next(ctx)
}

// Close releases the underlying transport.
func (e *Events) Close() error {
	if e.transport == nil {
		return nil
	}
	return e.transport.Close()
}
