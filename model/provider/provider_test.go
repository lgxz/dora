package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"empty base url", Config{Name: "m", BaseURL: ""}, "base URL is required"},
		{"unparseable", Config{Name: "m", BaseURL: "://bad"}, "parse base URL"},
		{"bad scheme", Config{Name: "m", BaseURL: "ftp://example.com"}, "must use http or https"},
		{"no host", Config{Name: "m", BaseURL: "https://"}, "must include a host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg)
			if err == nil {
				t.Fatalf("New() error = nil, want message containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New() error = %q, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	p, err := New(Config{Name: "m", BaseURL: "https://example.com/v1", Path: "/chat"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.Timeout() != 120*time.Second {
		t.Errorf("Timeout() = %v, want 120s", p.Timeout())
	}
	if p.StreamIdleTimeout() != 0 {
		t.Errorf("StreamIdleTimeout() = %v, want 0", p.StreamIdleTimeout())
	}
}

func TestNewCustomValues(t *testing.T) {
	p, err := New(Config{
		Name:             "m",
		BaseURL:          "https://example.com/v1/",
		Path:             "/chat/completions",
		Timeout:          5 * time.Second,
		StreamIdleTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.Timeout() != 5*time.Second {
		t.Errorf("Timeout() = %v, want 5s", p.Timeout())
	}
	if p.StreamIdleTimeout() != 3*time.Second {
		t.Errorf("StreamIdleTimeout() = %v, want 3s", p.StreamIdleTimeout())
	}
	// endpoint = base URL (trailing slash trimmed) + path
	if p.endpoint != "https://example.com/v1/chat/completions" {
		t.Errorf("endpoint = %q, want https://example.com/v1/chat/completions", p.endpoint)
	}
}

func TestNewHTTPClientSuppliedNotOverridden(t *testing.T) {
	client := &http.Client{}
	p, err := New(Config{
		Name:           "m",
		BaseURL:        "https://example.com",
		ConnectTimeout: 99 * time.Second, // must be ignored
		HTTPClient:     client,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if p.httpClient != client {
		t.Errorf("httpClient != supplied client")
	}
}

func TestPostStreamSuccess(t *testing.T) {
	var gotContentType, gotAccept, gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "data: {\"ok\":true}\n\n")
	}))
	defer server.Close()

	p, err := New(Config{Name: "m", BaseURL: server.URL, APIKey: "secret-key"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	body, err := p.PostStream(context.Background(), []byte(`{"x":1}`))
	if err != nil {
		t.Fatalf("PostStream() error = %v", err)
	}
	defer body.Close()
	data, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(data) != "data: {\"ok\":true}\n\n" {
		t.Errorf("body = %q, want data: {\"ok\":true}", data)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotAccept != "text/event-stream" {
		t.Errorf("Accept = %q, want text/event-stream", gotAccept)
	}
	if gotAuth != "Bearer secret-key" {
		t.Errorf("Authorization = %q, want Bearer secret-key", gotAuth)
	}
}

func TestPostStreamNoAPIKeyNoAuthHeader(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	p, err := New(Config{Name: "m", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	body, err := p.PostStream(context.Background(), nil)
	if err != nil {
		t.Fatalf("PostStream() error = %v", err)
	}
	body.Close()
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
}

func TestPostStreamRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, `{"error":{"message":"slow down"}}`)
	}))
	defer server.Close()

	p, err := New(Config{Name: "m", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = p.PostStream(context.Background(), nil)
	if err == nil {
		t.Fatal("PostStream() error = nil, want retryable error")
	}
	var retryable *dora.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("err = %v, want *dora.RetryableError", err)
	}
	if retryable.Kind != dora.RetryableRateLimit {
		t.Errorf("Kind = %v, want RetryableRateLimit", retryable.Kind)
	}
	if retryable.RetryAfter != 12*time.Second {
		t.Errorf("RetryAfter = %v, want 12s", retryable.RetryAfter)
	}
	if !strings.Contains(retryable.Err.Error(), "slow down") {
		t.Errorf("error = %q, want to contain 'slow down'", retryable.Err.Error())
	}
	if !strings.Contains(retryable.Err.Error(), "m") {
		t.Errorf("error = %q, want to contain provider prefix 'm'", retryable.Err.Error())
	}
}

func TestPostStreamReadsErrorBodyWithLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		// Body larger than M axBodyBytes to exercise the read limit in PostStream.
		w.Write([]byte(strings.Repeat("x", MaxBodyBytes+100)))
	}))
	defer server.Close()

	p, err := New(Config{Name: "m", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = p.PostStream(context.Background(), nil)
	if err == nil {
		t.Fatal("PostStream() error = nil, want error")
	}
	var retryable *dora.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("err = %v, want *dora.RetryableError", err)
	}
	if retryable.Kind != dora.RetryableGeneric {
		t.Errorf("Kind = %v, want RetryableGeneric", retryable.Kind)
	}
}