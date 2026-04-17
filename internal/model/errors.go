package model

import (
	"fmt"
	"strconv"
	"time"
)

// APIError is the top-level error response returned to clients (OpenAI-compatible format).
type APIError struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail contains the error information within an APIError.
type ErrorDetail struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

// ProviderError represents an error from an upstream LLM provider.
// Used internally for routing and fallback decisions.
type ProviderError struct {
	StatusCode int
	Type       string
	Message    string
	Retryable  bool
	RetryAfter time.Duration
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider error (%d): %s", e.StatusCode, e.Message)
}

// ParseRetryAfter parses an HTTP Retry-After header value, supporting both
// delay-seconds (integer) and HTTP-date (RFC 1123) formats per RFC 7231.
func ParseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := time.Parse(time.RFC1123, header); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
