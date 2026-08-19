package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgxz/dora/internal/progress"
)

func TestInfoSilencedWhenObserverNil(t *testing.T) {
	// A nil observer (--quiet) must not emit anything and must not panic.
	info(nil, "should be silent %d", 42)
}

func TestInfoEmitsThroughObserver(t *testing.T) {
	var output bytes.Buffer
	observer := progress.New(&output, false, false)
	info(observer, "Session %q", "system-status.sqlite")
	if !strings.Contains(output.String(), "Session \"system-status.sqlite\"") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSystemPromptReadsAgentsFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("Be concise."), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := systemPrompt(); got != "Be concise." {
		t.Fatalf("systemPrompt() = %q, want %q", got, "Be concise.")
	}
}

func TestSystemPromptEmptyWhenAgentsFileMissing(t *testing.T) {
	t.Setenv("DORA_HOME", t.TempDir())

	if got := systemPrompt(); got != "" {
		t.Fatalf("systemPrompt() = %q, want empty", got)
	}
}
