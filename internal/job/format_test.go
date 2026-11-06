package job

import (
	"testing"
	"time"
)

func TestFormatFinished(t *testing.T) {
	tests := []struct {
		name     string
		exit     int
		stdout   string
		stderr   string
		canceled bool
		want     string
	}{
		{name: "stdout only", stdout: "hello\n", want: "hello\n"},
		{name: "no output", want: "(no output)"},
		{name: "stderr after stdout", stdout: "out", stderr: "warn", want: "out\n[stderr]\nwarn"},
		{name: "stderr only", stderr: "warn", want: "[stderr]\nwarn"},
		{name: "stdout newline kept", stdout: "out\n", stderr: "warn", want: "out\n[stderr]\nwarn"},
		{name: "failure", exit: 7, stderr: "problem", want: "[exit code 7]\n[stderr]\nproblem"},
		{name: "failure no output", exit: 1, want: "[exit code 1]"},
		{name: "canceled", exit: -1, stdout: "partial", canceled: true, want: "[canceled]\npartial"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatFinished(tt.exit, tt.stdout, tt.stderr, tt.canceled); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatBackgroundAndSnapshot(t *testing.T) {
	got := FormatBackground("bash_0", 10*time.Second, "so far", "")
	want := "[background job bash_0: still running after 10s; use the job tool to poll or kill it]\nso far"
	if got != want {
		t.Fatalf("background = %q, want %q", got, want)
	}
	command := Snapshot{ID: "bash_0", Kind: KindCommand, Status: StatusDone, ExitCode: 2, Stdout: "x", Stderr: "y"}
	if got, want := FormatSnapshot(command), "[job bash_0: done, exit code 2]\nx\n[stderr]\ny"; got != want {
		t.Fatalf("snapshot = %q, want %q", got, want)
	}
	running := Snapshot{ID: "bash_1", Kind: KindCommand, Status: StatusRunning}
	if got, want := FormatSnapshot(running), "[job bash_1: running]\n(no output)"; got == want {
		t.Fatalf("an empty running job must not look like a finished no-output command: %q", got)
	}
	if got, want := FormatSnapshot(running), "[job bash_1: running]"; got != want {
		t.Fatalf("running snapshot = %q, want %q", got, want)
	}
	task := Snapshot{ID: "task_0", Kind: KindTask, Status: StatusFailed, Error: "boom"}
	if got, want := FormatSnapshot(task), "[job task_0: failed]\nerror: boom"; got != want {
		t.Fatalf("task = %q, want %q", got, want)
	}
	if got := FormatJobList(nil); got != "(no jobs)" {
		t.Fatalf("empty list = %q", got)
	}
	list := []Snapshot{{ID: "a", Status: StatusRunning}, {ID: "b", Status: StatusDone}}
	if got, want := FormatJobList(list), "a running\nb done"; got != want {
		t.Fatalf("list = %q, want %q", got, want)
	}
}

func TestParseResultInvertsFormats(t *testing.T) {
	r := ParseResult(FormatFinished(0, "out", "err", false))
	if !r.HasExit || r.ExitCode != 0 || r.Stdout != "out" || r.Stderr != "err" {
		t.Fatalf("clean = %#v", r)
	}
	r = ParseResult(FormatFinished(3, "", "bad", false))
	if !r.HasExit || r.ExitCode != 3 || r.Stdout != "" || r.Stderr != "bad" {
		t.Fatalf("failed = %#v", r)
	}
	r = ParseResult(FormatFinished(-1, "p", "", true))
	if !r.Canceled || r.Stdout != "p" {
		t.Fatalf("canceled = %#v", r)
	}
	r = ParseResult(FormatBackground("bash_0", time.Second, "x", "y"))
	if !r.Background || r.JobID != "bash_0" || r.Stdout != "x" || r.Stderr != "y" {
		t.Fatalf("background = %#v", r)
	}
	r = ParseResult(FormatSnapshot(Snapshot{ID: "bash_0", Kind: KindCommand, Status: StatusFailed, ExitCode: 4}))
	if r.JobID != "bash_0" || r.Status != "failed" || !r.HasExit || r.ExitCode != 4 || r.Background {
		t.Fatalf("job = %#v", r)
	}
	r = ParseResult(FormatSnapshot(Snapshot{ID: "bash_0", Kind: KindCommand, Status: StatusRunning}))
	if !r.Background || r.Status != "running" || r.HasExit {
		t.Fatalf("running job = %#v", r)
	}
	if r := ParseResult("(no output)"); r.Stdout != "" || !r.HasExit {
		t.Fatalf("no output = %#v", r)
	}
	if r := ParseResult("[1, 2, 3]\n"); r.Stdout != "[1, 2, 3]\n" || !r.HasExit {
		t.Fatalf("stdout starting with a bracket = %#v", r)
	}
}
