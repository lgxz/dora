package progress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora"
	"github.com/rivo/uniseg"
)

func TestRendererShowsDoraProgress(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, false)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	call := dora.ToolCall{
		ID:    "call-1",
		Name:  "bash",
		Input: json.RawMessage(`{"command":"pwd"}`),
	}
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageReceived,
		Message: dora.Message{Role: dora.RoleAssistant, Content: "我先看看当前目录。", ToolCalls: []dora.ToolCall{call}},
	})
	renderer.Observe(dora.Update{
		Kind:     dora.UpdateToolStarted,
		ToolCall: call,
	})
	renderer.Observe(dora.Update{
		Kind:     dora.UpdateToolFinished,
		ToolCall: call,
		Message:  dora.Message{Role: dora.RoleTool, ToolCallID: "call-1"},
	})
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})

	for _, want := range []string{
		"Thinking...",
		"我先看看当前目录",
		"pwd",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
}

func TestRendererGroupsToolCallsFromOneAssistantMessage(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, false)
	calls := []dora.ToolCall{
		{ID: "call-1", Name: "bash", Input: json.RawMessage(`{"command":"uptime"}`)},
		{ID: "call-2", Name: "bash", Input: json.RawMessage(`{"command":"df -h"}`)},
	}
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageReceived,
		Message: dora.Message{Role: dora.RoleAssistant, Content: "我会检查负载和磁盘。", ToolCalls: calls},
	})
	for _, call := range calls {
		renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: call})
		renderer.Observe(dora.Update{
			Kind:     dora.UpdateToolFinished,
			ToolCall: call,
			Message:  dora.Message{Role: dora.RoleTool, ToolCallID: call.ID, Content: "0"},
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
	New(&colored, true, true, false).Observe(dora.Update{Kind: dora.UpdateThinking})
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatalf("colored output = %q", colored.String())
	}

	var plain bytes.Buffer
	New(&plain, true, false, false).Observe(dora.Update{Kind: dora.UpdateThinking})
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("plain output = %q", plain.String())
	}
}

func TestRendererShowsSessionState(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, false)
	renderer.Session("system-status.sqlite")
	if !strings.Contains(output.String(), "Session \"system-status.sqlite\"") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRendererCarriesUsageWithoutRendering(t *testing.T) {
	// Usage is carried by UpdateMessageReceived but must not be rendered: the
	// renderer never prints a token summary line, whether usage is present or
	// nil.
	for _, usage := range []*dora.Usage{
		{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
		nil,
	} {
		var output bytes.Buffer
		renderer := New(&output, false, false, false)
		renderer.Observe(dora.Update{
			Kind:    dora.UpdateMessageReceived,
			Message: dora.Message{Role: dora.RoleAssistant, Content: "结论", ToolCalls: []dora.ToolCall{{ID: "1", Name: "bash"}}},
			Usage:   usage,
		})
		rendered := output.String()
		if strings.Contains(rendered, "in=") || strings.Contains(rendered, "out=") || strings.Contains(rendered, "tok") {
			t.Fatalf("output %q renders usage; want none", rendered)
		}
		if !strings.Contains(rendered, "结论") {
			t.Fatalf("output %q does not render the assistant message", rendered)
		}
	}
}

func TestRendererReplacesThinkingWithAssistantContentInTerminal(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, true, false, false)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageReceived,
		Message: dora.Message{Role: dora.RoleAssistant, Content: "我先检查系统状态。", ToolCalls: []dora.ToolCall{{ID: "1", Name: "bash"}}},
	})
	if !strings.Contains(output.String(), "\x1b[1A\r\x1b[2K") ||
		!strings.Contains(output.String(), "● 我先检查系统状态。") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRendererStreamsReasoningInTerminal(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, true, false, true)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "让我想想 "})
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "怎么查天气"})
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageReceived,
		Message: dora.Message{Role: dora.RoleAssistant, Content: "我来查一下。", ToolCalls: []dora.ToolCall{{ID: "1", Name: "bash"}}},
	})

	rendered := output.String()
	// The "Thinking..." placeholder is erased exactly once, by the first
	// reasoning delta; the reasoning text itself is never erased.
	if erases := strings.Count(rendered, "\x1b[1A\r\x1b[2K"); erases != 1 {
		t.Fatalf("output %q contains %d erase sequences, want 1", rendered, erases)
	}
	if !strings.Contains(rendered, "○ 让我想想 怎么查天气") {
		t.Fatalf("output = %q", rendered)
	}
	// The assistant line that follows starts on a fresh line.
	if !strings.Contains(rendered, "怎么查天气\n● 我来查一下。") {
		t.Fatalf("output = %q", rendered)
	}
}

