package events

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Event is the transport-neutral event protocol consumed by the dora -server
// mode. It carries a trigger signal plus structured payload, and is free of
// any specific transport wire details.
type Event struct {
	// ID uniquely identifies the event within the cluster.
	ID string `json:"id,omitempty"`
	// Type is the business category of the event (for example code-review,
	// deploy, alert). It is unrelated to transport routing.
	Type string `json:"type,omitempty"`
	// Sender is the name of the originating node.
	Sender string `json:"sender,omitempty"`
	// Receiver is the intended receiving node. Empty means broadcast;
	// non-empty means a directed unicast to that node.
	Receiver string `json:"receiver,omitempty"`
	// Msg is the human-readable message body of the event.
	Msg string `json:"msg,omitempty"`
	// Meta holds arbitrary key/value event payload.
	Meta map[string]string `json:"meta,omitempty"`
}

// encode serializes an Event into the wire format prefixed with a one-byte
// type tag. A single tag value (eventTag) is used today; the prefix leaves
// room for future message kinds without breaking compatibility.
const eventTag byte = 1

func encodeEvent(e Event) ([]byte, error) {
	payload, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 1, 1+len(payload))
	out[0] = eventTag
	out = append(out, payload...)
	return out, nil
}

func decodeEvent(data []byte) (Event, error) {
	if len(data) == 0 {
		return Event{}, fmt.Errorf("decode event: empty message")
	}
	if data[0] != eventTag {
		return Event{}, fmt.Errorf("decode event: unknown tag %d", data[0])
	}
	var e Event
	if err := json.Unmarshal(data[1:], &e); err != nil {
		return Event{}, fmt.Errorf("decode event: %w", err)
	}
	return e, nil
}

// Empty reports whether the event carries no meaningful content to process. An
// empty event (no type, message, or metadata) should not trigger a turn.
func (e Event) Empty() bool {
	return e.Type == "" && e.Msg == "" && len(e.Meta) == 0
}

// EventPrompt renders an Event into a natural-language user prompt suitable
// for starting a turn.
func EventPrompt(e Event) string {
	var b strings.Builder
	b.WriteString("You received an event from the cluster. Process it as the user's request.\n\n")

	if e.ID != "" {
		fmt.Fprintf(&b, "Event ID: %s\n", e.ID)
	}
	if e.Type != "" {
		fmt.Fprintf(&b, "Type: %s\n", e.Type)
	}
	switch {
	case e.Sender != "" && e.Receiver != "":
		fmt.Fprintf(&b, "From: %s (directed to %s)\n", e.Sender, e.Receiver)
	case e.Sender != "":
		fmt.Fprintf(&b, "From: %s (broadcast)\n", e.Sender)
	}
	if e.Msg != "" {
		fmt.Fprintf(&b, "Message: %s\n", e.Msg)
	}
	if len(e.Meta) > 0 {
		b.WriteString("Details:\n")
		for k, v := range e.Meta {
			fmt.Fprintf(&b, "  %s: %s\n", k, v)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
