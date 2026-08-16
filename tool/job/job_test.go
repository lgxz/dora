package jobtool

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lgxz/dora/internal/job"
)

func TestResultParsesImagesWhenVisionEnabled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatal(err)
	}
	tool := New(job.New(), true)
	result := tool.result(`{"stdout":"@@` + path + `@@"}`)
	if len(result.Images) != 1 || result.Images[0].Path != path {
		t.Fatalf("images = %#v", result.Images)
	}
}

func TestResultDoesNotParseImagesWithoutVision(t *testing.T) {
	tool := New(job.New(), false)
	result := tool.result(`{"stdout":"@@/tmp/shot.png@@"}`)
	if len(result.Images) != 0 {
		t.Fatalf("images = %#v", result.Images)
	}
}
