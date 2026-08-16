package imagefile

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

var pngBytes = []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}

func writeTempPNG(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
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

func TestDataURLRejectsInvalidFiles(t *testing.T) {
	t.Run("empty path", func(t *testing.T) {
		if _, err := DataURL(""); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("missing file", func(t *testing.T) {
		if _, err := DataURL(filepath.Join(t.TempDir(), "missing.png")); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("non-image", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "note.txt")
		if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := DataURL(path); err == nil {
			t.Fatal("expected error")
		}
	})
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "big.png")
		data := append(append([]byte(nil), pngBytes...), make([]byte, MaxBytes)...)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := DataURL(path); err == nil {
			t.Fatal("expected error")
		}
	})
}
