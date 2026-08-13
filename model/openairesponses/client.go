// Package openairesponses implements dora.Model using the OpenAI Responses
// API and its server-sent event stream.
package openairesponses

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
	"strconv"
	"strings"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/imageutil"
)

const maxEventBytes = 4 << 20

// defaultRateLimitRetryAfter is the delay used when a rate-limit (429)
// response does not carry a usable Retry-After header.
const defaultRateLimitRetryAfter = 30 * time.Second

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

// Client is an OpenAI Responses API model client.
type Client struct {
	endpoint          string
	apiKey            string
	model             string
	httpClient        *http.Client
	connectTimeout    time.Duration
	streamIdleTimeout time.Duration
	timeout           time.Duration
}

// New creates an OpenAI Responses API model client.
func New(cfg Config) (*Client, error) {
	if cfg.BaseURL == "" {
		return nil, errors.New("openai responses: base URL is required")
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("openai responses: parse base URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("openai responses: base URL must use http or https")
	}
	if parsed.Host == "" {
		return nil, errors.New("openai responses: base URL must include a host")
	}
	if cfg.Model == "" {
		return nil, errors.New("openai responses: model is required")
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
		endpoint:          strings.TrimRight(cfg.BaseURL, "/") + "/responses",
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
		return dora.Response{}, fmt.Errorf("openai responses: encode request: %w", err)
	}

	// Wrap the caller context with an idle/stall timeout that is reset on each
	// streaming event, so a slow-but-progressing stream is not killed.
	var onActivity func()
	if c.streamIdleTimeout > 0 {
		ctx, onActivity = withIdleTimeout(ctx, c.streamIdleTimeout)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai responses: create request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return dora.Response{}, retryable(fmt.Errorf("openai responses: send request: %w", err))
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		responseBody, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, maxEventBytes+1))
		if readErr != nil {
			return dora.Response{}, retryable(fmt.Errorf("openai responses: read error response: %w", readErr))
		}
		return dora.Response{}, apiError(httpResponse.StatusCode, httpResponse.Header, responseBody)
	}

	response, err := readStream(httpResponse.Body, emit, onActivity)
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai responses: %w", err)
	}
	latest, err := decodeContinuation(response.Continuation)
	if err != nil {
		return dora.Response{}, err
	}
	state := continuationState{BaseMessageCount: len(request.Messages)}
	if request.Continuation != "" {
		state, err = decodeContinuation(request.Continuation)
		if err != nil {
			return dora.Response{}, err
		}
		if state.MessageCount < state.BaseMessageCount || state.MessageCount > len(request.Messages) {
			return dora.Response{}, errors.New("openai responses: continuation has an invalid message boundary")
		}
		if err := appendContinuationMessages(&state.Items, request.Messages[state.MessageCount:]); err != nil {
			return dora.Response{}, err
		}
	}
	state.Items = append(state.Items, latest.Items...)
	state.MessageCount = len(request.Messages) + 1
	response.Continuation, err = encodeContinuation(state)
	if err != nil {
		return dora.Response{}, err
	}
	return response, nil
}

