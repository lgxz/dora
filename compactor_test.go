package dora

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
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

func TestRequestMessagesWithinBudgetIsUnchanged(t *testing.T) {
	// Build more than maxRetainedRounds rounds but keep them tiny so the whole
	// history fits within the budget; compaction must not drop or compress
	// anything, even though the round count exceeds the old fixed 32 threshold.
	history := []Message{{Role: RoleSystem, Content: "sys"}}
	history = append(history, Message{Role: RoleUser, Content: "u0"})
	for i := 0; i < maxRetainedRounds*2; i++ {
		history = append(history, Message{Role: RoleAssistant, Content: "a"})
		history = append(history, Message{Role: RoleTool, ToolCallID: "c", Content: "t"})
	}
	a := &Agent{contextWindow: DefaultContextWindowBytes}
	got := a.requestMessages(history, nil)
	if len(got) != len(history) {
		t.Fatalf("len = %d, want %d", len(got), len(history))
	}
	for i := range history {
		if got[i].Content != history[i].Content {
			t.Fatalf("message %d changed: %q", i, got[i].Content)
		}
	}
}

func TestAgentRunAppliesCompaction(t *testing.T) {
	// Seed enough large rounds and a tight contextWindow so the byte budget
	// falls short of the whole history but is rich enough for the greedy
	// retainer to hit its maxRetainedRounds cap, forcing compaction to keep
	// exactly the leading user message plus the last maxRetainedRounds rounds.
	const seedRounds = maxRetainedRounds + 16
	const roundBytes = 100
	roundContent := strings.Repeat("t", roundBytes)
	var calls int
	model := modelFunc(func(_ context.Context, request Request) (Response, error) {
		calls++
		switch calls {
		case 1:
			return Response{ToolCalls: []ToolCall{{ID: "c1", Name: "snap", Input: json.RawMessage(`{}`)}}}, nil
		case 2:
			// Compaction keeps leading user message + last maxRetainedRounds
			// rounds (each an assistant plus its tool message).
			want := 1 + 2*maxRetainedRounds
			if len(request.Messages) != want {
				t.Fatalf("message count = %d, want %d", len(request.Messages), want)
			}
			if request.Messages[0].Role != RoleUser {
				t.Fatalf("first message = %#v, want user", request.Messages[0])
			}
			// The last round is the one produced by this run's tool loop.
			last := request.Messages[len(request.Messages)-2:]
			if last[0].Role != RoleAssistant || last[1].Role != RoleTool ||
				last[1].ToolCallID != "c1" {
				t.Fatalf("last round = %#v", last)
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
	agent, err := New(model, tool)
	if err != nil {
		t.Fatal(err)
	}
	// A tight contextWindow makes the whole seeded history exceed the budget
	// while still allowing maxRetainedRounds rounds to be greedily retained.
	agent.contextWindow = maxRetainedRounds*roundBytes + 1000
	// Seed enough history so the second call exceeds the dynamic round budget.
	turn := NewTurn("u0")
	for i := 0; i < seedRounds; i++ {
		turn.rounds = append(turn.rounds, Round{
			Assistant: Message{Role: RoleAssistant, Content: "a"},
			Tools:     []Message{{Role: RoleTool, Content: roundContent, ToolCallID: "cx"}},
		})
	}
	if err := agent.Run(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}
func TestCompactMessagesTruncatesLongToolOutput(t *testing.T) {
	// A small contextWindow forces historical tool output to be truncated to a
	// bounded per-message budget while the current round stays unchanged.
	long := strings.Repeat("x", 100)
	history := []Message{
		{Role: RoleUser, Content: "u"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleTool, ToolCallID: "c1", Content: long},
		{Role: RoleAssistant, Content: "current"},
	}
	// keepRounds 2: current round ("current") plus a1; contextWindow 20 leaves
	// 13 bytes for the two historical messages (a1, tool1).
	got := compactMessages(history, 2, 20)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4; got %#v", len(got), got)
	}
	// Current round unchanged.
	if got[3].Content != "current" {
		t.Fatalf("current round changed: %#v", got[3])
	}
	// Historical tool content truncated, no longer the full original.
	if got[2].Content == long {
		t.Fatalf("tool content not truncated: len = %d", len(got[2].Content))
	}
}

func TestCompactMessagesCompactsJSONInput(t *testing.T) {
	big := strings.Repeat("y", 200)
	history := []Message{
		{Role: RoleUser, Content: "u"},
		{Role: RoleAssistant, Content: "a", ToolCalls: []ToolCall{{ID: "c", Input: json.RawMessage(`{"k":"` + big + `"}`)}}},
		{Role: RoleTool, ToolCallID: "c", Content: "t"},
		{Role: RoleAssistant, Content: "current"},
	}
	got := compactMessages(history, 2, 64)
	if len(got) != 4 {
		t.Fatalf("len = %d, want 4", len(got))
	}
	raw := got[1].ToolCalls[0].Input
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("tool input is not valid JSON after compaction: %v (%s)", err, raw)
	}
	s, _ := v["k"].(string)
	if len(s) >= 200 {
		t.Fatalf("JSON value not truncated: len = %d", len(s))
	}
}

func TestCompactMessagesBudgetEnoughKeepsOriginal(t *testing.T) {
	content := "short output"
	history := []Message{
		{Role: RoleUser, Content: "u"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleTool, ToolCallID: "c1", Content: content},
		{Role: RoleAssistant, Content: "current"},
	}
	got := compactMessages(history, 2, 1<<20)
	// Ample budget triggers compaction (a1/tool1 and current retained) but the
	// historical tool output is kept unchanged.
	if len(got) != 4 || got[2].Content != content {
		t.Fatalf("result = %#v, want content unchanged", got)
	}
}

func TestCompactMessagesZeroBudgetKeepsAll(t *testing.T) {
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
	for i := range history {
		if got[i].Content != history[i].Content {
			t.Fatalf("message %d content changed: %q", i, got[i].Content)
		}
	}
}

func TestCompactRoundEmptyIsSafe(t *testing.T) {
	got := compactRound(nil, 16)
	if len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

func TestCompactRoundCurrentRoundNotTruncated(t *testing.T) {
	// The current (last) round is appended unchanged by compactMessages; only
	// historical rounds are passed through compactRound. When the current round
	// alone exceeds the budget, historical rounds are dropped but the current
	// one is still kept at full length.
	long := strings.Repeat("z", 100)
	history := []Message{
		{Role: RoleUser, Content: "u"},
		{Role: RoleAssistant, Content: "a1"},
		{Role: RoleAssistant, Content: long},
	}
	tight := compactMessages(history, 2, 16)
	// long (100 bytes) alone exceeds the 16-byte budget, so only the current
	// round survives, unchanged.
	if len(tight) != 2 || tight[0].Role != RoleUser || tight[1].Content != long {
		t.Fatalf("tight budget result = %#v, want [user, long current]", tight)
	}
	// With an ample budget the current round also survives unchanged.
	big := compactMessages(history, 2, 1<<20)
	if len(big) != 3 || big[2].Content != long {
		t.Fatalf("ample budget result = %#v, want current preserved", big)
	}
}

func TestCompactMessagesDoesNotPollutePersistedTurn(t *testing.T) {
	// Turn.Messages returns a defensive copy; compactMessages mutates it. The
	// persisted round history must remain untouched.
	long := string(make([]byte, 100))
	turn := NewTurn("u0")
	if err := turn.bindSystem("sys"); err != nil {
		t.Fatal(err)
	}
	turn.rounds = append(turn.rounds, Round{
		Assistant: Message{Role: RoleAssistant, Content: "a1"},
		Tools:     []Message{{Role: RoleTool, ToolCallID: "c1", Content: long}},
	}, Round{
		Assistant: Message{Role: RoleAssistant, Content: "current"},
	})
	// Exceed the byte budget so compaction actually runs.
	for i := 0; i < defaultCompactionRounds; i++ {
		turn.rounds = append(turn.rounds, Round{
			Assistant: Message{Role: RoleAssistant, Content: "fill"},
			Tools:     []Message{{Role: RoleTool, ToolCallID: "cf", Content: "t"}},
		})
	}
	before := turn.Messages()
	a := &Agent{contextWindow: 16}
	compacted := a.requestMessages(turn.Messages(), nil)
	if len(compacted) == len(before) {
		t.Fatal("expected compaction to run")
	}
	after := turn.Messages()
	// Historical content preserved across the full snapshot.
	for i := range before {
		if before[i].Content != after[i].Content || before[i].Role != after[i].Role {
			t.Fatalf("persisted message %d changed: %#v -> %#v", i, before[i], after[i])
		}
	}
}

func TestRetainLowOccupancyKeepsAll(t *testing.T) {
	// Tiny rounds far under the budget with many more rounds than the old fixed
	// 32 threshold: low occupancy must retain the whole history unchanged.
	history := []Message{{Role: RoleSystem, Content: "sys"}, {Role: RoleUser, Content: "u0"}}
	for i := 0; i < maxRetainedRounds*2; i++ {
		history = append(history,
			Message{Role: RoleAssistant, Content: "a"},
			Message{Role: RoleTool, ToolCallID: "c", Content: "t"},
		)
	}
	a := &Agent{contextWindow: 1 << 20}
	keep, keepAll := a.dynamicRetainedRounds(history, nil)
	if !keepAll || keep != roundsIn(history) {
		t.Fatalf("low occupancy should keep all, keepAll=%v keep=%d rounds=%d",
			keepAll, keep, roundsIn(history))
	}
	got := a.requestMessages(history, nil)
	if len(got) != len(history) {
		t.Fatalf("low occupancy should return history unchanged: len = %d, want %d", len(got), len(history))
	}
}

func TestRetainHighOccupancyKeepsFewer(t *testing.T) {
	// 64 large rounds under a tight budget: the whole history cannot fit, so
	// fewer rounds are retained (still within the floor and cap).
	history := []Message{{Role: RoleUser, Content: "u0"}}
	for i := 0; i < 64; i++ {
		history = append(history,
			Message{Role: RoleAssistant, Content: "a"},
			Message{Role: RoleTool, ToolCallID: "c", Content: strings.Repeat("x", 2000)},
		)
	}
	a := &Agent{contextWindow: 32 << 10}
	keep, keepAll := a.dynamicRetainedRounds(history, nil)
	if keepAll {
		t.Fatal("high occupancy should not keep all history")
	}
	if keep < minRetainedRounds || keep > maxRetainedRounds {
		t.Fatalf("retained = %d, want within [%d, %d]", keep, minRetainedRounds, maxRetainedRounds)
	}
	got := a.requestMessages(history, nil)
	if len(got) >= len(history) {
		t.Fatalf("high occupancy should shrink history: len = %d, want < %d", len(got), len(history))
	}
}

func TestRetainFloorWithOversizedRound(t *testing.T) {
	// A modest budget and medium rounds: greedy would keep only a handful, but
	// minRetainedRounds must keep at least the floor worth of history.
	history := []Message{{Role: RoleUser, Content: "u0"}}
	round := strings.Repeat("r", 200)
	for i := 0; i < 16; i++ {
		history = append(history,
			Message{Role: RoleAssistant, Content: "a"},
			Message{Role: RoleTool, ToolCallID: "c", Content: round},
		)
	}
	a := &Agent{contextWindow: 1 << 10}
	keep, keepAll := a.dynamicRetainedRounds(history, nil)
	if keepAll {
		t.Fatal("tight budget should not keep all history")
	}
	if keep != minRetainedRounds {
		t.Fatalf("retained = %d, want floor %d", keep, minRetainedRounds)
	}
}

func TestRetainCapWithManyTinyRounds(t *testing.T) {
	// Budget rich enough to keep more than maxRetainedRounds tiny rounds, but the
	// whole (tripled) history exceeds it, so greedy hits the cap.
	history := []Message{{Role: RoleUser, Content: "u"}}
	for i := 0; i < maxRetainedRounds*3; i++ {
		history = append(history,
			Message{Role: RoleAssistant, Content: "a"},
			Message{Role: RoleTool, ToolCallID: "c", Content: "t"},
		)
	}
	a := &Agent{contextWindow: 120}
	keep, keepAll := a.dynamicRetainedRounds(history, nil)
	if keepAll {
		t.Fatal("history exceeds the budget, so not everything should be kept")
	}
	if keep != maxRetainedRounds {
		t.Fatalf("retained = %d, want cap %d", keep, maxRetainedRounds)
	}
}

func TestRetainUsesReportedUsageBaseline(t *testing.T) {
	// Tiny history. With nil usage the byte estimate is far under the budget, so
	// everything is retained. With a reported large total_tokens baseline the
	// occupancy jumps past the budget and drives compaction, even though the
	// bytes are identical.
	history := []Message{{Role: RoleUser, Content: "u0"}}
	for i := 0; i < 64; i++ {
		history = append(history,
			Message{Role: RoleAssistant, Content: "a"},
			Message{Role: RoleTool, ToolCallID: "c", Content: "t"},
		)
	}
	a := &Agent{contextWindow: 1 << 20}

	keepNil, keepAllNil := a.dynamicRetainedRounds(history, nil)
	if !keepAllNil {
		t.Fatalf("nil usage with tiny history should keep all, got keep=%d", keepNil)
	}
	if nilGot := a.requestMessages(history, nil); len(nilGot) != len(history) {
		t.Fatalf("nil usage should return history unchanged: len = %d, want %d", len(nilGot), len(history))
	}

	// A huge reported total_tokens pushes the mixed occupancy baseline far past
	// the budget.
	lastUsage := &Usage{TotalTokens: 1 << 20}
	keepUsage, keepAllUsage := a.dynamicRetainedRounds(history, lastUsage)
	if keepAllUsage {
		t.Fatalf("reported usage baseline should force compaction, keep=%d", keepUsage)
	}
	if keepUsage < minRetainedRounds || keepUsage > maxRetainedRounds {
		t.Fatalf("retained = %d out of bounds", keepUsage)
	}
	got := a.requestMessages(history, lastUsage)
	if len(got) >= len(history) {
		t.Fatalf("usage baseline should compact: len = %d, want < %d", len(got), len(history))
	}
}

// roundsIn counts the rounds in a history (one per assistant message).
func roundsIn(history []Message) int {
	count := 0
	for _, message := range history {
		if message.Role == RoleAssistant {
			count++
		}
	}
	return count
}