func TestRendererStreamsReasoningWithoutTerminal(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, true)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "考虑中"})
	renderer.Observe(dora.Update{Kind: dora.UpdateMessageReceived, Message: dora.Message{
		Role:    dora.RoleAssistant,
		Content: "答案",
	}})

	rendered := output.String()
	if strings.Contains(rendered, "\x1b[") {
		t.Fatalf("output = %q, want no escape sequences", rendered)
	}
	if !strings.Contains(rendered, "Thinking...") || !strings.Contains(rendered, "○ 考虑中\n") {
		t.Fatalf("output = %q", rendered)
	}
}

func TestRendererColorsReasoningWhenEnabled(t *testing.T) {
	var output bytes.Buffer
	New(&output, true, true, true).Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "思考"})
	if !strings.Contains(output.String(), "\x1b[2m") {
		t.Fatalf("output = %q, want dim styling", output.String())
	}
}

func TestRendererResetsReasoningMarkerEachRound(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, true)
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "第一轮"})
	renderer.Observe(dora.Update{Kind: dora.UpdateMessageReceived, Message: dora.Message{
		Role:      dora.RoleAssistant,
		Content:   "调用工具",
		ToolCalls: []dora.ToolCall{{ID: "1", Name: "bash"}},
	}})
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "第二轮"})

	rendered := output.String()
	if markers := strings.Count(rendered, "○ "); markers != 2 {
		t.Fatalf("output %q contains %d reasoning markers, want 2", rendered, markers)
	}
}

func TestRendererHidesReasoningByDefault(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, true, false, false)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "考虑中"})
	renderer.Observe(dora.Update{Kind: dora.UpdateMessageReceived, Message: dora.Message{
		Role:    dora.RoleAssistant,
		Content: "答案",
	}})

	rendered := output.String()
	if strings.Contains(rendered, "考虑中") || strings.Contains(rendered, "○ ") {
		t.Fatalf("output = %q, want reasoning hidden without --reasoning", rendered)
	}
	// The placeholder is still replaced by the assistant message.
	if !strings.Contains(rendered, "Thinking...") {
		t.Fatalf("output = %q", rendered)
	}
}

func TestRendererStreamsReasoningLineByLine(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, true, false, true)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "第一行完整\n"})
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "第二行尚未结束"})

	// The complete line is written eagerly; the partial line stays buffered.
	rendered := output.String()
	if !strings.Contains(rendered, "第一行完整\n") {
		t.Fatalf("output = %q, want the complete line flushed", rendered)
	}
	if strings.Contains(rendered, "第二行尚未结束") {
		t.Fatalf("output = %q, want the partial line buffered", rendered)
	}

	// The round's end flushes the partial line before the assistant output.
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageReceived,
		Message: dora.Message{Role: dora.RoleAssistant, Content: "我来查一下。", ToolCalls: []dora.ToolCall{{ID: "1", Name: "bash"}}},
	})
	if rendered = output.String(); !strings.Contains(rendered, "第二行尚未结束\n● 我来查一下。") {
		t.Fatalf("output = %q", rendered)
	}
}

func TestRendererFlushesReasoningWithoutNewlineAtCap(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, true)
	// A single line longer than the cap must still stream.
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: strings.Repeat("长", 2*reasoningFlushBytes)})
	if !strings.Contains(output.String(), "长") {
		t.Fatal("output has no reasoning, want the oversized line flushed at the cap")
	}
}

func TestRendererFlushesPendingReasoningWhenRoundAborts(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, true)
	// A model call that fails after reasoning never emits an assistant
	// message; the next round's placeholder must not swallow the buffer.
	renderer.Observe(dora.Update{Kind: dora.UpdateReasoningDelta, Delta: "中断的推理"})
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	if rendered := output.String(); !strings.Contains(rendered, "○ 中断的推理\nThinking...") {
		t.Fatalf("output = %q", rendered)
	}
}

