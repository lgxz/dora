package capstr

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestHeadTailPassthrough(t *testing.T) {
	for _, s := range []string{"", "hello", strings.Repeat("x", 100)} {
		if got := HeadTail(s, 100, 60, "M %d"); got != s {
			t.Fatalf("HeadTail(len %d) changed the string", len(s))
		}
	}
}

func TestHeadTailTruncates(t *testing.T) {
	s := "HEAD" + strings.Repeat("a", 4_990) + "MIDDLE" + strings.Repeat("b", 4_997) + "TAIL"
	got := HeadTail(s, 100, 60, "<%d cut>")
	if !strings.HasPrefix(got, "HEAD") || !strings.HasSuffix(got, "TAIL") {
		t.Fatalf("head/tail not preserved: %q...%q", got[:10], got[len(got)-10:])
	}
	if !strings.Contains(got, fmt.Sprintf("<%d cut>", len(s))) {
		t.Fatalf("marker missing: %q", got[50:80])
	}
	if len(got) > 100+64 {
		t.Fatalf("result %d bytes exceeds cap + marker", len(got))
	}
	if strings.Contains(got, "MIDDLE") {
		t.Fatal("middle should have been dropped")
	}
}

func TestHeadTailUTF8Safe(t *testing.T) {
	s := strings.Repeat("界", 5_000) // 3 bytes per rune
	for _, head := range []int{0, 1, 33, 50} {
		got := HeadTail(s, 100, head, "%d")
		if !utf8.ValidString(got) {
			t.Fatalf("head=%d produced invalid UTF-8", head)
		}
	}
}
