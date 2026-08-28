package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lgxz/dora/internal/config"
)

func TestRunACPRejectsConflictingCLIState(t *testing.T) {
	tests := []struct {
		name string
		opts options
		want string
	}{
		{name: "prompt", opts: options{promptArgs: []string{"hello"}}, want: "does not accept a prompt"},
		{name: "session", opts: options{sessionPath: "turns.sqlite"}, want: "does not accept --session"},
		{name: "workdir", opts: options{workdir: "/tmp"}, want: "does not accept --workdir"},
		{name: "events", opts: options{events: true}, want: "cannot be combined with --events"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := runACP(context.Background(), test.opts, config.Config{}, IO{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestRunValidatesACPArgumentsBeforeLoadingConfig(t *testing.T) {
	err := Run(context.Background(), []string{"--acp", "prompt"}, IO{Stderr: io.Discard})
	if err == nil || !strings.Contains(err.Error(), "does not accept a prompt") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunACPServesConfiguredAgent(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := writeTestConfig(t, configPath, `
providers:
  - name: openai
    base_url: https://example.test/v1
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
`); err != nil {
		t.Fatal(err)
	}
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return fakeChatResponse(`{"choices":[{"index":0,"delta":{"content":"hello over ACP"}}]}`), nil
	})}
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, []string{"--acp", "--no-skills", "--config", configPath}, IO{
			Stdin: serverReader, Stdout: serverWriter, Stderr: io.Discard,
			HTTPClient: httpClient, BuildVersion: "test",
		})
	}()
	encoder := json.NewEncoder(clientWriter)
	decoder := json.NewDecoder(clientReader)

	writeACPRequest(t, encoder, 1, "initialize", map[string]any{"protocolVersion": 1})
	_ = readACPResponse(t, decoder, 1, nil)
	writeACPRequest(t, encoder, 2, "session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	created := readACPResponse(t, decoder, 2, nil)
	var createdResult struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(created["result"], &createdResult); err != nil {
		t.Fatal(err)
	}

	writeACPRequest(t, encoder, 3, "session/prompt", map[string]any{
		"sessionId": createdResult.SessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": "hello"}},
	})
	var streamed string
	response := readACPResponse(t, decoder, 3, func(message map[string]json.RawMessage) {
		var params struct {
			Update struct {
				Kind    string `json:"sessionUpdate"`
				Content struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"update"`
		}
		if json.Unmarshal(message["params"], &params) == nil && params.Update.Kind == "agent_message_chunk" {
			streamed += params.Update.Content.Text
		}
	})
	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	if err := json.Unmarshal(response["result"], &promptResult); err != nil {
		t.Fatal(err)
	}
	if promptResult.StopReason != "end_turn" || streamed != "hello over ACP" {
		t.Fatalf("stop reason = %q, streamed = %q", promptResult.StopReason, streamed)
	}

	if err := clientWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunACPAdvertisesSetupWithoutConfiguredModel(t *testing.T) {
	clearBuiltinProviderKeys(t)
	t.Setenv("DORA_HOME", t.TempDir())
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Run(context.Background(), []string{"--acp"}, IO{
			Stdin: serverReader, Stdout: serverWriter, Stderr: io.Discard, BuildVersion: "test",
		})
	}()
	encoder := json.NewEncoder(clientWriter)
	decoder := json.NewDecoder(clientReader)

	writeACPRequest(t, encoder, 1, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientCapabilities": map[string]any{
			"terminal": true,
			"_meta":    map[string]any{"terminal-auth": true},
		},
	})
	initialized := readACPResponse(t, decoder, 1, nil)
	var initializeResult struct {
		AuthMethods []struct {
			ID   string   `json:"id"`
			Type string   `json:"type"`
			Args []string `json:"args"`
		} `json:"authMethods"`
	}
	if err := json.Unmarshal(initialized["result"], &initializeResult); err != nil {
		t.Fatal(err)
	}
	if len(initializeResult.AuthMethods) != 1 || initializeResult.AuthMethods[0].ID != "dora-setup" || initializeResult.AuthMethods[0].Type != "terminal" {
		t.Fatalf("auth methods = %#v", initializeResult.AuthMethods)
	}

	writeACPRequest(t, encoder, 2, "session/new", map[string]any{"cwd": t.TempDir(), "mcpServers": []any{}})
	var response struct {
		ID    int `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := decoder.Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.ID != 2 || response.Error.Code != -32000 {
		t.Fatalf("session/new response = %#v", response)
	}

	if err := clientWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func writeACPRequest(t *testing.T, encoder *json.Encoder, id int, method string, params any) {
	t.Helper()
	if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		t.Fatal(err)
	}
}

func readACPResponse(t *testing.T, decoder *json.Decoder, id int, notification func(map[string]json.RawMessage)) map[string]json.RawMessage {
	t.Helper()
	for {
		var message map[string]json.RawMessage
		if err := decoder.Decode(&message); err != nil {
			t.Fatal(err)
		}
		if rawID, ok := message["id"]; ok && string(rawID) == fmt.Sprintf("%d", id) {
			if rawError := message["error"]; len(rawError) != 0 && string(rawError) != "null" {
				t.Fatalf("response error = %s", rawError)
			}
			return message
		}
		if notification != nil {
			notification(message)
		}
	}
}
