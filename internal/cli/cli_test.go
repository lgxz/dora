package cli

import (
	"bytes"
	"context"
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
	"github.com/lgxz/dora/internal/update"
	"github.com/lgxz/dora/session"
	sqlitesession "github.com/lgxz/dora/session/sqlite"
	bashtool "github.com/lgxz/dora/tool/bash"
	"gopkg.in/yaml.v3"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("OPENAI_API_KEY", "test-key")
	_ = os.Setenv("DEEPSEEK_API_KEY", "")
	_ = os.Setenv("TRUST_API_KEY", "")
	_ = os.Setenv("DORA_MODEL", "")
	_ = os.Setenv("DORA_POLICY_TEXT_PROVIDER", "")
	_ = os.Setenv("DORA_POLICY_TEXT_PROFILE", "")
	_ = os.Setenv("DORA_POLICY_IMAGE_PROVIDER", "")
	_ = os.Setenv("DORA_POLICY_IMAGE_PROFILE", "")
	os.Exit(m.Run())
}

func TestRunCallsConfiguredModel(t *testing.T) {
	// An explicit config path must not depend on the default XDG layout.
	t.Setenv("XDG_CONFIG_HOME", "relative")
	// The real process environment wins over config env, so clear it to assert
	// that the config-local fallback is used.
	t.Setenv("OPENAI_API_KEY", "")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "test-model" {
			t.Fatalf("model = %#v", body["model"])
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"hello from model"}}]}`), nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := fmt.Sprintf(`
providers:
  - name: openai
    base_url: %s
    profiles:
      - name: fast
        model: test-model
        capabilities: [text]
env:
  OPENAI_API_KEY: secret
policy:
  text:
    provider: openai
    profile: fast
`, "https://example.test/v1")
	if err := writeTestConfig(t, configPath, configContents); err != nil {
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
	if !strings.Contains(stderr.String(), "Thinking...") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunStreamsReasoningOnlyWithFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("OPENAI_API_KEY", "")
	newHTTPClient := func() *http.Client {
		return &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return fakeChatResponse(`{"choices":[{"index":0,"delta":{"reasoning_content":"考虑中"}}]}`), nil
		})}
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configContents := fmt.Sprintf(`
providers:
  - name: openai
    base_url: %s
    profiles:
      - name: fast
        model: test-model
        capabilities: [text]
env:
  OPENAI_API_KEY: secret
policy:
  text:
    provider: openai
    profile: fast
