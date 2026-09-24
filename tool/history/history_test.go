package history

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/session"
	"github.com/lgxz/dora/session/sqlite"
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

func TestHistoryGetsStoredMalformedArguments(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	inputs := [][]byte{[]byte(`{"command":`), []byte(" \n{\"x\": 1}\t"), nil, []byte("\xff")}
	turn := dora.NewTurn("test")
	for _, raw := range inputs {
		if err := turn.AppendRound(dora.Round{
			Assistant: dora.Message{Role: dora.RoleAssistant, Reasoning: "checking", ToolCalls: []dora.ToolCall{{ID: "call", Name: "echo", Input: raw}}},
			Tools:     []dora.Message{{Role: dora.RoleTool, ToolCallID: "call", Content: "invalid arguments"}},
			Usage:     &dora.Usage{TotalTokens: 42},
		}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.CommitFailed(ctx, turn, errors.New("stopped")); err != nil {
		t.Fatal(err)
	}
	tool, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(ctx, json.RawMessage(`{"action":"get","turn_id":1,"limit":4}`))
	if err != nil {
		t.Fatal(err)
	}
	var page historyRoundPage
	if err := json.Unmarshal([]byte(result.Content), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 4 || page.Offset != 0 || page.Limit != 4 || len(page.Rounds) != 4 {
		t.Fatalf("page = %#v", page)
	}
	for i, round := range page.Rounds {
		if len(round.Assistant.ToolCalls) != 1 {
			t.Fatalf("calls = %#v", round.Assistant.ToolCalls)
		}
		call := round.Assistant.ToolCalls[0]
		got := []byte(call.Input)
		if call.InputBytes != nil {
			got = call.InputBytes
		}
		if !bytes.Equal(got, inputs[i]) {
			t.Fatalf("input = %q, want %q", got, inputs[i])
		}
		if call.ID != "call" || call.Name != "echo" || round.Assistant.Reasoning != "checking" || round.Usage.TotalTokens != 42 || round.Tools[0].Content != "invalid arguments" {
			t.Fatalf("round = %#v", round)
		}
	}
	result, err = tool.Execute(ctx, json.RawMessage(`{"action":"list"}`))
	if err != nil {
		t.Fatal(err)
	}
	var listing session.TurnPage
	if err := json.Unmarshal([]byte(result.Content), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.Total != 1 || listing.Turns[0].Status != session.TurnStatusFailed {
		t.Fatalf("listing = %#v", listing)
	}
}

func TestHistoryListsParentRelationsAndKeepsGetFormat(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.OpenMemory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	parent := dora.NewTurn("parent")
	_ = parent.Complete("parent answer", "")
	parentID, err := store.CommitTurn(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	child := dora.NewTurn("child")
	_ = child.Complete("child answer", "")
	childID, err := store.CommitChild(ctx, parentID, child, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(ctx, json.RawMessage(`{"action":"list"}`))
	var list session.TurnPage
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal([]byte(result.Content), &list); err != nil {
		t.Fatal(err)
	}

	if list.Total != 2 || list.Turns[0].ID != childID || list.Turns[0].Result != "child answer" || list.Turns[0].ParentTurnID == nil || *list.Turns[0].ParentTurnID != parentID || list.Turns[1].ParentTurnID != nil {
		t.Fatalf("list=%+v", list)
	}
	for _, id := range []int64{parentID, childID} {
		input, _ := json.Marshal(map[string]any{"action": "get", "turn_id": id})
		result, err = tool.Execute(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		var page historyRoundPage
		if err = json.Unmarshal([]byte(result.Content), &page); err != nil {
			t.Fatal(err)
		}

		var fields map[string]json.RawMessage
		if err = json.Unmarshal([]byte(result.Content), &fields); err != nil {
			t.Fatal(err)
		}
		if len(fields) != 4 || fields["total"] == nil || fields["offset"] == nil || fields["limit"] == nil || fields["rounds"] == nil {
			t.Fatalf("get shape changed: %s", result.Content)
		}
	}
}
