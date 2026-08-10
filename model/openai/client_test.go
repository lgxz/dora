package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

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

func TestGenerateRejectsInvalidToolArguments(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return streamResponse(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"1","function":{"name":"bad","arguments":"not-json"}}]}}]}`), nil
	})}

	client, err := New(Config{BaseURL: "https://example.test", Model: "test-model", HTTPClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Generate(context.Background(), dora.Request{})
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("error = %v", err)
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
