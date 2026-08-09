// Package progress renders Agent updates for the command line.
package progress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"dora"
)

const maxSummaryRunes = 72

const (
	blue   = "1;34"
	yellow = "33"
	green  = "32"
	red    = "31"
	dim    = "2"
)

// Renderer gives semantic Agent updates a concise Dora personality.
type Renderer struct {
	output        io.Writer
	color         bool
	thinkingCount int
	tools         map[string]toolRun
}

type toolRun struct {
	call    dora.ToolCall
	started time.Time
	index   int
	total   int
}

// New creates a progress Renderer. Color should only be enabled for a terminal.
func New(output io.Writer, color bool) *Renderer {
	return &Renderer{output: output, color: color, tools: make(map[string]toolRun)}
}

// Observe implements dora.Observer.
func (r *Renderer) Observe(update dora.Update) {
	if r == nil || r.output == nil {
		return
	}

	switch update.Kind {
	case dora.UpdateThinking:
		r.renderThinking()
	case dora.UpdateMessageAdded:
		switch update.Message.Role {
		case dora.RoleAssistant:
			r.renderAssistantMessage(update.Message)
		case dora.RoleTool:
			r.renderToolResult(update.Message.ToolCallID)
		}
	case dora.UpdateToolStarted:
		r.startTool(update.ToolCall)
	case dora.UpdateToolFailed:
		r.renderToolFailure(update.ToolCall)
	}
}

func (r *Renderer) renderThinking() {
	if r.thinkingCount > 0 {
		fmt.Fprintln(r.output)
	}
	phrase := "让我想想办法…"
	if r.thinkingCount > 0 {
		phrase = "我再整理一下…"
	}
	fmt.Fprintf(r.output, "%s  %s\n", r.paint(blue, "● dora"), phrase)
	r.thinkingCount++
}

func (r *Renderer) renderAssistantMessage(message dora.Message) {
	if len(message.ToolCalls) == 0 {
		return
	}
	if message.Content != "" {
		for _, line := range strings.Split(message.Content, "\n") {
			fmt.Fprintf(r.output, "%s %s\n", r.paint(blue, "│"), line)
		}
	}

	if len(message.ToolCalls) == 1 {
		fmt.Fprintf(
			r.output,
			"%s 准备使用 %s\n",
			r.paint(yellow, "╭"),
			r.paint(yellow, message.ToolCalls[0].Name),
		)
	} else {
		fmt.Fprintf(
			r.output,
			"%s 这次准备了 %d 次道具调用\n",
			r.paint(yellow, "╭"),
			len(message.ToolCalls),
		)
	}

	for index, call := range message.ToolCalls {
		r.tools[call.ID] = toolRun{
			call:  call,
			index: index + 1,
			total: len(message.ToolCalls),
		}
	}
}

func (r *Renderer) startTool(call dora.ToolCall) {
	run, ok := r.tools[call.ID]
	if !ok {
		run = toolRun{call: call, index: 1, total: 1}
	}
	run.call = call
	run.started = time.Now()
	r.tools[call.ID] = run
}

func (r *Renderer) renderToolResult(id string) {
	run, ok := r.tools[id]
	if !ok {
		fmt.Fprintf(r.output, "%s 道具带回了结果\n", r.paint(green, "╰"))
		return
	}
	delete(r.tools, id)
	r.renderToolLine(run, green, formatDuration(time.Since(run.started)))
}

func (r *Renderer) renderToolFailure(call dora.ToolCall) {
	run, ok := r.tools[call.ID]
	delete(r.tools, call.ID)
	if !ok {
		run = toolRun{call: call, index: 1, total: 1}
	}
	r.renderToolLine(run, red, "遇到了一点状况")
}

func (r *Renderer) renderToolLine(run toolRun, statusColor, status string) {
	branch := "╰"
	label := run.call.Name
	if run.total > 1 {
		if run.index < run.total {
			branch = "├"
		}
		label = fmt.Sprintf("%d. %s", run.index, run.call.Name)
	}
	fmt.Fprintf(
		r.output,
		"%s %s %s %s\n",
		r.paint(statusColor, branch),
		r.paint(yellow, label),
		r.paint(dim, "· "+toolSummary(run.call)),
		r.paint(statusColor, "· "+status),
	)
}

func toolSummary(call dora.ToolCall) string {
	if call.Name == "bash" {
		var input struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(call.Input, &input) == nil && input.Command != "" {
			return truncate(strings.Join(strings.Fields(input.Command), " "), maxSummaryRunes)
		}
	}

	var compact bytes.Buffer
	if json.Compact(&compact, call.Input) == nil && compact.Len() > 0 {
		return truncate(compact.String(), maxSummaryRunes)
	}
	return "参数已准备"
}

func truncate(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit-1]) + "…"
}

func (r *Renderer) paint(code, value string) string {
	if !r.color {
		return value
	}
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func formatDuration(duration time.Duration) string {
	if duration < time.Second {
		milliseconds := duration.Milliseconds()
		if milliseconds < 1 {
			milliseconds = 1
		}
		return fmt.Sprintf("%dms", milliseconds)
	}
	return fmt.Sprintf("%.1fs", duration.Seconds())
}
