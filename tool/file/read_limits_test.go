package file

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func readWith(t *testing.T, path string, extra string) string {
	t.Helper()
	result, err := NewReadTool().Execute(context.Background(), json.RawMessage(`{"path":`+strconv.Quote(path)+extra+`}`))
	if err != nil {
		t.Fatal(err)
	}
	return result.Content
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadTruncatesAHugeLineInsteadOfFailing(t *testing.T) {
	huge := "HEAD" + strings.Repeat("x", 5<<20) // far over the old 1 MB scanner limit
	path := writeTemp(t, "first\n"+huge+"\nlast\n")

	out := readWith(t, path, "")
	if !strings.HasPrefix(out, "1:first\n2:HEAD") || !strings.HasSuffix(out, "\n3:last\n") {
		t.Fatalf("lines around the huge one were lost: %.60q ... %q", out, out[len(out)-20:])
	}
	if !strings.Contains(out, fmt.Sprintf("[line truncated: %d bytes in total]", len(huge))) {
		t.Fatal("marker with the exact original length is missing")
	}
	if len(out) > maxReadLineBytes+200 {
		t.Fatalf("result is %d bytes", len(out))
	}
}

func TestReadSkipsAHugeLineBeforeTheOffset(t *testing.T) {
	path := writeTemp(t, strings.Repeat("y", 3<<20)+"\nafter\n")
	if out := readWith(t, path, `,"offset":2`); out != "2:after\n" {
		t.Fatalf("out = %q", out)
	}
}

func TestReadOutputIsBoundedAndTellsWhereToContinue(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&sb, "line %d %s\n", i, strings.Repeat("a", 90))
	}
	path := writeTemp(t, sb.String())

	out := readWith(t, path, `,"limit":5000`)
	if len(out) > maxReadOutputBytes+200 {
		t.Fatalf("result is %d bytes, over the limit", len(out))
	}
	match := regexp.MustCompile(`\[output limited to 32 KiB: showing lines 1-(\d+); continue with offset=(\d+)\]\n$`).FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("no continuation note at the end: %q", out[len(out)-120:])
	}
	last, _ := strconv.Atoi(match[1])
	next, _ := strconv.Atoi(match[2])
	if next != last+1 || last < 100 {
		t.Fatalf("note says lines 1-%d, offset=%d", last, next)
	}
	if got := strings.Count(out, "\n"); got != last+1 {
		t.Fatalf("%d lines in the output, note says %d", got-1, last)
	}

	// Following the note continues without a gap or duplicate.
	rest := readWith(t, path, fmt.Sprintf(`,"offset":%d,"limit":1`, next))
	if !strings.HasPrefix(rest, fmt.Sprintf("%d:line %d ", next, next)) {
		t.Fatalf("continuation = %.40q", rest)
	}
}

func TestReadWithinTheLimitHasNoNote(t *testing.T) {
	var sb strings.Builder
	for i := 1; i <= 200; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	out := readWith(t, writeTemp(t, sb.String()), "")
	if strings.Contains(out, "output limited") || strings.Count(out, "\n") != 200 {
		t.Fatalf("unexpected truncation: %d lines", strings.Count(out, "\n"))
	}
}

func TestReadAlwaysReturnsAtLeastOneLine(t *testing.T) {
	// One line already near the per-line cap, repeated: the very first line
	// must be returned even if later ones do not fit.
	line := strings.Repeat("z", maxReadLineBytes-100)
	path := writeTemp(t, strings.Repeat(line+"\n", 10))
	out := readWith(t, path, `,"limit":10`)
	if !strings.HasPrefix(out, "1:zzz") || !strings.Contains(out, "continue with offset=") {
		t.Fatalf("out = %.60q", out)
	}
}

func TestReadCutsLongLinesOnRuneBoundaries(t *testing.T) {
	for _, filler := range []string{"好", "é", "😀"} {
		path := writeTemp(t, strings.Repeat(filler, 20_000)+"\n")
		out := readWith(t, path, "")
		if !utf8.ValidString(out) {
			t.Fatalf("%q: output is not valid UTF-8", filler)
		}
		if !strings.Contains(out, "[line truncated:") {
			t.Fatalf("%q: not truncated", filler)
		}
	}
}

func TestReadLineEndings(t *testing.T) {
	path := writeTemp(t, "a\r\n\r\nb\nno newline")
	if out := readWith(t, path, ""); out != "1:a\n2:\n3:b\n4:no newline\n" {
		t.Fatalf("out = %q", out)
	}
}
