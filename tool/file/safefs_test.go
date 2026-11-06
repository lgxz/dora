package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lgxz/dora"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func execTool(t *testing.T, tool interface {
	Execute(context.Context, json.RawMessage) (dora.ToolResult, error)
}, input map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(context.Background(), raw)
	return result.Content, err
}

func lockEntries() int {
	pathLocks.mu.Lock()
	defer pathLocks.mu.Unlock()
	return len(pathLocks.entries)
}

func tokenFile(t *testing.T, n int) (string, string) {
	t.Helper()
	var sb strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&sb, "line %03d: token%03d\n", i, i)
	}
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, sb.String()
}

// Many edits to different parts of one file, issued at the same time, must all
// land. Without the per-file lock they overwrite each other.
func TestConcurrentEditsOfOneFileAllApply(t *testing.T) {
	const n = 60
	path, _ := tokenFile(t, n)
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out, err := execTool(t, NewEditTool(), map[string]any{
				"path": path, "old_string": fmt.Sprintf("token%03d", i), "new_string": fmt.Sprintf("DONE%03d", i),
			})
			if err != nil || !strings.HasPrefix(out, "replacements: 1") {
				failures.Add(1)
				t.Errorf("edit %d = %q, %v", i, out, err)
			}
		}(i)
	}
	wg.Wait()
	data, _ := os.ReadFile(path)
	if got := strings.Count(string(data), "DONE"); got != n || strings.Contains(string(data), "token") {
		t.Fatalf("%d of %d edits survived (%d reported failures)", got, n, failures.Load())
	}
	if got := lockEntries(); got != 0 {
		t.Fatalf("lock registry holds %d entries after all edits finished", got)
	}
}

