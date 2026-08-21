package openairesponses

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

func TestGenerateStreamMapsRequestAndEvents(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/responses" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		if request.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("accept = %q", request.Header.Get("Accept"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["stream"] != true || body["store"] != false || body["model"] != "test-model" {
			t.Fatalf("body = %#v", body)
		}
		include := body["include"].([]any)
		if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
			t.Fatalf("include = %#v", include)
		}
		input := body["input"].([]any)
		if input[0].(map[string]any)["content"] != "hello" {
			t.Fatalf("input = %#v", input)
		}
		tool := body["tools"].([]any)[0].(map[string]any)
		if tool["name"] != "weather" || tool["type"] != "function" {
			t.Fatalf("tool = %#v", tool)
		}

		return eventStream(strings.Join([]string{
			`data: {"type":"response.output_text.delta","delta":"hel"}`,
			"",
			`data: {"type":"response.output_text.delta","delta":"lo"}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp-1","output":[{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`,
			"",
		}, "\n")), nil
	})}

	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	var deltas strings.Builder
	response, err := client.GenerateStream(context.Background(), dora.Request{
		Messages: []dora.Message{{Role: dora.RoleUser, Content: "hello"}},
		Tools:    []dora.ToolSpec{{Name: "weather", InputSchema: json.RawMessage(`{"type":"object"}`)}},
	}, func(event dora.ModelEvent) {
		if event.Kind == dora.ModelEventContentDelta {
			deltas.WriteString(event.Delta)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	continued, continuationErr := decodeContinuation(response.Continuation)
	if response.Content != "hello" || continuationErr != nil || continued.BaseMessageCount != 1 || continued.MessageCount != 2 || len(continued.Items) != 1 || deltas.String() != "hello" {
		t.Fatalf("response = %#v, deltas = %q", response, deltas.String())
	}
}

func TestGenerateStreamReportsCompletedToolCall(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return eventStream(strings.Join([]string{
			`data: {"type":"response.output_item.added","item":{"id":"item-1","type":"function_call","call_id":"call-1","name":"weather","arguments":""}}`,
			"",
			`data: {"type":"response.function_call_arguments.done","item_id":"item-1","arguments":"{\"city\":\"Paris\"}"}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","call_id":"call-1","name":"weather","arguments":"{\"city\":\"Paris\"}"}]}}`,
			"",
		}, "\n")), nil
	})}
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	var ready dora.ToolCall
	response, err := client.GenerateStream(context.Background(), dora.Request{}, func(event dora.ModelEvent) {
		if event.Kind == dora.ModelEventToolCallReady {
			ready = event.ToolCall
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || ready.ID != "call-1" || ready.Name != "weather" || string(ready.Input) != `{"city":"Paris"}` {
		t.Fatalf("response = %#v, ready = %#v", response, ready)
	}
}

func TestGenerateStreamEmitsReasoningSummary(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return eventStream(strings.Join([]string{
			`data: {"type":"response.reasoning_summary_text.delta","delta":"think "}`,
			"",
			`data: {"type":"response.reasoning_summary_text.delta","delta":"hard"}`,
			"",
			`data: {"type":"response.completed","response":{"id":"resp-1","output":[{"id":"rs-1","type":"reasoning","summary":[{"type":"summary_text","text":"think "},{"type":"summary_text","text":"hard"}],"encrypted_content":"opaque"},{"type":"message","content":[{"type":"output_text","text":"hello"}]}]}}`,
			"",
		}, "\n")), nil
	})}
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	var deltas strings.Builder
	response, err := client.GenerateStream(context.Background(), dora.Request{}, func(event dora.ModelEvent) {
		if event.Kind == dora.ModelEventReasoningDelta {
			deltas.WriteString(event.Delta)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	// The completed payload's summary parts are the authoritative reasoning;
	// the streamed deltas mirror them.
	if response.Reasoning != "think hard" || deltas.String() != "think hard" {
		t.Fatalf("reasoning = %q, deltas = %q", response.Reasoning, deltas.String())
	}
	if response.Content != "hello" {
		t.Fatalf("content = %q", response.Content)
	}
	// The raw reasoning item must pass through the continuation untouched so
	// follow-up requests keep the encrypted reasoning context.
	continued, err := decodeContinuation(response.Continuation)
	if err != nil {
		t.Fatal(err)
	}
	if len(continued.Items) != 2 || !strings.Contains(string(continued.Items[0]), `"encrypted_content":"opaque"`) {
		t.Fatalf("continuation items = %#v", continued.Items)
	}
}

func TestContinuationReplaysTypedOutputAndToolResults(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if _, exists := body["previous_response_id"]; exists {
			t.Fatalf("unexpected previous_response_id in %#v", body)
		}
		input := body["input"].([]any)
		if len(input) != 4 {
			t.Fatalf("input = %#v", input)
		}
		if input[0].(map[string]any)["content"] != "weather?" {
			t.Fatalf("history = %#v", input[0])
		}
		reasoning := input[1].(map[string]any)
		if reasoning["type"] != "reasoning" || reasoning["encrypted_content"] != "opaque" {
			t.Fatalf("reasoning = %#v", reasoning)
		}
		call := input[2].(map[string]any)
		if call["type"] != "function_call" || call["call_id"] != "call-1" {
			t.Fatalf("call = %#v", call)
		}
		output := input[3].(map[string]any)
		if output["type"] != "function_call_output" || output["call_id"] != "call-1" || output["output"] != "sunny" {
			t.Fatalf("output = %#v", output)
		}
		return eventStream("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"sunny\"}]}]}}\n\n"), nil
	})}
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), dora.Request{
		Continuation: `{"base_message_count":1,"message_count":2,"items":[{"id":"reasoning-1","type":"reasoning","encrypted_content":"opaque"},{"type":"function_call","call_id":"call-1","name":"weather","arguments":"{}"}]}`,
		Messages: []dora.Message{
			{Role: dora.RoleUser, Content: "weather?"},
			{Role: dora.RoleAssistant, ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "weather"}}},
			{Role: dora.RoleTool, ToolCallID: "call-1", Content: "sunny"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestContinuationAccumulatesAcrossMultipleToolRounds(t *testing.T) {
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
			if len(input) != 1 {
				t.Fatalf("first input = %#v", input)
			}
			return eventStream(completedEvent(`[{"id":"reasoning-1","type":"reasoning","encrypted_content":"one"},{"type":"function_call","call_id":"call-1","name":"step","arguments":"{}"}]`)), nil
		case 2:
			if len(input) != 4 || input[3].(map[string]any)["call_id"] != "call-1" {
				t.Fatalf("second input = %#v", input)
			}
			return eventStream(completedEvent(`[{"id":"reasoning-2","type":"reasoning","encrypted_content":"two"},{"type":"function_call","call_id":"call-2","name":"step","arguments":"{}"}]`)), nil
		case 3:
			if len(input) != 7 ||
				input[1].(map[string]any)["id"] != "reasoning-1" ||
				input[3].(map[string]any)["call_id"] != "call-1" ||
				input[4].(map[string]any)["id"] != "reasoning-2" ||
				input[6].(map[string]any)["call_id"] != "call-2" {
				t.Fatalf("third input = %#v", input)
			}
			return eventStream(completedEvent(`[{"type":"message","content":[{"type":"output_text","text":"done"}]}]`)), nil
		default:
			t.Fatalf("request count = %d", calls)
			return nil, nil
		}
	})}
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	messages := []dora.Message{{Role: dora.RoleUser, Content: "run"}}
	first, err := client.Generate(context.Background(), dora.Request{Messages: messages})
	if err != nil {
		t.Fatal(err)
	}
	messages = append(messages,
		dora.Message{Role: dora.RoleAssistant, ToolCalls: first.ToolCalls},
		dora.Message{Role: dora.RoleTool, ToolCallID: "call-1", Content: "one"},
	)
	second, err := client.Generate(context.Background(), dora.Request{Messages: messages, Continuation: first.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	messages = append(messages,
		dora.Message{Role: dora.RoleAssistant, ToolCalls: second.ToolCalls},
		dora.Message{Role: dora.RoleTool, ToolCallID: "call-2", Content: "two"},
	)
	third, err := client.Generate(context.Background(), dora.Request{Messages: messages, Continuation: second.Continuation})
	if err != nil {
		t.Fatal(err)
	}
	if third.Content != "done" || calls != 3 {
		t.Fatalf("response = %#v, calls = %d", third, calls)
	}
}

func completedEvent(output string) string {
	return `data: {"type":"response.completed","response":{"id":"resp","output":` + output + "}}\n\n"
}

func TestGenerateRejectsStreamWithoutCompletion(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return eventStream("data: [DONE]\n\n"), nil
	})}
	client, err := New(Config{BaseURL: "https://example.test", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), dora.Request{})
	if err == nil || !strings.Contains(err.Error(), "before response.completed") {
		t.Fatalf("error = %v", err)
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
		{name: "bad request", status: http.StatusBadRequest, wantRetry: false},
		{name: "unauthorized", status: http.StatusUnauthorized, wantRetry: false},
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
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
	if parts[0]["type"] != "input_text" || parts[0]["text"] != "look" {
		t.Fatalf("text part = %#v", parts[0])
	}
	if parts[1]["type"] != "input_image" || parts[1]["image_url"] != "https://example.test/a.png" {
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
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)
	if parts[1]["image_url"] != want {
		t.Fatalf("url = %v, want %q", parts[1]["image_url"], want)
	}
}

