package dora

import (
	"context"
	"encoding/json"
	"testing"
)

func TestTurnBuildsMessagesAndDefensivelyCopiesRounds(t *testing.T) {
	turn := NewTurn("user")
	if err := turn.bindSystem("system"); err != nil {
		t.Fatal(err)
	}
	cached := int64(3)
	roundUsage := &Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12, InputDetails: &InputTokenDetails{CachedTokens: &cached}}
	round := Round{
		Assistant: Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}}},
		Tools:     []Message{{Role: RoleTool, ToolCallID: "call-1", Content: "hi"}},
		Usage:     roundUsage,
	}
	if err := turn.AppendRound(round, "next"); err != nil {
		t.Fatal(err)
	}
	round.Assistant.ToolCalls[0].Name = "changed"
	*roundUsage.InputDetails.CachedTokens = 99
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
	storedRounds := turn.Rounds()
	if storedRounds[0].Usage == roundUsage || storedRounds[0].Usage.InputDetails.CachedTokens == roundUsage.InputDetails.CachedTokens || *storedRounds[0].Usage.InputDetails.CachedTokens != 3 {
		t.Fatalf("round usage was not defensively copied: %#v", storedRounds[0].Usage)
	}
	if result, ok := turn.Result(); !ok || result != "done" || turn.Continuation() != "final" {
		t.Fatalf("result = %q, ok = %v, continuation = %q", result, ok, turn.Continuation())
	}
}

func TestAgentStoresRoundAndFinalUsage(t *testing.T) {
	responses := []Response{
		{ToolCalls: []ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{}`)}}, Usage: &Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}},
		{Content: "done", Usage: &Usage{InputTokens: 15, OutputTokens: 3, TotalTokens: 18}},
	}
	model := modelFunc(func(context.Context, Request) (Response, error) {
		response := responses[0]
		responses = responses[1:]
		return response, nil
	})
	tool := stubTool{
		spec:    ToolSpec{Name: "echo"},
		execute: func(context.Context, json.RawMessage) (string, error) { return "ok", nil },
	}
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	turn := NewTurn("user")
	if err := agent.Run(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if rounds := turn.Rounds(); len(rounds) != 1 || rounds[0].Usage == nil || rounds[0].Usage.TotalTokens != 12 {
		t.Fatalf("rounds = %#v", rounds)
	}
	usage := turn.Usage()
	if usage == nil || usage.TotalTokens != 18 {
		t.Fatalf("final usage = %#v", usage)
	}
	usage.TotalTokens = 99
	if turn.Usage().TotalTokens != 18 {
		t.Fatal("Usage returned mutable turn storage")
	}
}

func TestTurnOmitsEmptySystemMessage(t *testing.T) {
	turn := NewTurn("user")
	if err := turn.bindSystem(""); err != nil {
		t.Fatal(err)
	}
	messages := turn.Messages()
	if len(messages) != 1 || messages[0].Role != RoleUser || messages[0].Content != "user" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestTurnRejectsIncompleteRoundAndMutationAfterCompletion(t *testing.T) {
	turn := NewTurn("user")
	if err := turn.bindSystem("system"); err != nil {
		t.Fatal(err)
	}
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

func TestTurnRejectsDifferentSystemPromptBinding(t *testing.T) {
	turn := NewTurn("user")
	if err := turn.bindSystem("first"); err != nil {
		t.Fatal(err)
	}
	if err := turn.bindSystem("first"); err != nil {
		t.Fatalf("rebinding the same system prompt: %v", err)
	}
	if err := turn.bindSystem("second"); err == nil {
		t.Fatal("expected a different system prompt to be rejected")
	}
}
