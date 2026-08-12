// Package openai implements dora.Model using the OpenAI-compatible Chat
// Completions protocol.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/internal/imageutil"
)

const maxResponseBytes = 4 << 20

// Config configures a Client.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
	// ConnectTimeout bounds TCP connection setup. Zero uses a 10 second
	// default.
	ConnectTimeout time.Duration
	// StreamIdleTimeout bounds the idle time between streaming events. Zero
	// disables the idle timeout and leaves the stream governed by the caller's
	// context.
	StreamIdleTimeout time.Duration
	// Timeout bounds a non-streaming generation request. Zero uses a 120
	// second default.
	Timeout time.Duration
}

// Client is an OpenAI-compatible dora.Model.
type Client struct {
	endpoint          string
	apiKey            string
	model             string
	httpClient        *http.Client
	connectTimeout    time.Duration
	streamIdleTimeout time.Duration
	timeout           time.Duration
}

// New creates an OpenAI-compatible model client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("openai: base URL is required")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("openai: parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("openai: base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("openai: base URL must include a host")
	}
	if cfg.Model == "" {
		return nil, errors.New("openai: model is required")
	}

	connectTimeout := cfg.ConnectTimeout
	if connectTimeout == 0 {
		connectTimeout = 10 * time.Second
	}
	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: connectTimeout}).DialContext,
			},
		}
	}

	return &Client{
		endpoint:          strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions",
		apiKey:            cfg.APIKey,
		model:             cfg.Model,
		httpClient:        httpClient,
		connectTimeout:    connectTimeout,
		streamIdleTimeout: cfg.StreamIdleTimeout,
		timeout:           timeout,
	}, nil
}

// Generate implements dora.Model. The response is received as a stream even
// when the caller does not consume incremental events.
func (c *Client) Generate(ctx context.Context, request dora.Request) (dora.Response, error) {
	// Apply the overall timeout to non-streaming requests. Streaming requests
	// are governed by the caller's context plus an optional idle timeout.
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.GenerateStream(ctx, request, nil)
}

// GenerateStream implements dora.StreamingModel.
func (c *Client) GenerateStream(ctx context.Context, request dora.Request, emit func(dora.ModelEvent)) (dora.Response, error) {
	body, err := c.requestBody(request)
	if err != nil {
		return dora.Response{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai: encode request: %w", err)
	}

	// Wrap the caller context with an idle/stall timeout that is reset on each
	// streaming event, so a slow-but-progressing stream is not killed.
	var onActivity func()
	if c.streamIdleTimeout > 0 {
		ctx, onActivity = withIdleTimeout(ctx, c.streamIdleTimeout)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai: create request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return dora.Response{}, retryable(fmt.Errorf("openai: send request: %w", err))
	}
	defer httpResponse.Body.Close()

	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
		if err != nil {
			return dora.Response{}, retryable(fmt.Errorf("openai: read error response: %w", err))
		}
		return dora.Response{}, apiError(httpResponse.StatusCode, httpResponse.Header, responseBody)
	}
	response, err := readStream(httpResponse.Body, emit, onActivity)
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai: %w", err)
	}
	return response, nil
}

func (c *Client) requestBody(request dora.Request) (chatRequest, error) {
	body := chatRequest{Model: c.model, Stream: true}
	for _, message := range request.Messages {
		content, err := encodeContent(message)
		if err != nil {
			return chatRequest{}, err
		}
		converted := chatMessage{
			Role:       string(message.Role),
			Content:    content,
			ToolCallID: message.ToolCallID,
		}
		switch message.Role {
		case dora.RoleSystem, dora.RoleUser:
		case dora.RoleAssistant:
			for _, call := range message.ToolCalls {
				converted.ToolCalls = append(converted.ToolCalls, chatToolCall{
					ID:   call.ID,
					Type: "function",
					Function: chatFunctionCall{
						Name:      call.Name,
						Arguments: string(call.Input),
					},
				})
			}
		case dora.RoleTool:
			if message.ToolCallID == "" {
				return chatRequest{}, errors.New("openai: tool message is missing ToolCallID")
			}
		default:
			return chatRequest{}, fmt.Errorf("openai: unsupported message role %q", message.Role)
		}
		body.Messages = append(body.Messages, converted)
	}

	for _, spec := range request.Tools {
		parameters := spec.InputSchema
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if !json.Valid(parameters) {
			return chatRequest{}, fmt.Errorf("openai: tool %q has an invalid JSON schema", spec.Name)
		}
		body.Tools = append(body.Tools, chatTool{
			Type: "function",
			Function: chatFunction{
				Name:        spec.Name,
				Description: spec.Description,
				Parameters:  parameters,
			},
		})
	}
	return body, nil
}

