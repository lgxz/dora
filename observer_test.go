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
		UpdateMessageAdded,
		UpdateToolStarted,
		UpdateMessageAdded,
		UpdateThinking,
		UpdateMessageAdded,
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("update kinds = %#v, want %#v", kinds, want)
	}
	if updates[2].ToolCall.Name != "echo" || updates[3].Message.Content != "hello" {
		t.Fatalf("updates = %#v", updates)
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
