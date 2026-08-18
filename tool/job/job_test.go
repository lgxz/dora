package jobtool

import (
	"testing"

	"github.com/lgxz/dora/internal/job"
)

func TestResultReturnsContent(t *testing.T) {
	tool := New(job.New())
	result := tool.result(`{"stdout":"hello"}`)
	if result.Content != `{"stdout":"hello"}` {
		t.Fatalf("content = %q", result.Content)
	}
	if len(result.Images) != 0 {
		t.Fatalf("images = %#v", result.Images)
	}
}
