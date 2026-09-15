package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/lgxz/dora"
)

func TestWriteTraceFilePreservesParentTurn(t *testing.T) {
	turn := dora.NewTurn("inspect")
	usage := &dora.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	if err := turn.AppendRound(dora.Round{
		Assistant: dora.Message{
			Role:      dora.RoleAssistant,
			Reasoning: "need a command",
			ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "bash", Input: json.RawMessage("{")}},
		},
		Tools: []dora.Message{{
			Role: dora.RoleTool, ToolCallID: "call-1", Content: "output",
			Images: []dora.Image{{Path: "/tmp/result.png"}},
		}},
		Usage: usage,
	}, "opaque"); err != nil {
		t.Fatal(err)
	}
	if err := turn.Complete("done", "opaque-final"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "trace.json")
	if err := writeTraceFile(path, turn); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var trace turnTrace
	if err := json.Unmarshal(contents, &trace); err != nil {
		t.Fatal(err)
	}
	if trace.SchemaVersion != turnTraceSchemaVersion || trace.User != "inspect" {
		t.Fatalf("trace = %#v", trace)
	}
	if len(trace.Rounds) != 1 || trace.Rounds[0].Assistant.ToolCalls[0].Input != "{" {
		t.Fatalf("rounds = %#v", trace.Rounds)
	}
	if trace.Rounds[0].Tools[0].Images[0].Path != "/tmp/result.png" {
		t.Fatalf("tool result = %#v", trace.Rounds[0].Tools[0])
	}
	if trace.Final == nil || trace.Final.Content != "done" {
		t.Fatalf("final = %#v", trace.Final)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}
