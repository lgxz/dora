package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunCallsConfiguredModel(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(
				`{"choices":[{"message":{"role":"assistant","content":"hello from model"}}]}`,
			)),
			Header: make(http.Header),
		}, nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := fmt.Sprintf(`
model:
  provider: openai-compatible
  name: test-model
  base_url: %s
  api_key: secret
`, "https://example.test/v1")
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          &stderr,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "hello from model\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestReadPromptCombinesInstructionAndPipe(t *testing.T) {
	prompt, err := readPrompt([]string{"review", "this"}, strings.NewReader("diff"), false)
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "review this\n\ndiff" {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestRunHelpDoesNotRequireConfig(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), []string{"--help"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &output,
		Stderr:          &output,
		StdinIsTerminal: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Usage: dora") {
		t.Fatalf("help = %q", output.String())
	}
}
