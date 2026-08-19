package events

import (
	"strings"
	"testing"
)

func TestEventRoundTrip(t *testing.T) {
	orig := Event{
		ID:       "evt-123",
		Type:     "code-review",
		Sender:   "node-a",
		Receiver: "node-b",
		Msg:      "please review",
		Meta:     map[string]string{"key": "value"},
	}
	encoded, err := encodeEvent(orig)
	if err != nil {
		t.Fatalf("encodeEvent: %v", err)
	}
	decoded, err := decodeEvent(encoded)
	if err != nil {
		t.Fatalf("decodeEvent: %v", err)
	}
	if decoded.ID != orig.ID || decoded.Type != orig.Type || decoded.Sender != orig.Sender ||
		decoded.Receiver != orig.Receiver || decoded.Msg != orig.Msg || decoded.Meta["key"] != "value" {
		t.Fatalf("round trip mismatch: got %+v, want %+v", decoded, orig)
	}
}

func TestDecodeEventRejectsUnknownTag(t *testing.T) {
	if _, err := decodeEvent([]byte{0x02, 'x'}); err == nil {
		t.Fatal("expected error for unknown tag")
	}
}

func TestDecodeEventRejectsEmpty(t *testing.T) {
	if _, err := decodeEvent(nil); err == nil {
		t.Fatal("expected error for empty message")
	}
}

func TestEventPromptIncludesFields(t *testing.T) {
	ev := Event{
		ID:     "evt-1",
		Type:   "deploy",
		Sender: "node-a",
		Msg:    "ship it",
		Meta:   map[string]string{"branch": "main"},
	}
	prompt := EventPrompt(ev)
	for _, want := range []string{"evt-1", "deploy", "node-a", "ship it", "branch", "main"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt %q does not contain %q", prompt, want)
		}
	}
}

func TestEventPromptBroadcastVsDirected(t *testing.T) {
	broadcast := EventPrompt(Event{Sender: "node-a"})
	if !strings.Contains(broadcast, "broadcast") {
		t.Fatalf("expected broadcast marker, got %q", broadcast)
	}
	directed := EventPrompt(Event{Sender: "node-a", Receiver: "node-b"})
	if !strings.Contains(directed, "directed to node-b") {
		t.Fatalf("expected directed marker, got %q", directed)
	}
}

func TestNewDisabled(t *testing.T) {
	ev, err := New(Config{Enabled: false})
	if err != nil {
		t.Fatalf("New disabled: %v", err)
	}
	if ev.Enabled() {
		t.Fatal("expected disabled events")
	}
	if err := ev.Close(); err != nil {
		t.Fatalf("Close disabled: %v", err)
	}
}

func TestEventEmpty(t *testing.T) {
	tests := []struct {
		name string
		ev   Event
		want bool
	}{
		{"all empty", Event{}, true},
		{"only id and sender", Event{ID: "1", Sender: "a"}, true},
		{"has type", Event{Type: "x"}, false},
		{"has msg", Event{Msg: "hi"}, false},
		{"has meta", Event{Meta: map[string]string{"k": "v"}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.ev.Empty(); got != tt.want {
				t.Fatalf("Empty() = %v, want %v", got, tt.want)
			}
		})
	}
}
