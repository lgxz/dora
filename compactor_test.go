package dora

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCompactMessagesKeepsAllWhenFewerRounds(t *testing.T) {
	history := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleTool, ToolCallID: "c1", Content: "t1"},
		{Role: RoleAssistant, Content: "a2"},
	}
	got := compactMessages(history, 16, 0)
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
	got := compactMessages(history, 2, 0)
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
	got := compactMessages(history, 1, 0)
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
	got := compactMessages(history, 2, 0)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
}

func TestCompactMessagesZeroKeepsAll(t *testing.T) {
	history := []Message{{Role: RoleUser, Content: "u1"}}
	got := compactMessages(history, 0, 0)
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

func TestCompactRoundDropsImagesAndTruncates(t *testing.T) {
	round := []Message{
		{Role: RoleAssistant, Content: "a", ToolCalls: []ToolCall{{ID: "c1", Input: json.RawMessage(`{"data":"` + strings.Repeat("x", 100) + `"}`)}}},
		{Role: RoleTool, ToolCallID: "c1", Content: strings.Repeat("y", 100), Images: []Image{{Path: "/tmp/a.png"}}},
	}
	got := compactRound(round, 20)
	// Images are dropped.
	if len(got[1].Images) != 0 {
		t.Fatalf("images = %#v", got[1].Images)
	}
	// Content is truncated.
	if len(got[1].Content) >= 100 {
		t.Fatalf("content = %q", got[1].Content)
	}
	// Tool call input is compacted but stays valid JSON.
	var decoded map[string]string
	if err := json.Unmarshal(got[0].ToolCalls[0].Input, &decoded); err != nil {
		t.Fatalf("input not valid JSON: %v", err)
	}
	if len(decoded["data"]) >= 100 {
		t.Fatalf("data = %q", decoded["data"])
	}
}

func TestTruncateStringKeepsBeginningAndEnd(t *testing.T) {
	s := strings.Repeat("a", 50) + strings.Repeat("b", 50)
	got := truncateString(s, 20)
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) {
		t.Fatalf("missing beginning: %q", got)
	}
	if !strings.HasSuffix(got, strings.Repeat("b", 10)) {
		t.Fatalf("missing end: %q", got)
	}
	if !strings.Contains(got, "[truncated]") {
		t.Fatalf("missing marker: %q", got)
	}
}

func TestTruncateStringShortUnchanged(t *testing.T) {
	s := "short"
	if got := truncateString(s, 100); got != s {
		t.Fatalf("got %q", got)
	}
}

func TestTruncateStringUTF8Safe(t *testing.T) {
	s := strings.Repeat("中", 100)
	got := truncateString(s, 20)
	// Must not contain invalid UTF-8.
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8: %q", got)
	}
}

func TestCompactMessagesBudgetTruncatesHistorical(t *testing.T) {
	// A small contextWindow forces historical rounds to be truncated while the
	// current round stays intact.
	history := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleTool, ToolCallID: "c1", Content: strings.Repeat("y", 100)},
		{Role: RoleAssistant, Content: "a2"},
	}
	got := compactMessages(history, 2, 50)
	// Current round (a2) is unchanged.
	last := got[len(got)-1]
	if last.Content != "a2" {
		t.Fatalf("current round = %q", last.Content)
	}
	// Historical tool message is truncated.
	toolMsg := got[2]
	if len(toolMsg.Content) >= 100 {
		t.Fatalf("historical content = %q", toolMsg.Content)
	}
}

func TestCompactMessagesBudgetExhaustedKeepsCurrentOnly(t *testing.T) {
	// When the current round alone exceeds the budget, only it is kept.
	history := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleTool, ToolCallID: "c1", Content: "t1"},
		{Role: RoleAssistant, Content: strings.Repeat("z", 100)},
	}
	got := compactMessages(history, 2, 50)
	// Keep user + current round (a2).
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2; got %#v", len(got), got)
	}
	if got[0].Role != RoleUser || got[1].Role != RoleAssistant || got[1].Content != strings.Repeat("z", 100) {
		t.Fatalf("got %#v", got)
	}
}
	func TestCompactRoundCurrentRoundUnchanged(t *testing.T) {
	// The current (last) round is appended unchanged by compactMessages.
	history := []Message{
		{Role: RoleUser, Content: "u1"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleTool, ToolCallID: "c1", Content: strings.Repeat("y", 100), Images: []Image{{Path: "/tmp/a.png"}}},
		{Role: RoleAssistant, Content: "a2", Images: []Image{{Path: "/tmp/b.png"}}},
	}
	got := compactMessages(history, 2, 20)
	// Last round (a2) is current and unchanged.
	last := got[len(got)-1]
	if len(last.Images) != 1 || last.Images[0].Path != "/tmp/b.png" {
		t.Fatalf("current round images = %#v", last.Images)
	}
	// Earlier round (a1/tool) is compressed: images dropped, content truncated.
	toolMsg := got[2]
	if len(toolMsg.Images) != 0 {
		t.Fatalf("historical images = %#v", toolMsg.Images)
	}
	if len(toolMsg.Content) >= 100 {
		t.Fatalf("historical content = %q", toolMsg.Content)
	}
}