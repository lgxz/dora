package bash

import (
	"testing"

	"github.com/lgxz/dora"
)

func TestToolIsExclusive(t *testing.T) {
	var tool dora.ExclusiveTool = (*Tool)(nil)
	if !tool.Exclusive() {
		t.Fatal("bash must not overlap with other tool calls of the same response")
	}
}