// encodeContent renders a message's text and images into the Chat Completions
// content field. Without images the content stays a plain string for
// compatibility; with images it becomes an array of text and image_url parts.
func encodeContent(message dora.Message) (json.RawMessage, error) {
	if len(message.Images) == 0 {
		encoded, err := json.Marshal(message.Content)
		if err != nil {
			return nil, fmt.Errorf("openai: encode content: %w", err)
		}
		return encoded, nil
	}
	parts := make([]chatContentPart, 0, len(message.Images)+1)
	if message.Content != "" {
		parts = append(parts, chatContentPart{Type: "text", Text: message.Content})
	}
	for _, image := range message.Images {
		url, err := imageutil.DataURL(image)
		if err != nil {
			return nil, fmt.Errorf("openai: %w", err)
		}
		parts = append(parts, chatContentPart{
			Type: "image_url",
			ImageURL: chatImageURL{
				URL: url,
			},
		})
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return nil, fmt.Errorf("openai: encode content: %w", err)
	}
	return encoded, nil
}

type chatContentPart struct {
	Type     string       `json:"type"`
	Text     string       `json:"text,omitempty"`
	ImageURL chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
}

func readStream(reader io.Reader, emit func(dora.ModelEvent), onActivity func()) (dora.Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxResponseBytes)
	var result dora.Response
	calls := make(map[int]*streamedToolCall)
	done := false
	for scanner.Scan() {
		if onActivity != nil {
			onActivity()
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var event chatStreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return dora.Response{}, fmt.Errorf("decode stream event: %w", err)
		}
		for _, choice := range event.Choices {
			if choice.Index != 0 {
				continue
			}
			if choice.Delta.Content != "" {
				result.Content += choice.Delta.Content
				if emit != nil {
					emit(dora.ModelEvent{Kind: dora.ModelEventContentDelta, Delta: choice.Delta.Content})
				}
			}
			for _, delta := range choice.Delta.ToolCalls {
				call := calls[delta.Index]
				if call == nil {
					call = &streamedToolCall{}
					calls[delta.Index] = call
				}
				if delta.ID != "" {
					call.id = delta.ID
				}
				if delta.Function.Name != "" {
					call.name = delta.Function.Name
				}
				call.arguments.WriteString(delta.Function.Arguments)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return dora.Response{}, retryable(fmt.Errorf("read stream: %w", err))
	}
	if !done {
		return dora.Response{}, errors.New("stream ended before [DONE]")
	}
	indices := make([]int, 0, len(calls))
	for index := range calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := calls[index]
		arguments := json.RawMessage(call.arguments.String())
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		toolCall := dora.ToolCall{ID: call.id, Name: call.name, Input: arguments}
		result.ToolCalls = append(result.ToolCalls, toolCall)
		if emit != nil {
			emit(dora.ModelEvent{Kind: dora.ModelEventToolCallReady, ToolCall: toolCall})
		}
	}
	return result, nil
}

type streamedToolCall struct {
	id        string
	name      string
	arguments strings.Builder
}

func apiError(status int, header http.Header, body []byte) error {
	var decoded struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	message := ""
	if json.Unmarshal(body, &decoded) == nil && decoded.Error.Message != "" {
		message = decoded.Error.Message
	} else {
		message = strings.TrimSpace(string(body))
		if len(message) > 512 {
			message = message[:512] + "..."
		}
	}
	if message == "" {
		message = fmt.Sprintf("openai: API returned HTTP %d", status)
	} else {
		message = fmt.Sprintf("openai: API returned HTTP %d: %s", status, message)
	}
	err := errors.New(message)
	if isRetryableStatus(status) {
		return retryableWithDelay(err, parseRetryAfter(header))
	}
	return err
}

// isRetryableStatus reports whether an HTTP status is likely to succeed on a
// later attempt: rate limits and transient server errors.
func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// retryable wraps err as a dora.RetryableError with no suggested delay.
func retryable(err error) error {
	return &dora.RetryableError{Err: err}
}

// retryableWithDelay wraps err as a dora.RetryableError with a suggested delay.
func retryableWithDelay(err error, retryAfter time.Duration) error {
	return &dora.RetryableError{Err: err, RetryAfter: retryAfter}
}

// parseRetryAfter reads the Retry-After header, which may be a delay in
// seconds or an HTTP-date. It returns zero when the header is absent or
// unparseable.
func parseRetryAfter(header http.Header) time.Duration {
	value := header.Get("Retry-After")
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}

// withIdleTimeout returns a context that is cancelled when no activity is
// reported within idle for the given duration. The returned reset function
// must be called on each unit of activity to postpone the deadline.
func withIdleTimeout(ctx context.Context, idle time.Duration) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	timer := time.NewTimer(idle)
	reset := func() {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(idle)
	}
	go func() {
		select {
		case <-timer.C:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, reset
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools,omitempty"`
	Stream   bool          `json:"stream"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatStreamEvent struct {
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

var _ dora.StreamingModel = (*Client)(nil)
