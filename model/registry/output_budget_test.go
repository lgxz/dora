package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lgxz/dora"
)

type budgetTestTransport func(*http.Request) (*http.Response, error)

func (f budgetTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProviderOutputBudgetWireFormat(t *testing.T) {
	budget, hardLimit, zero := 4096, 8192, 0
	for _, provider := range []string{"azure", "openai", "custom"} {
		for _, modern := range []bool{false, true} {
			for _, tc := range []struct {
				name     string
				budget   *int
				override int
				want     *int
			}{
				{"default", &budget, 0, &budget},
				{"override", &budget, 8192, &hardLimit},
				{"clamped", &budget, 16384, &hardLimit},
				{"omitted", nil, 0, nil},
				{"zero", &zero, 0, &zero},
			} {
				t.Run(fmt.Sprintf("%s/modern=%t/%s", provider, modern, tc.name), func(t *testing.T) {
					key, absent := "max_tokens", "max_completion_tokens"
					if modern {
						key, absent = absent, key
					}
					model, err := Construct(ProviderConfig{
						Name: provider, API: "chat_completions",
						BaseURL: "https://resource.openai.azure.com/openai/v1/",
						APIKey:  "test-key",
						HTTPClient: &http.Client{Transport: budgetTestTransport(func(r *http.Request) (*http.Response, error) {
							if r.URL.String() != "https://resource.openai.azure.com/openai/v1/chat/completions" {
								t.Fatalf("URL = %s", r.URL)
							}
							if r.Header.Get("Authorization") != "Bearer test-key" {
								t.Fatal("missing authentication")
							}
							var body map[string]any
							if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
								t.Fatal(err)
							}
							if body["model"] != "my-deployment" {
								t.Fatalf("model = %v", body["model"])
							}
							if _, ok := body[absent]; ok {
								t.Fatalf("unexpected %s", absent)
							}
							if tc.want == nil {
								if _, ok := body[key]; ok {
									t.Fatalf("unexpected %s", key)
								}
							} else if body[key] != float64(*tc.want) {
								t.Fatalf("%s = %v, want %d", key, body[key], *tc.want)
							}
							return &http.Response{
								StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
								Body: io.NopCloser(strings.NewReader("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")),
							}, nil
						})},
					}, Profile{UseMaxCompletionTokens: modern, Model: "my-deployment", MaxTokens: tc.budget, MaxOutputTokens: &hardLimit})
					if err != nil {
						t.Fatal(err)
					}
					var override *int
					if tc.override > 0 {
						override = &tc.override
					}
					response, err := model.Generate(context.Background(), dora.Request{MaxOutputTokens: override})
					if err != nil {
						t.Fatal(err)
					}
					wantBudget := 0
					if tc.want != nil {
						wantBudget = *tc.want
					}
					if response.OutputBudget != wantBudget || response.Content != "ok" {
						t.Fatalf("response = %+v", response)
					}
				})
			}
		}
	}
}
