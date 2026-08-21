package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora"
)

func TestGenerateMapsConversationAndToolCall(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("accept = %q", request.Header.Get("Accept"))
		}

		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "test-model" {
			t.Fatalf("model = %#v", body["model"])
		}
		if body["stream"] != true {
			t.Fatalf("stream = %#v", body["stream"])
		}
		messages := body["messages"].([]any)
		if len(messages) != 3 || messages[2].(map[string]any)["tool_call_id"] != "call-old" {
			t.Fatalf("messages = %#v", messages)
		}
		tools := body["tools"].([]any)
		function := tools[0].(map[string]any)["function"].(map[string]any)
		if function["name"] != "weather" {
			t.Fatalf("function = %#v", function)
		}

		return streamResponse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-new","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]}}]}`), nil
	})}

	client, err := New(Config{
		BaseURL:    "https://example.test/v1",
		APIKey:     "secret",
		Model:      "test-model",
		HTTPClient: httpClient,
	})
	if err != nil {
		t.Fatal(err)
	}

	response, err := client.Generate(context.Background(), dora.Request{
		Messages: []dora.Message{
			{Role: dora.RoleUser, Content: "weather?"},
			{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{{ID: "call-old", Name: "weather", Input: json.RawMessage(`{"city":"Rome"}`)}}},
			{Role: dora.RoleTool, ToolCallID: "call-old", Content: "sunny"},
		},
		Tools: []dora.ToolSpec{{
			Name:        "weather",
			Description: "Get weather",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "weather" {
		t.Fatalf("response = %#v", response)
	}
	if string(response.ToolCalls[0].Input) != `{"city":"Paris"}` {
		t.Fatalf("input = %s", response.ToolCalls[0].Input)
	}
}

func TestGenerateStreamEmitsContentAndCompletedToolCall(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return streamResponse(
			`{"choices":[{"index":0,"delta":{"content":"hello "}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"world","tool_calls":[{"index":0,"id":"call-1","function":{"name":"weather","arguments":"{\"city\":"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Paris\"}"}}]}}]}`,
		), nil
	})}
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}

	var events []dora.ModelEvent
	response, err := client.GenerateStream(context.Background(), dora.Request{}, func(event dora.ModelEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "hello world" || len(response.ToolCalls) != 1 ||
		string(response.ToolCalls[0].Input) != `{"city":"Paris"}` {
		t.Fatalf("response = %#v", response)
	}
	if len(events) != 3 || events[0].Delta != "hello " || events[1].Delta != "world" ||
		events[2].Kind != dora.ModelEventToolCallReady {
		t.Fatalf("events = %#v", events)
	}
}

func TestGenerateReturnsAPIError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"message":"bad key"}}`), nil
	})}

	client, err := New(Config{BaseURL: "https://example.test", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), dora.Request{})
	if err == nil || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error = %v", err)
	}
}

func TestGenerateReturnsRawInvalidToolArguments(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return streamResponse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"bad","arguments":"not-json"}}]}}]}`), nil
	})}

	client, err := New(Config{BaseURL: "https://example.test", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Generate(context.Background(), dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "bad" ||
		string(response.ToolCalls[0].Input) != "not-json" {
		t.Fatalf("response = %#v", response)
	}
}

func TestGenerateClassifiesRetryableStatus(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		retryAfter string
		wantRetry  bool
		wantDelay  time.Duration
	}{
		{name: "rate limited", status: http.StatusTooManyRequests, retryAfter: "5", wantRetry: true, wantDelay: 5 * time.Second},
		{name: "rate limited without retry after", status: http.StatusTooManyRequests, wantRetry: true, wantDelay: defaultRateLimitRetryAfter},
		{name: "server error", status: http.StatusInternalServerError, wantRetry: true},
		{name: "bad gateway", status: http.StatusBadGateway, wantRetry: true},
		{name: "bad request", status: http.StatusBadRequest, wantRetry: false},
		{name: "unauthorized", status: http.StatusUnauthorized, wantRetry: false},
		{name: "forbidden", status: http.StatusForbidden, wantRetry: false},
		{name: "unprocessable", status: http.StatusUnprocessableEntity, wantRetry: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				header := make(http.Header)
				if test.retryAfter != "" {
					header.Set("Retry-After", test.retryAfter)
				}
				return &http.Response{
					StatusCode: test.status,
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"boom"}}`)),
					Header:     header,
				}, nil
			})}
			client, err := New(Config{BaseURL: "https://example.test", Model: "test-model", HTTPClient: httpClient})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Generate(context.Background(), dora.Request{})
			if err == nil {
				t.Fatal("expected error")
			}
			var retryable *dora.RetryableError
			gotRetry := errors.As(err, &retryable)
			if gotRetry != test.wantRetry {
				t.Fatalf("retryable = %v, want %v (error %v)", gotRetry, test.wantRetry, err)
			}
			if gotRetry && retryable.RetryAfter != test.wantDelay {
				t.Fatalf("retry after = %v, want %v", retryable.RetryAfter, test.wantDelay)
			}
		})
	}
}

