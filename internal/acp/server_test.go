package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"testing"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/app"
	"github.com/lgxz/dora/internal/job"
	sqlitesession "github.com/lgxz/dora/session/sqlite"
)

type modelFunc func(context.Context, dora.Request) (dora.Response, error)

func (f modelFunc) Generate(ctx context.Context, request dora.Request) (dora.Response, error) {
	return f(ctx, request)
}

type inspectTool struct{}

func (inspectTool) Spec() dora.ToolSpec { return dora.ToolSpec{Name: "inspect"} }

func (inspectTool) Execute(context.Context, json.RawMessage) (dora.ToolResult, error) {
	return dora.ToolResult{Content: `{"found":true}`}, nil
}

func TestInitializeAdvertisesTerminalSetupWhenSupported(t *testing.T) {
	tests := []struct {
		name         string
		capabilities acpsdk.ClientCapabilities
		wantMethod   bool
	}{
		{name: "not supported"},
		{name: "standard capability", capabilities: acpsdk.ClientCapabilities{
			Auth: acpsdk.AuthCapabilities{Terminal: true},
		}, wantMethod: true},
		{name: "registry legacy capability", capabilities: acpsdk.ClientCapabilities{
			Meta: map[string]any{"terminal-auth": true},
		}, wantMethod: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &server{version: "test"}
			response, err := server.Initialize(context.Background(), acpsdk.InitializeRequest{
				ProtocolVersion:    acpsdk.ProtocolVersionNumber,
				ClientCapabilities: test.capabilities,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.AuthMethods) == 0 {
				if test.wantMethod {
					t.Fatal("terminal auth method was not advertised")
				}
				return
			}
			if !test.wantMethod {
				t.Fatalf("unexpected auth methods = %#v", response.AuthMethods)
			}
			method := response.AuthMethods[0].Terminal
			if method == nil || method.Id != "dora-setup" || len(method.Args) != 1 || method.Args[0] != "--setup" {
				t.Fatalf("auth method = %#v", response.AuthMethods[0])
			}
		})
	}
}

func TestServePromptStreamsToolAndAnswerUpdates(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	calls := 0
	factory := func(ctx context.Context, cwd string) (*app.Session, error) {
		if cwd != filepath.Clean(cwd) || !filepath.IsAbs(cwd) {
			t.Fatalf("cwd = %q", cwd)
		}
		store, err := sqlitesession.OpenMemory(ctx)
		if err != nil {
			return nil, err
		}
		model := modelFunc(func(_ context.Context, _ dora.Request) (dora.Response, error) {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if calls == 1 {
				return dora.Response{ToolCalls: []dora.ToolCall{{
					ID: "call-1", Name: "inspect", Input: json.RawMessage(`{"path":"file.txt"}`),
				}}}, nil
			}
			return dora.Response{Content: "done"}, nil
		})
		agent, err := dora.New(model, inspectTool{})
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		return app.NewSession(agent, store, job.New(), cwd)
	}

	serveDone := make(chan error, 1)
	go func() {
		serveDone <- Serve(ctx, serverReader, serverWriter, Config{Version: "test", NewSession: factory})
	}()
	encoder := json.NewEncoder(clientWriter)
	decoder := json.NewDecoder(clientReader)

	sendRequest(t, encoder, 1, "initialize", map[string]any{"protocolVersion": 1})
	initialize := readUntilResponse(t, decoder, 1, nil)
	var initializeResult struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	decodeResult(t, initialize, &initializeResult)
	if initializeResult.ProtocolVersion != 1 {
		t.Fatalf("initialize = %#v", initializeResult)
	}

	cwd := t.TempDir()
	sendRequest(t, encoder, 2, "session/new", map[string]any{"cwd": cwd, "mcpServers": []any{}})
	created := readUntilResponse(t, decoder, 2, nil)
	var newSession struct {
		SessionID string `json:"sessionId"`
	}
	decodeResult(t, created, &newSession)
	if newSession.SessionID == "" {
		t.Fatal("session ID is empty")
	}

	sendRequest(t, encoder, 3, "session/prompt", map[string]any{
		"sessionId": newSession.SessionID,
		"prompt":    []any{map[string]any{"type": "text", "text": "inspect"}},
	})
	var updateKinds []string
	promptResponse := readUntilResponse(t, decoder, 3, func(message map[string]json.RawMessage) {
		if string(message["method"]) != `"session/update"` {
			return
		}
		var params struct {
			Update struct {
				Kind string `json:"sessionUpdate"`
			} `json:"update"`
		}
		if err := json.Unmarshal(message["params"], &params); err != nil {
			t.Fatal(err)
		}
		updateKinds = append(updateKinds, params.Update.Kind)
	})
	var promptResult struct {
		StopReason string `json:"stopReason"`
	}
	decodeResult(t, promptResponse, &promptResult)
	if promptResult.StopReason != "end_turn" {
		t.Fatalf("prompt response = %#v", promptResult)
	}
	wantKinds := []string{"tool_call", "tool_call_update", "agent_message_chunk"}
	if len(updateKinds) != len(wantKinds) {
		t.Fatalf("update kinds = %#v, want %#v", updateKinds, wantKinds)
	}
	for i := range wantKinds {
		if updateKinds[i] != wantKinds[i] {
			t.Fatalf("update kinds = %#v, want %#v", updateKinds, wantKinds)
		}
	}

	sendRequest(t, encoder, 4, "session/close", map[string]any{"sessionId": newSession.SessionID})
	_ = readUntilResponse(t, decoder, 4, nil)
	if err := clientWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serveDone; err != nil {
		t.Fatal(err)
	}
}

