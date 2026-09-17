package openairesponses

import (
	"context"
	"github.com/lgxz/dora"
	"net/http"
	"testing"
)

func TestIncompleteResponsesPreserveOutputAndUsage(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   dora.FinishReason
	}{
		{"max_output_tokens", dora.FinishOutputLimit}, {"content_filter", dora.FinishBlocked}, {"future", dora.FinishUnknown},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			budget := 32768
			client, err := New(Config{BaseURL: "https://example.test", Model: "test", MaxTokens: &budget, HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return eventStream(`data: {"type":"response.incomplete","response":{"id":"r","status":"incomplete","incomplete_details":{"reason":"` + tc.reason + `"},"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"partial reasoning"}]},{"type":"function_call","call_id":"c","name":"run","arguments":"{"}],"usage":{"input_tokens":10,"output_tokens":32768,"total_tokens":32778}}}` + "\n\n"), nil
			})}})
			if err != nil {
				t.Fatal(err)
			}
			r, err := client.Generate(context.Background(), dora.Request{})
			if err != nil {
				t.Fatal(err)
			}
			if r.FinishReason != tc.want || r.Reasoning != "partial reasoning" || len(r.ToolCalls) != 1 || r.OutputBudget != budget || r.Usage.TotalTokens != 32778 {
				t.Fatalf("response=%+v", r)
			}
		})
	}
}
