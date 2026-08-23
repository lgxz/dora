package events

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeSender struct {
	got Event
}

func (f *fakeSender) Send(ev Event) (Event, error) {
	if ev.ID == "" {
		ev.ID = "evt-fake"
	}
	if ev.Sender == "" {
		ev.Sender = "fake"
	}
	f.got = ev
	return ev, nil
}

func TestSendToolBuildsEventAndReportsID(t *testing.T) {
	f := &fakeSender{}
	tool, err := NewSendTool(f)
	if err != nil {
		t.Fatalf("NewSendTool: %v", err)
	}

	spec := tool.Spec()
	if spec.Name != "send_event" {
		t.Fatalf("spec name = %q", spec.Name)
	}

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"type":"reply","msg":"done","receiver":"node-b"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if f.got.Type != "reply" || f.got.Msg != "done" || f.got.Receiver != "node-b" {
		t.Fatalf("sent event = %+v", f.got)
	}
	var reported map[string]string
	if err := json.Unmarshal([]byte(result.Content), &reported); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if reported["id"] == "" || reported["status"] != "sent" {
		t.Fatalf("result = %+v", reported)
	}
}

func TestSendToolRequiresType(t *testing.T) {
	tool, err := NewSendTool(&fakeSender{})
	if err != nil {
		t.Fatalf("NewSendTool: %v", err)
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"msg":"no type"}`)); err == nil {
		t.Fatal("expected error for missing type")
	}
}

func TestNewEventIDIsUnique(t *testing.T) {
	a := newEventID()
	b := newEventID()
	if a == "" || b == "" {
		t.Fatal("newEventID returned empty")
	}
	if a == b {
		t.Fatalf("expected distinct IDs, got %q twice", a)
	}
}
