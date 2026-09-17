package cli

import (
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
)

func TestWriteTraceFilePreservesParentTurn(t *testing.T) {
	turn := dora.NewTurn("inspect")
	usage := &dora.Usage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}
	if err := turn.AppendRound(dora.Round{
		Assistant: dora.Message{
			Role:      dora.RoleAssistant,
			Reasoning: "need a command",
			ToolCalls: []dora.ToolCall{{ID: "call-1", Name: "bash", Input: json.RawMessage("{")}},
		},
		Tools: []dora.Message{{
			Role: dora.RoleTool, ToolCallID: "call-1", Content: "output",
			Images: []dora.Image{{Path: "/tmp/result.png"}},
		}},
		Usage: usage,
	}, "opaque"); err != nil {
		t.Fatal(err)
	}
	if err := turn.Complete("done", "opaque-final"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "trace.json")
	if err := writeTraceFile(path, turn); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var trace turnTrace
	if err := json.Unmarshal(contents, &trace); err != nil {
		t.Fatal(err)
	}
	if trace.SchemaVersion != turnTraceSchemaVersion || trace.User != "inspect" {
		t.Fatalf("trace = %#v", trace)
	}
	if len(trace.Rounds) != 1 || trace.Rounds[0].Assistant.ToolCalls[0].Input != "{" {
		t.Fatalf("rounds = %#v", trace.Rounds)
	}
	if trace.Rounds[0].Tools[0].Images[0].Path != "/tmp/result.png" {
		t.Fatalf("tool result = %#v", trace.Rounds[0].Tools[0])
	}
	if trace.Final == nil || trace.Final.Content != "done" {
		t.Fatalf("final = %#v", trace.Final)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %o, want 600", got)
	}
}

func TestTraceRecordsOutputLimitRecoveryWithoutDoubleCounting(t *testing.T) {
	for _, recoverSuccessfully := range []bool{false, true} {
		t.Run(fmt.Sprint(recoverSuccessfully), func(t *testing.T) {
			dir := t.TempDir()
			configPath := filepath.Join(dir, "config.yaml")
			tracePath := filepath.Join(dir, "trace.json")
			if err := os.WriteFile(configPath, []byte(`providers:
  - name: test
    base_url: https://example.test/v1
    profiles:
      - name: model
        capabilities: [text]
        max_tokens: 32768
        max_output_tokens: 65536
env:
  TEST_API_KEY: key
`), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				want := 32768
				if calls == 2 {
					want = 65536
				}
				if int(body["max_tokens"].(float64)) != want {
					t.Fatalf("budget=%v", body["max_tokens"])
				}
				event := `{"choices":[{"index":0,"delta":{"reasoning_content":"unfinished"},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":32768,"total_tokens":32868,"completion_tokens_details":{"reasoning_tokens":32768}}}`
				if recoverSuccessfully && calls == 2 {
					event = `{"choices":[{"index":0,"delta":{"content":"done","reasoning_content":"finished"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`
				}
				return fakeChatResponse(event), nil
			})}
			err := Run(context.Background(), []string{"--config", configPath, "--no-skills", "--trace-file", tracePath, "solve"}, IO{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard, StdinIsTerminal: true, HTTPClient: client})
			if recoverSuccessfully {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, dora.ErrOutputLimit) {
				t.Fatalf("error=%v", err)
			}
			encoded, err := os.ReadFile(tracePath)
			if err != nil {
				t.Fatal(err)
			}
			var trace turnTrace
			if err := json.Unmarshal(encoded, &trace); err != nil {
				t.Fatal(err)
			}
			if calls != 2 || trace.SchemaVersion != 2 || len(trace.Attempts) != 2 || trace.Attempts[0].Reasoning != "unfinished" || trace.Attempts[0].Disposition != "discarded" || trace.Attempts[1].Recovery != 1 {
				t.Fatalf("trace=%+v calls=%d", trace, calls)
			}
			if recoverSuccessfully {
				if trace.Status != "completed" || trace.Final == nil || trace.Final.Reasoning != "finished" || trace.TotalUsage.TotalTokens != 32988 {
					t.Fatalf("trace=%+v", trace)
				}
			} else if trace.Status != "failed" || trace.Final != nil || trace.TotalUsage.TotalTokens != 65736 || trace.Error == "" {
				t.Fatalf("trace=%+v", trace)
			}
		})
	}
}