func TestRendererTrimsTrailingNewlinesFromAssistantContent(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, true, false, false)
	renderer.Observe(dora.Update{Kind: dora.UpdateThinking})
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageReceived,
		Message: dora.Message{Role: dora.RoleAssistant, Content: "我先检查系统状态。\n\n", ToolCalls: []dora.ToolCall{{ID: "1", Name: "bash"}}},
	})
	// Trailing newlines must not render as empty continuation lines.
	if strings.Contains(output.String(), "│ \n") {
		t.Fatalf("output = %q", output.String())
	}
	if !strings.Contains(output.String(), "● 我先检查系统状态。") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestCommandSummaryCollapsesAndTruncatesCommand(t *testing.T) {
	call := dora.ToolCall{
		Name:  "bash",
		Input: json.RawMessage(`{"command":"first   line\n` + strings.Repeat("x", 100) + `"}`),
	}
	summary := presentTool(call, dora.Message{}).summary
	if strings.Contains(summary, "\n") || !strings.HasSuffix(summary, "…") {
		t.Fatalf("summary = %q", summary)
	}
}

func TestCommandSummaryShowsPowerShellCommand(t *testing.T) {
	summary := presentTool(dora.ToolCall{
		Name:  "powershell",
		Input: json.RawMessage(`{"command":"Get-Process"}`),
	}, dora.Message{}).summary
	if summary != "Get-Process" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestCommandSummariesUseFixedDisplayWidth(t *testing.T) {
	short := fitDisplayWidth("pwd", commandSummaryWidth)
	long := fitDisplayWidth(strings.Repeat("x", commandSummaryWidth+10), commandSummaryWidth)
	wide := fitDisplayWidth("读取中文文件", commandSummaryWidth)

	for name, value := range map[string]string{"short": short, "long": long, "wide": wide} {
		if got := uniseg.StringWidth(value); got != commandSummaryWidth {
			t.Fatalf("%s display width = %d, want %d: %q", name, got, commandSummaryWidth, value)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(long), "…") {
		t.Fatalf("long summary was not truncated: %q", long)
	}
}

func TestRendererAlignsCommandResultColumns(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, false)
	calls := []dora.ToolCall{
		{ID: "short", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)},
		{ID: "long", Name: "bash", Input: json.RawMessage(`{"command":"` + strings.Repeat("x", commandSummaryWidth+10) + `"}`)},
	}
	renderer.Observe(dora.Update{Kind: dora.UpdateMessageReceived, Message: dora.Message{Role: dora.RoleAssistant, ToolCalls: calls}})
	for _, call := range calls {
		renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: call})
		renderer.Observe(dora.Update{Kind: dora.UpdateToolFinished, ToolCall: call, Message: dora.Message{
			Role: dora.RoleTool, ToolCallID: call.ID, Content: `{"exit_code":0,"stdout":"ok"}`,
		}})
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("output lines = %q", lines)
	}
	firstColumn := strings.Index(lines[0], "·")
	secondColumn := strings.Index(lines[1], "·")
	if firstColumn < 0 || secondColumn < 0 {
		t.Fatalf("result separator missing: %q", lines)
	}
	firstWidth := uniseg.StringWidth(lines[0][:firstColumn])
	secondWidth := uniseg.StringWidth(lines[1][:secondColumn])
	if firstWidth != secondWidth {
		t.Fatalf("result columns = %d and %d: %q", firstWidth, secondWidth, lines)
	}
}

func TestSkillSummaryShowsName(t *testing.T) {
	summary := presentTool(
		dora.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":"system-status"}`)},
		dora.Message{},
	).summary
	if summary != "system-status" {
		t.Fatalf("summary = %q", summary)
	}
}

