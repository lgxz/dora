package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dora/internal/session"
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
	if !strings.Contains(stderr.String(), "dora: thinking") {
		t.Fatalf("stderr = %q", stderr.String())
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

func TestRunExecutesEnabledBashTool(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			tools := body["tools"].([]any)
			function := tools[0].(map[string]any)["function"].(map[string]any)
			if function["name"] != "bash" {
				t.Fatalf("function = %#v", function)
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf dora\"}"}}]}}]}`), nil
		case 2:
			messages := body["messages"].([]any)
			toolMessage := messages[len(messages)-1].(map[string]any)
			if toolMessage["role"] != "tool" || !strings.Contains(toolMessage["content"].(string), `"stdout":"dora"`) {
				t.Fatalf("tool message = %#v", toolMessage)
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"command worked"}}]}`), nil
		default:
			t.Fatalf("model called %d times", calls)
			return nil, nil
		}
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := fmt.Sprintf(`
model:
  provider: openai-compatible
  name: test-model
  base_url: https://example.test/v1
tools:
  bash:
    enabled: true
    working_dir: %s
`, t.TempDir())
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"--config", configPath, "run it"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || stdout.String() != "command worked\n" {
		t.Fatalf("calls = %d, stdout = %q", calls, stdout.String())
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

func TestRunQuietHidesProgress(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"quiet answer"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := `
model:
  provider: openai-compatible
  name: test-model
  base_url: https://example.test/v1
`
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          &stderr,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "quiet answer\n" || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func TestRunContinuesNamedSession(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages := body["messages"].([]any)
		switch calls {
		case 1:
			if len(messages) != 1 || messages[0].(map[string]any)["content"] != "first task" {
				t.Fatalf("first messages = %#v", messages)
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"first answer"}}]}`), nil
		case 2:
			if len(messages) != 3 ||
				messages[0].(map[string]any)["content"] != "first task" ||
				messages[1].(map[string]any)["content"] != "first answer" ||
				messages[2].(map[string]any)["content"] != "continue task" {
				t.Fatalf("continued messages = %#v", messages)
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"second answer"}}]}`), nil
		default:
			t.Fatalf("model called %d times", calls)
			return nil, nil
		}
	})}

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	configContents := `
model:
  provider: openai-compatible
  name: test-model
  base_url: https://example.test/v1
`
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(root, "sessions")

	var firstProgress bytes.Buffer
	firstOutput := bytes.Buffer{}
	if err := Run(context.Background(), []string{"-s", "system", "--config", configPath, "first task"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &firstOutput,
		Stderr:          &firstProgress,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
		SessionDir:      sessionDir,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(firstProgress.String(), "开始任务「system」") {
		t.Fatalf("first progress = %q", firstProgress.String())
	}

	var secondProgress bytes.Buffer
	secondOutput := bytes.Buffer{}
	if err := Run(context.Background(), []string{"--session", "system", "--config", configPath, "continue task"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &secondOutput,
		Stderr:          &secondProgress,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
		SessionDir:      sessionDir,
	}); err != nil {
		t.Fatal(err)
	}
	if secondOutput.String() != "second answer\n" || !strings.Contains(secondProgress.String(), "继续任务「system」") {
		t.Fatalf("second output = %q, progress = %q", secondOutput.String(), secondProgress.String())
	}

	store, err := session.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load("system")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 2 || len(snapshot.Messages) != 4 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func fakeJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