func TestGenerateStreamIdleTimeout(t *testing.T) {
	// A stream that stalls before emitting any content should fail with a
	// retryable error once the idle timeout elapses.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// Flush headers and then stall without sending any events.
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	}))
	defer server.Close()

	client, err := New(Config{
		BaseURL:           server.URL,
		Model:             "test-model",
		StreamIdleTimeout: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.GenerateStream(context.Background(), dora.Request{}, nil)
	if err == nil {
		t.Fatal("expected idle timeout error")
	}
	var retryable *dora.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("error = %v, want retryable", err)
	}
}

func TestEncodeContentWithoutImagesIsPlainString(t *testing.T) {
	encoded, err := encodeContent(dora.Message{Role: dora.RoleUser, Content: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `"hello"` {
		t.Fatalf("content = %s", encoded)
	}
}

func TestEncodeContentWithImageURL(t *testing.T) {
	encoded, err := encodeContent(dora.Message{
		Role:    dora.RoleUser,
		Content: "look",
		Images:  []dora.Image{{URL: "https://example.test/a.png"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(encoded, &parts); err != nil {
		t.Fatal(err)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %#v", parts)
	}
	if parts[0]["type"] != "text" || parts[0]["text"] != "look" {
		t.Fatalf("text part = %#v", parts[0])
	}
	imageURL := parts[1]["image_url"].(map[string]any)
	if parts[1]["type"] != "image_url" || imageURL["url"] != "https://example.test/a.png" {
		t.Fatalf("image part = %#v", parts[1])
	}
}

func TestEncodeContentWithImagePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shot.png")
	data := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a, 0x00}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeContent(dora.Message{
		Role:    dora.RoleUser,
		Content: "look",
		Images:  []dora.Image{{Path: path}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var parts []map[string]any
	if err := json.Unmarshal(encoded, &parts); err != nil {
		t.Fatal(err)
	}
	imageURL := parts[1]["image_url"].(map[string]any)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if imageURL["url"] != want {
		t.Fatalf("url = %v, want %q", imageURL["url"], want)
	}
}

func TestEncodeContentRejectsImageWithoutSource(t *testing.T) {
	_, err := encodeContent(dora.Message{
		Role:   dora.RoleUser,
		Images: []dora.Image{{}},
	})
	if err == nil {
		t.Fatal("expected error for image without Path or URL")
	}
}

func TestRequestBodyDefaultMaxTokensOnWire(t *testing.T) {
	// With no MaxTokens the field is omitted from the body.
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.requestBody(dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["max_tokens"]; exists {
		t.Fatalf("unexpected max_tokens in %#v", decoded)
	}
	if _, exists := decoded["temperature"]; exists {
		t.Fatalf("unexpected temperature in %#v", decoded)
	}
}

func TestRequestBodyEmitsMaxTokensAndTemperature(t *testing.T) {
	maxTokens := 4096
	temperature := 0.5
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", MaxTokens: &maxTokens, Temperature: &temperature})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.requestBody(dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["max_tokens"] != float64(4096) {
		t.Fatalf("max_tokens = %#v, want 4096", decoded["max_tokens"])
	}
	if decoded["temperature"] != 0.5 {
		t.Fatalf("temperature = %#v, want 0.5", decoded["temperature"])
	}
}

func TestRequestBodyEmitsExplicitZeroTemperature(t *testing.T) {
	temperature := 0.0
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", Temperature: &temperature})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.requestBody(dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if value, exists := decoded["temperature"]; !exists || value != 0.0 {
		t.Fatalf("temperature = %#v (exists %v), want 0", value, exists)
	}
	if _, exists := decoded["max_tokens"]; exists {
		t.Fatalf("unexpected max_tokens in %#v", decoded)
	}
}

func TestRequestBodyEmitsReasoningEffort(t *testing.T) {
	effort := "low"
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", ReasoningEffort: &effort})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.requestBody(dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %#v, want \"low\"", decoded["reasoning_effort"])
	}
}

func TestRequestBodyEmitsThinkingControl(t *testing.T) {
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", Thinking: NewThinkingControl("disabled")})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.requestBody(dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	thinking := decoded["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking = %#v, want {\"type\":\"disabled\"}", decoded["thinking"])
	}
}

func TestRequestBodyOmitsThinkingControlsWhenUnset(t *testing.T) {
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model"})
	if err != nil {
		t.Fatal(err)
	}
	body, err := client.requestBody(dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, exists := decoded["reasoning_effort"]; exists {
		t.Fatalf("unexpected reasoning_effort in %#v", decoded)
	}
	if _, exists := decoded["thinking"]; exists {
		t.Fatalf("unexpected thinking in %#v", decoded)
	}
}

func TestGenerateSendsMaxTokensOnWire(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		// The chat_completions key is max_tokens, not max_output_tokens.
		if body["max_tokens"] != float64(32768) {
			t.Fatalf("max_tokens = %#v, want 32768", body["max_tokens"])
		}
		return streamResponse(`{"choices":[{"index":0,"delta":{"content":"ok"}}]}`), nil
	})}
	maxTokens := 32768
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient, MaxTokens: &maxTokens})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Generate(context.Background(), dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "ok" {
		t.Fatalf("content = %q", response.Content)
	}
}