`, "https://example.test/v1")
	if err := writeTestConfig(t, configPath, configContents); err != nil {
		t.Fatal(err)
	}

	run := func(reasoningFlag bool) string {
		args := []string{"--config", configPath}
		if reasoningFlag {
			args = append(args, "--reasoning")
		}
		args = append(args, "hello")
		var stderr bytes.Buffer
		if err := Run(context.Background(), args, IO{
			Stdin:           strings.NewReader(""),
			Stdout:          io.Discard,
			Stderr:          &stderr,
			StdinIsTerminal: true,
			HTTPClient:      newHTTPClient(),
		}); err != nil {
			t.Fatal(err)
		}
		return stderr.String()
	}

	if stderr := run(false); strings.Contains(stderr, "考虑中") {
		t.Fatalf("stderr without --reasoning = %q, want reasoning hidden", stderr)
	}
	if stderr := run(true); !strings.Contains(stderr, "考虑中") {
		t.Fatalf("stderr with --reasoning = %q, want reasoning streamed", stderr)
	}
}

func TestRunRejectsRemovedImageFlags(t *testing.T) {
	for _, flagName := range []string{"--vision", "--image"} {
		t.Run(flagName, func(t *testing.T) {
			err := Run(context.Background(), []string{flagName, "value"}, IO{
				Stdin:           strings.NewReader(""),
				Stdout:          io.Discard,
				Stderr:          io.Discard,
				StdinIsTerminal: true,
			})
			if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
				t.Fatalf("error = %v", err)
			}
		})
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
	if err := writeTestConfig(t, configPath, "model:\n  provider: deepseek\n"); err != nil {
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
		if body["model"] != "deepseek-v4-flash" || body["stream"] != true {
			t.Fatalf("body = %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"trust answer"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, "model:\n  provider: trust\n"); err != nil {
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

func TestRunConfigEnvironmentAutoSelectsBuiltinDeepSeek(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("env:\n  DEEPSEEK_API_KEY: deepseek-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
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
		if body["model"] != "deepseek-v4-flash" {
			t.Fatalf("body = %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"default deepseek answer"}}]}`), nil
	})}

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
	if stdout.String() != "default deepseek answer\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunConfigEnvironmentAutoSelectsBuiltinTrust(t *testing.T) {
	t.Setenv("TRUST_API_KEY", "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("env:\n  TRUST_API_KEY: trust-secret\npolicy:\n  text:\n    provider: trust\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.trustoken.cn/v1/chat/completions" {
			t.Fatalf("url = %q", request.URL.String())
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "deepseek-v4-flash" {
			t.Fatalf("model = %#v", body["model"])
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"trust answer"}}]}`), nil
	})}

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

func TestRunEmptyPolicySelectsFirstProvider(t *testing.T) {
	// Order = priority: with no explicit policy, the first text model in the
	// catalog (deepseek) is selected even when multiple providers have keys.
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("TRUST_API_KEY", "")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("env:\n  DEEPSEEK_API_KEY: deepseek-secret\n  TRUST_API_KEY: trust-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://api.deepseek.com/chat/completions" {
			t.Fatalf("url = %q", request.URL.String())
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"first provider"}}]}`), nil
	})}
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
	if stdout.String() != "first provider\n" {
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
	if err := writeTestConfig(t, configPath, configContents); err != nil {
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

func TestRunMapsChatThinkingLowToReasoningEffort(t *testing.T) {
	// gpt-5 (openai) chat_completions with thinking: low sends reasoning_effort
	// at the top level and no nested reasoning object.
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["reasoning_effort"] != "low" {
			t.Fatalf("reasoning_effort = %#v, want \"low\"", body["reasoning_effort"])
		}
		if _, exists := body["reasoning"]; exists {
			t.Fatalf("unexpected reasoning in %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
  thinking: low
`); err != nil {
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
}

func TestRunMapsDeepSeekChatThinkingOffToDisabled(t *testing.T) {
	// deepseek chat_completions with thinking: off sends thinking.type: disabled
	// and no reasoning_effort.
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		thinking := body["thinking"].(map[string]any)
		if thinking["type"] != "disabled" {
			t.Fatalf("thinking = %#v, want {\"type\":\"disabled\"}", body["thinking"])
		}
		if _, exists := body["reasoning_effort"]; exists {
			t.Fatalf("unexpected reasoning_effort in %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, "model:\n  provider: deepseek\n  thinking: off\n"); err != nil {
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
}

func TestRunMapsOpenAIResponsesThinkingOffToNone(t *testing.T) {
	// openai responses with thinking: off sends reasoning.effort: none and
	// keeps reasoning.encrypted_content in include.
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		reasoning := body["reasoning"].(map[string]any)
		if reasoning["effort"] != "none" {
			t.Fatalf("reasoning = %#v, want {\"effort\":\"none\"}", body["reasoning"])
		}
		if _, exists := body["reasoning_effort"]; exists {
			t.Fatalf("unexpected reasoning_effort in %#v", body)
		}
		return fakeResponsesOutput(`[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  api: responses
  name: test-model
  base_url: https://example.test/v1
  thinking: off
`); err != nil {
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
}

func TestRunIgnoresDeepSeekChatThinkingMinimal(t *testing.T) {
	// deepseek does not support minimal on chat_completions; it is silently
	// dropped and no reasoning params are sent.
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["reasoning_effort"]; exists {
			t.Fatalf("unexpected reasoning_effort in %#v", body)
		}
		if _, exists := body["thinking"]; exists {
			t.Fatalf("unexpected thinking in %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, "model:\n  provider: deepseek\n  thinking: minimal\n"); err != nil {
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
}

func TestRunThinkingFlagOverridesChatConfig(t *testing.T) {
	// --thinking low overrides an openai chat config with no config thinking,
	// so the outgoing body gets reasoning_effort: low and no nested reasoning.
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["reasoning_effort"] != "low" {
			t.Fatalf("reasoning_effort = %#v, want \"low\"", body["reasoning_effort"])
		}
		if _, exists := body["reasoning"]; exists {
			t.Fatalf("unexpected reasoning in %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "--thinking", "low", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunThinkingFlagOverridesDeepSeekDefaultOff(t *testing.T) {
	// A deepseek config with no thinking defaults to off; --thinking medium
	// must beat that default and send reasoning_effort: medium (no disabled
	// thinking object and no reasoning_effort).
	t.Setenv("DEEPSEEK_API_KEY", "deepseek-secret")
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["reasoning_effort"] != "medium" {
			t.Fatalf("reasoning_effort = %#v, want \"medium\"", body["reasoning_effort"])
		}
		if _, exists := body["thinking"]; exists {
			t.Fatalf("unexpected thinking in %#v", body)
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, "model:\n  provider: deepseek\n"); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "--thinking", "medium", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRunRejectsInvalidThinkingFlag(t *testing.T) {
	// --thinking turbo is not a legal mode and must be rejected before any
	// model call, mentioning the allowed set.
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected model request %s", request.URL.String())
		return nil, nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := Run(context.Background(), []string{"--thinking", "turbo", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          &stderr,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	})
	if err == nil {
		t.Fatal("expected an error for invalid --thinking value")
	}
	if !strings.Contains(err.Error(), `--thinking must be one of "off", "minimal", "low", "medium", "high"`) {
		t.Fatalf("error = %q, want the allowed set", err.Error())
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
	t.Setenv("HOME", t.TempDir())
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
	if err := writeTestConfig(t, configPath, configContents); err != nil {
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
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
agent:
  max_rounds: 5
tools:
  bash:
    enabled: true
`); err != nil {
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

func TestRunDoesNotPromptAfterMaximumRoundsWithoutTerminalInput(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf limited\"}"}}]}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
agent:
  max_rounds: 1
tools:
  bash:
    enabled: true
`); err != nil {
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

func TestRunSkipsDefaultBashWhenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DORA_HOME", t.TempDir())
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		// The job tool is always present, but the bash tool must NOT be
		// (bash is unavailable).
		tools, ok := body["tools"].([]any)
		if !ok {
			t.Fatalf("expected tools (job tool always present), got none")
		}
		for _, tool := range tools {
			fn := tool.(map[string]any)["function"].(map[string]any)
			if fn["name"] == "bash" {
				t.Fatalf("bash tool should be skipped, got %#v", tools)
			}
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"no bash needed"}}]}`), nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`); err != nil {
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
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
tools:
  bash:
    enabled: true
`); err != nil {
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
	t.Setenv("HOME", t.TempDir())
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
		if len(tools) != 9 {
			t.Fatalf("tools = %#v", tools)
		}
		var names []string
		for _, raw := range tools {
			function := raw.(map[string]any)["function"].(map[string]any)
			names = append(names, function["name"].(string))
		}
		if strings.Join(names, ",") != "bash,powershell,job,view_image,read,write,edit,grep,glob" {
			t.Fatalf("tool names = %#v", names)
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"both available"}}]}`), nil
	})}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
tools:
  bash:
    enabled: true
  powershell:
    enabled: true
`); err != nil {
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
	if err := writeTestConfig(t, configPath, configContents); err != nil {
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
		// The job tool is always present, but the skill tool must NOT be.
		tools, ok := body["tools"].([]any)
		if !ok {
			t.Fatalf("expected tools (job tool always present), got none")
		}
		for _, tool := range tools {
			fn := tool.(map[string]any)["function"].(map[string]any)
			if fn["name"] == "skill" {
				t.Fatalf("skill tool should be absent, got %#v", tools)
			}
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"no skills"}}]}`), nil
	})}
	root := t.TempDir()
	t.Setenv("DORA_HOME", root)
	t.Setenv("HOME", filepath.Join(root, "home"))
	if err := os.Mkdir(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
tools:
  bash:
    enabled: false
  powershell:
    enabled: false
`); err != nil {
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

func TestRunRejectsMissingConfiguredSkillDirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, fmt.Sprintf(`
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
skills:
  directories:
    - %s
`, filepath.Join(root, "missing"))); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
	})
	if err == nil || !strings.Contains(err.Error(), "inspect skill directory") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunLoadsConfiguredSkillDirectories(t *testing.T) {
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
		// The job tool is always present; find the skill tool among them.
		var skillTool map[string]any
		for _, tool := range tools {
			fn := tool.(map[string]any)["function"].(map[string]any)
			if fn["name"] == "skill" {
				skillTool = fn
				break
			}
		}
		if skillTool == nil {
			t.Fatalf("skill tool not found in tools = %#v", tools)
		}
		description := skillTool["description"].(string)
		if !strings.Contains(description, "alpha") || !strings.Contains(description, "beta") {
			t.Fatalf("description = %q", description)
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
	})}
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, fmt.Sprintf(`
model:
  provider: openai
skills:
  directories:
    - %s
    - %s
    - %s
tools:
  bash:
    enabled: false
  powershell:
    enabled: false
`, first, first, second)); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), []string{
		"-q", "--config", configPath, "hello",
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
		// The job tool is always present, but the skill tool must NOT be.
		tools, ok := body["tools"].([]any)
		if !ok {
			t.Fatalf("expected tools (job tool always present), got none")
		}
		for _, tool := range tools {
			fn := tool.(map[string]any)["function"].(map[string]any)
			if fn["name"] == "skill" {
				t.Fatalf("skill tool should be disabled, got %#v", tools)
			}
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
	})}
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, fmt.Sprintf(`
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
`, missing)); err != nil {
		t.Fatal(err)
	}

	if err := Run(context.Background(), []string{
		"-q", "--config", configPath, "--no-skills", "hello",
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

func TestRunLoadsSkillFromAgentsHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", filepath.Join(root, "dora"))
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	writeCLITestSkill(t, filepath.Join(home, ".agents", "skills"), "system-status")

	var capturedDescription string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, tool := range body["tools"].([]any) {
			fn := tool.(map[string]any)["function"].(map[string]any)
			if fn["name"] == "skill" {
				capturedDescription = fn["description"].(string)
			}
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
	})}
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
tools:
  bash:
    enabled: false
  powershell:
    enabled: false
