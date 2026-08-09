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
	"net/http"
	"net/url"
	"strings"
	"time"

	"dora"
)

const maxEventBytes = 4 << 20

// Config configures a Client.
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// Client is an OpenAI Responses API model client.
type Client struct {
	endpoint   string
	apiKey     string
	model      string
	httpClient *http.Client
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

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Minute}
	}
	return &Client{
		endpoint:   strings.TrimRight(cfg.BaseURL, "/") + "/responses",
		apiKey:     cfg.APIKey,
		model:      cfg.Model,
		httpClient: httpClient,
	}, nil
}

// Generate implements dora.Model. The response is received as a stream even
// when the caller does not consume incremental events.
func (c *Client) Generate(ctx context.Context, request dora.Request) (dora.Response, error) {
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
		return dora.Response{}, fmt.Errorf("openai responses: send request: %w", err)
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		responseBody, readErr := io.ReadAll(io.LimitReader(httpResponse.Body, maxEventBytes+1))
		if readErr != nil {
			return dora.Response{}, fmt.Errorf("openai responses: read error response: %w", readErr)
		}
		return dora.Response{}, apiError(httpResponse.StatusCode, responseBody)
	}

	response, err := readStream(httpResponse.Body, emit)
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
			if message.Content != "" {
				if err := appendInput(input, inputItem{Role: string(message.Role), Content: message.Content}); err != nil {
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
			if message.Content != "" {
				if err := appendInput(input, inputItem{Role: string(message.Role), Content: message.Content}); err != nil {
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

func readStream(reader io.Reader, emit func(dora.ModelEvent)) (dora.Response, error) {
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
				if !json.Valid(arguments) {
					return fmt.Errorf("tool %q returned invalid JSON arguments", item.Name)
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
		return dora.Response{}, fmt.Errorf("read stream: %w", err)
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
			if !json.Valid(arguments) {
				return dora.Response{}, fmt.Errorf("tool %q returned invalid JSON arguments", item.Name)
			}
			result.ToolCalls = append(result.ToolCalls, dora.ToolCall{
				ID: item.CallID, Name: item.Name, Input: arguments,
			})
		}
	}
	return result, nil
}

func apiError(status int, body []byte) error {
	var decoded struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &decoded) == nil && decoded.Error.Message != "" {
		return fmt.Errorf("openai responses: API returned HTTP %d: %s", status, decoded.Error.Message)
	}
	message := strings.TrimSpace(string(body))
	if len(message) > 512 {
		message = message[:512] + "..."
	}
	if message == "" {
		return fmt.Errorf("openai responses: API returned HTTP %d", status)
	}
	return fmt.Errorf("openai responses: API returned HTTP %d: %s", status, message)
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
	Type    string `json:"type,omitempty"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
	CallID  string `json:"call_id,omitempty"`
	Output  string `json:"output,omitempty"`
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
