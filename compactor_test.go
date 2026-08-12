package dora

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCompactMessagesKeepsAllWhenFewerRounds(t *testing.T) {
	history := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleTool, ToolCallID: "c1", Content: "t1"},
		{Role: RoleAssistant, Content: "a2"},
	}
	got := compactMessages(history, 16)
	if len(got) != len(history) {
		t.Fatalf("len = %d, want %d", len(got), len(history))
	}
}

func TestCompactMessagesKeepsRecentRounds(t *testing.T) {
	history := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleTool, ToolCallID: "c1", Content: "t1"},
		{Role: RoleAssistant, Content: "a2"},
		{Role: RoleTool, ToolCallID: "c2", Content: "t2"},
		{Role: RoleAssistant, Content: "a3"},
	}
	got := compactMessages(history, 2)
	// Keep system + user + last 2 rounds (a2/t2 and a3).
	want := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a2"},
		{Role: RoleTool, ToolCallID: "c2", Content: "t2"},
		{Role: RoleAssistant, Content: "a3"},
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d; got %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Role != want[i].Role || got[i].Content != want[i].Content {
			t.Fatalf("message %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestCompactMessagesKeepsRoundIntact(t *testing.T) {
	// A round with multiple tool messages must never be split.
	history := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1", ToolCalls: []ToolCall{{ID: "c1"}, {ID: "c2"}}},
		{Role: RoleTool, ToolCallID: "c1", Content: "t1"},
		{Role: RoleTool, ToolCallID: "c2", Content: "t2"},
		{Role: RoleAssistant, Content: "a2"},
	}
	got := compactMessages(history, 1)
	// Keep user + last round (a2). The a1 round (with its tools) is dropped.
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2; got %#v", len(got), got)
	}
	if got[0].Role != RoleUser || got[1].Role != RoleAssistant || got[1].Content != "a2" {
		t.Fatalf("got %#v", got)
	}
}

func TestCompactMessagesNoAssistantKeepsAll(t *testing.T) {
	history := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "u1"},
	}
	got := compactMessages(history, 2)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestCompactMessagesZeroKeepsAll(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: "u1"}}
	got := compactMessages(history, 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
}

func TestAgentRequestMessagesUsesCompaction(t *testing.T) {
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "c1", Name: "snap", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			// The last round (assistant a1 with its tool call and tool result)
			// plus the leading user message should be present when
			// MaxHistoryRounds is 1.
			if len(request.Messages) != 3 {
				t.Fatalf("message count = %d, want 3; got %#v", len(request.Messages), request.Messages)
			}
			if request.Messages[0].Role != RoleUser ||
				request.Messages[1].Role != RoleAssistant ||
				request.Messages[2].Role != RoleTool {
				t.Fatalf("messages = %#v", request.Messages)
			}
			return Response{Content: "done"}, nil
		default:
			t.Fatal("model called too many times")
			return Response{}, nil
		}
	})
	tool := stubTool{
		spec: ToolSpec{Name: "snap"},
		execute: func(context.Context, json.RawMessage) (string, error) {
			return "ok", nil
		},
	}
	agent, err := NewWithConfig(model, AgentConfig{MaxHistoryRounds: 1}, tool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.Run(context.Background(), []Message{{Role: RoleUser, Content: "u1"}}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}