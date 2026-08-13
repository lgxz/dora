package progress

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora"
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
		"pwd ·",
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
		"uptime",
		"df -h",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "bash ·") {
		t.Fatalf("output %q contains a bash tool-name prefix", output.String())
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
	if !strings.Contains(output.String(), "Starting task \"system-status\"") ||
		!strings.Contains(output.String(), "Resuming task \"system-status\"") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRendererShowsFreshSession(t *testing.T) {
	var output bytes.Buffer
	New(&output, false, false).FreshSession("system-status")
	if !strings.Contains(output.String(), "Restarting task \"system-status\"") {
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

func TestRendererTrimsTrailingNewlinesFromAssistantContent(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, true, false)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{
		Kind: dora.UpdateMessageAdded,
		Message: dora.Message{
			Role:      dora.RoleAssistant,
			Content:   "我先检查系统状态。\n\n",
			ToolCalls: []dora.ToolCall{{ID: "1", Name: "bash"}},
		},
	})
	// Trailing newlines must not render as empty continuation lines.
	if strings.Contains(output.String(), "│ \n") {
		t.Fatalf("output = %q", output.String())
	}
	if !strings.Contains(output.String(), "● 我先检查系统状态。") {
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

func TestToolSummaryShowsPowerShellCommand(t *testing.T) {
	summary := toolSummary(dora.ToolCall{
		Name:  "powershell",
		Input: json.RawMessage(`{"command":"Get-Process"}`),
	})
	if summary != "Get-Process" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestToolSummaryShowsSkillName(t *testing.T) {
	summary := toolSummary(dora.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":"system-status"}`)})
	if summary != "system-status" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestRendererUsesStartedAtForToolDuration(t *testing.T) {
	// The UpdateToolStarted event carries the real start time. The renderer
	// must use it to compute the duration instead of time.Now(), which would
	// otherwise report ~1ms when the event is delivered after the tool finishes.
	var output bytes.Buffer
	renderer := New(&output, false, false)
	call := dora.ToolCall{ID: "call-1", Name: "bash", Input: json.RawMessage(`{"command":"sleep 1"}`)}
	renderer.Observe(dora.Update{
		Kind: dora.UpdateMessageAdded,
		Message: dora.Message{
			Role:      dora.RoleAssistant,
			Content:   "我会等待一秒。",
			ToolCalls: []dora.ToolCall{call},
		},
	})

	startedAt := time.Now().Add(-2 * time.Second)
	renderer.Observe(dora.Update{
		Kind:      dora.UpdateToolStarted,
		ToolCall:  call,
		StartedAt: startedAt,
	})
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageAdded,
		Message: dora.Message{Role: dora.RoleTool, ToolCallID: call.ID},
	})

	if !strings.Contains(output.String(), "2.0s") {
		t.Fatalf("output %q does not report the ~2s duration from StartedAt", output.String())
	}
	if strings.Contains(output.String(), "1ms") {
		t.Fatalf("output %q reports ~1ms instead of the StartedAt-based duration", output.String())
	}
}

func TestRendererFallsBackToNowWhenStartedAtZero(t *testing.T) {
	// Backward compatibility: callers that do not populate StartedAt must still
	// render a duration based on the event delivery time.
	var output bytes.Buffer
	renderer := New(&output, false, false)
	call := dora.ToolCall{ID: "call-1", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)}
	renderer.Observe(dora.Update{
		Kind: dora.UpdateMessageAdded,
		Message: dora.Message{
			Role:      dora.RoleAssistant,
			Content:   "我会查看目录。",
			ToolCalls: []dora.ToolCall{call},
		},
	})
	renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: call})
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageAdded,
		Message: dora.Message{Role: dora.RoleTool, ToolCallID: call.ID},
	})
	if !strings.Contains(output.String(), "ms") {
		t.Fatalf("output %q does not report a duration when StartedAt is zero", output.String())
	}
}
