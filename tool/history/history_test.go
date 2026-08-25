package history

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/session"
)

type readerStub struct {
	listOptions session.ListOptions
	getID       int64
	getOptions  session.RoundOptions
}

func (reader *readerStub) ListTurns(_ context.Context, options session.ListOptions) (session.TurnPage, error) {
	reader.listOptions = options
	return session.TurnPage{Total: 3, Offset: options.Offset, Limit: options.Limit}, nil
}

func (reader *readerStub) GetRounds(_ context.Context, id int64, options session.RoundOptions) (session.RoundPage, error) {
	reader.getID, reader.getOptions = id, options
	return session.RoundPage{Total: 7, Offset: options.Offset, Limit: options.Limit}, nil
}

func TestHistoryListAndGetPagination(t *testing.T) {
	reader := &readerStub{}
	tool, err := New(reader)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), json.RawMessage(`{"action":"list","offset":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if reader.listOptions != (session.ListOptions{Offset: 2, Limit: defaultListLimit}) || !strings.Contains(result.Content, `"total":3`) {
		t.Fatalf("options = %#v, result = %s", reader.listOptions, result.Content)
	}
	result, err = tool.Execute(context.Background(), json.RawMessage(`{"action":"get","turn_id":4,"offset":1,"limit":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if reader.getID != 4 || reader.getOptions != (session.RoundOptions{Offset: 1, Limit: 2}) || !strings.Contains(result.Content, `"total":7`) {
		t.Fatalf("id = %d, options = %#v, result = %s", reader.getID, reader.getOptions, result.Content)
	}
}

func TestHistoryValidatesInput(t *testing.T) {
	tool, err := New(&readerStub{})
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{
		`{"action":"get"}`,
		`{"action":"list","limit":0}`,
		`{"action":"list","unknown":true}`,
		`{"action":"delete"}`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("input %s succeeded", raw)
		}
	}
}

func TestHistoryCapsLargeResults(t *testing.T) {
	big := strings.Repeat("x", 200_000)
	reader := &bigReaderStub{content: big}
	tool, err := New(reader)
	if err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{
		`{"action":"get","turn_id":1,"limit":50}`,
		`{"action":"list","limit":50}`,
	} {
		result, err := tool.Execute(context.Background(), json.RawMessage(raw))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Content) > maxResultBytes+256 {
			t.Fatalf("%s result not capped: %d bytes", raw, len(result.Content))
		}
		if !strings.Contains(result.Content, "history result truncated") {
			t.Fatalf("%s result missing truncation marker", raw)
		}
		if !strings.HasPrefix(result.Content, `{"total":`) {
			t.Fatalf("%s result head not preserved: %q", raw, result.Content[:20])
		}
	}
}

type bigReaderStub struct {
	content string
}

func (reader *bigReaderStub) ListTurns(_ context.Context, _ session.ListOptions) (session.TurnPage, error) {
	return session.TurnPage{
		Total: 1,
		Turns: []session.TurnSummary{{ID: 1, User: reader.content, Result: reader.content}},
	}, nil
}

func (reader *bigReaderStub) GetRounds(_ context.Context, _ int64, _ session.RoundOptions) (session.RoundPage, error) {
	return session.RoundPage{
		Total: 1,
		Rounds: []dora.Round{{
			Assistant: dora.Message{Role: dora.RoleAssistant, Content: reader.content},
		}},
	}, nil
}
