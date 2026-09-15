package cli

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/lgxz/dora"
)

const turnTraceSchemaVersion = 1

// turnTrace is a provider-neutral, one-shot representation of the parent
// Turn. The Harbor adapter converts this private interchange format to ATIF.
// Tool input is kept as text so a malformed provider payload cannot prevent
// the rest of a failed turn from being recorded.
type turnTrace struct {
	SchemaVersion int          `json:"schema_version"`
	System        string       `json:"system,omitempty"`
	User          string       `json:"user"`
	Rounds        []traceRound `json:"rounds"`
	Final         *traceFinal  `json:"final,omitempty"`
}

type traceRound struct {
	Assistant traceAssistant    `json:"assistant"`
	Tools     []traceToolResult `json:"tools"`
	Usage     *dora.Usage       `json:"usage,omitempty"`
}

type traceAssistant struct {
	Content   string          `json:"content,omitempty"`
	Reasoning string          `json:"reasoning,omitempty"`
	ToolCalls []traceToolCall `json:"tool_calls"`
}

type traceToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"`
}

type traceToolResult struct {
	ToolCallID string       `json:"tool_call_id"`
	Content    string       `json:"content,omitempty"`
	Images     []dora.Image `json:"images,omitempty"`
}

type traceFinal struct {
	Content string      `json:"content"`
	Usage   *dora.Usage `json:"usage,omitempty"`
}

func newTurnTrace(turn *dora.Turn) *turnTrace {
	if turn == nil {
		return nil
	}
	trace := &turnTrace{
		SchemaVersion: turnTraceSchemaVersion,
		System:        turn.System(),
		User:          turn.User(),
		Rounds:        make([]traceRound, 0, len(turn.Rounds())),
	}
	for _, round := range turn.Rounds() {
		converted := traceRound{
			Assistant: traceAssistant{
				Content:   round.Assistant.Content,
				Reasoning: round.Assistant.Reasoning,
				ToolCalls: make([]traceToolCall, len(round.Assistant.ToolCalls)),
			},
			Tools: make([]traceToolResult, len(round.Tools)),
			Usage: round.Usage,
		}
		for i, call := range round.Assistant.ToolCalls {
			converted.Assistant.ToolCalls[i] = traceToolCall{
				ID: call.ID, Name: call.Name, Input: string(call.Input),
			}
		}
		for i, result := range round.Tools {
			converted.Tools[i] = traceToolResult{
				ToolCallID: result.ToolCallID,
				Content:    result.Content,
				Images:     result.Images,
			}
		}
		trace.Rounds = append(trace.Rounds, converted)
	}
	if result, ok := turn.Result(); ok {
		trace.Final = &traceFinal{Content: result, Usage: turn.Usage()}
	}
	return trace
}

func writeTraceFile(path string, turn *dora.Turn) error {
	if path == "" {
		return nil
	}
	encoded, err := json.MarshalIndent(newTurnTrace(turn), "", "  ")
	if err != nil {
		return fmt.Errorf("encode trace: %w", err)
	}
	encoded = append(encoded, '\n')

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("open trace file: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("set trace file permissions: %w", err)
	}
	if _, err := file.Write(encoded); err != nil {
		_ = file.Close()
		return fmt.Errorf("write trace file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close trace file: %w", err)
	}
	return nil
}
