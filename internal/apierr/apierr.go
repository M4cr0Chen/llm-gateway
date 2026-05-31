// Package apierr renders OpenAI-compatible error responses. It lives in
// its own package so middleware (auth, rate limit) and handlers can share
// a single error shape without creating an import cycle.
package apierr

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
)

// Write writes an OpenAI-compatible error JSON body with the given status.
func Write(w http.ResponseWriter, status int, errType, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(model.APIError{
		Error: model.ErrorDetail{
			Message: message,
			Type:    errType,
			Code:    code,
		},
	})
}

// WriteRateLimit writes an OpenAI-compatible 429 response and sets the
// Retry-After header to the duration rounded up to whole seconds (per
// RFC 7231). An empty message is replaced with the default
// "Rate limit exceeded. Try again in Xs." form. The three
// X-RateLimit-* headers are the caller's responsibility because they
// must also appear on allowed responses.
func WriteRateLimit(w http.ResponseWriter, retryAfter time.Duration, message string) {
	secs := int(math.Ceil(retryAfter.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	if message == "" {
		message = fmt.Sprintf("Rate limit exceeded. Try again in %ds.", secs)
	}
	Write(w, http.StatusTooManyRequests, "rate_limit_error", "rate_limit_exceeded", message)
}