func TestGenerateStreamEmitsReasoningDeltas(t *testing.T) {
	// Providers expose the chain-of-thought under different delta field names;
	// every known variant must stream as reasoning, separate from content.
	fields := []string{"reasoning_content", "reasoning", "reason"}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return streamResponse(
					fmt.Sprintf(`{"choices":[{"index":0,"delta":{%q:"think "}}]}`, field),
					fmt.Sprintf(`{"choices":[{"index":0,"delta":{%q:"hard"}}]}`, field),
					`{"choices":[{"index":0,"delta":{"content":"answer"}}]}`,
				), nil
			})}
			client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
			if err != nil {
				t.Fatal(err)
			}

			var events []dora.ModelEvent
			response, err := client.GenerateStream(context.Background(), dora.Request{}, func(event dora.ModelEvent) {
				events = append(events, event)
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.Reasoning != "think hard" {
				t.Fatalf("reasoning = %q", response.Reasoning)
			}
			if response.Content != "answer" {
				t.Fatalf("content = %q, want %q with no reasoning leak", response.Content, "answer")
			}
			if len(events) != 3 || events[0].Kind != dora.ModelEventReasoningDelta || events[1].Kind != dora.ModelEventReasoningDelta ||
				events[2].Kind != dora.ModelEventContentDelta || events[0].Delta != "think " || events[1].Delta != "hard" {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestReasoningDeltaPreferReasoningContent(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return streamResponse(`{"choices":[{"index":0,"delta":{"reasoning_content":"primary","reasoning":"secondary","reason":"tertiary"}}]}`), nil
	})}
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Generate(context.Background(), dora.Request{})
	if err != nil {
		t.Fatal(err)
	}
	if response.Reasoning != "primary" {
		t.Fatalf("reasoning = %q, want the reasoning_content value", response.Reasoning)
	}
}

func TestRequestBodyResendsReasoningPerPolicy(t *testing.T) {
	toolCall := dora.ToolCall{ID: "call-1", Name: "weather", Input: json.RawMessage(`{}`)}
	request := dora.Request{Messages: []dora.Message{
		{Role: dora.RoleUser, Content: "hi"},
		{Role: dora.RoleAssistant, Content: "checking", Reasoning: "chain", ToolCalls: []dora.ToolCall{toolCall}},
		{Role: dora.RoleTool, ToolCallID: "call-1", Content: "sunny"},
		{Role: dora.RoleAssistant, Content: "final", Reasoning: "done thinking"},
	}}

	decodedRequest := func(t *testing.T, preserve *bool) []map[string]any {
		t.Helper()
		client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", PreserveThinking: preserve})
		if err != nil {
			t.Fatal(err)
		}
		body, err := client.requestBody(request)
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct {
			Messages []map[string]any `json:"messages"`
		}
		if err := json.Unmarshal(payload, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded.Messages
	}

	trueVal := true
	t.Run("preserve keeps reasoning on tool-calling messages only", func(t *testing.T) {
		messages := decodedRequest(t, &trueVal)
		// messages[0] user, [1] assistant with tool calls, [2] tool, [3] final assistant.
		if messages[1]["reasoning_content"] != "chain" {
			t.Fatalf("tool-calling assistant reasoning_content = %#v, want %q", messages[1]["reasoning_content"], "chain")
		}
		if _, exists := messages[3]["reasoning_content"]; exists {
			t.Fatalf("final assistant message unexpectedly carries reasoning_content: %#v", messages[3])
		}
	})

	t.Run("unset preserver thinking strips reasoning from history", func(t *testing.T) {
		for i, message := range decodedRequest(t, nil) {
			if _, exists := message["reasoning_content"]; exists {
				t.Fatalf("message %d unexpectedly carries reasoning_content: %#v", i, message)
			}
		}
	})

	t.Run("explicit false strips reasoning from history", func(t *testing.T) {
		falseVal := false
		for i, message := range decodedRequest(t, &falseVal) {
			if _, exists := message["reasoning_content"]; exists {
				t.Fatalf("message %d unexpectedly carries reasoning_content: %#v", i, message)
			}
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func streamResponse(events ...string) *http.Response {
	var body strings.Builder
	for _, event := range events {
		body.WriteString("data: ")
		body.WriteString(event)
		body.WriteString("\n\n")
	}
	body.WriteString("data: [DONE]\n\n")
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body.String())),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
}
