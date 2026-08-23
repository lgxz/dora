package provider

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lgxz/dora"
)

func TestIsRetryableStatus(t *testing.T) {
	tests := []struct {
		status int
		want   bool
	}{
		{http.StatusOK, false},
		{http.StatusNotFound, false},
		{http.StatusBadRequest, false},
		{http.StatusTooManyRequests, true},
		{http.StatusInternalServerError, true},
		{http.StatusBadGateway, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tt := range tests {
		if got := IsRetryableStatus(tt.status); got != tt.want {
			t.Errorf("IsRetryableStatus(%d) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	// An HTTP-date exactly one second in the future.
	future := time.Now().Add(time.Second)
	futureHeader := http.Header{}
	futureHeader.Set("Retry-After", future.UTC().Format(http.TimeFormat))

	tests := []struct {
		name   string
		header http.Header
		want   time.Duration
	}{
		{"absent", http.Header{}, 0},
		{"integer seconds", http.Header{"Retry-After": {"5"}}, 5 * time.Second},
		{"negative integer", http.Header{"Retry-After": {"-3"}}, 0},
		{"non-numeric", http.Header{"Retry-After": {"abc"}}, 0},
		{"http-date future", futureHeader, time.Second},
		{"http-date past", http.Header{"Retry-After": {time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)}}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseRetryAfter(tt.header)
			if tt.name == "http-date future" {
				// Exact match is flaky; the parse returns time.Until(when) which
				// shrinks between the two calls. Assert it's positive and <= 2s.
				if got <= 0 || got > 2*time.Second {
					t.Fatalf("ParseRetryAfter() = %v, want (0, 2s]", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("ParseRetryAfter() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAPIErrorJSONMessage(t *testing.T) {
	err := APIError("pfx", http.StatusBadRequest, http.Header{}, []byte(`{"error":{"message":"bad param"}}`))
	if strings.Contains(err.Error(), "bad param") == false {
		t.Errorf("error = %q, want to contain 'bad param'", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "pfx: API returned HTTP 400") {
		t.Errorf("error = %q, want 'pfx: ' message prefix", err.Error())
	}
	var retryable *dora.RetryableError
	if errors.As(err, &retryable) {
		t.Errorf("err = %v, want non-retryable for 400", err)
	}
}

func TestAPIErrorNonJSONBodyTruncation(t *testing.T) {
	long := strings.Repeat("a", 600)
	err := APIError("pfx", http.StatusBadRequest, http.Header{}, []byte(long))
	msg := err.Error()
	if len(msg) > 512+len("pfx: API returned HTTP 400: ")+3 {
		t.Errorf("message too long, len = %d (body not truncated): %q", len(msg), msg)
	}
	if !strings.HasSuffix(msg, "...") {
		t.Errorf("message = %q, want '...' suffix for truncated body", msg)
	}
}

func TestAPIErrorGenericNonJSONBody(t *testing.T) {
	err := APIError("pfx", http.StatusBadGateway, http.Header{}, []byte("boom"))
	var retryable *dora.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("err = %v, want *dora.RetryableError for 502", err)
	}
	if retryable.Kind != dora.RetryableGeneric {
		t.Errorf("Kind = %v, want RetryableGeneric", retryable.Kind)
	}
	if !strings.Contains(retryable.Err.Error(), "boom") {
		t.Errorf("error = %q, want to contain 'boom'", retryable.Err.Error())
	}
}

func TestAPIErrorRateLimitUsesHeader(t *testing.T) {
	header := http.Header{"Retry-After": {"7"}}
	err := APIError("pfx", http.StatusTooManyRequests, header, []byte(`{"error":{"message":"too fast"}}`))
	var retryable *dora.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("err = %v, want *dora.RetryableError", err)
	}
	if retryable.Kind != dora.RetryableRateLimit {
		t.Errorf("Kind = %v, want RetryableRateLimit", retryable.Kind)
	}
	if retryable.RetryAfter != 7*time.Second {
		t.Errorf("RetryAfter = %v, want 7s", retryable.RetryAfter)
	}
}

func TestAPIErrorRateLimitUsesDefault(t *testing.T) {
	err := APIError("pfx", http.StatusTooManyRequests, http.Header{}, []byte("{}"))
	var retryable *dora.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("err = %v, want *dora.RetryableError", err)
	}
	if retryable.Kind != dora.RetryableRateLimit {
		t.Errorf("Kind = %v, want RetryableRateLimit", retryable.Kind)
	}
	if retryable.RetryAfter != DefaultRateLimitRetryAfter {
		t.Errorf("RetryAfter = %v, want %v", retryable.RetryAfter, DefaultRateLimitRetryAfter)
	}
}

func TestAPIErrorEmptyBody(t *testing.T) {
	err := APIError("pfx", http.StatusInternalServerError, http.Header{}, []byte{})
	var retryable *dora.RetryableError
	if !errors.As(err, &retryable) {
		t.Fatalf("err = %v, want *dora.RetryableError", err)
	}
	if retryable.Kind != dora.RetryableGeneric {
		t.Errorf("Kind = %v, want RetryableGeneric", retryable.Kind)
	}
	if !strings.Contains(err.Error(), "API returned HTTP 500") {
		t.Errorf("error = %q, want 'API returned HTTP 500'", err.Error())
	}
}

func TestRetryableWrappers(t *testing.T) {
	base := errors.New("the cause")

	r := Retryable(base)
	var retryable *dora.RetryableError
	if !errors.As(r, &retryable) {
		t.Fatalf("Retryable() result is not a *dora.RetryableError")
	}
	if retryable.Err != base {
		t.Errorf("Retryable() Err = %v, want %v", retryable.Err, base)
	}
	if retryable.Kind != dora.RetryableGeneric {
		t.Errorf("Retryable() Kind = %v, want RetryableGeneric (zero)", retryable.Kind)
	}
	if retryable.RetryAfter != 0 {
		t.Errorf("Retryable() RetryAfter = %v, want 0", retryable.RetryAfter)
	}

	r2 := RetryableWithDelay(base, 9*time.Second, dora.RetryableRateLimit)
	if !errors.As(r2, &retryable) {
		t.Fatalf("RetryableWithDelay() result is not a *dora.RetryableError")
	}
	if retryable.Err != base {
		t.Errorf("RetryableWithDelay() Err = %v, want %v", retryable.Err, base)
	}
	if retryable.RetryAfter != 9*time.Second {
		t.Errorf("RetryableWithDelay() RetryAfter = %v, want 9s", retryable.RetryAfter)
	}
	if retryable.Kind != dora.RetryableRateLimit {
		t.Errorf("RetryableWithDelay() Kind = %v, want RetryableRateLimit", retryable.Kind)
	}

	// Error/Unwrap round-trip.
	if r2.Error() != base.Error() {
		t.Errorf("Error() = %q, want %q", r2.Error(), base.Error())
	}
	if !errors.Is(r2, base) {
		t.Errorf("errors.Is(r2, base) = false, want true via Unwrap")
	}
}