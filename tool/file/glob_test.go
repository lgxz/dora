package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bmatcuk/doublestar/v4"
)

func TestGlobTool(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "utils.py"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.py","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "main.py") || !strings.Contains(out, "utils.py") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "data.txt") {
		t.Fatalf("data.txt should not match, out = %q", out)
	}
}

func TestGlobToolRecursive(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models", "trainer.py"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool()
	// **/*.py should match files in all subdirectories.
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"**/*.py","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "main.py") || !strings.Contains(out, "trainer.py") {
		t.Fatalf("out = %q", out)
	}
}

func TestGlobToolIgnoreDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".venv"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".venv", "lib.py"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"**/*.py","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "main.py") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, ".venv") {
		t.Fatalf(".venv should be ignored, out = %q", out)
	}
}

func TestGlobToolNoMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("out = %q", out)
	}
}

func TestGlobToolMissingPath(t *testing.T) {
	tool := NewGlobTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.py","path":"/nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

// TestGlobToSlashPathSeparator verifies the separator contract the GlobTool
// fix relies on: doublestar.Match hardcodes '/' as the path separator, so a
// Windows-style backslash relative path (produced by filepath.Rel on Windows)
// only matches a forward-slash pattern after filepath.ToSlash. filepath.ToSlash
// is a no-op on Unix, so this test asserts the underlying doublestar behavior
// directly instead of constructing a real Windows filesystem tree.
func TestGlobToSlashPathSeparator(t *testing.T) {
	// A raw backslash path is treated as a single segment and must not match
	// the multi-segment "tests/**/*.py" pattern.
	if matched, err := doublestar.Match("tests/**/*.py", `tests\unit\x.py`); err != nil {
		t.Fatal(err)
	} else if matched {
		t.Fatal("backslash path should not match tests/**/*.py before normalize")
	}
	// The same path after filepath.ToSlash ("/"-separated, as produced on
	// Windows by the GlobTool fix) does match the recursive pattern.
	matched, err := doublestar.Match("tests/**/*.py", "tests/unit/x.py")
	if err != nil {
		t.Fatal(err)
	}
	if !matched {
		t.Fatal("slash-normalized path should match tests/**/*.py")
	}
}

// ============================================================================
// Glob edge case tests
// ============================================================================

// TestGlobMaxResults verifies max_results caps the number of returned lines.
func TestGlobMaxResults(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		if err := os.WriteFile(filepath.Join(dir, "f"+string(rune('a'+i))+".go"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go","path":"`+dir+`","max_results":3}`))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 results, got %d: %q", len(lines), out)
	}
}

// TestGlobMaxResultsUnset verifies the default cap is enforced when not given.
func TestGlobMaxResultsDefault(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < defaultGlobMaxResults+5; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.go", i)), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got != defaultGlobMaxResults {
		t.Fatalf("expected default cap %d, got %d", defaultGlobMaxResults, got)
	}
}

// TestGlobPathIsFile verifies glob errors when the path is not a directory.
func TestGlobPathIsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*","path":"`+path+`"}`))
	if err == nil {
		t.Fatal("expected error when path is a file")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestGlobMissingPattern verifies the required-pattern error.
func TestGlobMissingPattern(t *testing.T) {
	tool := NewGlobTool()
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error for missing pattern")
	}
}

// TestGlobUnknownField verifies unknown JSON fields are rejected.
func TestGlobUnknownField(t *testing.T) {
	tool := NewGlobTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go","bogus":1}`))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

// TestGlobMultipleJSONValues verifies trailing JSON is rejected.
func TestGlobMultipleJSONValues(t *testing.T) {
	tool := NewGlobTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go"} {"x":1}`))
	if err == nil {
		t.Fatal("expected error for multiple JSON values")
	}
}

// TestGlobInvalidMaxResults verifies invalid max_results values are rejected.
func TestGlobInvalidMaxResults(t *testing.T) {
	tool := NewGlobTool()
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go","max_results":0}`)); err == nil {
		t.Fatal("expected error for max_results=0")
	}
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go","max_results":-1}`)); err == nil {
		t.Fatal("expected error for max_results=-1")
	}
}

// TestGlobDefaultPath verifies the path defaults to the current directory.
func TestGlobDefaultPath(t *testing.T) {
	tool := NewGlobTool()
	// glob.go itself exists in the package directory, so searching "." (the
	// test working directory = the package dir) must find it.
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"glob.go"}`))
	if err != nil {
		t.Fatal(err)
	}
	// When the search root is ".", WalkDir yields relative paths (e.g.
	// "glob.go") rather than absolute paths. The tool's normal contract is
	// to emit paths under the search root, so both forms are valid depending
	// on the root given; here we just confirm the file was found.
	if !strings.Contains(out, "glob.go") {
		t.Fatalf("expected glob.go, got %q", out)
	}
}

// TestGlobCharacterClass verifies bracket expressions work.
func TestGlobCharacterClass(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.log", "b.log", "c.txt", "d.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"[ab].log","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.log") || !strings.Contains(out, "b.log") {
		t.Fatalf("expected a.log and b.log, got %q", out)
	}
	if strings.Contains(out, "c.txt") || strings.Contains(out, "d.txt") {
		t.Fatalf("unexpected matches: %q", out)
	}
}

// TestGlobBraceExpansion verifies doublestar brace alternation works.
func TestGlobBraceExpansion(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"config.yaml", "config.yml", "other.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"config.{yaml,yml}","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "config.yaml") || !strings.Contains(out, "config.yml") {
		t.Fatalf("expected both config extensions, got %q", out)
	}
}

// TestGlobSingleStarDoesNotCrossDirs verifies that a single * does not match a
// path separator (no recursive matching).
func TestGlobSingleStarDoesNotCrossDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "top.go"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sub", "nested.go"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"*.go","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "top.go") {
		t.Fatalf("expected top.go, got %q", out)
	}
	if strings.Contains(out, "nested.go") {
		t.Fatalf("single * must not reach nested.go, got %q", out)
	}
}

// TestGlobIgnoreDirs verifies several ignore directories are skipped.
func TestGlobIgnoreDirs(t *testing.T) {
	dir := t.TempDir()
	for _, ignored := range []string{"node_modules", "target", "build", "dist", ".cache", "__pycache__"} {
		if err := os.MkdirAll(filepath.Join(dir, ignored), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ignored, "x.go"), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.go"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"**/*.go","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "keep.go") {
		t.Fatalf("expected keep.go, got %q", out)
	}
	count := strings.Count(strings.ReplaceAll(out, dir+string(os.PathSeparator), ""), ".go")
	if count != 1 {
		t.Fatalf("ignored dirs should produce 0 results, but out = %q", out)
	}
}

// TestGlobHiddenDotFile verifies dotfiles can be matched via .* pattern.
func TestGlobHiddenDotFile(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".env", "visible.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(""), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := NewGlobTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":".env","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, ".env") {
		t.Fatalf("expected .env, got %q", out)
	}
	if strings.Contains(out, "visible.txt") {
		t.Fatalf("visible.txt should not match, got %q", out)
	}
}
