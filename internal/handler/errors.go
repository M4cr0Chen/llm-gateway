package handler

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/apierr"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
)

// WriteError is a thin alias for apierr.Write, kept for the existing
// in-package call sites.
func WriteError(w http.ResponseWriter, status int, errType, code, message string) {
	apierr.Write(w, status, errType, code, message)
}

// WriteRateLimitError is a thin alias for apierr.WriteRateLimit, kept so
// in-package call sites can share the same idiom as WriteError. The
// rate-limit middleware itself calls apierr directly to stay outside
// this package and avoid the handler ↔ middleware import cycle (admin
// handlers depend on middleware for context helpers).
func WriteRateLimitError(w http.ResponseWriter, retryAfter time.Duration, message string) {
	apierr.WriteRateLimit(w, retryAfter, message)
}

// handleProviderError maps a provider error to an HTTP response.
// If the error is a *model.ProviderError, its StatusCode is forwarded
// (with 5xx mapped to 502 since the gateway itself is healthy).
// All other errors are treated as 502 upstream errors.
func handleProviderError(w http.ResponseWriter, err error) {
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		status := pe.StatusCode
		if status >= 500 {
			status = http.StatusBadGateway
		}
		apierr.Write(w, status, pe.Type, "provider_error", pe.Message)
		return
	}

	apierr.Write(w, http.StatusBadGateway, "upstream_error", "provider_error", "upstream provider error")
}

// handleNoHealthyProviders writes the 503 all_providers_down response
// emitted when every candidate's circuit breaker was open at the start
// of routing (M4.2). The shape matches docs/api-spec.md.
func handleNoHealthyProviders(w http.ResponseWriter) {
	apierr.Write(w, http.StatusServiceUnavailable, "service_unavailable", "all_providers_down",
		"all candidate providers are unavailable")
}

// handleFallbackExhausted writes the 502 provider_error response emitted
// when the Router tried `attempts` distinct providers and every one
// returned a retryable error. attempts is the count made, not the
// configured max_attempts (those can diverge when the healthy candidate
// pool shrinks mid-request).
func handleFallbackExhausted(w http.ResponseWriter, attempts int) {
	apierr.Write(w, http.StatusBadGateway, "upstream_error", "provider_error",
		fmt.Sprintf("all %d providers returned errors", attempts))
}
