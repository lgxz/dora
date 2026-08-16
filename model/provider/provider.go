// Package provider contains the shared HTTP transport, retry, and SSE
// infrastructure used by the OpenAI-compatible model adapters. It is the
// single home for connection-level concerns (base URL, API key, timeouts,
// http.Client) and the error/retry classification that were previously
// duplicated across the openai and openairesponses packages.
package provider

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// MaxBodyBytes bounds a single response or error body read.
	MaxBodyBytes = 4 << 20
	// DefaultRateLimitRetryAfter is the delay used when a rate-limit (429)
	// response does not carry a usable Retry-After header.
	DefaultRateLimitRetryAfter = 30 * time.Second
)

// Config describes how to connect to a model provider endpoint.
type Config struct {
	// Name labels the provider in error messages (e.g. "openai").
	Name string
	// BaseURL is the API root, e.g. "https://api.openai.com/v1".
	BaseURL string
	// Path is appended to BaseURL to form the request endpoint, e.g.
	// "/chat/completions".
	Path string
	// APIKey authenticates requests via the Bearer scheme. Empty omits the
	// Authorization header.
	APIKey string
	// Timeout bounds a non-streaming generation request. Zero uses a 120
	// second default.
	Timeout time.Duration
	// ConnectTimeout bounds TCP connection setup when HTTPClient is nil. Zero
	// uses a 10 second default. Ignored when HTTPClient is set.
	ConnectTimeout time.Duration
	// StreamIdleTimeout bounds the idle time between streaming events. Zero
	// disables the idle timeout and leaves the stream governed by the caller's
	// context.
	StreamIdleTimeout time.Duration
	// HTTPClient optionally supplies a custom client (e.g. for tests). When
	// nil, a client is built with a dialer honouring ConnectTimeout. When set,
	// ConnectTimeout is ignored.
	HTTPClient *http.Client
}

// Provider holds the resolved connection for a model endpoint. The same
// Provider may be shared by multiple model instances that differ only in
// generation parameters.
type Provider struct {
	name              string
	endpoint          string
	apiKey            string
	httpClient        *http.Client
	timeout           time.Duration
	streamIdleTimeout time.Duration
}

// New validates cfg and returns a Provider. The provider Name is used as the
// prefix in all transport-level error messages.
func New(cfg Config) (*Provider, error) {
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("%s: base URL is required", cfg.Name)
	}
	parsed, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%s: parse base URL: %w", cfg.Name, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("%s: base URL must use http or https", cfg.Name)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("%s: base URL must include a host", cfg.Name)
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		connectTimeout := cfg.ConnectTimeout
		if connectTimeout == 0 {
			connectTimeout = 10 * time.Second
		}
		httpClient = &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: connectTimeout}).DialContext,
			},
		}
	}

	return &Provider{
		name:              cfg.Name,
		endpoint:          strings.TrimRight(cfg.BaseURL, "/") + cfg.Path,
		apiKey:            cfg.APIKey,
		httpClient:        httpClient,
		timeout:           timeout,
		streamIdleTimeout: cfg.StreamIdleTimeout,
	}, nil
}

// Timeout returns the overall non-streaming request timeout after defaults
// are applied.
func (p *Provider) Timeout() time.Duration { return p.timeout }

// StreamIdleTimeout returns the streaming idle timeout; zero disables it.
func (p *Provider) StreamIdleTimeout() time.Duration { return p.streamIdleTimeout }

// PostStream sends a JSON POST request expecting an SSE response stream. On
// success it returns the response body for the caller to read and close; on
// failure it returns a plain or dora.RetryableError labelled with the
// provider name.
func (p *Provider) PostStream(ctx context.Context, payload []byte) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("%s: create request: %w", p.name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, Retryable(fmt.Errorf("%s: send request: %w", p.name, err))
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, MaxBodyBytes+1))
		resp.Body.Close()
		if readErr != nil {
			return nil, Retryable(fmt.Errorf("%s: read error response: %w", p.name, readErr))
		}
		return nil, APIError(p.name, resp.StatusCode, resp.Header, body)
	}
	return resp.Body, nil
}
