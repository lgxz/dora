package imageutil

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngBytes is a minimal valid PNG header so http.DetectContentType reports
// image/png.
var pngBytes = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}

func writeTempPNG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseTagsSingle(t *testing.T) {
	path := writeTempPNG(t)
	paths, notes := ParseTags("prefix @@" + path + "@@ suffix")
	if len(paths) != 1 || paths[0] != path {
		t.Fatalf("paths = %#v", paths)
	}
	if len(notes) != 0 {
		t.Fatalf("notes = %#v", notes)
	}
}

func TestParseTagsMultiple(t *testing.T) {
	a := writeTempPNG(t)
	b := writeTempPNG(t)
	paths, _ := ParseTags("@@" + a + "@@ text @@" + b + "@@")
	if len(paths) != 2 || paths[0] != a || paths[1] != b {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestParseTagsSkipsEmpty(t *testing.T) {
	paths, _ := ParseTags("@@@@ @@@@   ")
	if len(paths) != 0 {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestParseTagsNone(t *testing.T) {
	paths, notes := ParseTags("no tags here")
	if paths != nil || notes != nil {
		t.Fatalf("paths = %#v, notes = %#v", paths, notes)
	}
}

func TestParseTagsReportsInvalidPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.png")
	paths, notes := ParseTags("@@@" + missing + "@@@")
	if len(paths) != 0 {
		t.Fatalf("paths = %#v", paths)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "could not be attached") {
		t.Fatalf("notes = %#v", notes)
	}
}

func TestDataURLReadsPathAndSniffsMime(t *testing.T) {
	path := writeTempPNG(t)
	got, err := DataURL(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngBytes)
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestDataURLRejectsEmptyPath(t *testing.T) {
	if _, err := DataURL(""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestDataURLRejectsMissingFile(t *testing.T) {
	if _, err := DataURL(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestDataURLRejectsNonImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DataURL(path); err == nil {
		t.Fatal("expected error for non-image file")
	}
}

func TestDataURLRejectsOversizedImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.png")
	data := append([]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, make([]byte, MaxImageBytes)...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DataURL(path); err == nil {
		t.Fatal("expected error for oversized image")
	}
}