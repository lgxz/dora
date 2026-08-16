package history

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

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
