// Package progress renders Agent updates for the command line.
package progress

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lgxz/dora"
)

const (
	blue   = "1;34"
	yellow = "33"
	green  = "32"
	red    = "31"
	dim    = "2"
)

const commandSummaryWidth = 72

// Renderer gives semantic Agent updates a concise Dora personality.
type Renderer struct {
	output   io.Writer
	color    bool
	terminal bool
	waiting  bool
	// reasoning tracks whether reasoning has been streamed in the current
	// round, so the assistant message that follows starts on a fresh line.
	reasoning bool
	tools      map[string]toolRun
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

// Session reports the SQLite history file used by this turn.
func (r *Renderer) Session(path string) {
	if r == nil || r.output == nil {
		return
	}
	fmt.Fprintf(r.output, "%s Session \"%s\"\n", r.paint(blue, "⌁"), path)
}

// Observe implements dora.Observer.
func (r *Renderer) Observe(update dora.Update) {
	if r == nil || r.output == nil {
		return
	}

	switch update.Kind {
	case dora.UpdateThinking:
		r.renderThinking()
	case dora.UpdateReasoningDelta:
		r.renderReasoning(update.Delta)
	case dora.UpdateMessageAdded:
		switch update.Message.Role {
		case dora.RoleAssistant:
			r.renderAssistantMessage(update.Message)
		case dora.RoleTool:
			r.renderToolResult(update.Message)
		}
	case dora.UpdateToolStarted:
		r.startTool(update)
	case dora.UpdateToolFailed:
		r.renderToolFailure(update)
	}
}

func (r *Renderer) renderThinking() {
	if r.terminal {
		fmt.Fprintf(r.output, "%s Thinking...\n", r.paint(blue, "●"))
	} else {
		fmt.Fprintln(r.output, "dora: thinking...")
	}
	r.waiting = true
	r.reasoning = false
}

// renderReasoning streams the model's chain-of-thought in dim style. The
// "Thinking..." placeholder is replaced by the first delta of a round; the
// reasoning text itself is never erased afterwards.
func (r *Renderer) renderReasoning(delta string) {
	if r.terminal && r.waiting {
		r.waiting = false
		fmt.Fprint(r.output, "\x1b[1A\r\x1b[2K")
	}
	if !r.reasoning {
		r.reasoning = true
		fmt.Fprint(r.output, r.paint(dim, "○ "))
	}
	fmt.Fprint(r.output, r.paint(dim, delta))
}

func (r *Renderer) renderAssistantMessage(message dora.Message) {
	if r.reasoning {
		r.reasoning = false
		fmt.Fprintln(r.output)
	}
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
	lines := strings.Split(strings.TrimRight(message.Content, "\n"), "\n")
	fmt.Fprintf(r.output, "%s %s\n", r.paint(blue, "●"), lines[0])
	for _, line := range lines[1:] {
		fmt.Fprintf(r.output, "%s %s\n", r.paint(blue, "│"), line)
	}
}

func (r *Renderer) startTool(update dora.Update) {
	call := update.ToolCall
	run, ok := r.tools[call.ID]
	if !ok {
		run = toolRun{call: call}
	}
	run.call = call
	// Prefer the real start time carried by the event; fall back to now for
	// callers that do not populate StartedAt.
	run.started = update.StartedAt
	if run.started.IsZero() {
		run.started = time.Now()
	}
	r.tools[call.ID] = run
}

func (r *Renderer) renderToolResult(message dora.Message) {
	run, ok := r.tools[message.ToolCallID]
	if !ok {
		return
	}
	delete(r.tools, message.ToolCallID)
	r.renderToolLine(presentTool(run.call, message), formatDuration(time.Since(run.started)))
}

func (r *Renderer) renderToolFailure(update dora.Update) {
	call := update.ToolCall
	run, ok := r.tools[call.ID]
	delete(r.tools, call.ID)
	if !ok {
		run = toolRun{call: call}
	}
	duration := "failed"
	if !run.started.IsZero() {
		duration = formatDuration(time.Since(run.started))
	}
	r.renderToolLine(presentToolFailure(run.call, update.Err), duration)
}

func (r *Renderer) renderToolLine(presentation toolPresentation, duration string) {
	statusColor := green
	marker := "•"
	switch presentation.outcome {
	case outcomeWarning:
		statusColor = yellow
		marker = "△"
	case outcomeFailure:
		statusColor = red
		marker = "△"
	}

	fmt.Fprint(r.output, r.paint(statusColor, marker))
	if presentation.name != "" {
		fmt.Fprint(r.output, " ", r.paint(yellow, presentation.name))
	}
	summary := presentation.summary
	if presentation.name == "" {
		summary = fitDisplayWidth(summary, commandSummaryWidth)
	}
	if summary != "" {
		fmt.Fprint(r.output, " ", r.paint(dim, summary))
	}
	if presentation.result != "" {
		fmt.Fprint(r.output, " ", r.paint(statusColor, "· "+presentation.result))
	}
	fmt.Fprintln(r.output, " "+r.paint(dim, "· "+duration))
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