func TestContinuationCarriesImages(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		input := body["input"].([]any)
		// The continuation replays the base message (with image) plus the
		// continuation item and the appended tool result.
		if len(input) != 3 {
			t.Fatalf("input = %#v", input)
		}
		content := input[0].(map[string]any)["content"].([]any)
		if len(content) != 2 || content[1].(map[string]any)["type"] != "input_image" {
			t.Fatalf("content = %#v", content)
		}
		return eventStream("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-2\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}]}}\n\n"), nil
	})}
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), dora.Request{
		Continuation: `{"base_message_count":1,"message_count":1,"items":[{"type":"function_call","call_id":"call-1","name":"weather","arguments":"{}"}]}`,
		Messages: []dora.Message{
			{Role: dora.RoleUser, Content: "look", Images: []dora.Image{{URL: "https://example.test/a.png"}}},
			{Role: dora.RoleTool, ToolCallID: "call-1", Content: "sunny"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRequestBodyDefaultMaxOutputTokensOmitted(t *testing.T) {
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
	if _, exists := decoded["max_output_tokens"]; exists {
		t.Fatalf("unexpected max_output_tokens in %#v", decoded)
	}
	if _, exists := decoded["max_tokens"]; exists {
		t.Fatalf("unexpected max_tokens in %#v", decoded)
	}
	if _, exists := decoded["temperature"]; exists {
		t.Fatalf("unexpected temperature in %#v", decoded)
	}
}

func TestRequestBodyEmitsMaxOutputTokensAndTemperature(t *testing.T) {
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
	// The Responses API key is max_output_tokens, not max_tokens.
	if decoded["max_output_tokens"] != float64(4096) {
		t.Fatalf("max_output_tokens = %#v, want 4096", decoded["max_output_tokens"])
	}
	if _, exists := decoded["max_tokens"]; exists {
		t.Fatalf("unexpected max_tokens in %#v", decoded)
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
	if _, exists := decoded["max_output_tokens"]; exists {
		t.Fatalf("unexpected max_output_tokens in %#v", decoded)
	}
}

func TestRequestBodyEmitsReasoningNone(t *testing.T) {
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", Reasoning: NewReasoningControl("none")})
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
	reasoning := decoded["reasoning"].(map[string]any)
	if reasoning["effort"] != "none" {
		t.Fatalf("reasoning = %#v, want {\"effort\":\"none\"}", decoded["reasoning"])
	}
}

func TestRequestBodyEmitsReasoningHigh(t *testing.T) {
	client, err := New(Config{BaseURL: "https://example.test/v1", Model: "test-model", Reasoning: NewReasoningControl("high")})
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
	reasoning := decoded["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Fatalf("reasoning = %#v, want {\"effort\":\"high\"}", decoded["reasoning"])
	}
}

func TestRequestBodyKeepsIncludeAndOmitsReasoningWhenUnset(t *testing.T) {
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
	include := decoded["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Fatalf("include = %#v, want [reasoning.encrypted_content]", decoded["include"])
	}
	if _, exists := decoded["reasoning"]; exists {
		t.Fatalf("unexpected reasoning in %#v", decoded)
	}
}

func TestGenerateSendsMaxOutputTokensOnWire(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		// The Responses API key is max_output_tokens, not max_tokens.
		if body["max_output_tokens"] != float64(32768) {
			t.Fatalf("max_output_tokens = %#v, want 32768", body["max_output_tokens"])
		}
		if _, exists := body["max_tokens"]; exists {
			t.Fatalf("unexpected max_tokens in %#v", body)
		}
		return eventStream("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"ok\"}]}]}}\n\n"), nil
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func eventStream(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
	}
}
