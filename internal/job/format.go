package job

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The command and job tools return plain text rather than JSON so the model
// reads output exactly as printed (no quote or newline escaping, no wrapper
// keys). The text is:
//
//	[header line]        only when something besides a clean exit needs saying
//	<stdout>
//	[stderr]             only when stderr is not empty
//	<stderr>
//
// A clean exit with stdout only is just the stdout. A clean exit that printed
// nothing is "(no output)".
const (
	stderrMarker = "[stderr]"
	noOutput     = "(no output)"
)

// Result is the display form of a rendered command or job result. It is
// produced by ParseResult, which inverts the text formats below on a best
// effort basis (for progress display and tests; the model reads the text).
type Result struct {
	ExitCode int
	// HasExit reports whether the text stated an exit code.
	HasExit  bool
	Canceled bool
	// JobID is set for a command moved to the background and for job results.
	JobID string
	// Background reports a command that is still running as a background job.
	Background bool
	// Status is the job status for job results ("" otherwise).
	Status string
	Stdout string
	Stderr string
}

// FormatFinished renders a command that ran to completion, or was canceled
// (exit code ignored, header "[canceled]").
func FormatFinished(exitCode int, stdout, stderr string, canceled bool) string {
	switch {
	case canceled:
		return joinHeader("[canceled]", stdout, stderr)
	case exitCode != 0:
		return joinHeader(fmt.Sprintf("[exit code %d]", exitCode), stdout, stderr)
	}
	return joinHeader("", stdout, stderr)
}

// FormatBackground renders a command that did not finish within wait and is
// now running as the background job id.
func FormatBackground(id string, wait time.Duration, stdout, stderr string) string {
	header := fmt.Sprintf("[background job %s: still running after %s; use the job tool to poll or kill it]", id, wait)
	return joinHeader(header, stdout, stderr)
}

// FormatBackgroundTask renders the reply to a task started in the background.
func FormatBackgroundTask(id string) string {
	return fmt.Sprintf("[background job %s: still running; use the job tool to poll or kill it, and check it before exiting]", id)
}

// FormatSnapshot renders a polled job: command output, or a task result.
func FormatSnapshot(s Snapshot) string {
	header := fmt.Sprintf("[job %s: %s", s.ID, s.Status)
	if s.Kind == KindCommand && s.Status != StatusRunning && s.Status != StatusCancelling {
		header += fmt.Sprintf(", exit code %d", s.ExitCode)
	}
	header += "]"
	if s.Kind == KindTask {
		body := s.Result
		if s.Error != "" {
			body = "error: " + s.Error
		}
		return joinHeader(header, body, "")
	}
	return joinHeader(header, s.Stdout, s.Stderr)
}

// FormatJobList renders the job list, one "id status" line per job.
func FormatJobList(snapshots []Snapshot) string {
	if len(snapshots) == 0 {
		return "(no jobs)"
	}
	lines := make([]string, len(snapshots))
	for i, s := range snapshots {
		lines[i] = s.ID + " " + string(s.Status)
	}
	return strings.Join(lines, "\n")
}

func joinHeader(header, stdout, stderr string) string {
	var b strings.Builder
	b.WriteString(header)
	body := stdout
	if stderr != "" {
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += stderrMarker + "\n" + stderr
	}
	if body != "" {
		if header != "" {
			b.WriteByte('\n')
		}
		b.WriteString(body)
	}
	if b.Len() == 0 {
		return noOutput
	}
	return b.String()
}

var (
	headerExit       = regexp.MustCompile(`^\[exit code (-?\d+)\]`)
	headerBackground = regexp.MustCompile(`^\[background job (\S+): `)
	headerJob        = regexp.MustCompile(`^\[job (\S+): (\w+)(?:, exit code (-?\d+))?\]`)
)

// ParseResult recovers the display fields of text produced by FormatFinished,
// FormatBackground or FormatSnapshot. It is best effort: stdout loses one
// trailing newline when stderr follows, and a stdout that itself contains a
// "[stderr]" line is split there.
func ParseResult(text string) Result {
	var r Result
	body := text
	if m := headerExit.FindStringSubmatch(text); m != nil {
		r.ExitCode, _ = strconv.Atoi(m[1])
		r.HasExit = true
		body = after(text, m[0])
	} else if strings.HasPrefix(text, "[canceled]") {
		r.Canceled = true
		body = after(text, "[canceled]")
	} else if m := headerBackground.FindStringSubmatch(text); m != nil {
		r.JobID, r.Background = m[1], true
		end := strings.Index(text, "]\n")
		if end < 0 {
			end = len(text) - 1
		}
		body = after(text, text[:end+1])
	} else if m := headerJob.FindStringSubmatch(text); m != nil {
		r.JobID, r.Status = m[1], m[2]
		if m[3] != "" {
			r.ExitCode, _ = strconv.Atoi(m[3])
			r.HasExit = true
		}
		r.Background = m[2] == string(StatusRunning) || m[2] == string(StatusCancelling)
		body = after(text, m[0])
	} else {
		r.HasExit = true // a clean exit has no header
	}
	if body == noOutput {
		body = ""
	}
	switch {
	case strings.HasPrefix(body, stderrMarker+"\n"):
		r.Stderr = strings.TrimPrefix(body, stderrMarker+"\n")
	default:
		if i := strings.Index(body, "\n"+stderrMarker+"\n"); i >= 0 {
			r.Stdout, r.Stderr = body[:i], body[i+len(stderrMarker)+2:]
		} else {
			r.Stdout = body
		}
	}
	return r
}

// after drops prefix and the single newline that separates a header from the
// body.
func after(text, prefix string) string {
	return strings.TrimPrefix(strings.TrimPrefix(text, prefix), "\n")
}
