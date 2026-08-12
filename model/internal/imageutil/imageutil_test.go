package imageutil

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/lgxz/dora"
)

func TestDataURLUsesURLDirectly(t *testing.T) {
	got, err := DataURL(dora.Image{URL: "https://example.test/a.png"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://example.test/a.png" {
		t.Fatalf("url = %q", got)
	}
}

func TestDataURLReadsPathAndSniffsMime(t *testing.T) {
	// Minimal PNG header so http.DetectContentType reports image/png.
	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	path := filepath.Join(t.TempDir(), "img.png")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := DataURL(dora.Image{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if got != want {
		t.Fatalf("url = %q, want %q", got, want)
	}
}

func TestDataURLRejectsMissingSource(t *testing.T) {
	if _, err := DataURL(dora.Image{}); err == nil {
		t.Fatal("expected error for image with neither Path nor URL")
	}
}

func TestDataURLRejectsMissingFile(t *testing.T) {
	if _, err := DataURL(dora.Image{Path: filepath.Join(t.TempDir(), "missing.png")}); err == nil {
		t.Fatal("expected error for missing file")
	}
}