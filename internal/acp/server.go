// Package acp implements Dora's Agent Client Protocol frontend.
package acp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/lgxz/dora"
	"github.com/lgxz/dora/internal/app"
)

// SessionFactory constructs one isolated Dora application session rooted at
// the absolute cwd supplied by the ACP client.
type SessionFactory func(context.Context, string) (*app.Session, error)

// AuthenticationRequired reports that setup must complete before a session
// can be created. It is exposed for frontend assembly without leaking the ACP
// SDK into the application layer.
func AuthenticationRequired() error {
	return acpsdk.NewAuthRequired(map[string]any{
		"authMethodId": "dora-setup",
	})
}

// Config configures the ACP agent frontend.
type Config struct {
	Version    string
	NewSession SessionFactory
}

// Serve runs an ACP v1 agent over newline-delimited JSON-RPC on stdin/stdout.
// It returns when the peer disconnects or ctx is cancelled.
func Serve(ctx context.Context, stdin io.Reader, stdout io.Writer, cfg Config) error {
	if stdin == nil || stdout == nil {
		return errors.New("acp: stdin and stdout are required")
	}
	if cfg.NewSession == nil {
		return errors.New("acp: session factory is required")
	}
	agent := &server{
		version:    cfg.Version,
		newSession: cfg.NewSession,
		sessions:   make(map[acpsdk.SessionId]*app.Session),
	}
	connection := acpsdk.NewAgentSideConnection(agent, stdout, stdin)
	agent.connection = connection
	defer agent.closeAll()

	select {
	case <-ctx.Done():
		return nil
	case <-connection.Done():
		return nil
	}
}

type server struct {
	connection *acpsdk.AgentSideConnection
	version    string
	newSession SessionFactory

	mu       sync.RWMutex
	sessions map[acpsdk.SessionId]*app.Session
}

func (s *server) Initialize(_ context.Context, params acpsdk.InitializeRequest) (acpsdk.InitializeResponse, error) {
	version := s.version
	if version == "" {
		version = "dev"
	}
	authMethods := []acpsdk.AuthMethod{}
	if supportsTerminalAuth(params.ClientCapabilities) {
		description := "Configure a model provider and API key for Dora"
		authMethods = append(authMethods, acpsdk.AuthMethod{Terminal: &acpsdk.AuthMethodTerminalInline{
			Id:          "dora-setup",
			Name:        "Configure Dora",
			Description: &description,
			Type:        "terminal",
			Args:        []string{"--setup"},
		}})
	}
	return acpsdk.InitializeResponse{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		AgentInfo: &acpsdk.Implementation{
			Name:    "dora",
			Version: version,
		},
		AgentCapabilities: acpsdk.AgentCapabilities{
			LoadSession: false,
			SessionCapabilities: acpsdk.SessionCapabilities{
				Close: &acpsdk.SessionCloseCapabilities{},
			},
		},
		AuthMethods: authMethods,
	}, nil
}

func supportsTerminalAuth(capabilities acpsdk.ClientCapabilities) bool {
	if capabilities.Auth.Terminal {
		return true
	}
	// The ACP Registry validator still advertises the pre-standard capability
	// marker. Accept it until the validator migrates to auth.terminal.
	value, ok := capabilities.Meta["terminal-auth"]
	supported, valid := value.(bool)
	return ok && valid && supported
}

func (s *server) Authenticate(context.Context, acpsdk.AuthenticateRequest) (acpsdk.AuthenticateResponse, error) {
	return acpsdk.AuthenticateResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodAuthenticate)
}

func (s *server) Logout(context.Context, acpsdk.LogoutRequest) (acpsdk.LogoutResponse, error) {
	return acpsdk.LogoutResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodLogout)
}

func (s *server) NewSession(ctx context.Context, params acpsdk.NewSessionRequest) (acpsdk.NewSessionResponse, error) {
	if !filepath.IsAbs(params.Cwd) {
		return acpsdk.NewSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "cwd must be an absolute path"})
	}
	if len(params.McpServers) != 0 {
		return acpsdk.NewSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "Dora does not support ACP-provided MCP servers"})
	}
	if len(params.AdditionalDirectories) != 0 {
		return acpsdk.NewSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "Dora does not support additionalDirectories"})
	}
	application, err := s.newSession(ctx, params.Cwd)
	if err != nil {
		return acpsdk.NewSessionResponse{}, err
	}
	id, err := newSessionID()
	if err != nil {
		_ = application.Close()
		return acpsdk.NewSessionResponse{}, fmt.Errorf("acp: create session id: %w", err)
	}
	s.mu.Lock()
	s.sessions[id] = application
	s.mu.Unlock()
	return acpsdk.NewSessionResponse{SessionId: id}, nil
}

