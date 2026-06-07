package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/middleware"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// ChatHandler serves the /v1/chat/completions endpoint. It owns the
// strategy-aware router; the registry is no longer threaded in here —
// resolution happens inside router.Route.
type ChatHandler struct {
	router router.Router
}

// NewChatHandler builds a ChatHandler around the given Router.
func NewChatHandler(r router.Router) *ChatHandler {
	return &ChatHandler{router: r}
}

// maxRequestBodySize is the maximum allowed request body size (10 MB).
const maxRequestBodySize = 10 << 20

// HandleChatCompletion handles both streaming and non-streaming chat completions.
func (h *ChatHandler) HandleChatCompletion(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodySize)

	var req model.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		if isMaxBytesError(err) {
			WriteError(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "request_too_large",
				"request body exceeds 10MB limit")
			return
		}
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			fmt.Sprintf("invalid request body: %v", err))
		return
	}

	if req.Model == "" {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"model is required")
		return
	}
	if len(req.Messages) == 0 {
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"messages must not be empty")
		return
	}

	p, decision, err := h.router.Route(r.Context(), &req, routerMeta(r))
	if err != nil {
		writeRouterError(w, req.Model, err)
		return
	}
	middleware.SetModel(r.Context(), decision.Model)
	middleware.SetProvider(r.Context(), decision.Provider)
	middleware.SetStrategy(r.Context(), decision.Strategy)
	middleware.SetGroup(r.Context(), decision.Group)

	if req.Stream {
		h.handleStream(w, r, p, &req, decision)
	} else {
		h.handleNonStream(w, r, p, &req, decision)
	}
}

func (h *ChatHandler) handleNonStream(w http.ResponseWriter, r *http.Request, p provider.Provider, req *model.ChatCompletionRequest, decision router.Decision) {
	resp, err := p.ChatCompletion(r.Context(), req)
	if err != nil {
		handleProviderError(w, err)
		return
	}

	middleware.SetTokens(r.Context(), resp.Usage.PromptTokens, resp.Usage.CompletionTokens)

	w.Header().Set("Content-Type", "application/json")
	setRoutingHeaders(w.Header(), decision)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *ChatHandler) handleStream(w http.ResponseWriter, r *http.Request, p provider.Provider, req *model.ChatCompletionRequest, decision router.Decision) {
	logger := middleware.LoggerFromContext(r.Context())

	ch, err := p.ChatCompletionStream(r.Context(), req)
	if err != nil {
		handleProviderError(w, err)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, "internal_error", "internal_error",
			"streaming not supported")
		// Drain the channel to avoid goroutine leak.
		for range ch {
		}
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	setRoutingHeaders(w.Header(), decision)

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

// routerMeta builds router.RequestMeta from the per-request middleware
// state. OrgID/KeyID come from the auth layer; the rest of the struct is
// reserved for the M4.2 fallback loop (Attempt, TriedNames) and the
// router itself (Group).
func routerMeta(r *http.Request) router.RequestMeta {
	meta := router.RequestMeta{}
	if info, ok := auth.KeyInfoFromContext(r.Context()); ok {
		meta.OrgID = info.OrgID
		meta.KeyID = info.KeyID
	}
	return meta
}

// setRoutingHeaders stamps the X-LLM-Gateway-* headers that describe the
// routing decision. Group is omitted entirely when empty (concrete or
// alias request); Attempts is hard-coded to "1" in M4.1 since the
// fallback loop is deferred.
func setRoutingHeaders(h http.Header, decision router.Decision) {
	h.Set("X-LLM-Gateway-Provider", decision.Provider)
	h.Set("X-LLM-Gateway-Strategy", decision.Strategy)
	h.Set("X-LLM-Gateway-Attempts", "1")
	if decision.Group != "" {
		h.Set("X-LLM-Gateway-Group", decision.Group)
	}
}

// writeRouterError maps a router-layer error to the documented HTTP
// shape. ErrUnknownModel → 400 invalid_model (matches M1 behaviour);
// ErrNoHealthyProviders → 503 all_providers_down. Anything else is an
// internal_error — the router does not emit other typed errors today.
func writeRouterError(w http.ResponseWriter, modelName string, err error) {
	switch {
	case errors.Is(err, router.ErrUnknownModel):
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_model",
			fmt.Sprintf("unknown model %q", modelName))
	case errors.Is(err, router.ErrNoHealthyProviders):
		WriteError(w, http.StatusServiceUnavailable, "service_unavailable", "all_providers_down",
			"all candidate providers are unavailable")
	default:
		WriteError(w, http.StatusInternalServerError, "internal_error", "internal_error",
			fmt.Sprintf("routing failed: %v", err))
	}
}

// isMaxBytesError reports whether err was caused by exceeding the MaxBytesReader limit.
func isMaxBytesError(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}