`); err != nil {
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
	if !strings.Contains(capturedDescription, "system-status") {
		t.Fatalf("description = %q", capturedDescription)
	}
}

func TestRunSkipsAbsentDefaultSkillDirectories(t *testing.T) {
	root := t.TempDir()
	// Neither <doraHome>/skills nor ~/.agents/skills exists.
	t.Setenv("DORA_HOME", filepath.Join(root, "dora"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, tool := range body["tools"].([]any) {
			fn := tool.(map[string]any)["function"].(map[string]any)
			if fn["name"] == "skill" {
				t.Fatalf("skill tool should be absent, got %#v", body["tools"])
			}
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
	})}
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
tools:
  bash:
    enabled: false
  powershell:
    enabled: false
`); err != nil {
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

func TestRunConfiguredDirectoriesReplaceDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", filepath.Join(root, "dora"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	// A default-located skill that must NOT be loaded because a configured
	// directory replaces the defaults.
	writeCLITestSkill(t, filepath.Join(root, "dora", "skills"), "default-skill")
	configured := filepath.Join(root, "configured")
	writeCLITestSkill(t, configured, "configured-skill")

	var capturedDescription string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, tool := range body["tools"].([]any) {
			fn := tool.(map[string]any)["function"].(map[string]any)
			if fn["name"] == "skill" {
				capturedDescription = fn["description"].(string)
			}
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
	})}
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, fmt.Sprintf(`
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
`, configured)); err != nil {
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
	if !strings.Contains(capturedDescription, "configured-skill") {
		t.Fatalf("description = %q", capturedDescription)
	}
	if strings.Contains(capturedDescription, "default-skill") {
		t.Fatalf("default skill should not load when configured directories are set, got %q", capturedDescription)
	}
}

