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
			Code    any    `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	message := ""
	code := ""
	if json.Unmarshal(body, &decoded) == nil && decoded.Error.Message != "" {
		message = decoded.Error.Message
		if text, ok := decoded.Error.Code.(string); ok {
			code = text
		}
		code += " " + decoded.Error.Type
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
	if IsContextLengthFailure(status, code, message) {
		return &dora.ContextLengthError{Err: err}
	}
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

// contextLengthMarkers are lower-case fragments that OpenAI-compatible and
// Anthropic-style servers put in the error code or message when the input is
// larger than the context window.
var contextLengthMarkers = []string{
	"context_length_exceeded",
	"maximum context length",
	"context length exceeded",
	"exceeds the context window",
	"exceed the context window",
	"exceeds context window",
	"context window of",
	"context window exceeded",
	"prompt is too long",
	"input is too long",
	"input is too large",
	"exceeds the maximum number of input tokens",
	"exceeds the model's maximum context",
	"reduce the length of the messages",
}

// IsContextLengthFailure reports whether a client-side HTTP rejection (400,
// 413 or 422) says the request exceeded the model's context window. code is
// the error.code/error.type text of the body when present. Rate limits and
// server errors are never context failures, even if their text mentions
// tokens (a per-minute token quota is not a window overflow).
func IsContextLengthFailure(status int, code, message string) bool {
	switch status {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
	default:
		return false
	}
	return MentionsContextLength(code + " " + message)
}

// MentionsContextLength reports whether text contains a known context-window
// overflow phrase. Stream error events use it where no HTTP status exists.
func MentionsContextLength(text string) bool {
	text = strings.ToLower(text)
	for _, marker := range contextLengthMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// StreamError builds the error for an error event received inside a 2xx
// stream, classifying context-window overflows (the only failure the agent
// handles specially) and leaving everything else as a plain error.
func StreamError(message string) error {
	err := errors.New(message)
	if MentionsContextLength(message) {
		return &dora.ContextLengthError{Err: err}
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

// StreamDecodeError labels a stream JSON error and retries incomplete payloads.
// encoding/json exposes premature EOF as a SyntaxError, not io.ErrUnexpectedEOF.
// Other syntax and type errors remain permanent protocol errors.
func StreamDecodeError(err error) error {
	wrapped := fmt.Errorf("decode stream event: %w", err)
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) && syntax.Error() == "unexpected end of JSON input" {
		return Retryable(wrapped)
	}
	return wrapped
}

// RetryableWithDelay wraps err as a dora.RetryableError with a suggested delay
// and kind.
func RetryableWithDelay(err error, retryAfter time.Duration, kind dora.RetryableErrorKind) error {
	return &dora.RetryableError{Err: err, RetryAfter: retryAfter, Kind: kind}
}

// maxParsedRetryAfterSeconds is one year; anything longer is equally "not soon".
const maxParsedRetryAfterSeconds = 365 * 24 * 3600

// ParseRetryAfter reads the Retry-After header, which may be a delay in
// seconds or an HTTP-date. It returns zero when the header is absent or
// unparseable.
func ParseRetryAfter(header http.Header) time.Duration {
	value := header.Get("Retry-After")
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		if seconds > maxParsedRetryAfterSeconds {
			seconds = maxParsedRetryAfterSeconds // avoid Duration overflow
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return 0
}
