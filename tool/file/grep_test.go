package file

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestGrepTool(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello world\nfoo bar\nTODO fix\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"TODO","path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "3:TODO fix") {
		t.Fatalf("out = %q", out)
	}
}

func TestGrepToolDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("match here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("nothing here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"match","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt:1:match here") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "b.txt") {
		t.Fatalf("b.txt should not match, out = %q", out)
	}
}

func TestGrepToolLiteral(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("a.b\naxb\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	// Literal match: "a.b" should only match the literal "a.b", not "axb".
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"a.b","path":"`+path+`","regex":false}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1:a.b") {
		t.Fatalf("out = %q", out)
	}
	if strings.Contains(out, "axb") {
		t.Fatalf("literal match should not match axb, out = %q", out)
	}
}

func TestGrepToolIgnoreCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("Hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"hello","path":"`+path+`","ignore_case":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1:Hello") {
		t.Fatalf("out = %q", out)
	}
}

func TestGrepToolNoMatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"missing","path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("out = %q", out)
	}
}

func TestGrepToolMissingPath(t *testing.T) {
	tool := NewGrepTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"x","path":"/nonexistent"}`))
	if err == nil {
		t.Fatal("expected error for missing path")
	}
}

// ============================================================================
// Grep edge case tests
// ============================================================================

// TestGrepRegex verifies regex mode matching.
func TestGrepRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	content := "error: disk full\njust fine\nError Again\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"^error","path":"`+path+`","regex":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1:error: disk full") {
		t.Fatalf("expected regex match, got %q", out)
	}
	if strings.Contains(out, "just fine") {
		t.Fatalf("unexpected match: %q", out)
	}
}

// TestGrepInvalidRegex verifies an invalid regex returns an error.
func TestGrepInvalidRegex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("abc\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"[abc","path":"`+path+`","regex":true}`))
	if err == nil {
		t.Fatal("expected error for invalid regex")
	}
}

// TestGrepRegexIgnoreCase verifies ignore_case combines with regex.
func TestGrepRegexIgnoreCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("HELLO world\nhi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"^hello","path":"`+path+`","regex":true,"ignore_case":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1:HELLO world") {
		t.Fatalf("expected case-insensitive regex match, got %q", out)
	}
}

// TestGrepSingleFileOutput verifies single-file matches omit the filename.
func TestGrepSingleFileOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "solo.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\nalpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"alpha","path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1:alpha") || !strings.Contains(out, "3:alpha") {
		t.Fatalf("expected line numbers only, got %q", out)
	}
	if strings.Contains(out, path) {
		t.Fatalf("single file output should not contain the path, got %q", out)
	}
}

// TestGrepMaxResults verifies max_results caps matches.
func TestGrepMaxResults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	if err := os.WriteFile(path, []byte("hit\nhit\nhit\nhit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"hit","path":"`+path+`","max_results":2}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(out), "\n")); got != 2 {
		t.Fatalf("expected 2 results, got %d: %q", got, out)
	}
}

// TestGrepLongLine verifies lines larger than the initial scanner buffer are
// still scanned (growable buffer up to maxReadBytes).
func TestGrepLongLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "long.txt")
	big := strings.Repeat("x", 200*1024) + "needle" + strings.Repeat("y", 100)
	if err := os.WriteFile(path, []byte(big+"\nsecond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"needle","path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "1:") {
		t.Fatalf("expected match on long line, got %q", out)
	}
}

// TestGrepEmptyFile verifies searching an empty file yields no matches.
func TestGrepEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"x","path":"`+path+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "no matches") {
		t.Fatalf("expected no matches, got %q", out)
	}
}

// TestGrepEmptyPattern verifies the required-pattern error.
func TestGrepEmptyPattern(t *testing.T) {
	tool := NewGrepTool()
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"","path":"/tmp"}`)); err == nil {
		t.Fatal("expected error for empty pattern")
	}
}

// TestGrepMissingPath verifies the required-path error.
func TestGrepMissingPath(t *testing.T) {
	tool := NewGrepTool()
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"x"}`)); err == nil {
		t.Fatal("expected error for missing path")
	}
}

// TestGrepUnknownField verifies unknown JSON fields are rejected.
func TestGrepUnknownField(t *testing.T) {
	tool := NewGrepTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"x","path":"/tmp","bogus":1}`))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

// TestGrepMultipleValues verifies trailing JSON is rejected.
func TestGrepMultipleValues(t *testing.T) {
	tool := NewGrepTool()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"x","path":"/tmp"} {"a":1}`))
	if err == nil {
		t.Fatal("expected error for multiple JSON values")
	}
}

// TestGrepInvalidMaxResults verifies invalid max_results values are rejected.
func TestGrepInvalidMaxResults(t *testing.T) {
	tool := NewGrepTool()
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"x","path":"/tmp","max_results":0}`)); err == nil {
		t.Fatal("expected error for max_results=0")
	}
}

// TestGrepDirectoryIgnoreDirs verifies ignore directories are skipped during a
// directory search.
func TestGrepDirectoryIgnoreDirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("match\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "node_modules", "dep.txt"), []byte("match\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"match","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "keep.txt") {
		t.Fatalf("expected keep.txt match, got %q", out)
	}
	if strings.Contains(out, "node_modules") {
		t.Fatalf("node_modules should be ignored, got %q", out)
	}
}

// TestGrepSubdirectoryMatch verifies a directory search reaches nested files.
func TestGrepSubdirectoryMatch(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "nested", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "deep", "x.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := NewGrepTool()
	out, err := tool.Execute(context.Background(), json.RawMessage(`{"pattern":"needle","path":"`+dir+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "x.txt:1:needle") {
		t.Fatalf("expected nested match, got %q", out)
	}
}

