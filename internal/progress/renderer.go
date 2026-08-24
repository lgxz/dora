// Package progress renders Agent updates for the command line.
package progress

import (
	"bytes"
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

// modelStatusInterval limits in-place terminal writes while model deltas are
// arriving. Observer callbacks run synchronously with the model stream, so
// repainting for every delta can slow generation on a busy or remote terminal.
const modelStatusInterval = 250 * time.Millisecond

// reasoningFlushBytes bounds how much reasoning may buffer without a newline
// before it is flushed anyway, so a pathologically long single line still
// streams.
const reasoningFlushBytes = 4 << 10

// Renderer gives semantic Agent updates a concise Dora personality.
type Renderer struct {
	output   io.Writer
	color    bool
	terminal bool
	// showReasoning controls whether reasoning deltas are streamed. Streaming
	// them writes to the output for every reasoning token, which can slow the
	// run on slow terminals, so display is opt-in (--reasoning).
	showReasoning bool
	waiting       bool
	// modelStarted, modelBytes, and modelStatusAt track the current model call
	// so hidden streaming output can update the Thinking line with useful live
	// progress without exposing reasoning content.
	modelStarted  time.Time
	modelBytes    int
	modelStatusAt time.Time
	// reasoning tracks whether reasoning has been streamed in the current
	// round, so the assistant message that follows starts on a fresh line.
	reasoning bool
	// pending holds reasoning bytes that have not formed a complete line yet.
	pending []byte
	tools   map[string]toolRun
}

type toolRun struct {
	call    dora.ToolCall
	started time.Time
}

// New creates a progress Renderer. Terminal enables in-place status updates;
// color controls ANSI styling independently so NO_COLOR can be respected.
// showReasoning enables streaming captured model reasoning (opt-in because
// per-token writes slow runs on slow terminals).
func New(output io.Writer, terminal, color, showReasoning bool) *Renderer {
	return &Renderer{output: output, terminal: terminal, color: color, showReasoning: showReasoning, tools: make(map[string]toolRun)}
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
	case dora.UpdateContentDelta:
		r.receiveModelDelta(update.Delta, !r.reasoning)
	case dora.UpdateReasoningDelta:
		if r.showReasoning {
			r.receiveModelDelta(update.Delta, false)
			r.renderReasoning(update.Delta)
		} else {
			r.receiveModelDelta(update.Delta, true)
		}
	case dora.UpdateMessageReceived:
		r.renderAssistantMessage(update.Message)
	case dora.UpdateToolStarted:
		r.startTool(update)
	case dora.UpdateToolFinished:
		r.renderToolFinished(update)
	}
}

func (r *Renderer) renderThinking() {
	// A round that aborted without an assistant message still ends its
	// reasoning run here, so the next placeholder starts on a fresh line.
	r.endReasoning()
	r.modelStarted = time.Now()
	r.modelBytes = 0
	r.modelStatusAt = time.Time{}
	if r.terminal || r.color {
		fmt.Fprintf(r.output, "%s Thinking...\n", r.paint(blue, "●"))
	} else {
		fmt.Fprintln(r.output, "Thinking...")
	}
	r.waiting = true
}

// receiveModelDelta counts exact UTF-8 bytes received from content and
// reasoning streams. In terminal mode it periodically repaints the Thinking
// placeholder with elapsed time and bytes received. Non-terminal output stays
// stable, and visible reasoning suppresses the status repaint because the
// streamed reasoning itself already shows progress.
func (r *Renderer) receiveModelDelta(delta string, showStatus bool) {
	r.modelBytes += len(delta)
	if !showStatus || !r.terminal || !r.waiting || r.modelStarted.IsZero() {
		return
	}

	now := time.Now()
	if !r.modelStatusAt.IsZero() && now.Sub(r.modelStatusAt) < modelStatusInterval {
		return
	}
	r.modelStatusAt = now
	fmt.Fprint(r.output, "\x1b[1A\r\x1b[2K")
	fmt.Fprintf(r.output, "%s Thinking... %s · %s\n",
		r.paint(blue, "●"), formatDuration(now.Sub(r.modelStarted)), formatBytes(r.modelBytes))
}

// renderReasoning streams the model's chain-of-thought in dim style. The
// "Thinking..." placeholder is replaced by the first delta of a round; the
// reasoning text itself is never erased afterwards. Deltas are buffered and
// written one complete line at a time (with a size cap for lines without
// newlines), because terminal writes run on the Agent's goroutine and
// per-token writes slow the model stream on slow terminals.
func (r *Renderer) renderReasoning(delta string) {
	if r.terminal && r.waiting {
		r.waiting = false
		fmt.Fprint(r.output, "\x1b[1A\r\x1b[2K")
	}
	if !r.reasoning {
		r.reasoning = true
		fmt.Fprint(r.output, r.paint(dim, "○ "))
	}
	r.pending = append(r.pending, delta...)
	for {
		index := bytes.IndexByte(r.pending, '\n')
		if index < 0 {
			break
		}
		fmt.Fprint(r.output, r.paint(dim, string(r.pending[:index+1])))
		r.pending = r.pending[index+1:]
	}
	if len(r.pending) >= reasoningFlushBytes {
		fmt.Fprint(r.output, r.paint(dim, string(r.pending)))
		r.pending = nil
	}
}

// endReasoning writes any buffered partial line and moves following output to
// a fresh line. It is a no-op unless reasoning streamed in this round.
func (r *Renderer) endReasoning() {
	if !r.reasoning {
		return
	}
	r.reasoning = false
	if len(r.pending) > 0 {
		fmt.Fprint(r.output, r.paint(dim, string(r.pending)))
		r.pending = nil
	}
	fmt.Fprintln(r.output)
}

func (r *Renderer) renderAssistantMessage(message dora.Message) {
	r.endReasoning()
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

// renderToolFinished renders a tool's completion based on its error. A tool
// that returned a process-level error (update.Err != nil) is presented as a
// failure. A tool that executed successfully carries its result message, whose
// content the formatter inspects to pick success, warning, or failure styling
// (for example bash exit codes), so no regression to content-level signals.
func (r *Renderer) renderToolFinished(update dora.Update) {
	call := update.ToolCall
	run, ok := r.tools[call.ID]
	delete(r.tools, call.ID)
	if !ok {
		run = toolRun{call: call}
	}
	if update.Err != nil {
		duration := "failed"
		if !run.started.IsZero() {
			duration = formatDuration(time.Since(run.started))
		}
		r.renderToolLine(presentToolFailure(call, update.Err), duration)
		return
	}
	duration := "n/a"
	if !run.started.IsZero() {
		duration = formatDuration(time.Since(run.started))
	}
	r.renderToolLine(presentTool(call, update.Message), duration)
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
