package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
