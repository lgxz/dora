package powershell

import (
	"testing"

	"github.com/lgxz/dora"
)

func TestToolIsExclusive(t *testing.T) {
	var tool dora.ExclusiveTool = (*Tool)(nil)
	if !tool.Exclusive() {
		t.Fatal("powershell must not overlap with other tool calls of the same response")
	}
}
