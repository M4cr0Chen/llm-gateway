package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
)

// writeError writes an OpenAI-compatible error response.
func writeError(w http.ResponseWriter, status int, errType, code, message string) {
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
		writeError(w, status, pe.Type, "provider_error", pe.Message)
		return
	}

	writeError(w, http.StatusBadGateway, "upstream_error", "provider_error", "upstream provider error")
}