func (s *server) Prompt(ctx context.Context, params acpsdk.PromptRequest) (acpsdk.PromptResponse, error) {
	application := s.session(params.SessionId)
	if application == nil {
		return acpsdk.PromptResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "session not found"})
	}
	prompt, err := promptText(params.Prompt)
	if err != nil {
		return acpsdk.PromptResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": err.Error()})
	}

	updates := newUpdateObserver(ctx, s.connection, params.SessionId)
	_, runErr := application.Prompt(ctx, prompt, app.PromptOptions{Observer: updates})
	updateErr := updates.Close()
	if updateErr != nil && runErr == nil {
		return acpsdk.PromptResponse{}, updateErr
	}
	response := acpsdk.PromptResponse{UserMessageId: params.MessageId}
	switch {
	case runErr == nil:
		response.StopReason = acpsdk.StopReasonEndTurn
		return response, nil
	case errors.Is(runErr, context.Canceled):
		response.StopReason = acpsdk.StopReasonCancelled
		return response, nil
	case errors.Is(runErr, dora.ErrMaxRounds):
		response.StopReason = acpsdk.StopReasonMaxTurnRequests
		return response, nil
	default:
		return acpsdk.PromptResponse{}, runErr
	}
}

func (s *server) Cancel(_ context.Context, params acpsdk.CancelNotification) error {
	if application := s.session(params.SessionId); application != nil {
		application.Cancel()
	}
	return nil
}

func (s *server) CloseSession(_ context.Context, params acpsdk.CloseSessionRequest) (acpsdk.CloseSessionResponse, error) {
	s.mu.Lock()
	application := s.sessions[params.SessionId]
	delete(s.sessions, params.SessionId)
	s.mu.Unlock()
	if application == nil {
		return acpsdk.CloseSessionResponse{}, acpsdk.NewInvalidParams(map[string]any{"error": "session not found"})
	}
	return acpsdk.CloseSessionResponse{}, application.Shutdown()
}

func (s *server) ListSessions(context.Context, acpsdk.ListSessionsRequest) (acpsdk.ListSessionsResponse, error) {
	return acpsdk.ListSessionsResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionList)
}

func (s *server) ResumeSession(context.Context, acpsdk.ResumeSessionRequest) (acpsdk.ResumeSessionResponse, error) {
	return acpsdk.ResumeSessionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionResume)
}

func (s *server) SetSessionConfigOption(context.Context, acpsdk.SetSessionConfigOptionRequest) (acpsdk.SetSessionConfigOptionResponse, error) {
	return acpsdk.SetSessionConfigOptionResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionSetConfigOption)
}

func (s *server) SetSessionMode(context.Context, acpsdk.SetSessionModeRequest) (acpsdk.SetSessionModeResponse, error) {
	return acpsdk.SetSessionModeResponse{}, acpsdk.NewMethodNotFound(acpsdk.AgentMethodSessionSetMode)
}

func (s *server) session(id acpsdk.SessionId) *app.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessions[id]
}

func (s *server) closeAll() {
	s.mu.Lock()
	sessions := s.sessions
	s.sessions = make(map[acpsdk.SessionId]*app.Session)
	s.mu.Unlock()
	for _, application := range sessions {
		_ = application.Shutdown()
	}
}

func newSessionID() (acpsdk.SessionId, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return acpsdk.SessionId("dora_" + hex.EncodeToString(random[:])), nil
}

func promptText(blocks []acpsdk.ContentBlock) (string, error) {
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch {
		case block.Text != nil:
			parts = append(parts, block.Text.Text)
		case block.ResourceLink != nil:
			link := block.ResourceLink
			label := link.Name
			if link.Title != nil && *link.Title != "" {
				label = *link.Title
			}
			parts = append(parts, fmt.Sprintf("Resource %q: %s", label, link.Uri))
		default:
			return "", errors.New("prompt contains a content type not advertised by Dora")
		}
	}
	prompt := strings.TrimSpace(strings.Join(parts, "\n\n"))
	if prompt == "" {
		return "", errors.New("prompt must contain text or a resource link")
	}
	return prompt, nil
}

var _ acpsdk.Agent = (*server)(nil)

type updateObserver struct {
	ctx        context.Context
	connection *acpsdk.AgentSideConnection
	sessionID  acpsdk.SessionId
	updates    chan dora.Update
	done       chan struct{}

	mu  sync.Mutex
	err error
}

