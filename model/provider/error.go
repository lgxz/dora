package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lgxz/dora"
)

// APIError builds an error for a non-2xx HTTP response, extracting a message
// from the JSON error body when present and classifying retryable status
// codes. prefix labels the source (e.g. "openai") in the message.
func APIError(prefix string, status int, header http.Header, body []byte) error {
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
		message = fmt.Sprintf("%s: API returned HTTP %d", prefix, status)
	} else {
		message = fmt.Sprintf("%s: API returned HTTP %d: %s", prefix, status, message)
	}
	err := errors.New(message)
	if IsRetryableStatus(status) {
		if status == http.StatusTooManyRequests {
			delay := ParseRetryAfter(header)
			if delay == 0 {
				delay = DefaultRateLimitRetryAfter
			}
			return RetryableWithDelay(err, delay, dora.RetryableRateLimit)
		}
		return RetryableWithDelay(err, ParseRetryAfter(header), dora.RetryableGeneric)
	}
	return err
}

// IsRetryableStatus reports whether an HTTP status is likely to succeed on a
// later attempt: rate limits and transient server errors.
func IsRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

// Retryable wraps err as a dora.RetryableError with no suggested delay.
func Retryable(err error) error {
	return &dora.RetryableError{Err: err}
}

// RetryableWithDelay wraps err as a dora.RetryableError with a suggested delay
// and kind.
func RetryableWithDelay(err error, retryAfter time.Duration, kind dora.RetryableErrorKind) error {
	return &dora.RetryableError{Err: err, RetryAfter: retryAfter, Kind: kind}
}

// ParseRetryAfter reads the Retry-After header, which may be a delay in
// seconds or an HTTP-date. It returns zero when the header is absent or
// unparseable.
func ParseRetryAfter(header http.Header) time.Duration {
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
