package cli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/session"
	"github.com/lgxz/dora/internal/update"
	bashtool "github.com/lgxz/dora/tool/bash"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("OPENAI_API_KEY", "test-secret")
	os.Exit(m.Run())
}

func TestRunCallsConfiguredModel(t *testing.T) {
	// An explicit config path must not depend on the default XDG layout.
	t.Setenv("XDG_CONFIG_HOME", "relative")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"hello from model"}}]}`), nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := fmt.Sprintf(`
model:
  provider: openai
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

func TestRunAttachesImageFlagToUserMessage(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	imagePath := filepath.Join(t.TempDir(), "shot.png")
	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	if err := os.WriteFile(imagePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages := body["messages"].([]any)
		if len(messages) != 1 {
			t.Fatalf("messages = %#v", messages)
		}
		content := messages[0].(map[string]any)["content"].([]any)
		if len(content) != 2 {
			t.Fatalf("content = %#v", content)
		}
		imageURL := content[1].(map[string]any)["image_url"].(map[string]any)
		want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
		if imageURL["url"] != want {
			t.Fatalf("image url = %v, want %q", imageURL["url"], want)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"described"}}]}`), nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := fmt.Sprintf(`
model:
  provider: openai
  name: test-model
  base_url: %s
  api_key: secret
  vision: true
`, "https://example.test/v1")
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"-q", "--config", configPath, "--image", imagePath, "describe"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "described\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunCallsDeepSeekPreset(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.deepseek.com/chat/completions" {
			t.Fatalf("url = %q", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer deepseek-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "deepseek-v4-flash" || body["stream"] != true {
			t.Fatalf("body = %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"deepseek answer"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("model:\n  provider: deepseek\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "deepseek answer\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunCallsTrustPreset(t *testing.T) {
	t.Setenv("TRUST_API_KEY", "trust-secret")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.trustoken.cn/v1/chat/completions" {
			t.Fatalf("url = %q", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer trust-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "auto" || body["stream"] != true {
			t.Fatalf("body = %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"trust answer"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("model:\n  provider: trust\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "trust answer\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunUsesDefaultsWhenDefaultConfigIsMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DORA_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("TRUST_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.deepseek.com/chat/completions" {
			t.Fatalf("url = %q", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer deepseek-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"default answer"}}]}`), nil
	})}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "default answer\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunSelectsTrustFromEnvironmentWhenDefaultConfigIsMissing(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("DORA_HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("TRUST_API_KEY", "trust-secret")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.trustoken.cn/v1/chat/completions" {
			t.Fatalf("url = %q", request.URL.String())
		}
		if request.Header.Get("Authorization") != "Bearer trust-secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "auto" {
			t.Fatalf("body = %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"automatic trust answer"}}]}`), nil
	})}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "automatic trust answer\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunRejectsMissingExplicitConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.yaml")
	err := Run(context.Background(), []string{"-q", "--config", path, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
	})
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunCallsResponsesProvider(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["store"] != false {
			t.Fatalf("body = %#v", body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello from responses\"}]}]}}\n\n",
			)),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		}, nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := `
model:
  provider: openai
  api: responses
  name: test-model
  base_url: https://example.test/v1
`
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "hello from responses\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunResumesResponsesContinuationWithoutReloadingSkill(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		input := body["input"].([]any)
		switch calls {
		case 1:
			if len(input) != 1 || input[0].(map[string]any)["content"] != "first task" {
				t.Fatalf("first input = %#v", input)
			}
			return fakeResponsesOutput(`[{"type":"function_call","call_id":"call-skill","name":"skill","arguments":"{\"name\":\"winuse\"}"}]`), nil
		case 2:
			if len(input) != 3 ||
				input[1].(map[string]any)["type"] != "function_call" ||
				input[2].(map[string]any)["type"] != "function_call_output" {
				t.Fatalf("tool continuation input = %#v", input)
			}
			return fakeResponsesOutput(`[{"type":"message","content":[{"type":"output_text","text":"first answer"}]}]`), nil
		case 3:
			if len(input) != 5 ||
				input[1].(map[string]any)["type"] != "function_call" ||
				input[2].(map[string]any)["type"] != "function_call_output" ||
				input[3].(map[string]any)["type"] != "message" ||
				input[4].(map[string]any)["content"] != "second task" {
				t.Fatalf("resumed input = %#v", input)
			}
			return fakeResponsesOutput(`[{"type":"message","content":[{"type":"output_text","text":"second answer"}]}]`), nil
		default:
			t.Fatalf("model called %d times", calls)
			return nil, nil
		}
	})}

	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	skillDir := filepath.Join(root, "skills", "winuse")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: winuse
