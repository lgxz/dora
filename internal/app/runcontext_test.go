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
	got := RunContext("/work/project", now, false)
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
	got := RunContext("", time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC), false)
	if !strings.Contains(got, "Working directory: "+directory+"\n") {
		t.Fatalf("run context = %q, want process directory %q", got, directory)
	}
}

func TestRunContextMentionsEarlierTurnsOnlyWhenThereAreSome(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	if got := RunContext("/work", now, false); strings.Contains(got, "history") {
		t.Fatalf("fresh session run context = %q", got)
	}
	got := RunContext("/work", now, true)
	want := "Working directory: /work\nSession: earlier turns are saved but are not part of this conversation; use the history tool to read them.\n</run_context>"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("run context = %q, want suffix %q", got, want)
	}
}
