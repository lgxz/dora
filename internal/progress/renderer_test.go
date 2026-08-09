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
	renderer := New(&output, false, false)
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
		"dora: thinking",
		"我先看看当前目录",
		"bash · pwd ·",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRendererGroupsToolCallsFromOneAssistantMessage(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false)
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
		"bash · uptime",
		"uptime",
		"bash · df -h",
		"df -h",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "道具调用") {
		t.Fatalf("output contains redundant batch heading: %q", output.String())
	}
}

func TestRendererUsesColorOnlyWhenEnabled(t *testing.T) {
	var colored bytes.Buffer
	New(&colored, true, true).Observe(dora.Update{Kind: dora.UpdateThinking})
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatalf("colored output = %q", colored.String())
	}

	var plain bytes.Buffer
	New(&plain, true, false).Observe(dora.Update{Kind: dora.UpdateThinking})
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("plain output = %q", plain.String())
	}
}

func TestRendererShowsSessionState(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false)
	renderer.Session("system-status", false)
	renderer.Session("system-status", true)
	if !strings.Contains(output.String(), "开始任务「system-status」") ||
		!strings.Contains(output.String(), "继续任务「system-status」") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRendererShowsFreshSession(t *testing.T) {
	var output bytes.Buffer
	New(&output, false, false).FreshSession("system-status")
	if !strings.Contains(output.String(), "重新开始任务「system-status」") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRendererReplacesThinkingWithAssistantContentInTerminal(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, true, false)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{
		Kind: dora.UpdateMessageAdded,
		Message: dora.Message{
			Role:      dora.RoleAssistant,
			Content:   "我先检查系统状态。",
			ToolCalls: []dora.ToolCall{{ID: "1", Name: "bash"}},
		},
	})
	if !strings.Contains(output.String(), "\x1b[1A\r\x1b[2K") ||
		!strings.Contains(output.String(), "● 我先检查系统状态。") {
		t.Fatalf("output = %q", output.String())
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