func TestToolPresentations(t *testing.T) {
	tests := []struct {
		name        string
		call        dora.ToolCall
		message     dora.Message
		wantName    string
		wantSummary string
		wantResult  string
		wantOutcome outcome
	}{
		{
			name:        "command success",
			call:        dora.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"go   test ./..."}`)},
			message:     dora.Message{Content: `{"exit_code":0,"stdout":"ok","stderr":"","timed_out":false,"truncated":false}`},
			wantSummary: "go test ./...",
			wantResult:  "2B/0B",
		},
		{
			name:        "command failure",
			call:        dora.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"false"}`)},
			message:     dora.Message{Content: `{"exit_code":1,"stderr":"failed"}`},
			wantSummary: "false",
			wantResult:  "0B/6B",
			wantOutcome: outcomeFailure,
		},
		{
			name:        "command combines stream sizes",
			call:        dora.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"mixed-output"}`)},
			message:     dora.Message{Content: `{"exit_code":0,"stdout":"hello","stderr":"warn"}`},
			wantSummary: "mixed-output",
			wantResult:  "5B/4B",
		},
		{
			name:        "command timeout",
			call:        dora.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"sleep 10"}`)},
			message:     dora.Message{Content: `{"exit_code":-1,"timed_out":true}`},
			wantSummary: "sleep 10",
			wantResult:  "timed out",
			wantOutcome: outcomeFailure,
		},
		{
			name:        "command truncated",
			call:        dora.ToolCall{Name: "bash", Input: json.RawMessage(`{"command":"generate-output"}`)},
			message:     dora.Message{Content: `{"exit_code":0,"truncated":true}`},
			wantSummary: "generate-output",
			wantResult:  "output truncated",
			wantOutcome: outcomeWarning,
		},
		{
			name:        "read range",
			call:        dora.ToolCall{Name: "read", Input: json.RawMessage(`{"path":"agent.go","offset":2,"limit":3}`)},
			message:     dora.Message{Content: "2:a\n3:b\n"},
			wantName:    "read",
			wantSummary: "agent.go:2-4",
			wantResult:  "2 lines",
		},
		{
			name:        "read binary",
			call:        dora.ToolCall{Name: "read", Input: json.RawMessage(`{"path":"image.png"}`)},
			message:     dora.Message{Content: "(binary file; use the bash tool to inspect it)"},
			wantName:    "read",
			wantSummary: "image.png",
			wantResult:  "binary file",
			wantOutcome: outcomeWarning,
		},
		{
			name:        "write created",
			call:        dora.ToolCall{Name: "write", Input: json.RawMessage(`{"path":"notes.md","content":"private"}`)},
			message:     dora.Message{Content: "bytes_written: 2048, created: true"},
			wantName:    "write",
			wantSummary: "notes.md",
			wantResult:  "created, 2.0 KB",
		},
		{
			name:        "write appended",
			call:        dora.ToolCall{Name: "write", Input: json.RawMessage(`{"path":"notes.md","content":"x","append":true}`)},
			message:     dora.Message{Content: "bytes_written: 1, created: false"},
			wantName:    "write",
			wantSummary: "append notes.md",
			wantResult:  "appended, 1 B",
		},
		{
			name:        "edit no match",
			call:        dora.ToolCall{Name: "edit", Input: json.RawMessage(`{"path":"agent.go","old_string":"secret","new_string":"other"}`)},
			message:     dora.Message{Content: "old_string not found in file"},
			wantName:    "edit",
			wantSummary: "agent.go",
			wantResult:  "old_string not found",
			wantOutcome: outcomeWarning,
		},
		{
			name:        "edit replacements",
			call:        dora.ToolCall{Name: "edit", Input: json.RawMessage(`{"path":"agent.go","old_string":"a","new_string":"bb","replace_all":true}`)},
			message:     dora.Message{Content: "replacements: 2, bytes_changed: 2"},
			wantName:    "edit",
			wantSummary: "agent.go (all)",
			wantResult:  "2 replacements, +2 B",
		},
		{
			name:        "grep matches",
			call:        dora.ToolCall{Name: "grep", Input: json.RawMessage(`{"pattern":"ToolStarted","path":"."}`)},
			message:     dora.Message{Content: "a.go:1:x\nb.go:2:y\n"},
			wantName:    "grep",
			wantSummary: `"ToolStarted" in .`,
			wantResult:  "2 matches",
		},
		{
			name:        "skill loaded",
			call:        dora.ToolCall{Name: "skill", Input: json.RawMessage(`{"name":"system-status"}`)},
			message:     dora.Message{Content: "complete skill contents"},
			wantName:    "skill",
			wantSummary: "system-status",
			wantResult:  "loaded",
		},
		{
			name:        "glob no matches",
			call:        dora.ToolCall{Name: "glob", Input: json.RawMessage(`{"pattern":"**/*.rs"}`)},
			message:     dora.Message{Content: "(no matches)\n"},
			wantName:    "glob",
			wantSummary: `"**/*.rs" in .`,
			wantResult:  "no matches",
			wantOutcome: outcomeWarning,
		},
		{
			name:        "history list",
			call:        dora.ToolCall{Name: "history", Input: json.RawMessage(`{"action":"list"}`)},
			message:     dora.Message{Content: `{"total":12,"turns":[{},{}]}`},
			wantName:    "history",
			wantSummary: "list",
			wantResult:  "2/12 turns",
		},
		{
			name:        "history get",
			call:        dora.ToolCall{Name: "history", Input: json.RawMessage(`{"action":"get","turn_id":7}`)},
			message:     dora.Message{Content: `{"total":9,"rounds":[{}]}`},
			wantName:    "history",
			wantSummary: "get turn 7",
			wantResult:  "1/9 rounds",
		},
		{
			name:        "task background",
			call:        dora.ToolCall{Name: "task", Input: json.RawMessage(`{"instruction":"inspect independently","background":true}`)},
			message:     dora.Message{Content: `{"job_id":"task_0","status":"running"}`},
			wantName:    "task",
			wantSummary: "inspect independently",
			wantResult:  "background task_0",
			wantOutcome: outcomeWarning,
		},
		{
			name:        "job error",
			call:        dora.ToolCall{Name: "job", Input: json.RawMessage(`{"action":"status","job_id":"missing"}`)},
			message:     dora.Message{Content: `{"error":"job not found"}`},
			wantName:    "job",
			wantSummary: "status missing",
			wantResult:  "job not found",
			wantOutcome: outcomeFailure,
		},
		{
			name:        "job running",
			call:        dora.ToolCall{Name: "job", Input: json.RawMessage(`{"action":"poll","job_id":"bash_0"}`)},
			message:     dora.Message{Content: `{"job_id":"bash_0","status":"running","exit_code":0}`},
			wantName:    "job",
			wantSummary: "poll bash_0",
			wantResult:  "running, exit 0",
			wantOutcome: outcomeWarning,
		},
		{
			name:        "task job done",
			call:        dora.ToolCall{Name: "job", Input: json.RawMessage(`{"action":"poll","job_id":"task_0"}`)},
			message:     dora.Message{Content: `{"job_id":"task_0","status":"done","result":"independent result"}`},
			wantName:    "job",
			wantSummary: "poll task_0",
			wantResult:  "done",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := presentTool(test.call, test.message)
			if got.name != test.wantName || got.summary != test.wantSummary ||
				got.result != test.wantResult || got.outcome != test.wantOutcome {
				t.Fatalf("presentation = %#v", got)
			}
		})
	}
}

