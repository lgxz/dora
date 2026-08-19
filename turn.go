package dora

import (
	"errors"
	"fmt"
)

// Round is one assistant tool-call message followed by the corresponding tool
// result messages in call order.
type Round struct {
	Assistant Message   `json:"assistant"`
	Tools     []Message `json:"tools"`
}

// Turn is one independent Agent run. It starts with an optional system message
// (omitted when empty) and a user message, accumulates complete tool rounds,
// and ends with one final assistant result. A Turn is mutable while it is
// running and cannot be changed after Complete.
type Turn struct {
	system       string
	user         string
	rounds       []Round
	result       string
	continuation string
	completed    bool
}

// NewTurn creates a fresh, independent turn.
func NewTurn(system, user string) *Turn {
	return &Turn{system: system, user: user}
}

// Messages returns a defensive copy of the complete current turn in model
// message order.
func (t *Turn) Messages() []Message {
	if t == nil {
		return nil
	}
	count := 1
	if t.system != "" {
		count++
	}
	for _, round := range t.rounds {
		count += 1 + len(round.Tools)
	}
	if t.completed {
		count++
	}
	messages := make([]Message, 0, count)
	if t.system != "" {
		messages = append(messages, Message{Role: RoleSystem, Content: t.system})
	}
	messages = append(messages, Message{Role: RoleUser, Content: t.user})
	for _, round := range t.rounds {
		messages = append(messages, cloneMessage(round.Assistant))
		messages = append(messages, cloneMessages(round.Tools)...)
	}
	if t.completed {
		messages = append(messages, Message{Role: RoleAssistant, Content: t.result})
	}
	return messages
}

// Rounds returns defensive copies of the completed tool rounds.
func (t *Turn) Rounds() []Round {
	if t == nil || t.rounds == nil {
		return nil
	}
	rounds := make([]Round, len(t.rounds))
	for i, round := range t.rounds {
		rounds[i] = cloneRound(round)
	}
	return rounds
}

// Continuation returns the opaque provider state for the next model call in
// this turn.
func (t *Turn) Continuation() string {
	if t == nil {
		return ""
	}
	return t.continuation
}

// AppendRound appends one complete assistant/tool round.
func (t *Turn) AppendRound(round Round, continuation string) error {
	if t == nil {
		return errors.New("dora: turn is nil")
	}
	if t.completed {
		return errors.New("dora: turn is already complete")
	}
	if err := validateRound(round); err != nil {
		return err
	}
	t.rounds = append(t.rounds, cloneRound(round))
	t.continuation = continuation
	return nil
}

// Complete records the final assistant text. The final response has no tool
// calls, so only its text is retained.
func (t *Turn) Complete(result, continuation string) error {
	if t == nil {
		return errors.New("dora: turn is nil")
	}
	if t.completed {
		return errors.New("dora: turn is already complete")
	}
	t.result = result
	t.continuation = continuation
	t.completed = true
	return nil
}

// Completed reports whether the turn has a final assistant result.
func (t *Turn) Completed() bool { return t != nil && t.completed }

// System returns the turn's system prompt.
func (t *Turn) System() string {
	if t == nil {
		return ""
	}
	return t.system
}

// User returns the turn's user input.
func (t *Turn) User() string {
	if t == nil {
		return ""
	}
	return t.user
}

// Result returns the final assistant text when the turn is complete.
func (t *Turn) Result() (string, bool) {
	if t == nil || !t.completed {
		return "", false
	}
	return t.result, true
}

func validateRound(round Round) error {
	if round.Assistant.Role != RoleAssistant {
		return fmt.Errorf("dora: round assistant has role %q", round.Assistant.Role)
	}
	if len(round.Assistant.ToolCalls) == 0 {
		return errors.New("dora: round assistant has no tool calls")
	}
	if len(round.Tools) != len(round.Assistant.ToolCalls) {
		return fmt.Errorf("dora: round has %d tool results for %d tool calls", len(round.Tools), len(round.Assistant.ToolCalls))
	}
	for i, message := range round.Tools {
		if message.Role != RoleTool {
			return fmt.Errorf("dora: round tool result %d has role %q", i, message.Role)
		}
		if message.ToolCallID != round.Assistant.ToolCalls[i].ID {
			return fmt.Errorf("dora: round tool result %d has call ID %q, want %q", i, message.ToolCallID, round.Assistant.ToolCalls[i].ID)
		}
	}
	return nil
}

func cloneRound(round Round) Round {
	return Round{
		Assistant: cloneMessage(round.Assistant),
		Tools:     cloneMessages(round.Tools),
	}
}
