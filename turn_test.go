package dora

import (
	"encoding/json"
	"testing"
)

func TestTurnBuildsMessagesAndDefensivelyCopiesRounds(t *testing.T) {
	turn := NewTurn("system", "user")
	round := Round{
		Assistant: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}}},
		Tools:     []Message{{Role: RoleTool, ToolCallID: "call-1", Content: "hi"}},
	}
	if err := turn.AppendRound(round, "next"); err != nil {
		t.Fatal(err)
	}
	round.Assistant.ToolCalls[0].Name = "changed"
	if err := turn.Complete("done", "final"); err != nil {
		t.Fatal(err)
	}

	messages := turn.Messages()
	if len(messages) != 5 || messages[0].Content != "system" || messages[1].Content != "user" ||
		messages[2].ToolCalls[0].Name != "echo" || messages[3].Content != "hi" || messages[4].Content != "done" {
		t.Fatalf("messages = %#v", messages)
	}
	messages[2].ToolCalls[0].Name = "mutated"
	if turn.Messages()[2].ToolCalls[0].Name != "echo" {
		t.Fatal("Messages returned mutable turn storage")
	}
	if result, ok := turn.Result(); !ok || result != "done" || turn.Continuation() != "final" {
		t.Fatalf("result = %q, ok = %v, continuation = %q", result, ok, turn.Continuation())
	}
}

func TestTurnOmitsEmptySystemMessage(t *testing.T) {
	turn := NewTurn("", "user")
	messages := turn.Messages()
	if len(messages) != 1 || messages[0].Role != RoleUser || messages[0].Content != "user" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestTurnRejectsIncompleteRoundAndMutationAfterCompletion(t *testing.T) {
	turn := NewTurn("system", "user")
	err := turn.AppendRound(Round{Assistant: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1"}}}}, "")
	if err == nil {
		t.Fatal("expected missing tool result error")
	}
	if err := turn.Complete("done", ""); err != nil {
		t.Fatal(err)
	}
	if err := turn.AppendRound(Round{}, ""); err == nil {
		t.Fatal("expected completed turn error")
	}
	if err := turn.Complete("again", ""); err == nil {
		t.Fatal("expected duplicate completion error")
	}
}