func TestGenericPresentationRedactsSensitiveArguments(t *testing.T) {
	presentation := presentTool(dora.ToolCall{
		Name:  "remote",
		Input: json.RawMessage(`{"action":"send","api_token":"top-secret","body":"also-secret"}`),
	}, dora.Message{Content: "secret result"})

	if presentation.summary != `action="send" api_token=<redacted>` || presentation.result != "done" {
		t.Fatalf("presentation = %#v", presentation)
	}
	if strings.Contains(presentation.summary, "top-secret") || strings.Contains(presentation.result, "secret result") {
		t.Fatalf("presentation leaked content: %#v", presentation)
	}
}

func TestGenericPresentationOmitsPayloadAndHandlesInvalidJSON(t *testing.T) {
	payload := presentTool(dora.ToolCall{
		Name:  "remote",
		Input: json.RawMessage(`{"body":"top-secret"}`),
	}, dora.Message{})
	if payload.summary != "body=<omitted>" || strings.Contains(payload.summary, "top-secret") {
		t.Fatalf("payload presentation = %#v", payload)
	}

	invalid := presentTool(dora.ToolCall{Name: "remote", Input: json.RawMessage(`{"broken"`)}, dora.Message{})
	if invalid.summary != "arguments unavailable" {
		t.Fatalf("invalid presentation = %#v", invalid)
	}
}

func TestFormatStreamSizesUsesCompactStdoutStderrPair(t *testing.T) {
	if got := formatStreamSizes(217, 64*1024); got != "217B/64K" {
		t.Fatalf("formatStreamSizes = %q", got)
	}
	if got := formatStreamSizes(0, 0); got != "" {
		t.Fatalf("empty formatStreamSizes = %q", got)
	}
}

func TestKnownPresentationDoesNotLeakPayloadArguments(t *testing.T) {
	presentation := presentTool(dora.ToolCall{
		Name:  "write",
		Input: json.RawMessage(`{"path":"notes.md","content":"top-secret"}`),
	}, dora.Message{Content: "bytes_written: 10, created: false"})

	if strings.Contains(presentation.summary, "top-secret") {
		t.Fatalf("presentation leaked write content: %#v", presentation)
	}
}

