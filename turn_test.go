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
		{FinishReason: FinishToolCalls, ToolCalls: []ToolCall{{ID: "call-1", Name: "echo", Input: json.RawMessage(`{}`)}}, Usage: &Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}},
		{FinishReason: FinishStop, Content: "done", Usage: &Usage{InputTokens: 15, OutputTokens: 3, TotalTokens: 18}},
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

func TestTurnPlacesContextInFrontOfUserInput(t *testing.T) {
	turn := NewTurn("do the work")
	if err := turn.bindSystem("system"); err != nil {
		t.Fatal(err)
	}
	if err := turn.bindContext("<environment>host</environment>\n\n<run_context>today</run_context>"); err != nil {
		t.Fatal(err)
	}
	if err := turn.bindContext("<environment>host</environment>\n\n<run_context>today</run_context>"); err != nil {
		t.Fatalf("rebinding the same context: %v", err)
	}
	if err := turn.bindContext("<run_context>tomorrow</run_context>"); err == nil {
		t.Fatal("expected a different context to be rejected")
	}

	messages := turn.Messages()
	want := "<environment>host</environment>\n\n<run_context>today</run_context>\n\ndo the work"
	if len(messages) != 2 || messages[0].Role != RoleSystem || messages[0].Content != "system" ||
		messages[1].Role != RoleUser || messages[1].Content != want {
		t.Fatalf("messages = %#v", messages)
	}
	// The preamble is not part of what the user typed, so persistence and
	// history keep the input alone.
	if turn.User() != "do the work" || turn.Context() == "" {
		t.Fatalf("user = %q, context = %q", turn.User(), turn.Context())
	}
}

func TestTurnWithoutContextKeepsUserInputUnchanged(t *testing.T) {
	turn := NewTurn("user")
	if err := turn.bindContext(""); err != nil {
		t.Fatal(err)
	}
	messages := turn.Messages()
	if len(messages) != 1 || messages[0].Content != "user" {
		t.Fatalf("messages = %#v", messages)
	}
}

func TestAgentBuildsContextFromAgentAndRun(t *testing.T) {
	var requests []Request
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		requests = append(requests, request)
		return Response{FinishReason: FinishStop, Content: "done"}, nil
	})
	agent, err := NewWithConfig(model, AgentConfig{SystemPrompt: "system", Context: "  <environment>host</environment>  "})
	if err != nil {
		t.Fatal(err)
	}

	turn := NewTurn("task")
	if err := agent.RunObservedWithOptions(context.Background(), turn, nil, RunOptions{Context: "<run_context>today</run_context>"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || len(requests[0].Messages) != 2 {
		t.Fatalf("requests = %#v", requests)
	}
	if got := requests[0].Messages[0].Content; got != "system" {
		t.Fatalf("system prompt = %q; context must not leak into it", got)
	}
	want := "<environment>host</environment>\n\n<run_context>today</run_context>\n\ntask"
	if got := requests[0].Messages[1].Content; got != want {
		t.Fatalf("first user message = %q, want %q", got, want)
	}

	// A run without any context sends the input alone.
	plain, err := New(model)
	if err != nil {
		t.Fatal(err)
	}
	if err := plain.RunObservedWithOptions(context.Background(), NewTurn("task"), nil, RunOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := requests[1].Messages[0].Content; got != "task" {
		t.Fatalf("plain first message = %q", got)
	}
}