func (c *Client) requestBody(request dora.Request) (responsesRequest, error) {
	body := responsesRequest{
		Model:   c.model,
		Stream:  true,
		Store:   false,
		Include: []string{"reasoning.encrypted_content"},
	}

	if request.Continuation != "" {
		state, err := decodeContinuation(request.Continuation)
		if err != nil {
			return responsesRequest{}, err
		}
		if len(state.Items) == 0 {
			return responsesRequest{}, errors.New("openai responses: continuation contains no output items")
		}
		if state.BaseMessageCount < 0 || state.MessageCount < state.BaseMessageCount || state.MessageCount > len(request.Messages) {
			return responsesRequest{}, errors.New("openai responses: continuation has an invalid message boundary")
		}
		if err := appendMessages(&body.Input, request.Messages[:state.BaseMessageCount]); err != nil {
			return responsesRequest{}, err
		}
		body.Input = append(body.Input, state.Items...)
		if err := appendContinuationMessages(&body.Input, request.Messages[state.MessageCount:]); err != nil {
			return responsesRequest{}, err
		}
	} else {
		if err := appendMessages(&body.Input, request.Messages); err != nil {
			return responsesRequest{}, err
		}
	}

	for _, spec := range request.Tools {
		parameters := spec.InputSchema
		if len(parameters) == 0 {
			parameters = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if !json.Valid(parameters) {
			return responsesRequest{}, fmt.Errorf("openai responses: tool %q has an invalid JSON schema", spec.Name)
		}
		body.Tools = append(body.Tools, responseTool{
			Type:        "function",
			Name:        spec.Name,
			Description: spec.Description,
			Parameters:  parameters,
		})
	}
	return body, nil
}

func appendContinuationMessages(input *[]json.RawMessage, messages []dora.Message) error {
	for _, message := range messages {
		switch message.Role {
		case dora.RoleSystem, dora.RoleUser, dora.RoleAssistant:
			if len(message.ToolCalls) > 0 {
				return errors.New("openai responses: continuation contains an uncovered assistant tool call")
			}
			if message.Content != "" || len(message.Images) > 0 {
				content, err := encodeContent(message)
				if err != nil {
					return err
				}
				if err := appendInput(input, inputItem{Role: string(message.Role), Content: content}); err != nil {
					return err
				}
			}
		case dora.RoleTool:
			if message.ToolCallID == "" {
				return errors.New("openai responses: tool message is missing ToolCallID")
			}
			if err := appendInput(input, inputItem{
				Type:   "function_call_output",
				CallID: message.ToolCallID,
				Output: message.Content,
			}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("openai responses: unsupported message role %q", message.Role)
		}
	}
	return nil
}

func appendMessages(input *[]json.RawMessage, messages []dora.Message) error {
	for _, message := range messages {
		switch message.Role {
		case dora.RoleSystem, dora.RoleUser, dora.RoleAssistant:
			if message.Content != "" || len(message.Images) > 0 {
				content, err := encodeContent(message)
				if err != nil {
					return err
				}
				if err := appendInput(input, inputItem{Role: string(message.Role), Content: content}); err != nil {
					return err
				}
			}
		case dora.RoleTool:
			// A completed historical tool exchange has no portable reasoning
			// items. Its final assistant message is retained instead.
		default:
			return fmt.Errorf("openai responses: unsupported message role %q", message.Role)
		}
	}
	return nil
}

// encodeContent renders a message's text and images into the Responses API
// content field. Without images the content stays a plain string for
// compatibility; with images it becomes an array of input_text and input_image
// parts.
func encodeContent(message dora.Message) (json.RawMessage, error) {
	if len(message.Images) == 0 {
		encoded, err := json.Marshal(message.Content)
		if err != nil {
			return nil, fmt.Errorf("openai responses: encode content: %w", err)
		}
		return encoded, nil
	}
	parts := make([]responseContentPart, 0, len(message.Images)+1)
	if message.Content != "" {
		parts = append(parts, responseContentPart{Type: "input_text", Text: message.Content})
	}
	for _, image := range message.Images {
		url, err := imageDataURL(image)
		if err != nil {
			return nil, fmt.Errorf("openai responses: %w", err)
		}
		parts = append(parts, responseContentPart{Type: "input_image", ImageURL: url})
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		return nil, fmt.Errorf("openai responses: encode content: %w", err)
	}
	return encoded, nil
}

type responseContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

// imageDataURL resolves an image reference to a data URL. A URL is used
// directly; a Path is read and encoded by imageutil.
func imageDataURL(image dora.Image) (string, error) {
	if image.URL != "" {
		return image.URL, nil
	}
	if image.Path == "" {
		return "", errors.New("image has neither Path nor URL")
	}
	return imageutil.DataURL(image.Path)
}

func appendInput(input *[]json.RawMessage, item inputItem) error {
	encoded, err := encodeInput(item)
	if err != nil {
		return err
	}
	*input = append(*input, encoded)
	return nil
}

func encodeInput(item inputItem) (json.RawMessage, error) {
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("openai responses: encode input item: %w", err)
	}
	return encoded, nil
}

func encodeContinuation(state continuationState) (string, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return "", fmt.Errorf("encode continuation: %w", err)
	}
	return string(encoded), nil
}

func decodeContinuation(value string) (continuationState, error) {
	var state continuationState
	if err := json.Unmarshal([]byte(value), &state); err != nil {
		return continuationState{}, fmt.Errorf("openai responses: decode continuation: %w", err)
	}
	return state, nil
}