func newUpdateObserver(ctx context.Context, connection *acpsdk.AgentSideConnection, sessionID acpsdk.SessionId) *updateObserver {
	o := &updateObserver{
		ctx:        ctx,
		connection: connection,
		sessionID:  sessionID,
		updates:    make(chan dora.Update, 128),
		done:       make(chan struct{}),
	}
	go o.run()
	return o
}

func (o *updateObserver) Observe(update dora.Update) {
	select {
	case o.updates <- update:
	case <-o.ctx.Done():
	}
}

func (o *updateObserver) Close() error {
	close(o.updates)
	<-o.done
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

func (o *updateObserver) run() {
	defer close(o.done)
	contentStreamed := false
	reasoningStreamed := false
	for update := range o.updates {
		var protocolUpdate *acpsdk.SessionUpdate
		switch update.Kind {
		case dora.UpdateContentDelta:
			contentStreamed = true
			value := acpsdk.UpdateAgentMessageText(update.Delta)
			protocolUpdate = &value
		case dora.UpdateReasoningDelta:
			reasoningStreamed = true
			value := acpsdk.UpdateAgentThoughtText(update.Delta)
			protocolUpdate = &value
		case dora.UpdateMessageReceived:
			if update.Message.Reasoning != "" && !reasoningStreamed {
				o.send(acpsdk.UpdateAgentThoughtText(update.Message.Reasoning))
			}
			if update.Message.Content != "" && !contentStreamed {
				value := acpsdk.UpdateAgentMessageText(update.Message.Content)
				protocolUpdate = &value
			}
			contentStreamed = false
			reasoningStreamed = false
		case dora.UpdateToolStarted:
			value := startToolUpdate(update.ToolCall)
			protocolUpdate = &value
		case dora.UpdateToolFinished:
			value := finishToolUpdate(update)
			protocolUpdate = &value
		}
		if protocolUpdate != nil {
			o.send(*protocolUpdate)
		}
	}
}

func (o *updateObserver) send(update acpsdk.SessionUpdate) {
	o.mu.Lock()
	failed := o.err != nil
	o.mu.Unlock()
	if failed {
		return
	}
	err := o.connection.SessionUpdate(o.ctx, acpsdk.SessionNotification{
		SessionId: o.sessionID,
		Update:    update,
	})
	if err == nil {
		return
	}
	o.mu.Lock()
	if o.err == nil {
		o.err = err
	}
	o.mu.Unlock()
	// Keep draining Observer events after a transport failure so the Agent run
	// cannot deadlock on the bounded update queue. Close reports the first error.
}

func startToolUpdate(call dora.ToolCall) acpsdk.SessionUpdate {
	var rawInput any
	if len(call.Input) != 0 {
		if err := json.Unmarshal(call.Input, &rawInput); err != nil {
			rawInput = string(call.Input)
		}
	}
	return acpsdk.StartToolCall(
		acpsdk.ToolCallId(call.ID),
		call.Name,
		acpsdk.WithStartKind(toolKind(call.Name)),
		acpsdk.WithStartStatus(acpsdk.ToolCallStatusInProgress),
		acpsdk.WithStartRawInput(rawInput),
	)
}

func finishToolUpdate(update dora.Update) acpsdk.SessionUpdate {
	status := acpsdk.ToolCallStatusCompleted
	output := any(update.Message.Content)
	content := update.Message.Content
	if update.Err != nil {
		status = acpsdk.ToolCallStatusFailed
		output = map[string]any{"error": update.Err.Error()}
		content = update.Err.Error()
	} else if json.Valid([]byte(update.Message.Content)) {
		_ = json.Unmarshal([]byte(update.Message.Content), &output)
	}
	options := []acpsdk.ToolCallUpdateOpt{
		acpsdk.WithUpdateStatus(status),
		acpsdk.WithUpdateRawOutput(output),
	}
	if content != "" {
		options = append(options, acpsdk.WithUpdateContent([]acpsdk.ToolCallContent{
			acpsdk.ToolContent(acpsdk.TextBlock(content)),
		}))
	}
	return acpsdk.UpdateToolCall(acpsdk.ToolCallId(update.ToolCall.ID), options...)
}

func toolKind(name string) acpsdk.ToolKind {
	switch name {
	case "read", "view_image", "history", "skill":
		return acpsdk.ToolKindRead
	case "write", "edit":
		return acpsdk.ToolKindEdit
	case "grep", "glob":
		return acpsdk.ToolKindSearch
	case "bash", "powershell", "task", "job":
		return acpsdk.ToolKindExecute
	default:
		return acpsdk.ToolKindOther
	}
}