func TestRunExpandsTildeSkillDirectory(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DORA_HOME", filepath.Join(root, "dora"))
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	writeCLITestSkill(t, filepath.Join(home, "myskills"), "tilde-skill")

	var capturedDescription string
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for _, tool := range body["tools"].([]any) {
			fn := tool.(map[string]any)["function"].(map[string]any)
			if fn["name"] == "skill" {
				capturedDescription = fn["description"].(string)
			}
		}
		return fakeJSONResponse(`{"choices":[{"message":{"role":"assistant","content":"done"}}]}`), nil
	})}
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
skills:
  directories:
    - ~/myskills
tools:
  bash:
    enabled: false
  powershell:
    enabled: false
`); err != nil {
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
	if !strings.Contains(capturedDescription, "tilde-skill") {
		t.Fatalf("description = %q", capturedDescription)
	}
}

func TestRunRejectsRelativeSkillDirectory(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
skills:
  directories:
    - foo/
`); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), []string{"-q", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          io.Discard,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
	})
	if err == nil || !strings.Contains(err.Error(), `skill directory "foo/" must be an absolute path or start with ~/`) {
		t.Fatalf("error = %v", err)
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
	if err := writeTestConfig(t, configPath, configContents); err != nil {
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
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`); err != nil {
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

// writeTestConfig converts the former flat test fixtures into the catalog-only
func TestRunStoresIndependentTurnsInSQLiteSession(t *testing.T) {
	var calls int
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		messages := body["messages"].([]any)
		if len(messages) != 1 {
			t.Fatalf("call %d loaded history into model messages: %#v", calls, messages)
		}
		if messages[0].(map[string]any)["content"] != []string{"first", "second"}[calls-1] {
			t.Fatalf("call %d messages = %#v", calls, messages)
		}
		foundHistory := false
		for _, raw := range body["tools"].([]any) {
			function := raw.(map[string]any)["function"].(map[string]any)
			if function["name"] == "history" {
				foundHistory = true
			}
		}
		if foundHistory != (calls > 1) {
			t.Fatalf("call %d history tool present = %v", calls, foundHistory)
		}
		return fakeJSONResponse(fmt.Sprintf(`{"choices":[{"message":{"role":"assistant","content":"answer %d"}}]}`, calls)), nil
	})}

	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := writeTestConfig(t, configPath, `
model:
  provider: openai
  name: test-model
  base_url: https://example.test/v1
`); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(root, "turns.sqlite")
	for _, prompt := range []string{"first", "second"} {
		if err := Run(context.Background(), []string{"-q", "--no-skills", "--session", sessionPath, "--config", configPath, prompt}, IO{
			Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
			StdinIsTerminal: true, HTTPClient: httpClient,
		}); err != nil {
			t.Fatal(err)
		}
	}

	store, err := sqlitesession.Open(context.Background(), sessionPath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	page, err := store.ListTurns(context.Background(), session.ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Turns) != 2 || page.Turns[0].User != "second" || page.Turns[1].User != "first" ||
		page.Turns[0].Result != "answer 2" || page.Turns[0].RoundCount != 0 {
		t.Fatalf("page = %#v", page)
	}
}

func TestRunRejectsRemovedFreshFlag(t *testing.T) {
	err := Run(context.Background(), []string{"--fresh", "hello"}, IO{Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunModelFlagOverridesConfiguredProfile(t *testing.T) {
	// -m trust/deepseek-v4-pro must beat the configured policy default
	// (deepseek-v4-flash) and route to the deepseek-v4-pro model.
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "deepseek-v4-pro" {
			t.Fatalf("model = %#v, want \"deepseek-v4-pro\"", body["model"])
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"pro answer"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
providers:
  - name: trust
    base_url: https://api.trustoken.cn/v1
    profiles:
      - name: deepseek-v4-flash
        model: deepseek-v4-flash
        capabilities: [text]
      - name: deepseek-v4-pro
        model: deepseek-v4-pro
        capabilities: [text]
env:
  TRUST_API_KEY: trust-secret
policy:
  text:
    provider: trust
    profile: deepseek-v4-flash
`); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "-m", "trust/deepseek-v4-pro", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "pro answer\n" {
		t.Fatalf("stdout = %q, want \"pro answer\\n\"", stdout.String())
	}
}

