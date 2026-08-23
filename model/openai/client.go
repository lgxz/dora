// Package openai implements dora.Model using the OpenAI-compatible Chat
// Completions protocol.
package openai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
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
	// one response. Nil sends no cap and leaves it to the provider default.
	MaxTokens *int
	// Temperature controls sampling randomness in [0, 2]. Nil sends no value
	// and leaves it to the provider default.
	Temperature *float64
	// ReasoningEffort controls the model's reasoning effort for compatible
	// providers. Nil sends no value and leaves it to the provider default.
	ReasoningEffort *string
	// Thinking controls "thinking mode" reasoning for providers that support
	// it over Chat Completions (e.g. DeepSeek's disabled control). Nil sends
	// no value and leaves it to the provider default.
	Thinking *ThinkingControl
	// PreserveThinking controls whether assistant history messages that carry
	// tool calls include their captured reasoning as reasoning_content in the
	// request. When enabled, captured reasoning is resent on tool-calling turns
	// (some providers such as DeepSeek require it and reject the request
	// otherwise); providers that expect reasoning to be stripped from history
	// (e.g. Qwen/DashScope) must leave it disabled (the default).
	PreserveThinking *bool
}

// Client is an OpenAI-compatible dora.Model. Connection-level concerns live
// in the embedded *provider.Provider; this struct only holds the model name
// and generation parameters.
type Client struct {
	provider         *provider.Provider
	model            string
	maxTokens        *int
	temperature      *float64
	reasoningEffort  *string
	thinking         *thinkingControl
	preserveThinking bool
}

// New creates an OpenAI-compatible model client.
func New(cfg Config) (*Client, error) {
	p, err := provider.New(provider.Config{
		Name:              "openai",
		BaseURL:           cfg.BaseURL,
		Path:              "/chat/completions",
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
		return nil, errors.New("openai: model is required")
	}
	return &Client{
		provider:         p,
		model:            cfg.Model,
		maxTokens:        cfg.MaxTokens,
		temperature:      cfg.Temperature,
		reasoningEffort:  cfg.ReasoningEffort,
		thinking:         cfg.Thinking,
		preserveThinking: cfg.PreserveThinking != nil && *cfg.PreserveThinking,
	}, nil
}

// PreserveThinking reports whether the configured flag is enabled. It is used
// by tests and by the registry to surface the profile-level setting.
func (c *Client) PreserveThinking() bool {
	return c.preserveThinking
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
		return dora.Response{}, fmt.Errorf("openai: encode request: %w", err)
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
		return dora.Response{}, fmt.Errorf("openai: %w", err)
	}
	return response, nil
}

