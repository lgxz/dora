package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lgxz/dora"
)

// SendTool lets the model send an event back into the cluster. The source node
// name and an ID are filled in by the Events facade when absent.
type SendTool struct {
	sender Sender
}

// Sender is the minimal send capability the tool needs. It fills defaults and
// returns the finalized event.
type Sender interface {
	Send(ev Event) (Event, error)
}

// NewSendTool creates a send_event tool backed by sender.
func NewSendTool(sender Sender) (*SendTool, error) {
	if sender == nil {
		return nil, errors.New("events: sender is nil")
	}
	return &SendTool{sender: sender}, nil
}

// Spec implements dora.Tool.
func (t *SendTool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "send_event",
		Description: "Send an event into the cluster. Leave receiver empty to broadcast to all nodes, or set it to a node name for a directed event. The source node and an ID are filled in automatically.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "type": {"type": "string", "description": "Event category (for example reply, notify)."},
    "msg": {"type": "string", "description": "Human-readable message body."},
    "receiver": {"type": "string", "description": "Target node name. Empty broadcasts to all nodes."},
    "meta": {"type": "object", "additionalProperties": {"type": "string"}, "description": "Optional key/value payload."}
  },
  "required": ["type"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *SendTool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	if t == nil || t.sender == nil {
		return dora.ToolResult{}, errors.New("events: send tool is not initialized")
	}
	input, err := decodeSendInput(raw)
	if err != nil {
		return dora.ToolResult{}, err
	}

	ev, err := t.sender.Send(Event{
		Type:     input.Type,
		Receiver: input.Receiver,
		Msg:      input.Msg,
		Meta:     input.Meta,
	})
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("events: send: %w", err)
	}

	result, err := json.Marshal(map[string]string{
		"id":     ev.ID,
		"status": "sent",
	})
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("events: encode result: %w", err)
	}
	return dora.ToolResult{Content: string(result)}, nil
}

type sendInput struct {
	Type     string            `json:"type"`
	Receiver string            `json:"receiver,omitempty"`
	Msg      string            `json:"msg,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}

func decodeSendInput(raw json.RawMessage) (sendInput, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value sendInput
	if err := decoder.Decode(&value); err != nil {
		return sendInput{}, fmt.Errorf("events: decode input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return sendInput{}, errors.New("events: input must contain one JSON value")
		}
		return sendInput{}, fmt.Errorf("events: decode input: %w", err)
	}
	if value.Type == "" {
		return sendInput{}, errors.New("events: type is required")
	}
	return value, nil
}

var _ dora.Tool = (*SendTool)(nil)
