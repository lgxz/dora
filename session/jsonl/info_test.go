package jsonl

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/session"
)

func saveCompletedTurn(t *testing.T, store *Store, prompt string) {
	t.Helper()
	turn := dora.NewTurn(prompt)
	if err := turn.Complete("done", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginTurn(turn).Finish(context.Background(), session.TurnStatusCompleted, nil); err != nil {
		t.Fatal(err)
	}
}

func TestReadInfoReportsWorkdirAndFirstPrompt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	store, err := Open(ctx, path, WithWorkdir("/work/project"))
	if err != nil {
		t.Fatal(err)
	}
	saveCompletedTurn(t, store, "first question")
	saveCompletedTurn(t, store, "second question")

	// The file is still open, so this also shows that reading takes no lock.
	info, err := ReadInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Path != path || info.Workdir != "/work/project" || info.Prompt != "first question" || info.Empty || info.ModTime.IsZero() {
		t.Fatalf("info = %#v", info)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadInfoOfFileWithoutTurns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	store, err := Open(ctx, path, WithWorkdir("/work"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := ReadInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Empty || info.Prompt != "" || info.Workdir != "/work" {
		t.Fatalf("info = %#v", info)
	}
}

func TestReadInfoKeepsALongFirstRecordNonEmpty(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	saveCompletedTurn(t, store, strings.Repeat("x", 2*infoBufferSize))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// A prompt too long to parse must never make the session look empty,
	// because an empty session may be deleted.
	info, err := ReadInfo(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Empty || info.Prompt != "" {
		t.Fatalf("info = %#v", info)
	}
}

func TestReadInfoRejectsOtherFiles(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"empty.jsonl":   "",
		"text.jsonl":    "hello\n",
		"torn.jsonl":    `{"t":"header","v":1`,
		"version.jsonl": `{"t":"header","v":2}` + "\n",
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadInfo(path); err == nil {
			t.Errorf("ReadInfo(%s) accepted %q", name, content)
		}
	}
	if _, err := ReadInfo(dir); err == nil {
		t.Error("ReadInfo accepted a directory")
	}
	if _, err := ReadInfo(filepath.Join(dir, "missing.jsonl")); err == nil {
		t.Error("ReadInfo accepted a missing file")
	}
}

func TestWorkdirIsOnlyWrittenToNewFiles(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	store, err := Open(ctx, path, WithWorkdir("/first"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(ctx, path, WithWorkdir("/second"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := ReadInfo(path)
	if err != nil || info.Workdir != "/first" {
		t.Fatalf("info = %#v, err = %v", info, err)
	}
}

func TestHeaderWithoutWorkdirStaysMinimal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("{\"t\":\"header\",\"v\":1}\n")) {
		t.Fatalf("header = %q", data)
	}
}
