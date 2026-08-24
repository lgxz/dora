package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveWorkingDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("directory = %q, want %q", got, want)
	}
	if empty, err := resolveWorkingDirectory(""); err != nil || empty != "" {
		t.Fatalf("empty directory = %q, err = %v", empty, err)
	}
}

func TestResolveWorkingDirectoryRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file.txt")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := resolveWorkingDirectory(path)
	if err == nil || !strings.Contains(err.Error(), "is not a directory") {
		t.Fatalf("error = %v", err)
	}
}
