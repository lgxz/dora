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
	output   io.Writer
	color    bool
	terminal bool
	waiting  bool
	tools    map[string]toolRun
}

type toolRun struct {
	call    dora.ToolCall
	started time.Time
}

// New creates a progress Renderer. Terminal enables in-place status updates;
// color controls ANSI styling independently so NO_COLOR can be respected.
func New(output io.Writer, terminal, color bool) *Renderer {
	return &Renderer{output: output, terminal: terminal, color: color, tools: make(map[string]toolRun)}
}

// Session reports whether a named conversation is new or being continued.
func (r *Renderer) Session(name string, resumed bool) {
	if r == nil || r.output == nil {
		return
	}
	action := "开始任务"
	if resumed {
		action = "继续任务"
	}
	fmt.Fprintf(r.output, "%s %s「%s」\n", r.paint(blue, "⌁"), action, name)
}

// FreshSession reports that an existing name is starting without its history.
func (r *Renderer) FreshSession(name string) {
	if r == nil || r.output == nil {
		return
	}
	fmt.Fprintf(r.output, "%s 重新开始任务「%s」\n", r.paint(blue, "⌁"), name)
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
	if r.terminal {
		fmt.Fprintf(r.output, "%s 正在想办法…\n", r.paint(blue, "●"))
	} else {
		fmt.Fprintln(r.output, "dora: thinking...")
	}
	r.waiting = true
}

func (r *Renderer) renderAssistantMessage(message dora.Message) {
	r.replaceThinking(message)
	if len(message.ToolCalls) == 0 {
		return
	}

	for _, call := range message.ToolCalls {
		r.tools[call.ID] = toolRun{
			call: call,
		}
	}
}

func (r *Renderer) replaceThinking(message dora.Message) {
	if r.waiting {
		r.waiting = false
		if r.terminal {
			fmt.Fprint(r.output, "\x1b[1A\r\x1b[2K")
		}
	}
	if message.Content == "" || len(message.ToolCalls) == 0 {
		return
	}
	lines := strings.Split(message.Content, "\n")
	fmt.Fprintf(r.output, "%s %s\n", r.paint(blue, "●"), lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(r.output, "%s %s\n", r.paint(blue, "│"), line)
	}
}

func (r *Renderer) startTool(call dora.ToolCall) {
	run, ok := r.tools[call.ID]
	if !ok {
		run = toolRun{call: call}
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
		run = toolRun{call: call}
	}
	r.renderToolLine(run, red, "遇到了一点状况")
}

func (r *Renderer) renderToolLine(run toolRun, statusColor, status string) {
	label := run.call.Name
	marker := "•"
	if statusColor == red {
		marker = "△"
	}
	fmt.Fprintf(
		r.output,
		"%s %s %s %s\n",
		r.paint(statusColor, marker),
		r.paint(yellow, label),
		r.paint(dim, "· "+toolSummary(run.call)),
		r.paint(statusColor, "· "+status),
	)
}

func toolSummary(call dora.ToolCall) string {
	if call.Name == "skill" {
		var input struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(call.Input, &input) == nil && input.Name != "" {
			return input.Name
		}
	}
	if call.Name == "bash" || call.Name == "powershell" {
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
