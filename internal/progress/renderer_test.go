package progress

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"dora"
)

func TestRendererShowsDoraProgress(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	call := dora.ToolCall{
		ID:    "call-1",
		Name:  "bash",
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	renderer.Observe(dora.Update{
		Kind: dora.UpdateMessageAdded,
		Message: dora.Message{
			Role:      dora.RoleAssistant,
			Content:   "我先看看当前目录。",
			ToolCalls: []dora.ToolCall{call},
		},
	})
	renderer.Observe(dora.Update{
		Kind:     dora.UpdateToolStarted,
		ToolCall: call,
	})
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageAdded,
		Message: dora.Message{Role: dora.RoleTool, ToolCallID: "call-1"},
	})
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})

	for _, want := range []string{
		"让我想想办法",
		"我先看看当前目录",
		"准备使用 bash",
		"bash · pwd ·",
		"我再整理一下",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRendererGroupsToolCallsFromOneAssistantMessage(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false)
	calls := []dora.ToolCall{
		{ID: "call-1", Name: "bash", Input: json.RawMessage(`{"command":"uptime"}`)},
		{ID: "call-2", Name: "bash", Input: json.RawMessage(`{"command":"df -h"}`)},
	}
	renderer.Observe(dora.Update{
		Kind: dora.UpdateMessageAdded,
		Message: dora.Message{
			Role:      dora.RoleAssistant,
			Content:   "我会检查负载和磁盘。",
			ToolCalls: calls,
		},
	})
	for _, call := range calls {
		renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: call})
		renderer.Observe(dora.Update{
			Kind:    dora.UpdateMessageAdded,
			Message: dora.Message{Role: dora.RoleTool, ToolCallID: call.ID},
		})
	}

	for _, want := range []string{
		"我会检查负载和磁盘",
		"准备了 2 次道具调用",
		"1. bash",
		"uptime",
		"2. bash",
		"df -h",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRendererUsesColorOnlyWhenEnabled(t *testing.T) {
	var colored bytes.Buffer
	New(&colored, true).Observe(dora.Update{Kind: dora.UpdateThinking})
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatalf("colored output = %q", colored.String())
	}

	var plain bytes.Buffer
	New(&plain, false).Observe(dora.Update{Kind: dora.UpdateThinking})
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("plain output = %q", plain.String())
	}
}

func TestToolSummaryCollapsesAndTruncatesCommand(t *testing.T) {
	call := dora.ToolCall{
		Name:  "bash",
		Input: json.RawMessage(`{"command":"first   line\n` + strings.Repeat("x", 100) + `"}`),
	}
	summary := toolSummary(call)
	if strings.Contains(summary, "\n") || !strings.HasSuffix(summary, "…") {
		t.Fatalf("summary = %q", summary)
	}
}