description: Operate native application interfaces.
---
Use the bundled interface tool.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  api: responses
  name: test-model
  base_url: https://example.test/v1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(root, "sessions")
	run := func(prompt string) (string, error) {
		var output bytes.Buffer
		err := Run(context.Background(), []string{"-q", "-s", "wechat", "--config", configPath, prompt}, IO{
			Stdin:           strings.NewReader(""),
			Stdout:          &output,
			Stderr:          io.Discard,
			StdinIsTerminal: true,
			HTTPClient:      httpClient,
			SessionDir:      sessionDir,
		})
		return output.String(), err
	}
	if output, err := run("first task"); err != nil || output != "first answer\n" {
		t.Fatalf("first output = %q, error = %v", output, err)
	}
	if output, err := run("second task"); err != nil || output != "second answer\n" {
		t.Fatalf("second output = %q, error = %v", output, err)
	}
	if calls != 3 {
		t.Fatalf("model calls = %d, want 3", calls)
	}
	store, err := session.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load("wechat")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 2 || snapshot.Continuation == "" ||
		snapshot.Backend.Provider != "openai" || snapshot.Backend.API != "responses" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

type updaterFunc func(context.Context) (update.Result, error)

func (function updaterFunc) Update(ctx context.Context) (update.Result, error) {
	return function(ctx)
}

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
	t.Setenv("DORA_HOME", t.TempDir())
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
	configContents := `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
tools:
  bash:
    enabled: true
`
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

func TestRunContinuesAfterMaximumRounds(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		switch calls {
		case 1:
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf continued\"}"}}]}}]}`), nil
		case 2:
			messages := body["messages"].([]any)
			if len(messages) != 3 || messages[2].(map[string]any)["role"] != "tool" {
				t.Fatalf("resumed messages = %#v", messages)
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"finished"}}]}`), nil
		default:
			t.Fatalf("model called %d times", calls)
			return nil, nil
		}
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
agent:
  max_rounds: 5
tools:
  bash:
    enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"-q", "--max-rounds", "1", "--config", configPath, "run it"}, IO{
		Stdin:            strings.NewReader("yes\n"),
		Stdout:           &stdout,
		Stderr:           &stderr,
		StdinIsTerminal:  true,
		TerminalProgress: true,
		HTTPClient:       httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || stdout.String() != "finished\n" ||
		!strings.Contains(stderr.String(), "maximum rounds reached") {
		t.Fatalf("calls = %d, stdout = %q, stderr = %q", calls, stdout.String(), stderr.String())
	}
}

