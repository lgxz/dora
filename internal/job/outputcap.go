package job

import (
	"fmt"
	"unicode/utf8"
)

// MaxResultBytes caps the stdout or stderr one command result returns to the
// model. Output beyond the cap is truncated head+tail with a marker directing
// the model to redirect to a file, so a single oversized command cannot
// overflow the model request or the context budget.
const MaxResultBytes = 32 << 10

// The retained output is split between head and tail; the tail keeps late
// errors and summaries visible.
const (
	capHeadBytes = 24 << 10
	capTailBytes = MaxResultBytes - capHeadBytes
)

// CapOutput truncates command output to at most MaxResultBytes (plus the
// marker), keeping the head and tail. It is UTF-8 safe: cut points back off to
// rune boundaries.
func CapOutput(s string) string {
	if len(s) <= MaxResultBytes {
		return s
	}
	headEnd := capHeadBytes
	for headEnd > 0 && !utf8.RuneStart(s[headEnd]) {
		headEnd--
	}
	tailStart := len(s) - capTailBytes
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	return s[:headEnd] +
		fmt.Sprintf("\n... [truncated: %d bytes total, showing head+tail; redirect output to a file to inspect all of it] ...\n", len(s)) +
		s[tailStart:]
}
