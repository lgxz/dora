package app

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunContextUsesLocalDateAndWorkingDirectory(t *testing.T) {
	// The local date differs from UTC to catch accidental UTC conversion.
	now := time.Date(2026, 9, 10, 0, 30, 0, 0, time.FixedZone("local", 2*60*60))
	got := RunContext("/work/project", now, -1)
	want := "<run_context>\nDate (local): 2026-09-10\nWorking directory: /work/project\n</run_context>"
	if got != want {
		t.Fatalf("run context = %q, want %q", got, want)
	}
}

func TestRunContextFallsBackToProcessDirectory(t *testing.T) {
	directory, err := os.Getwd()
	if err != nil {
		t.Skip("no process working directory")
	}
	got := RunContext("", time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), -1)
	if !strings.Contains(got, "Working directory: "+directory+"\n") {
		t.Fatalf("run context = %q, want process directory %q", got, directory)
	}
}

func TestRunContextSessionLine(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	prefix := "<run_context>\nDate (local): 2026-09-10\nWorking directory: /work\n"
	for _, test := range []struct {
		name  string
		saved int
		want  string
	}{
		{"unknown", -1, ""},
		{"new session", 0, "Session: new, no earlier turns.\n"},
		{"one turn", 1, "Session: 1 earlier turn is saved but is not part of this conversation; use the history tool to read it (turn 1).\n"},
		{"several turns", 3, "Session: 3 earlier turns are saved but are not part of this conversation; use the history tool to read them (latest: turn 3).\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := RunContext("/work", now, test.saved)
			want := prefix + test.want + "</run_context>"
			if got != want {
				t.Fatalf("run context = %q, want %q", got, want)
			}
		})
	}
}
