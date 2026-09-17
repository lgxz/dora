package openai

import (
	"context"
	"encoding/json"
	"github.com/lgxz/dora"
	"net/http"
	"testing"
)

func TestFinishReasonsAndActualOutputBudget(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want dora.FinishReason
	}{
		{"stop", dora.FinishStop}, {"tool_calls", dora.FinishToolCalls}, {"length", dora.FinishOutputLimit},
		{"content_filter", dora.FinishBlocked}, {"future_reason", dora.FinishUnknown}, {"", ""},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			cap := 32768
			client, err := New(Config{BaseURL: "https://example.test", Model: "test", MaxTokens: &cap, HTTPClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				var req struct {
					MaxTokens int `json:"max_tokens"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if req.MaxTokens != 32768 {
					t.Fatalf("budget=%d", req.MaxTokens)
				}
				event, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": tc.raw}}})
				return streamResponse(`{"choices":[{"index":0,"delta":{"reasoning_content":"unfinished"}}]}`, string(event), `{"choices":[],"usage":{"prompt_tokens":3417,"completion_tokens":32768,"total_tokens":36185,"completion_tokens_details":{"reasoning_tokens":32768}}}`), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Generate(context.Background(), dora.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if response.FinishReason != tc.want || response.RawFinishReason != tc.raw || response.OutputBudget != 32768 || response.Reasoning != "unfinished" || response.Usage.OutputTokens != 32768 {
				t.Fatalf("response=%+v", response)
			}
		})
	}
}
