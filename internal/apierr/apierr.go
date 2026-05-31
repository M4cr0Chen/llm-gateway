// Package apierr renders OpenAI-compatible error responses. It lives in
// its own package so middleware (auth, rate limit) and handlers can share
// a single error shape without creating an import cycle.
package apierr

import (
	"encoding/json"
	"net/http"

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
