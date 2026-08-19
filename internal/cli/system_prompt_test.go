package cli

import (
	"os"
	"path/filepath"
	"testing"
)

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
