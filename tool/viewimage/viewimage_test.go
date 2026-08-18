package viewimage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgxz/dora"
)

func writePNG(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	// Minimal PNG header is enough for http.DetectContentType to report image/png.
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00, 0x00, 0x00, 0x0d}
	if err := os.WriteFile(path, png, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("os.UserHomeDir unavailable: %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"tilde slash", "~/foo.png", filepath.Join(home, "foo.png")},
		{"bare tilde", "~", home},
		{"other user", "~other/foo.png", "~other/foo.png"},
		{"middle tilde", "/tmp/~/a.png", "/tmp/~/a.png"},
		{"middle tilde dir", "/tmp/~nick/a.png", "/tmp/~nick/a.png"},
		{"relative", "foo.png", "foo.png"},
		{"absolute", "/abs/a.png", "/abs/a.png"},
		{"nested directory", "~/a/b/c.png", filepath.Join(home, "a", "b", "c.png")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := expandHome(tt.in); got != tt.want {
				t.Fatalf("expandHome(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExecuteLocalPath(t *testing.T) {
	path := writePNG(t, "shot.png")
	var got dora.Image
	tool := New()
	tool.SetViewer(func(image dora.Image, prompt string) (string, error) {
		got = image
		return "a screenshot", nil
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"path":`+strconvQuote(path)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Path != path {
		t.Fatalf("viewer image = %#v", got)
	}
	if result.Content != "a screenshot" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestExecuteRemoteURL(t *testing.T) {
	var got dora.Image
	tool := New()
	tool.SetViewer(func(image dora.Image, prompt string) (string, error) {
		got = image
		return "a picture", nil
	})

	result, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/a.png"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.URL != "https://example.com/a.png" {
		t.Fatalf("viewer image = %#v", got)
	}
	if result.Content != "a picture" {
		t.Fatalf("content = %q", result.Content)
	}
}

func TestExecuteUsesCustomPrompt(t *testing.T) {
	path := writePNG(t, "shot.png")
	var gotPrompt string
	tool := New()
	tool.SetViewer(func(_ dora.Image, prompt string) (string, error) {
		gotPrompt = prompt
		return "a screenshot", nil
	})

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":`+strconvQuote(path)+`,"prompt":"Count the cats"}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "Count the cats" {
		t.Fatalf("prompt = %q, want %q", gotPrompt, "Count the cats")
	}
}

func TestExecuteUsesDefaultPrompt(t *testing.T) {
	path := writePNG(t, "shot.png")
	var gotPrompt string
	tool := New()
	tool.SetViewer(func(_ dora.Image, prompt string) (string, error) {
		gotPrompt = prompt
		return "a screenshot", nil
	})

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":`+strconvQuote(path)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "Describe this image." {
		t.Fatalf("prompt = %q, want %q", gotPrompt, "Describe this image.")
	}
}

func TestExecuteTrimsBlankPromptToDefault(t *testing.T) {
	path := writePNG(t, "shot.png")
	var gotPrompt string
	tool := New()
	tool.SetViewer(func(_ dora.Image, prompt string) (string, error) {
		gotPrompt = prompt
		return "a screenshot", nil
	})

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":`+strconvQuote(path)+`,"prompt":"   "}`))
	if err != nil {
		t.Fatal(err)
	}
	if gotPrompt != "Describe this image." {
		t.Fatalf("prompt = %q, want %q", gotPrompt, "Describe this image.")
	}
}

func TestExecuteWithoutViewerErrors(t *testing.T) {
	tool := New()
	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"https://example.com/a.png"}`))
	if err == nil || !strings.Contains(err.Error(), "no viewer configured") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteRejectsBothPathAndURL(t *testing.T) {
	tool := New()

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"/tmp/a.png","url":"https://example.com/a.png"}`))
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteRejectsNeitherPathNorURL(t *testing.T) {
	tool := New()

	_, err := tool.Execute(context.Background(), json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("error = %v", err)
	}
}

func TestExecuteRejectsMissingFile(t *testing.T) {
	tool := New()

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":"/no/such/file.png"}`))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestExecuteRejectsOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.png")
	if err := os.WriteFile(path, make([]byte, 4<<20+1), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := New()

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":`+strconvQuote(path)+`}`))
	if err == nil {
		t.Fatal("expected error for oversized file")
	}
}

func TestExecuteRejectsNonImageFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	tool := New()

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"path":`+strconvQuote(path)+`}`))
	if err == nil {
		t.Fatal("expected error for non-image file")
	}
}

func TestExecuteRejectsInvalidURL(t *testing.T) {
	tool := New()

	_, err := tool.Execute(context.Background(), json.RawMessage(`{"url":"not-a-url"}`))
	if err == nil {
		t.Fatal("expected error for invalid URL")
	}
}

func strconvQuote(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}