func TestRendererClassifiesSemanticAndToolFailures(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, false)
	command := dora.ToolCall{ID: "command", Name: "bash", Input: json.RawMessage(`{"command":"false"}`)}
	renderer.Observe(dora.Update{Kind: dora.UpdateMessageReceived, Message: dora.Message{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{command}}})
	renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: command})
	renderer.Observe(dora.Update{Kind: dora.UpdateToolFinished, ToolCall: command, Message: dora.Message{Role: dora.RoleTool, ToolCallID: command.ID, Content: `{"exit_code":1,"stderr":"failed"}`}})

	read := dora.ToolCall{ID: "read", Name: "read", Input: json.RawMessage(`{"path":"missing.go"}`)}
	renderer.Observe(dora.Update{Kind: dora.UpdateMessageReceived, Message: dora.Message{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{read}}})
	renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: read})
	renderer.Observe(dora.Update{Kind: dora.UpdateToolFinished, ToolCall: read, Err: fmt.Errorf(`execute tool "read": read: open missing.go: no such file`)})

	for _, want := range []string{
		"△ false",
		"· 0B/6B ·",
		"△ read missing.go · read: open missing.go: no such file ·",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("output %q does not contain %q", output.String(), want)
		}
	}
	if strings.Contains(output.String(), "exit 1") {
		t.Fatalf("output contains an unnecessary exit code: %q", output.String())
	}
}

func TestRendererDistinguishesToolFinishedSuccessAndError(t *testing.T) {
	var output bytes.Buffer
	renderer := New(&output, false, false, false)
	// A tool that succeeds with empty result renders a success line.
	ok := dora.ToolCall{ID: "ok", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)}
	renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: ok})
	renderer.Observe(dora.Update{Kind: dora.UpdateToolFinished, ToolCall: ok, Message: dora.Message{Role: dora.RoleTool, ToolCallID: ok.ID, Content: `{"exit_code":0,"stdout":"ok"}`}})

	// A tool that failed at the process level renders a red failure line via
	// presentToolFailure, without inspecting the result content.
	bad := dora.ToolCall{ID: "bad", Name: "read", Input: json.RawMessage(`{"path":"missing.go"}`)}
	renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: bad})
	renderer.Observe(dora.Update{Kind: dora.UpdateToolFinished, ToolCall: bad, Err: fmt.Errorf("execute tool %q: boom", "read")})

	rendered := output.String()
	if !strings.Contains(rendered, "•") || !strings.Contains(rendered, "2B/0B") {
		t.Fatalf("output %q does not render the success tool line with its result", rendered)
	}
	if !strings.Contains(rendered, "△ read missing.go · boom ·") {
		t.Fatalf("output %q does not render a red failure tool line", rendered)
	}
}

func TestRendererUsesStartedAtForToolDuration(t *testing.T) {
	// The UpdateToolStarted event carries the real start time. The renderer
	// must use it to compute the duration instead of time.Now(), which would
	// otherwise report ~1ms when the event is delivered after the tool finishes.
	var output bytes.Buffer
	renderer := New(&output, false, false, false)
	call := dora.ToolCall{ID: "call-1", Name: "bash", Input: json.RawMessage(`{"command":"sleep 1"}`)}
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageReceived,
		Message: dora.Message{Role: dora.RoleAssistant, Content: "我会等待一秒。", ToolCalls: []dora.ToolCall{call}},
	})

	startedAt := time.Now().Add(-2 * time.Second)
	renderer.Observe(dora.Update{
		Kind:      dora.UpdateToolStarted,
		ToolCall:  call,
		StartedAt: startedAt,
	})
	renderer.Observe(dora.Update{
		Kind:     dora.UpdateToolFinished,
		ToolCall: call,
		Message:  dora.Message{Role: dora.RoleTool, ToolCallID: call.ID, Content: "0"},
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
	renderer := New(&output, false, false, false)
	call := dora.ToolCall{ID: "call-1", Name: "bash", Input: json.RawMessage(`{"command":"pwd"}`)}
	renderer.Observe(dora.Update{
		Kind:    dora.UpdateMessageReceived,
		Message: dora.Message{Role: dora.RoleAssistant, Content: "我会查看目录。", ToolCalls: []dora.ToolCall{call}},
	})
	renderer.Observe(dora.Update{Kind: dora.UpdateToolStarted, ToolCall: call})
	renderer.Observe(dora.Update{
		Kind:     dora.UpdateToolFinished,
		ToolCall: call,
		Message:  dora.Message{Role: dora.RoleTool, ToolCallID: call.ID, Content: "0"},
	})
	if !strings.Contains(output.String(), "ms") {
		t.Fatalf("output %q does not report a duration when StartedAt is zero", output.String())
	}
}
