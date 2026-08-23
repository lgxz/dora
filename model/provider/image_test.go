package provider

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgxz/dora"
)

func TestImageDataURLURL(t *testing.T) {
	got, err := ImageDataURL(dora.Image{URL: "https://example.com/img.png"})
	if err != nil {
		t.Fatalf("ImageDataURL() error = %v", err)
	}
	if got != "https://example.com/img.png" {
		t.Errorf("ImageDataURL() = %q, want URL unchanged", got)
	}
}

func TestImageDataURLFromPath(t *testing.T) {
	// A minimal 1x1 PNG that http.DetectContentType recognises as image/png.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, // PNG signature
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52, 0x00, 0x00, 0x00, 0x01,
		0x00, 0x00, 0x00, 0x01, 0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41, 0x54, 0x08, 0xd7, 0x63,
		0xf8, 0xcf, 0xc0, 0x00, 0x00, 0x00, 0x03, 0x00, 0x01, 0x4a, 0x9d, 0x84,
		0x97, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae, 0x42, 0x60,
		0x82,
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "pixel.png")
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := ImageDataURL(dora.Image{Path: path})
	if err != nil {
		t.Fatalf("ImageDataURL() error = %v", err)
	}
	if !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("ImageDataURL() = %q, want 'data:image/png;base64,' prefix", got)
	}
	if got == "" {
		t.Errorf("ImageDataURL() = empty string, want encoded data URL")
	}
}

func TestImageDataURLNeither(t *testing.T) {
	if _, err := ImageDataURL(dora.Image{}); err == nil {
		t.Fatal("ImageDataURL() error = nil, want error when neither Path nor URL set")
	}
}