func TestRunDeclinesMaximumRoundsAndSavesSession(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf saved\"}"}}]}}]}`), nil
	})}
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
agent:
  max_rounds: 1
tools:
  bash:
    enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(root, "sessions")
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "-s", "limited", "--config", configPath, "run it"}, IO{
		Stdin:            strings.NewReader("no\n"),
		Stdout:           &stdout,
		Stderr:           io.Discard,
		StdinIsTerminal:  true,
		TerminalProgress: true,
		HTTPClient:       httpClient,
		SessionDir:       sessionDir,
	}); err != nil {
		t.Fatal(err)
	}
	store, err := session.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load("limited")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || stdout.Len() != 0 || snapshot.Revision != 1 || len(snapshot.Messages) != 3 {
		t.Fatalf("calls = %d, stdout = %q, snapshot = %#v", calls, stdout.String(), snapshot)
	}
}

func TestRunDoesNotPromptAfterMaximumRoundsWithoutTerminalInput(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf limited\"}"}}]}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
agent:
  max_rounds: 1
tools:
  bash:
    enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"-q", "--config", configPath, "run it"}, IO{
		Stdin:            strings.NewReader(""),
		Stdout:           io.Discard,
		Stderr:           &stderr,
		StdinIsTerminal:  false,
		TerminalProgress: false,
		HTTPClient:       httpClient,
	})
	if !errors.Is(err, dora.ErrMaxRounds) {
		t.Fatalf("error = %v, want %v", err, dora.ErrMaxRounds)
	}
	if strings.Contains(stderr.String(), "continue?") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunRejectsNonPositiveMaximumRoundsFlag(t *testing.T) {
	err := Run(context.Background(), []string{"--max-rounds", "0", "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
	})
	if err == nil || !strings.Contains(err.Error(), "positive integer") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunRejectsNegativeMaximumHistoryRoundsFlag(t *testing.T) {
	err := Run(context.Background(), []string{"--max-history-rounds", "-1", "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
	})
	if err == nil || !strings.Contains(err.Error(), "non-negative integer") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunMaximumHistoryRoundsFlagOverridesConfig(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		// The config sets max_history_rounds to 1, but the flag overrides it to
		// 2, so the second model call should still receive the full history
		// (user + assistant + tool) instead of being compacted.
		if calls == 2 {
			messages, ok := body["messages"].([]any)
			if !ok || len(messages) != 3 {
				t.Fatalf("messages = %#v, want 3", body["messages"])
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf ok\"}"}}]}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
agent:
  max_history_rounds: 1
tools:
  bash:
    enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "--max-history-rounds", "2", "--config", configPath, "run it"}, IO{
		Stdin:            strings.NewReader(""),
		Stdout:           &stdout,
		Stderr:           io.Discard,
		StdinIsTerminal:  true,
		TerminalProgress: true,
		HTTPClient:       httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestRunSkipsDefaultBashWhenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DORA_HOME", t.TempDir())
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["tools"]; exists {
			t.Fatalf("unexpected tools = %#v", body["tools"])
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"no bash needed"}}]}`), nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "no bash needed\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunReportsExplicitlyEnabledBashWhenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
tools:
  bash:
    enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), []string{"--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
	})
	if !errors.Is(err, bashtool.ErrUnavailable) {
		t.Fatalf("error = %v, want Bash unavailable", err)
	}
}

func TestRunRegistersBashAndPowerShellWhenAvailable(t *testing.T) {
	t.Setenv("DORA_HOME", t.TempDir())
	bin := t.TempDir()
	for _, name := range []string{"bash", "pwsh", "pwsh.exe"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("placeholder"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		tools := body["tools"].([]any)
		if len(tools) != 2 {
			t.Fatalf("tools = %#v", tools)
		}
		var names []string
		for _, raw := range tools {
			function := raw.(map[string]any)["function"].(map[string]any)
			names = append(names, function["name"].(string))
		}
		if strings.Join(names, ",") != "bash,powershell" {
			t.Fatalf("tool names = %#v", names)
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"both available"}}]}`), nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
tools:
  bash:
    enabled: true
  powershell:
    enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunLoadsSkillFromDoraHome(t *testing.T) {
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
			if function["name"] != "skill" || !strings.Contains(function["description"].(string), "system-status") {
				t.Fatalf("function = %#v", function)
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-skill","type":"function","function":{"name":"skill","arguments":"{\"name\":\"system-status\"}"}}]}}]}`), nil
		case 2:
			messages := body["messages"].([]any)
			result := messages[len(messages)-1].(map[string]any)
			if result["role"] != "tool" || !strings.Contains(result["content"].(string), "Inspect CPU and memory") {
				t.Fatalf("skill result = %#v", result)
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"skill loaded"}}]}`), nil
		default:
			t.Fatalf("model called %d times", calls)
			return nil, nil
		}
	})}

	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	skillRoot := filepath.Join(root, "skills")
	skillDir := filepath.Join(skillRoot, "system-status")
	if err := os.MkdirAll(skillDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: system-status
description: Analyze local system resources.
---

Inspect CPU and memory before drawing conclusions.
`), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	configContents := `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`
	if err := os.WriteFile(configPath, []byte(configContents), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "--config", configPath, "inspect"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || stdout.String() != "skill loaded\n" {
		t.Fatalf("calls = %d, stdout = %q", calls, stdout.String())
	}
}

