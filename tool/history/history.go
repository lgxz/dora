// Package history exposes saved turns from a session to the model.
package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/capstr"
	"github.com/lgxz/dora/session"
)

const (
	defaultListLimit  = 10
	defaultRoundLimit = 5
	maxLimit          = 50
	// maxResultBytes bounds the marshaled list/get payload. A page of saved
	// turns can embed arbitrarily large user prompts, final responses, and
	// tool results; without a cap one history call could overflow the model
	// request the same way an oversized command output does.
	maxResultBytes   = 32 << 10
	resultHeadBytes  = 24 << 10
	resultMarkerText = "\n... [history result truncated: %d bytes total; narrow the query with a smaller limit or an offset] ...\n"
)

// Tool lets the model explicitly inspect saved turns. Previous turns are
// never inserted into the current model context automatically.
type Tool struct {
	reader session.Reader
}

// New creates a history tool backed by reader.
func New(reader session.Reader) (*Tool, error) {
	if reader == nil {
		return nil, errors.New("history: reader is nil")
	}
	return &Tool{reader: reader}, nil
}

// Spec implements dora.Tool.
func (t *Tool) Spec() dora.ToolSpec {
	return dora.ToolSpec{
		Name:        "history",
		Description: "Inspect saved turns and model usage in this session. Earlier turns are not in the current context; use list to find turn IDs, status, errors, and final-response usage, then get to read tool rounds and their usage. In get results, tool-call input is argument text encoded as a string; input_bytes is a base64 fallback for invalid UTF-8.",
		InputSchema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "action": {"type": "string", "enum": ["list", "get"]},
    "turn_id": {"type": "integer", "minimum": 1, "description": "Required for get."},
    "offset": {"type": "integer", "minimum": 0, "description": "Turn offset for list (newest first), or round offset for get (chronological)."},
    "limit": {"type": "integer", "minimum": 1, "maximum": 50}
  },
  "required": ["action"],
  "additionalProperties": false
}`),
	}
}

// Execute implements dora.Tool.
func (t *Tool) Execute(ctx context.Context, raw json.RawMessage) (dora.ToolResult, error) {
	if t == nil || t.reader == nil {
		return dora.ToolResult{}, errors.New("history: tool is not initialized")
	}
	input, err := decodeInput(raw)
	if err != nil {
		return dora.ToolResult{}, err
	}

	var value any
	switch input.Action {
	case "list":
		limit := defaultListLimit
		if input.Limit != nil {
			limit = *input.Limit
		}
		value, err = t.reader.ListTurns(ctx, session.ListOptions{Offset: input.Offset, Limit: limit})
	case "get":
		if input.TurnID == nil {
			return dora.ToolResult{}, errors.New("history: turn_id is required for get")
		}
		limit := defaultRoundLimit
		if input.Limit != nil {
			limit = *input.Limit
		}
		var page session.RoundPage
		page, err = t.reader.GetRounds(ctx, *input.TurnID, session.RoundOptions{Offset: input.Offset, Limit: limit})
		value = encodeRoundPage(page)
	default:
		return dora.ToolResult{}, fmt.Errorf("history: unknown action %q", input.Action)
	}
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("history: %s: %w", input.Action, err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return dora.ToolResult{}, fmt.Errorf("history: encode result: %w", err)
	}
	return dora.ToolResult{Content: capstr.HeadTail(string(encoded), maxResultBytes, resultHeadBytes, resultMarkerText)}, nil
}

type input struct {
	Action string `json:"action"`
	TurnID *int64 `json:"turn_id,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
}

func decodeInput(raw json.RawMessage) (input, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var value input
	if err := decoder.Decode(&value); err != nil {
		return input{}, fmt.Errorf("history: decode input: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return input{}, errors.New("history: input must contain one JSON value")
		}
		return input{}, fmt.Errorf("history: decode input: %w", err)
	}
	if value.Action == "" {
		return input{}, errors.New("history: action is required")
	}
	if value.Offset < 0 {
		return input{}, errors.New("history: offset must be non-negative")
	}
	if value.Limit != nil && (*value.Limit <= 0 || *value.Limit > maxLimit) {
		return input{}, fmt.Errorf("history: limit must be between 1 and %d", maxLimit)
	}
	if value.TurnID != nil && *value.TurnID <= 0 {
		return input{}, errors.New("history: turn_id must be positive")
	}
	return value, nil
}

var _ dora.Tool = (*Tool)(nil)

// History presents arguments as text rather than embedding untrusted JSON.
// Keep this representation at the tool boundary; model adapters and session
// readers still receive the original argument bytes.
type historyToolCall struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Input      string `json:"input"`
	InputBytes []byte `json:"input_bytes,omitempty"` // lossless fallback for invalid UTF-8
}

type historyMessage struct {
	dora.Message
	ToolCalls []historyToolCall `json:"tool_calls,omitempty"`
}

type historyRound struct {
	Assistant historyMessage   `json:"assistant"`
	Tools     []historyMessage `json:"tools"`
	Usage     *dora.Usage      `json:"usage,omitempty"`
}

type historyRoundPage struct {
	Total  int            `json:"total"`
	Offset int            `json:"offset"`
	Limit  int            `json:"limit"`
	Rounds []historyRound `json:"rounds"`
}

func encodeHistoryMessage(message dora.Message) historyMessage {
	result := historyMessage{Message: message}
	for _, call := range message.ToolCalls {
		record := historyToolCall{ID: call.ID, Name: call.Name, Input: string(call.Input)}
		if !utf8.Valid(call.Input) {
			record.InputBytes = []byte(call.Input)
		}
		result.ToolCalls = append(result.ToolCalls, record)
	}
	return result
}

func encodeRoundPage(page session.RoundPage) historyRoundPage {
	result := historyRoundPage{Total: page.Total, Offset: page.Offset, Limit: page.Limit}
	if page.Rounds != nil {
		result.Rounds = make([]historyRound, len(page.Rounds))
	}
	for i, round := range page.Rounds {
		item := historyRound{Assistant: encodeHistoryMessage(round.Assistant), Usage: round.Usage}
		if round.Tools != nil {
			item.Tools = make([]historyMessage, len(round.Tools))
		}
		for j, message := range round.Tools {
			item.Tools[j] = encodeHistoryMessage(message)
		}
		result.Rounds[i] = item
	}
	return result
}
