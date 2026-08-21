// Package openairesponses implements dora.Model using the OpenAI Responses
// API and its server-sent event stream.
package openairesponses

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/lgxz/dora"
	"github.com/lgxz/dora/model/provider"
)

// defaultRateLimitRetryAfter is retained as an alias for tests that assert the
// fallback delay used when a 429 response omits Retry-After.
const defaultRateLimitRetryAfter = provider.DefaultRateLimitRetryAfter

// Config configures a Client.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
	// ConnectTimeout bounds TCP connection setup. Zero uses a 10 second
	// default. Ignored when HTTPClient is set.
	ConnectTimeout time.Duration
	// StreamIdleTimeout bounds the idle time between streaming events. Zero
	// disables the idle timeout and leaves the stream governed by the caller's
	// context.
	StreamIdleTimeout time.Duration
	// Timeout bounds a non-streaming generation request. Zero uses a 120
	// second default.
	Timeout time.Duration
	// MaxTokens caps the number of tokens the model is allowed to generate in
	// one response. Nil sends no cap and leaves it to the provider default. It
	// is sent as max_output_tokens on the Responses API. An explicit 0 means
	// "no explicit cap" and is sent as-is.
	MaxTokens *int
	// Temperature controls sampling randomness in [0, 2]. Nil sends no value
	// and leaves it to the provider default.
	Temperature *float64
	// Reasoning controls the model's reasoning effort. Nil sends no value and
	// leaves it to the provider default.
	Reasoning *ReasoningControl
}

// Client is an OpenAI Responses API model client. Connection-level concerns
// live in the embedded *provider.Provider; this struct only holds the model
// name and generation parameters.
type Client struct {
	provider    *provider.Provider
	model       string
	maxTokens   *int
	temperature *float64
	reasoning   *reasoningControl
}

// New creates an OpenAI Responses API model client.
func New(cfg Config) (*Client, error) {
	p, err := provider.New(provider.Config{
		Name:              "openai responses",
		BaseURL:           cfg.BaseURL,
		Path:              "/responses",
		APIKey:            cfg.APIKey,
		Timeout:           cfg.Timeout,
		ConnectTimeout:    cfg.ConnectTimeout,
		StreamIdleTimeout: cfg.StreamIdleTimeout,
		HTTPClient:        cfg.HTTPClient,
	})
	if err != nil {
		return nil, err
	}
	if cfg.Model == "" {
		return nil, errors.New("openai responses: model is required")
	}
	return &Client{
		provider:    p,
		model:       cfg.Model,
		maxTokens:   cfg.MaxTokens,
		temperature: cfg.Temperature,
		reasoning:   cfg.Reasoning,
	}, nil
}

// Generate implements dora.Model. The response is received as a stream even
// when the caller does not consume incremental events.
func (c *Client) Generate(ctx context.Context, request dora.Request) (dora.Response, error) {
	// Apply the overall timeout to non-streaming requests. Streaming requests
	// are governed by the caller's context plus an optional idle timeout.
	ctx, cancel := context.WithTimeout(ctx, c.provider.Timeout())
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
	if idle := c.provider.StreamIdleTimeout(); idle > 0 {
		ctx, onActivity = provider.WithIdleTimeout(ctx, idle)
	}

	reader, err := c.provider.PostStream(ctx, payload)
	if err != nil {
		return dora.Response{}, err
	}
	defer reader.Close()

	response, err := readStream(reader, emit, onActivity)
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
		Model:           c.model,
		Stream:          true,
		Store:           false,
		Include:         []string{"reasoning.encrypted_content"},
		MaxOutputTokens: c.maxTokens,
		Temperature:     c.temperature,
		Reasoning:       c.reasoning,
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
		url, err := provider.ImageDataURL(image)
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
	scanner.Buffer(make([]byte, 64<<10), provider.MaxBodyBytes)
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
		case "response.reasoning_summary_text.delta":
			if emit != nil && event.Delta != "" {
				emit(dora.ModelEvent{Kind: dora.ModelEventReasoningDelta, Delta: event.Delta})
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
		return dora.Response{}, provider.Retryable(fmt.Errorf("read stream: %w", err))
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
		case "reasoning":
			for _, summary := range item.Summary {
				result.Reasoning += summary.Text
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

type responsesRequest struct {
	Model           string            `json:"model"`
	Input           []json.RawMessage `json:"input"`
	Tools           []responseTool    `json:"tools,omitempty"`
	Stream          bool              `json:"stream"`
	Store           bool              `json:"store"`
	Include         []string          `json:"include,omitempty"`
	MaxOutputTokens *int              `json:"max_output_tokens,omitempty"`
	Temperature     *float64          `json:"temperature,omitempty"`
	Reasoning       *reasoningControl `json:"reasoning,omitempty"`
}

// reasoningControl controls the model's reasoning effort on the Responses API.
type reasoningControl struct {
	Effort string `json:"effort"`
}

// ReasoningControl is an exported alias for the reasoning control that the CLI
// uses to build the wire field.
type ReasoningControl = reasoningControl

// NewReasoningControl returns a reasoning control with the given effort.
func NewReasoningControl(effort string) *reasoningControl {
	return &reasoningControl{Effort: effort}
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
	// Summary holds the reasoning summaries of reasoning output items. The
	// items themselves are preserved byte-for-byte in the continuation via
	// their raw JSON; only the summary text is surfaced on the response.
	Summary []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

var _ dora.StreamingModel = (*Client)(nil)
