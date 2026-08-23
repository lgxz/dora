package dora

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestRunObservedReportsConversationProgress(t *testing.T) {
	var modelCalls int
	model := modelFunc(func(context.Context, Request) (Response, error) {
		modelCalls++
		if modelCalls == 1 {
			return Response{ToolCalls: []ToolCall{{
				ID:    "call-1",
				Name:  "echo",
				Input: json.RawMessage(`{"text":"hello"}`),
			}}}, nil
		}
		return Response{Content: "done"}, nil
	})
	tool := stubTool{
		spec: ToolSpec{Name: "echo"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "hello", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	var updates []Update
	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		updates = append(updates, update)
	}))
	if err != nil {
		t.Fatal(err)
	}

	var kinds []UpdateKind
	for _, update := range updates {
		kinds = append(kinds, update.Kind)
	}
	want := []UpdateKind{
		UpdateThinking,
		UpdateMessageReceived,
		UpdateToolStarted,
		UpdateToolFinished,
		UpdateThinking,
		UpdateMessageReceived,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("update kinds = %#v, want %#v", kinds, want)
	}
	if updates[1].ToolCall.Name != "" || updates[2].ToolCall.Name != "echo" ||
		updates[3].ToolCall.Name != "echo" || updates[3].Message.Content != "hello" {
		t.Fatalf("updates = %#v", updates)
	}
	// Each message-received event follows its round's thinking event, carries
	// the assistant message, and has a nil usage payload (the stub model
	// reports none), without panicking.
	for _, index := range []int{1, 5} {
		if updates[index].Kind != UpdateMessageReceived {
			t.Fatalf("update %d = %#v, want UpdateMessageReceived", index, updates[index])
		}
		if updates[index].Message.Role != RoleAssistant {
			t.Fatalf("update %d message role = %#v, want RoleAssistant", index, updates[index].Message.Role)
		}
		if updates[index].Usage != nil {
			t.Fatalf("update %d usage = %#v, want nil", index, updates[index].Usage)
		}
	}
	// The tool round emits one UpdateToolFinished after its tool starts,
	// carrying the tool result message and no error.
	if updates[3].Err != nil {
		t.Fatalf("update 3 err = %#v, want nil", updates[3].Err)
	}
	if updates[3].Message.Role != RoleTool || updates[3].Message.Content != "hello" {
		t.Fatalf("update 3 message = %#v, want the tool result", updates[3].Message)
	}
}

func TestObserverEventsDoNotIncludeRemovedKinds(t *testing.T) {
	// Regression guard: the removed events UpdateMessageAdded, UpdateToolFailed,
	// and UpdateUsage must never be emitted. The constants no longer compile, so
	// an up-to-date tree cannot reference them; this test additionally fails if
	// the deleted strings are ever re-introduced as kinds.
	model := modelFunc(func(context.Context, Request) (Response, error) {
		return Response{Content: "done"}, nil
	})
	agent, err := New(model)
	if err != nil {
		t.Fatal(err)
	}

	var kinds []UpdateKind
	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		kinds = append(kinds, update.Kind)
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range kinds {
		switch kind {
		case "message_added", "tool_failed", "usage":
			t.Fatalf("observer emitted a removed kind %q", kind)
		}
	}
}

func TestObserverCannotMutateConversation(t *testing.T) {
	modelCalls := 0
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		modelCalls++
		if modelCalls == 1 {
			return Response{ToolCalls: []ToolCall{{
				ID:    "call-1",
				Name:  "echo",
				Input: json.RawMessage(`{"value":1}`),
			}}}, nil
		}
		call := request.Messages[1].ToolCalls[0]
		if call.Name != "echo" || string(call.Input) != `{"value":1}` {
			t.Fatalf("history was mutated: %#v", call)
		}
		return Response{Content: "done"}, nil
	})
	tool := stubTool{
		spec: ToolSpec{Name: "echo"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "ok", nil
		},
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}

	_, err = runAgentObserved(agent, context.Background(), nil, ObserverFunc(func(update Update) {
		if len(update.Message.ToolCalls) > 0 {
			update.Message.ToolCalls[0].Name = "changed"
			update.Message.ToolCalls[0].Input[0] = '['
		}
		if update.ToolCall.Name != "" {
			update.ToolCall.Name = "changed"
			update.ToolCall.Input[0] = '['
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
}