func readStream(reader io.Reader, emit func(dora.ModelEvent), onActivity func()) (dora.Response, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), maxEventBytes)
	var data strings.Builder
	var completed *responsesResponse
	items := make(map[string]responseItem)

	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		payload := data.String()
		data.Reset()
		if payload == "[DONE]" {
			return nil
		}

		var event streamEvent
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			return fmt.Errorf("decode stream event: %w", err)
		}
		switch event.Type {
		case "response.output_text.delta":
			if emit != nil && event.Delta != "" {
				emit(dora.ModelEvent{Kind: dora.ModelEventContentDelta, Delta: event.Delta})
			}
		case "response.output_item.added":
			items[event.Item.ID] = event.Item
		case "response.function_call_arguments.done":
			if emit != nil {
				item := items[event.ItemID]
				arguments := json.RawMessage(event.Arguments)
				if len(arguments) == 0 {
					arguments = json.RawMessage(`{}`)
				}
				emit(dora.ModelEvent{Kind: dora.ModelEventToolCallReady, ToolCall: dora.ToolCall{
					ID: item.CallID, Name: item.Name, Input: arguments,
				}})
			}
		case "response.completed":
			completed = &event.Response
		case "response.failed", "response.incomplete":
			message := event.Response.Error.Message
			if message == "" {
				message = event.Type
			}
			return errors.New(message)
		case "error":
			if event.Message == "" {
				return errors.New("stream returned an error")
			}
			return errors.New(event.Message)
		}
		return nil
	}

	for scanner.Scan() {
		if onActivity != nil {
			onActivity()
		}
		line := scanner.Text()
		if line == "" {
			if err := dispatch(); err != nil {
				return dora.Response{}, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return dora.Response{}, retryable(fmt.Errorf("read stream: %w", err))
	}
	if err := dispatch(); err != nil {
		return dora.Response{}, err
	}
	if completed == nil {
		return dora.Response{}, errors.New("stream ended before response.completed")
	}
	return completed.toDora()
}

func (response responsesResponse) toDora() (dora.Response, error) {
	continuation, err := encodeContinuation(continuationState{Items: response.Output})
	if err != nil {
		return dora.Response{}, err
	}
	result := dora.Response{Continuation: continuation}
	for _, rawItem := range response.Output {
		var item responseItem
		if err := json.Unmarshal(rawItem, &item); err != nil {
			return dora.Response{}, fmt.Errorf("decode output item: %w", err)
		}
		switch item.Type {
		case "message":
			for _, content := range item.Content {
				if content.Type == "output_text" {
					result.Content += content.Text
				}
			}
		case "function_call":
			arguments := json.RawMessage(item.Arguments)
			if len(arguments) == 0 {
				arguments = json.RawMessage(`{}`)
			}
			result.ToolCalls = append(result.ToolCalls, dora.ToolCall{
				ID: item.CallID, Name: item.Name, Input: arguments,
			})
		}
	}
	return result, nil
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
		message = fmt.Sprintf("openai responses: API returned HTTP %d", status)
	} else {
		message = fmt.Sprintf("openai responses: API returned HTTP %d: %s", status, message)
	}
	err := errors.New(message)
	if isRetryableStatus(status) {
		if status == http.StatusTooManyRequests {
			delay := parseRetryAfter(header)
			if delay == 0 {
				delay = defaultRateLimitRetryAfter
			}
			return retryableWithDelay(err, delay, dora.RetryableRateLimit)
		}
		return retryableWithDelay(err, parseRetryAfter(header), dora.RetryableGeneric)
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

// retryableWithDelay wraps err as a dora.RetryableError with a suggested delay
// and kind.
func retryableWithDelay(err error, retryAfter time.Duration, kind dora.RetryableErrorKind) error {
	return &dora.RetryableError{Err: err, RetryAfter: retryAfter, Kind: kind}
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

type responsesRequest struct {
	Model   string            `json:"model"`
	Input   []json.RawMessage `json:"input"`
	Tools   []responseTool    `json:"tools,omitempty"`
	Stream  bool              `json:"stream"`
	Store   bool              `json:"store"`
	Include []string          `json:"include,omitempty"`
}

type inputItem struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
	CallID  string          `json:"call_id,omitempty"`
	Output  string          `json:"output,omitempty"`
}

type continuationState struct {
	BaseMessageCount int               `json:"base_message_count"`
	MessageCount     int               `json:"message_count"`
	Items            []json.RawMessage `json:"items"`
}

type responseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type streamEvent struct {
	Type      string            `json:"type"`
	Delta     string            `json:"delta"`
	Arguments string            `json:"arguments"`
	ItemID    string            `json:"item_id"`
	Item      responseItem      `json:"item"`
	Message   string            `json:"message"`
	Response  responsesResponse `json:"response"`
}

type responsesResponse struct {
	ID     string            `json:"id"`
	Output []json.RawMessage `json:"output"`
	Error  struct {
		Message string `json:"message"`
	} `json:"error"`
}

type responseItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

var _ dora.StreamingModel = (*Client)(nil)