func TestRunIgnoresEmptyDefaultSkillDirectory(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["tools"]; exists {
			t.Fatalf("unexpected tools = %#v", body["tools"])
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"no skills"}}]}`), nil
	})}
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	if err := os.Mkdir(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
tools:
  bash:
    enabled: false
  powershell:
    enabled: false
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsMissingAdditionalSkillDirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
skills:
  directories:
    - %s
`, filepath.Join(root, "missing"))), 0o600); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
	})
	if err == nil || !strings.Contains(err.Error(), "read directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunAddsRepeatedCommandSkillDirectories(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	writeCLITestSkill(t, first, "alpha")
	writeCLITestSkill(t, second, "beta")

	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		tools := body["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("tools = %#v", tools)
		}
		function := tools[0].(map[string]any)["function"].(map[string]any)
		description := function["description"].(string)
		if !strings.Contains(description, "alpha") || !strings.Contains(description, "beta") {
			t.Fatalf("description = %q", description)
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
	})}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
tools:
  bash:
    enabled: false
  powershell:
    enabled: false
`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), []string{
		"-q", "--config", configPath,
		"--skills-dir", first,
		"--skills-dir", first,
		"--skills-dir", second,
		"hello",
	}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunNoSkillsDisablesEverySkillSource(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	writeCLITestSkill(t, filepath.Join(root, "skills"), "default-skill")
	missing := filepath.Join(root, "missing")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["tools"]; exists {
			t.Fatalf("unexpected tools = %#v", body["tools"])
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
	})}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`
model:
  provider: openai
skills:
  directories:
    - %s
tools:
  bash:
    enabled: false
  powershell:
    enabled: false
`, missing)), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), []string{
		"-q", "--config", configPath, "--skills-dir", missing, "--no-skills", "hello",
	}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeCLITestSkill(t *testing.T, root, name string) {
	t.Helper()
	directory := filepath.Join(root, name)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	contents := fmt.Sprintf("---\nname: %s\ndescription: Test skill %s.\n---\n\nUse this skill.\n", name, name)
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
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

func TestRunVersionDoesNotRequireConfigOrPrompt(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), []string{"--version"}, IO{
		Stdin:   strings.NewReader(""),
		Stdout:  &output,
		Stderr:  io.Discard,
		Version: "dora 1.2.3 (commit abc123, built today)",
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "dora 1.2.3 (commit abc123, built today)\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunUpdateDoesNotRequireConfigOrPrompt(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), []string{"-update"}, IO{
		Stdin:  strings.NewReader(""),
		Stdout: &output,
		Stderr: io.Discard,
		Updater: updaterFunc(func(context.Context) (update.Result, error) {
			return update.Result{Current: "1.0.0", Latest: "1.1.0", Updated: true}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "Updated dora 1.0.0 -> 1.1.0\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunUpdateReportsCurrentVersion(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), []string{"--update"}, IO{
		Stdin:  strings.NewReader(""),
		Stdout: &output,
		Stderr: io.Discard,
		Updater: updaterFunc(func(context.Context) (update.Result, error) {
			return update.Result{Current: "1.1.0", Latest: "1.1.0"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "dora 1.1.0 is already up to date\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunUpdateAcceptsForceFlag(t *testing.T) {
	var output bytes.Buffer
	err := Run(context.Background(), []string{"-update", "-force"}, IO{
		Stdin:  strings.NewReader(""),
		Stdout: &output,
		Stderr: io.Discard,
		Updater: updaterFunc(func(context.Context) (update.Result, error) {
			return update.Result{Current: "dev", Latest: "1.1.0", Updated: true}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "Updated dora dev -> 1.1.0\n" {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRunQuietHidesProgress(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"quiet answer"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := `
