package model

import "fmt"

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
}

func (e *ProviderError) Error() string {
	return fmt.Sprintf("provider error (%d): %s", e.StatusCode, e.Message)
}