func TestRunModelFlagProviderOnlySelectsDefaultProfile(t *testing.T) {
	// -m trust (no slash) selects the trust provider's first matching profile,
	// overriding the configured deepseek default.
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "trust-default" {
			t.Fatalf("model = %#v, want \"trust-default\"", body["model"])
		}
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"trust default"}}]}`), nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
providers:
  - name: deepseek
    base_url: https://api.deepseek.com
    profiles:
      - name: deepseek-v4-flash
        model: deepseek-v4-flash
        capabilities: [text]
  - name: trust
    base_url: https://api.trustoken.cn/v1
    profiles:
      - name: trust-default
        model: trust-default
        capabilities: [text]
env:
  DEEPSEEK_API_KEY: deepseek-secret
  TRUST_API_KEY: trust-secret
policy:
  text:
    provider: deepseek
    profile: deepseek-v4-flash
`); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	if err := Run(context.Background(), []string{"-q", "-m", "trust", "--config", configPath, "hello"}, IO{
		Stdin:           strings.NewReader(""),
		Stdout:          &stdout,
		Stderr:          io.Discard,
		StdinIsTerminal: true,
		HTTPClient:      httpClient,
	}); err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "trust default\n" {
		t.Fatalf("stdout = %q, want \"trust default\\n\"", stdout.String())
	}
}