// ============================================================================
// Dual-backend consistency tests
// ============================================================================

// sortGrepLines normalizes grep output by sorting its non-empty lines. The
// unrelated ordering of matched *files* in a directory search is incidental
// (the fallback walker emits in WalkDir order while ripgrep emits in its own
// traversal order), so the consistency invariant is the *set* of matches, not
// a specific file emission order. Within each line the format must still match
// exactly, so the comparison remains strict.
func sortGrepLines(out string) string {
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	var kept []string
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	sort.Strings(kept)
	return strings.Join(kept, "\n")
}

// runGrepBothBackends runs one input through both the ripgrep backend and the
// built-in fallback, returning both outputs. The rg output is nil when ripgrep
// is not installed so tests can assert fallback as ground truth and compare rg
// when available. Keeping each scenario defined once and asserting the two
// backends agree guarantees they stay consistent.
func runGrepBothBackends(t *testing.T, raw string) (fallbackOut string, rgOut string, rgAvailable bool) {
	t.Helper()
	tool := &GrepTool{}
	input, err := decodeGrepInput(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	fallbackOut, err = tool.executeFallback(context.Background(), input)
	if err != nil {
		t.Fatalf("fallback backend: %v", err)
	}
	if _, err := exec.LookPath("rg"); err != nil {
		return fallbackOut, "", false // rg not installed: only fallback covered
	}
	rgOut, err = tool.executeWithRG(context.Background(), input)
	if err != nil {
		t.Fatalf("rg backend: %v", err)
	}
	return fallbackOut, rgOut, true
}

// TestGrepBackendsAgree drives a single set of scenarios through both the
// ripgrep and fallback backends. Fallback is always asserted as ground truth;
// when ripgrep is installed, the two outputs must agree on the same set of
// matched lines (sorted to ignore incidental file emission order).
func TestGrepBackendsAgree(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name         string
		raw          string
		wantContains string
	}

	// Fixture files are created eagerly so raw JSON paths are fixed before the
	// table runs.
	build := func(name string, content string) string {
		p := filepath.Join(t.TempDir(), name)
		if content != "" {
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}

	plain := build("plain.txt", "hello world\nfoo bar\nTODO fix\n")
	regexFile := build("regex.txt", "error: disk full\njust fine\nError Again\n")
	longLine := build("long.txt", strings.Repeat("x", 200*1024)+"needle"+strings.Repeat("y", 100)+"\nsecond\n")
	multi := build("multi.txt", "hit\nhit\nhit\nhit\n")

	// A directory tree with nested matches plus ignore dirs (node_modules,
	// build) that both backends must skip.
	dir := t.TempDir()
	for name, content := range map[string]string{
		"keep.txt":             "match here\n",
		"nested/deep/x.txt":    "needle\n",
		"nested/another.txt":   "match too\n",
		"node_modules/dep.txt": "match\n",
		"build/gen.txt":        "match\n",
	} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cases := []testCase{
		{
			name:         "single file basic match",
			raw:          `{"pattern":"TODO","path":"` + plain + `"}`,
			wantContains: "3:TODO fix",
		},
		{
			name:         "single file output omits path",
			raw:          `{"pattern":"foo","path":"` + plain + `"}`,
			wantContains: "2:foo bar",
		},
		{
			name:         "regex mode",
			raw:          `{"pattern":"^error","path":"` + regexFile + `","regex":true}`,
			wantContains: "1:error: disk full",
		},
		{
			name:         "regex ignore case",
			raw:          `{"pattern":"^error","path":"` + regexFile + `","regex":true,"ignore_case":true}`,
			wantContains: "1:error: disk full\n3:Error Again",
		},
		{
			name:         "directory search reaches nested files",
			raw:          `{"pattern":"needle","path":"` + dir + `"}`,
			wantContains: filepath.Join(dir, "nested/deep/x.txt") + ":1:needle",
		},
		{
			name:         "ignore dirs skipped",
			raw:          `{"pattern":"match","path":"` + dir + `"}`,
			wantContains: "keep.txt:1:match here",
		},
		{
			name: "long line larger than scanner buffer",
			raw:  `{"pattern":"needle","path":"` + longLine + `"}`,
			// The match is on line 1 which is >64KB; fallback uses a growable
			// buffer. Only assert a line-numbered hit exists (the line itself
			// is huge), then rely on the == equality to pin the backends.
			wantContains: "1:",
		},
		{
			// max_results semantics: both backends cap results within a single
			// file. (For multi-file directory searches rg's --max-count applies
			// per file while the fallback caps globally, so the outputs can
			// legitimately differ there; the single-file form is the consistent
			// scenario.)
			name:         "max results truncates",
			raw:          `{"pattern":"hit","path":"` + multi + `","max_results":2}`,
			wantContains: "1:hit\n2:hit",
		},
		{
			name:         "no matches",
			raw:          `{"pattern":"no-such-token","path":"` + dir + `"}`,
			wantContains: "(no matches)",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fallbackOut, rgOut, rgAvailable := runGrepBothBackends(t, tc.raw)
			if !strings.Contains(fallbackOut, tc.wantContains) {
				t.Fatalf("fallback missing %q, got %q", tc.wantContains, fallbackOut)
			}
			if rgAvailable && sortGrepLines(rgOut) != sortGrepLines(fallbackOut) {
				t.Fatalf("backends diverged:\nfallback: %q\nrg:       %q", fallbackOut, rgOut)
			}
		})
	}
}
