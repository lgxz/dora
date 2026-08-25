package job

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCapOutputPassthrough(t *testing.T) {
	for _, s := range []string{"", "hello", strings.Repeat("x", MaxResultBytes)} {
		if got := CapOutput(s); got != s {
			t.Fatalf("CapOutput(len %d) changed the output", len(s))
		}
	}
}

func TestCapOutputTruncatesLongOutput(t *testing.T) {
	var b strings.Builder
	b.WriteString("HEAD-MARKER\n")
	for i := 0; i < 40_000; i++ {
		fmt.Fprintf(&b, "line-%05d padding padding padding\n", i)
	}
	b.WriteString("\nTAIL-MARKER")
	s := b.String()

	got := CapOutput(s)
	if !utf8.ValidString(got) {
		t.Fatal("CapOutput produced invalid UTF-8")
	}
	// Head and tail survive; the middle is replaced by a marker naming the
	// original size.
	if !strings.HasPrefix(got, "HEAD-MARKER\n") {
		t.Fatal("head was not preserved")
	}
	if !strings.HasSuffix(got, "\nTAIL-MARKER") && !strings.HasSuffix(got, "TAIL-MARKER") {
		t.Fatalf("tail was not preserved: %q", got[len(got)-40:])
	}
	if !strings.Contains(got, fmt.Sprintf("%d bytes total", len(s))) {
		t.Fatalf("marker does not name the total size")
	}
	// The capped result stays within the cap plus marker overhead.
	if len(got) > MaxResultBytes+256 {
		t.Fatalf("capped length %d exceeds cap + marker overhead", len(got))
	}
	if strings.Contains(got, "line-20000") {
		t.Fatal("middle content should have been dropped")
	}
}

func TestCapOutputUTF8Boundary(t *testing.T) {
	// A string of multibyte runes long enough to force both cut points to land
	// inside a rune.
	s := strings.Repeat("界", MaxResultBytes) // 3 bytes per rune
	got := CapOutput(s)
	if !utf8.ValidString(got) {
		t.Fatal("CapOutput split a rune at a cut point")
	}
}

func TestSnapshotCapsCommandOutput(t *testing.T) {
	big := strings.Repeat("y", MaxResultBytes*4)
	out := &OutputBuffer{}
	out.StdoutWriter().Write([]byte(big))
	out.StderrWriter().Write([]byte(big))

	j := &Job{kind: KindCommand, out: out}
	snapshot := j.snapshot(true)
	if snapshot.Stdout == big || len(snapshot.Stdout) > MaxResultBytes+256 {
		t.Fatalf("snapshot stdout not capped: %d bytes", len(snapshot.Stdout))
	}
	if snapshot.Stderr == big || len(snapshot.Stderr) > MaxResultBytes+256 {
		t.Fatalf("snapshot stderr not capped: %d bytes", len(snapshot.Stderr))
	}
}
