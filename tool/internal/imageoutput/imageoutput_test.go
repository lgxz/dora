package imageoutput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var pngBytes = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}

func writeTempPNG(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseReturnsContentAndImages(t *testing.T) {
	a := writeTempPNG(t, "a.png")
	b := writeTempPNG(t, "b.png")
	content := "prefix @@" + a + "@@ text @@" + b + "@@"
	result := Parse(content)
	if result.Content != content {
		t.Fatalf("content = %q", result.Content)
	}
	if len(result.Images) != 2 || result.Images[0].Path != a || result.Images[1].Path != b {
		t.Fatalf("images = %#v", result.Images)
	}
}

func TestParseLeavesPlainContentUnchanged(t *testing.T) {
	result := Parse("plain output")
	if result.Content != "plain output" || result.Images != nil {
		t.Fatalf("result = %#v", result)
	}
}

func TestParseReportsInvalidPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.png")
	result := Parse("captured @@" + missing + "@@")
	if len(result.Images) != 0 || !strings.Contains(result.Content, "could not be attached") {
		t.Fatalf("result = %#v", result)
	}
}