func (c *Client) requestBody(request dora.Request) (chatRequest, error) {
	body := chatRequest{Model: c.model, Stream: true, MaxTokens: c.maxTokens, Temperature: c.temperature, ReasoningEffort: c.reasoningEffort, Thinking: c.thinking}
	body.StreamOptions = &chatStreamOptions{IncludeUsage: boolPtr(true)}
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
			// When PreserveThinking is enabled, intermediate tool-calling
			// assistant messages carry their reasoning back; the final
			// (tool-free) assistant message is never resent, and providers that
			// expect reasoning to be stripped keep it off entirely.
			if c.preserveThinking && message.Reasoning != "" && len(message.ToolCalls) > 0 {
				converted.ReasoningContent = message.Reasoning
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
		url, err := provider.ImageDataURL(image)
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
	scanner.Buffer(make([]byte, 64<<10), provider.MaxBodyBytes)
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
			if reasoning := reasoningDelta(choice.Delta); reasoning != "" {
				result.Reasoning += reasoning
				if emit != nil {
					emit(dora.ModelEvent{Kind: dora.ModelEventReasoningDelta, Delta: reasoning})
				}
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
		// The final chunk with include_usage carries empty choices and a usage
		// block; capture it. Chunks with empty choices and no usage are skipped.
		if event.Usage != nil {
			result.Usage = usageFromChat(event.Usage)
		}
	}
	if err := scanner.Err(); err != nil {
		return dora.Response{}, provider.Retryable(fmt.Errorf("read stream: %w", err))
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

type chatRequest struct {
	Model           string             `json:"model"`
	Messages        []chatMessage      `json:"messages"`
	Tools           []chatTool         `json:"tools,omitempty"`
	Stream          bool               `json:"stream"`
	MaxTokens       *int               `json:"max_tokens,omitempty"`
	Temperature     *float64           `json:"temperature,omitempty"`
	ReasoningEffort *string            `json:"reasoning_effort,omitempty"`
	Thinking        *thinkingControl   `json:"thinking,omitempty"`
	StreamOptions   *chatStreamOptions `json:"stream_options,omitempty"`
}

// chatStreamOptions requests per-stream token usage from Chat Completions. When
// IncludeUsage is set the stream's final chunk (empty choices) carries usage.
type chatStreamOptions struct {
	IncludeUsage *bool `json:"include_usage"`
}

// thinkingControl controls "thinking mode" reasoning for providers that
// support it over Chat Completions (e.g. DeepSeek's disabled control).
type thinkingControl struct {
	Type string `json:"type"`
}

// ThinkingControl is an exported alias for the "thinking mode" control that
// the CLI uses to build the wire field for providers that support it.
type ThinkingControl = thinkingControl

// NewThinkingControl returns a "thinking mode" control with the given type.
func NewThinkingControl(ctrlType string) *thinkingControl {
	return &thinkingControl{Type: ctrlType}
}

type chatMessage struct {
	Role             string          `json:"role"`
	Content          json.RawMessage `json:"content,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
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
		Index int             `json:"index"`
		Delta chatStreamDelta `json:"delta"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

// chatUsage mirrors the token accounting block of a Chat Completions stream
// chunk. When stream_options.include_usage is enabled the provider emits a
// final chunk with empty choices that carries this block.
type chatUsage struct {
	PromptTokens      int64             `json:"prompt_tokens"`
	CompletionTokens  int64             `json:"completion_tokens"`
	TotalTokens       int64             `json:"total_tokens"`
	PromptDetails     *chatUsageDetails `json:"prompt_tokens_details,omitempty"`
	CompletionDetails *chatUsageDetails `json:"completion_tokens_details,omitempty"`
}

type chatUsageDetails struct {
	ReasoningTokens *int64 `json:"reasoning_tokens"`
	CachedTokens    *int64 `json:"cached_tokens,omitempty"`
}

// chatStreamDelta is one streamed message delta. Reasoning models expose their
// chain-of-thought in a non-standard field; providers disagree on the name, so
// all known candidates are decoded and reasoningDelta picks the first
// non-empty one.
type chatStreamDelta struct {
	Content          string               `json:"content"`
	ReasoningContent string               `json:"reasoning_content"`
	Reasoning        string               `json:"reasoning"`
	Reason           string               `json:"reason"`
	ToolCalls        []chatStreamToolCall `json:"tool_calls"`
}

type chatStreamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// reasoningDelta extracts the chain-of-thought chunk from a stream delta,
// preferring the DeepSeek-style reasoning_content field over the shorter
// reasoning and reason variants some providers use.
func reasoningDelta(delta chatStreamDelta) string {
	if delta.ReasoningContent != "" {
		return delta.ReasoningContent
	}
	if delta.Reasoning != "" {
		return delta.Reasoning
	}
	return delta.Reason
}

// boolPtr returns a pointer to the given boolean value.
func boolPtr(value bool) *bool {
	return &value
}

// usageFromChat converts a Chat Completions token block into the neutral
// dora.Usage shape, mapping prompt/completion tokens onto input/output tokens.
// It is nil safe: an absent usage block yields nil.
func usageFromChat(u *chatUsage) *dora.Usage {
	if u == nil {
		return nil
	}
	usage := &dora.Usage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.PromptDetails != nil && u.PromptDetails.ReasoningTokens != nil {
		usage.InputDetails = &dora.TokenDetails{ReasoningTokens: u.PromptDetails.ReasoningTokens}
	}
	if u.CompletionDetails != nil && u.CompletionDetails.ReasoningTokens != nil {
		usage.OutputDetails = &dora.TokenDetails{ReasoningTokens: u.CompletionDetails.ReasoningTokens}
	}
	return usage
}

var _ dora.StreamingModel = (*Client)(nil)