func TestRunRejectsInvalidModelFlag(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected model request %s", request.URL.String())
		return nil, nil
	})}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, "model:\n  provider: deepseek\n"); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []string{"/deepseek-v4-flash", "a/b/c", "a//b"} {
		err := Run(context.Background(), []string{"-q", "-m", spec, "--config", configPath, "hello"}, IO{
			Stdin:           strings.NewReader(""),
			Stdout:          io.Discard,
			Stderr:          io.Discard,
			StdinIsTerminal: true,
			HTTPClient:      httpClient,
		})
		if err == nil || !strings.Contains(err.Error(), "expected PROVIDER or PROVIDER/PROFILE") {
			t.Fatalf("-m %q error = %v", spec, err)
		}
	}
}

// schema. Production config loading intentionally provides no such legacy
// conversion; this helper keeps the behavioral CLI tests focused on Run.
func writeTestConfig(t *testing.T, path, contents string) error {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal([]byte(contents), &root); err != nil {
		return err
	}
	if _, exists := root["providers"]; !exists {
		legacy, _ := root["model"].(map[string]any)
		provider, _ := legacy["provider"].(string)
		if provider == "" {
			provider = "openai"
		}
		name, _ := legacy["name"].(string)
		if name == "" {
			switch provider {
			case "deepseek":
				name = "deepseek-v4-flash"
			case "trust":
				name = "deepseek-v4-flash"
			default:
				name = "gpt-5"
			}
		}
		providerConfig := map[string]any{"name": provider}
		for _, field := range []string{"api", "base_url", "timeout_seconds", "connect_timeout_seconds", "stream_idle_timeout_seconds"} {
			if value, ok := legacy[field]; ok {
				providerConfig[field] = value
			}
		}
		if value, ok := legacy["api_key"]; ok {
			environment, _ := root["env"].(map[string]any)
			if environment == nil {
				environment = make(map[string]any)
				root["env"] = environment
			}
			environment[strings.ToUpper(strings.ReplaceAll(provider, "-", "_"))+"_API_KEY"] = value
		}
		if provider == "openai" {
			if _, exists := providerConfig["base_url"]; !exists {
				providerConfig["base_url"] = "https://api.openai.com/v1"
			}
		}
		modelConfig := map[string]any{"name": name, "model": name, "capabilities": []string{"text"}}
		for _, field := range []string{"thinking", "max_tokens", "temperature"} {
			if value, ok := legacy[field]; ok {
				modelConfig[field] = value
			}
		}
		if value, ok := legacy["vision"]; ok {
			if v, _ := value.(bool); v {
				modelConfig["capabilities"] = []string{"text", "image_input"}
			}
		}
		providerConfig["profiles"] = []any{modelConfig}
		root["providers"] = []any{providerConfig}
		root["policy"] = map[string]any{
			"text": map[string]any{"provider": provider, "profile": name},
		}
		delete(root, "model")
	}
	encoded, err := yaml.Marshal(root)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
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
