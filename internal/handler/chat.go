package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/M4cr0Chen/llm-gateway/internal/middleware"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// ChatHandler serves the /v1/chat/completions endpoint.
type ChatHandler struct {
	registry *provider.Registry
}

// NewChatHandler creates a new ChatHandler.
func NewChatHandler(registry *provider.Registry) *ChatHandler {
	return &ChatHandler{registry: registry}
}

// maxRequestBodySize is the maximum allowed request body size (10 MB).
const maxRequestBodySize = 10 << 20

// HandleChatCompletion handles both streaming and non-streaming chat completions.
func (h *ChatHandler) HandleChatCompletion(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

	var req model.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large",
				"request body exceeds 10MB limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			fmt.Sprintf("invalid request body: %v", err))
		return
	}

	if req.Model == "" {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"model is required")
		return
	}
	if len(req.Messages) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"messages must not be empty")
		return
	}

	p, err := h.registry.Resolve(req.Model)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", "invalid_model", err.Error())
		return
	}

	if req.Stream {
		h.handleStream(w, r, p, &req)
	} else {
		h.handleNonStream(w, r, p, &req)
	}
}

func (h *ChatHandler) handleNonStream(w http.ResponseWriter, r *http.Request, p provider.Provider, req *model.ChatCompletionRequest) {
	resp, err := p.ChatCompletion(r.Context(), req)
	if err != nil {
		handleProviderError(w, err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-LLM-Gateway-Provider", p.Name())
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *ChatHandler) handleStream(w http.ResponseWriter, r *http.Request, p provider.Provider, req *model.ChatCompletionRequest) {
	logger := middleware.LoggerFromContext(r.Context())

	ch, err := p.ChatCompletionStream(r.Context(), req)
	if err != nil {
		handleProviderError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "internal_error", "internal_error",
			"streaming not supported")
		// Drain the channel to avoid goroutine leak.
		for range ch {
		}
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-LLM-Gateway-Provider", p.Name())

	for evt := range ch {
		if evt.Err != nil {
			logger.Warn("stream error from provider", "error", evt.Err, "provider", p.Name())
			// Drain remaining events to unblock the provider goroutine.
			for range ch {
			}
			break
		}

		data, err := json.Marshal(evt.Chunk)
		if err != nil {
			logger.Warn("failed to marshal chunk", "error", err)
			for range ch {
			}
			break
		}

		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}

	// Write [DONE] sentinel unless the client disconnected.
	if r.Context().Err() == nil {
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}
}

// isMaxBytesError reports whether err was caused by exceeding the MaxBytesReader limit.
func isMaxBytesError(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}
