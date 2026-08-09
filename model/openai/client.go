// Package openai implements dora.Model using the OpenAI-compatible Chat
// Completions protocol.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dora"
)

const maxResponseBytes = 4 << 20

// Config configures a Client.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// Client is an OpenAI-compatible dora.Model.
type Client struct {
	endpoint   string
	apiKey     string
	model      string
	httpClient *http.Client
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

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}

	return &Client{
		endpoint:   strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions",
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		httpClient: httpClient,
	}, nil
}

// Generate implements dora.Model.
func (c *Client) Generate(ctx context.Context, request dora.Request) (dora.Response, error) {
	body, err := c.requestBody(request)
	if err != nil {
		return dora.Response{}, err
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai: encode request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai: create request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	httpResponse, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai: send request: %w", err)
	}
	defer httpResponse.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(httpResponse.Body, maxResponseBytes+1))
	if err != nil {
		return dora.Response{}, fmt.Errorf("openai: read response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return dora.Response{}, errors.New("openai: response exceeds 4 MiB")
	}
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return dora.Response{}, apiError(httpResponse.StatusCode, responseBody)
	}

	var decoded chatResponse
	if err := json.Unmarshal(responseBody, &decoded); err != nil {
		return dora.Response{}, fmt.Errorf("openai: decode response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return dora.Response{}, errors.New("openai: response contains no choices")
	}

	message := decoded.Choices[0].Message
	result := dora.Response{Content: message.Content}
	for _, call := range message.ToolCalls {
		arguments := json.RawMessage(call.Function.Arguments)
		if len(arguments) == 0 {
			arguments = json.RawMessage(`{}`)
		}
		if !json.Valid(arguments) {
			return dora.Response{}, fmt.Errorf("openai: tool %q returned invalid JSON arguments", call.Function.Name)
		}
		result.ToolCalls = append(result.ToolCalls, dora.ToolCall{
			ID:    call.ID,
			Name:  call.Function.Name,
			Input: arguments,
		})
	}
	return result, nil
}

func (c *Client) requestBody(request dora.Request) (chatRequest, error) {
	body := chatRequest{Model: c.model}
	for _, message := range request.Messages {
		converted := chatMessage{
			Role:       string(message.Role),
			Content:    message.Content,
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

func apiError(status int, body []byte) error {
	var decoded struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &decoded) == nil && decoded.Error.Message != "" {
		return fmt.Errorf("openai: API returned HTTP %d: %s", status, decoded.Error.Message)
	}
	message := strings.TrimSpace(string(body))
	if len(message) > 512 {
		message = message[:512] + "..."
	}
	if message == "" {
		return fmt.Errorf("openai: API returned HTTP %d", status)
	}
	return fmt.Errorf("openai: API returned HTTP %d: %s", status, message)
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Tools    []chatTool    `json:"tools,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
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

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}
