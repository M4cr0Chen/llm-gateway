package handler

import (
	"errors"
	"net/http"

	"github.com/M4cr0Chen/llm-gateway/internal/apierr"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
)

// WriteError is a thin alias for apierr.Write, kept for the existing
// in-package call sites.
func WriteError(w http.ResponseWriter, status int, errType, code, message string) {
	apierr.Write(w, status, errType, code, message)
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
