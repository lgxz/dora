package openairesponses

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"dora"
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
