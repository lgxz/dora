// Package capstr bounds large strings by keeping the head and tail around an
// inserted marker. Cut points are UTF-8 safe.
package capstr

import (
	"fmt"
	"unicode/utf8"
)

// HeadTail truncates s to at most max bytes (plus the marker), keeping the
// first head bytes and the last max-head bytes. marker is a format string
// receiving the original length as its only %d argument and is inserted
// between the kept head and tail. Strings within max are returned unchanged.
func HeadTail(s string, max, head int, marker string) string {
	if len(s) <= max {
		return s
	}
	if head < 0 || head > max {
		head = max / 2
	}
	headEnd := head
	for headEnd > 0 && !utf8.RuneStart(s[headEnd]) {
		headEnd--
	}
	tailStart := len(s) - (max - head)
	for tailStart < len(s) && !utf8.RuneStart(s[tailStart]) {
		tailStart++
	}
	return s[:headEnd] + fmt.Sprintf(marker, len(s)) + s[tailStart:]
}