func TestPromptTextSupportsTextAndResourceLinks(t *testing.T) {
	title := "Specification"
	got, err := promptText(contentBlockList{
		{text: "Review this"},
		{resourceName: "spec", resourceTitle: &title, resourceURI: "file:///tmp/spec.md"},
	}.blocks())
	if err != nil {
		t.Fatal(err)
	}
	want := "Review this\n\nResource \"Specification\": file:///tmp/spec.md"
	if got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
}

func TestNewSessionRejectsUnsupportedScope(t *testing.T) {
	created := 0
	protocol := &server{newSession: func(context.Context, string) (*app.Session, error) {
		created++
		return nil, errors.New("must not be called")
	}}
	tests := []struct {
		name   string
		params acpsdk.NewSessionRequest
	}{
		{name: "relative cwd", params: acpsdk.NewSessionRequest{Cwd: "relative"}},
		{name: "mcp server", params: acpsdk.NewSessionRequest{Cwd: t.TempDir(), McpServers: []acpsdk.McpServer{{}}}},
		{name: "additional directory", params: acpsdk.NewSessionRequest{Cwd: t.TempDir(), AdditionalDirectories: []string{t.TempDir()}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := protocol.NewSession(context.Background(), test.params)
			var requestError *acpsdk.RequestError
			if !errors.As(err, &requestError) || requestError.Code != -32602 {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if created != 0 {
		t.Fatalf("session factory calls = %d", created)
	}
}

func TestCancelMapsPromptToCancelledStopReason(t *testing.T) {
	started := make(chan struct{})
	store, err := sqlitesession.OpenMemory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	agent, err := dora.New(modelFunc(func(ctx context.Context, _ dora.Request) (dora.Response, error) {
		close(started)
		<-ctx.Done()
		return dora.Response{}, ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	application, err := app.NewSession(agent, store, job.New(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	sid := acpsdk.SessionId("session-1")
	protocol := &server{sessions: map[acpsdk.SessionId]*app.Session{sid: application}}

	done := make(chan struct {
		response acpsdk.PromptResponse
		err      error
	}, 1)
	go func() {
		response, err := protocol.Prompt(context.Background(), acpsdk.PromptRequest{
			SessionId: sid,
			Prompt:    []acpsdk.ContentBlock{acpsdk.TextBlock("wait")},
		})
		done <- struct {
			response acpsdk.PromptResponse
			err      error
		}{response: response, err: err}
	}()
	<-started
	if err := protocol.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: sid}); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.err != nil || result.response.StopReason != acpsdk.StopReasonCancelled {
		t.Fatalf("response = %#v, error = %v", result.response, result.err)
	}
}

// ContentBlockForTest keeps the union construction readable without coupling
// this test to generated discriminator fields.
type ContentBlockForTest struct {
	text          string
	resourceName  string
	resourceTitle *string
	resourceURI   string
}

type contentBlockList []ContentBlockForTest

func (blocks contentBlockList) blocks() []acpsdk.ContentBlock {
	result := make([]acpsdk.ContentBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.text != "" {
			result = append(result, acpsdk.TextBlock(block.text))
			continue
		}
		value := acpsdk.ResourceLinkBlock(block.resourceName, block.resourceURI)
		value.ResourceLink.Title = block.resourceTitle
		result = append(result, value)
	}
	return result
}

func sendRequest(t *testing.T, encoder *json.Encoder, id int, method string, params any) {
	t.Helper()
	if err := encoder.Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}); err != nil {
		t.Fatal(err)
	}
}

func readUntilResponse(t *testing.T, decoder *json.Decoder, id int, notification func(map[string]json.RawMessage)) map[string]json.RawMessage {
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

func decodeResult(t *testing.T, message map[string]json.RawMessage, target any) {
	t.Helper()
	if err := json.Unmarshal(message["result"], target); err != nil {
		t.Fatal(err)
	}
}