model:
  provider: openai
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

func TestRunFormatsMarkdownOnlyForInteractiveOutput(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		terminal bool
	}{
		{name: "terminal", terminal: true},
		{name: "redirected", terminal: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"# Result\n\nA **useful** answer."}}]}`), nil
			})}
			args := []string{"-q", "--config", configPath, "hello"}
			var stdout bytes.Buffer
			if err := Run(context.Background(), args, IO{
				Stdin:            strings.NewReader(""),
				Stdout:           &stdout,
				Stderr:           io.Discard,
				StdoutIsTerminal: test.terminal,
				TerminalWidth:    60,
				StdinIsTerminal:  true,
				HTTPClient:       httpClient,
			}); err != nil {
				t.Fatal(err)
			}
			if stdout.String() != "# Result\n\nA **useful** answer.\n" {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
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
  provider: openai
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
	if !strings.Contains(firstProgress.String(), "Starting task \"system\"") {
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
	if secondOutput.String() != "second answer\n" || !strings.Contains(secondProgress.String(), "Resuming task \"system\"") {
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
	if snapshot.Revision != 2 || len(snapshot.Messages) != 4 ||
		snapshot.Backend.Provider != "openai" || snapshot.Backend.API != "chat_completions" || snapshot.Continuation != "" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRunRejectsSessionBackendMismatchUnlessFresh(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	store, err := session.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("task", 0, session.Snapshot{
		Backend: session.Backend{
			Provider: "openai",
			API:      "chat_completions",
			Model:    "old-model",
			BaseURL:  "https://old.example/v1",
		},
		Messages: []dora.Message{{Role: dora.RoleUser, Content: "old"}},
	}); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: new-model
  base_url: https://new.example/v1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	requestCount := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"fresh"}}]}`), nil
	})}
	streams := IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
		SessionDir:      sessionDir,
	}
	err = Run(context.Background(), []string{"-q", "-s", "task", "--config", configPath, "continue"}, streams)
	if err == nil || !strings.Contains(err.Error(), "use --fresh") || requestCount != 0 {
		t.Fatalf("error = %v, requests = %d", err, requestCount)
	}
	if err := Run(context.Background(), []string{"-q", "-s", "task", "--fresh", "--config", configPath, "restart"}, streams); err != nil {
		t.Fatal(err)
	}
	if requestCount != 1 {
		t.Fatalf("requests = %d", requestCount)
	}
	snapshot, err := store.Load("task")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Backend.Model != "new-model" || snapshot.Messages[0].Content != "restart" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRunFreshReplacesVersionOneOnlyOnSuccess(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "old.json")
	oldContents := `{"version":1,"revision":4,"messages":[{"role":"user","content":"old"}]}`
	if err := os.WriteFile(sessionPath, []byte(oldContents), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"failed"}}`)),
				Header:     make(http.Header),
			}, nil
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"new"}}]}`), nil
	})}
	run := func() error {
		return Run(context.Background(), []string{"-q", "-s", "old", "--fresh", "--config", configPath, "restart"}, IO{
			Stdin:           strings.NewReader(""),
			Stdout:          io.Discard,
			Stderr:          io.Discard,
			StdinIsTerminal: true,
			HTTPClient:      httpClient,
			SessionDir:      sessionDir,
		})
	}
	if err := run(); err == nil {
		t.Fatal("expected first run to fail")
	}
	afterFailure, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterFailure) != oldContents {
		t.Fatalf("version 1 session changed after failure: %s", afterFailure)
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	store, err := session.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Load("old")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 5 || snapshot.Messages[0].Content != "restart" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestRunFreshReplacesSessionOnlyOnSuccess(t *testing.T) {
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
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"old answer"}}]}`), nil
		case 2:
			if len(messages) != 1 || messages[0].(map[string]any)["content"] != "fresh task" {
				t.Fatalf("fresh messages = %#v", messages)
			}
			return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"fresh answer"}}]}`), nil
		case 3:
			if len(messages) != 1 || messages[0].(map[string]any)["content"] != "failing task" {
				t.Fatalf("failing messages = %#v", messages)
			}
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"failed"}}`)),
				Header:     make(http.Header),
			}, nil
		default:
			t.Fatalf("model called %d times", calls)
			return nil, nil
		}
	})}

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(root, "sessions")
	run := func(args ...string) (string, error) {
		var progress bytes.Buffer
		err := Run(context.Background(), append([]string{"--config", configPath}, args...), IO{
			Stdin:           strings.NewReader(""),
			Stdout:          io.Discard,
			Stderr:          &progress,
			StdinIsTerminal: true,
			HTTPClient:      httpClient,
			SessionDir:      sessionDir,
		})
		return progress.String(), err
	}
	if _, err := run("-s", "replaceable", "old task"); err != nil {
		t.Fatal(err)
	}
	progress, err := run("-s", "replaceable", "--fresh", "fresh task")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress, "Restarting task \"replaceable\"") {
		t.Fatalf("progress = %q", progress)
	}

	store, err := session.New(sessionDir)
	if err != nil {
		t.Fatal(err)
	}
	freshSnapshot, err := store.Load("replaceable")
	if err != nil {
		t.Fatal(err)
	}
	if freshSnapshot.Revision != 2 || len(freshSnapshot.Messages) != 2 || freshSnapshot.Messages[0].Content != "fresh task" {
		t.Fatalf("fresh snapshot = %#v", freshSnapshot)
	}

	if _, err := run("-s", "replaceable", "--fresh", "failing task"); err == nil {
		t.Fatal("expected model error")
	}
	afterFailure, err := store.Load("replaceable")
	if err != nil {
		t.Fatal(err)
	}
	if afterFailure.Revision != freshSnapshot.Revision || len(afterFailure.Messages) != 2 || afterFailure.Messages[0].Content != "fresh task" {
		t.Fatalf("session changed after failure: %#v", afterFailure)
	}
}

func TestRunFreshRequiresNamedSession(t *testing.T) {
	err := Run(context.Background(), []string{"--fresh", "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
	})
	if err == nil || !strings.Contains(err.Error(), "requires --session") {
		t.Fatalf("error = %v", err)
	}
}

func fakeJSONResponse(body string) *http.Response {
	var decoded struct {
		Choices []struct {
			Message json.RawMessage `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil || len(decoded.Choices) == 0 {
		panic("invalid fake Chat Completions response")
	}
	event, err := json.Marshal(map[string]any{
		"choices": []any{map[string]any{
			"index": 0,
			"delta": decoded.Choices[0].Message,
		}},
	})
	if err != nil {
		panic(err)
	}
	return fakeChatResponse(string(event))
}

func fakeChatResponse(event string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data: " + event + "\n\ndata: [DONE]\n\n")),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
}

func fakeResponsesOutput(output string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body: io.NopCloser(strings.NewReader(
			`data: {"type":"response.completed","response":{"id":"resp","output":` + output + "}}\n\n",
		)),
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
	}
}