// The same file reached through a symlink or a hard link shares one lock.
func TestConcurrentEditsThroughAliasesAllApply(t *testing.T) {
	const n = 30
	path, _ := tokenFile(t, n)
	dir := filepath.Dir(path)
	symlink := filepath.Join(dir, "sym.txt")
	hardlink := filepath.Join(dir, "hard.txt")
	if err := os.Symlink(path, symlink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Link(path, hardlink); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	paths := []string{path, symlink, hardlink}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// The hard link stops being the same file once an atomic write
			// replaces the target, so only the symlink and the path are used
			// for the later edits; the lock must still treat them as one file.
			p := paths[i%2]
			if _, err := execTool(t, NewEditTool(), map[string]any{
				"path": p, "old_string": fmt.Sprintf("token%03d", i), "new_string": fmt.Sprintf("DONE%03d", i),
			}); err != nil {
				t.Errorf("edit %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	data, _ := os.ReadFile(path)
	if got := strings.Count(string(data), "DONE"); got != n {
		t.Fatalf("%d of %d edits survived through aliases", got, n)
	}
	if info, err := os.Lstat(symlink); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was replaced by a regular file: %v, %v", info, err)
	}
}

func TestEditRefusesAmbiguousMatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	original := "foo\nbar foo\nfoo\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := execTool(t, NewEditTool(), map[string]any{"path": path, "old_string": "foo", "new_string": "X"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "appears 3 times") || strings.Contains(out, "replacements:") {
		t.Fatalf("out = %q, want an ambiguity message with the count", out)
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatalf("ambiguous edit changed the file: %q", data)
	}

	out, err = execTool(t, NewEditTool(), map[string]any{"path": path, "old_string": "bar foo", "new_string": "bar X"})
	if err != nil || !strings.HasPrefix(out, "replacements: 1") {
		t.Fatalf("unique edit = %q, %v", out, err)
	}
	out, err = execTool(t, NewEditTool(), map[string]any{"path": path, "old_string": "foo", "new_string": "Y", "replace_all": true})
	if err != nil || !strings.HasPrefix(out, "replacements: 2") {
		t.Fatalf("replace_all = %q, %v", out, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "Y\nbar X\nY\n" {
		t.Fatalf("content = %q", data)
	}
}

func TestEditWithIdenticalReplacementIsNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	out, err := execTool(t, NewEditTool(), map[string]any{"path": path, "old_string": "keep", "new_string": "keep"})
	if err != nil || out != "replacements: 1, bytes_changed: 0" {
		t.Fatalf("out = %q, err = %v; an identical replacement is found, not 'not found'", out, err)
	}
	after, _ := os.Stat(path)
	if !os.SameFile(before, after) {
		t.Fatal("a no-op edit rewrote the file")
	}
}

// A reader must never observe a truncated or mixed file while a writer replaces
// it, which is what a crash or a kill in the middle of a write looks like.
func TestWriteIsAtomicForConcurrentReaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.txt")
	a := bytes.Repeat([]byte("a"), 256<<10)
	b := bytes.Repeat([]byte("b"), 256<<10)
	if err := os.WriteFile(path, a, 0o644); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	var bad atomic.Int32
	for r := 0; r < 3; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				data, err := os.ReadFile(path)
				if err != nil {
					bad.Add(1)
					t.Errorf("read: %v", err)
					return
				}
				if len(data) != len(a) || !(bytes.Equal(data, a) || bytes.Equal(data, b)) {
					bad.Add(1)
					t.Errorf("observed a torn file: %d bytes", len(data))
					return
				}
			}
		}()
	}
	for i := 0; i < 40 && bad.Load() == 0; i++ {
		content := a
		if i%2 == 0 {
			content = b
		}
		if _, err := execTool(t, NewWriteTool(), map[string]any{"path": path, "content": string(content)}); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestAtomicWriteKeepsModeSymlinkAndLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(target, []byte("old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := execTool(t, NewWriteTool(), map[string]any{"path": link, "content": "new\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := execTool(t, NewEditTool(), map[string]any{"path": link, "old_string": "new", "new_string": "newer"}); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(target); string(data) != "newer\n" {
		t.Fatalf("target = %q", data)
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced instead of followed")
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(target); info.Mode().Perm() != 0o755 {
			t.Fatalf("mode = %v, want 0755 preserved", info.Mode().Perm())
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestWriteCreatesParentDirectoriesAndReportsCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "new.txt")
	out, err := execTool(t, NewWriteTool(), map[string]any{"path": path, "content": "hi"})
	if err != nil || out != "bytes_written: 2, created: true" {
		t.Fatalf("out = %q, err = %v", out, err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o644 && runtime.GOOS != "windows" {
		t.Fatalf("new file mode = %v, want 0644", info.Mode().Perm())
	}
	out, err = execTool(t, NewWriteTool(), map[string]any{"path": path, "content": "+more", "append": true})
	if err != nil || out != "bytes_written: 5, created: false" {
		t.Fatalf("append out = %q, err = %v", out, err)
	}
	if data, _ := os.ReadFile(path); string(data) != "hi+more" {
		t.Fatalf("content = %q", data)
	}
}

// An unwritable directory with a writable file used to work with an in-place
// write and must keep working.
func TestAtomicWriteFallsBackWhenDirectoryIsNotWritable(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict this user")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	if _, err := execTool(t, NewEditTool(), map[string]any{"path": path, "old_string": "old", "new_string": "new"}); err != nil {
		t.Fatalf("edit in a read-only directory: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
}

// If something else modifies the file after it was read and before the new
// content is committed, the edit must fail instead of overwriting that change.
func TestEditDetectsExternalModification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte("alpha beta"), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeCommit = func() {
		if err := os.WriteFile(path, []byte("alpha beta + external change"), 0o644); err != nil {
			t.Error(err)
		}
	}
	defer func() { beforeCommit = nil }()

	_, err := execTool(t, NewEditTool(), map[string]any{"path": path, "old_string": "alpha", "new_string": "ALPHA"})
	if !errors.Is(err, errFileChanged) {
		t.Fatalf("err = %v, want errFileChanged", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "alpha beta + external change" {
		t.Fatalf("the external change was overwritten: %q", data)
	}

	beforeCommit = nil
	if out, err := execTool(t, NewEditTool(), map[string]any{"path": path, "old_string": "alpha", "new_string": "ALPHA"}); err != nil || !strings.HasPrefix(out, "replacements: 1") {
		t.Fatalf("retry after re-reading = %q, %v", out, err)
	}
}

func TestEditMissingFileStillErrors(t *testing.T) {
	_, err := execTool(t, NewEditTool(), map[string]any{"path": filepath.Join(t.TempDir(), "nope"), "old_string": "a", "new_string": "b"})
	if err == nil || !strings.Contains(err.Error(), "edit:") {
		t.Fatalf("err = %v", err)
	}
}

// A write or an edit must not overlap with the other calls of its response,
// while a read stays freely concurrent.
func TestFileToolsExclusivity(t *testing.T) {
	var write, edit dora.Tool = NewWriteTool(), NewEditTool()
	for _, tool := range []dora.Tool{write, edit} {
		exclusive, ok := tool.(dora.ExclusiveTool)
		if !ok || !exclusive.Exclusive() {
			t.Errorf("%T must be exclusive", tool)
		}
	}
	if _, ok := dora.Tool(NewReadTool()).(dora.ExclusiveTool); ok {
		t.Error("read must stay concurrent")
	}
}
