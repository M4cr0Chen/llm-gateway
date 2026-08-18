package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/middleware"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// ChatHandler serves the /v1/chat/completions endpoint. It owns the
// strategy-aware router; the registry is no longer threaded in here —
// resolution, the fallback loop, and the actual upstream call all live
// inside router.Route / router.RouteStream.
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

	if req.Stream {
		h.handleStream(w, r, &req)
	} else {
		h.handleNonStream(w, r, &req)
	}
}

func (h *ChatHandler) handleNonStream(w http.ResponseWriter, r *http.Request, req *model.ChatCompletionRequest) {
	resp, decision, err := h.router.Route(r.Context(), req, routerMeta(r))
	stampDecisionContext(r, decision)
	if err != nil {
		writeRouterError(w, req.Model, decision, err)
		return
	}

	middleware.SetTokens(r.Context(), resp.Usage.PromptTokens, resp.Usage.CompletionTokens)

	w.Header().Set("Content-Type", "application/json")
	setRoutingHeaders(w.Header(), decision)
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *ChatHandler) handleStream(w http.ResponseWriter, r *http.Request, req *model.ChatCompletionRequest) {
	logger := middleware.LoggerFromContext(r.Context())

	ch, decision, err := h.router.RouteStream(r.Context(), req, routerMeta(r))
	stampDecisionContext(r, decision)
	if err != nil {
		writeRouterError(w, req.Model, decision, err)
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
			logger.Warn("stream error from provider", "error", evt.Err, "provider", decision.Provider)
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
// state. OrgID/KeyID come from the auth layer; Attempt and TriedNames
// stay zero/nil at entry — the Router populates them as it iterates.
func routerMeta(r *http.Request) router.RequestMeta {
	meta := router.RequestMeta{}
	if info, ok := auth.KeyInfoFromContext(r.Context()); ok {
		meta.OrgID = info.OrgID
		meta.KeyID = info.KeyID
	}
	return meta
}

// stampDecisionContext mirrors the routing Decision into the per-request
// middleware state so the access logger can include it on every line
// (including the error paths, where setRoutingHeaders is not called).
func stampDecisionContext(r *http.Request, decision router.Decision) {
	ctx := r.Context()
	if decision.Model != "" {
		middleware.SetModel(ctx, decision.Model)
	}
	if decision.Provider != "" {
		middleware.SetProvider(ctx, decision.Provider)
	}
	if decision.Strategy != "" {
		middleware.SetStrategy(ctx, decision.Strategy)
	}
	if decision.Group != "" {
		middleware.SetGroup(ctx, decision.Group)
	}
	if decision.Attempts > 0 {
		middleware.SetAttempts(ctx, decision.Attempts)
	}
}

// setRoutingHeaders stamps the X-LLM-Gateway-* headers that describe the
// routing decision. Group is omitted entirely when empty (concrete or
// alias request).
func setRoutingHeaders(h http.Header, decision router.Decision) {
	h.Set("X-LLM-Gateway-Provider", decision.Provider)
	h.Set("X-LLM-Gateway-Strategy", decision.Strategy)
	h.Set("X-LLM-Gateway-Attempts", strconv.Itoa(decision.Attempts))
	if decision.Group != "" {
		h.Set("X-LLM-Gateway-Group", decision.Group)
	}
}

// writeRouterError maps a router-layer error to the documented HTTP
// shape:
//
//   - ErrUnknownModel        → 400 invalid_model
//   - ErrNoHealthyProviders  → 503 all_providers_down
//   - else, attempts > 1     → 502 with "all N providers returned errors"
//     (every fallback hop failed)
//   - else                   → handleProviderError (preserves 4xx
//     passthrough; maps 5xx to 502)
func writeRouterError(w http.ResponseWriter, modelName string, decision router.Decision, err error) {
	switch {
	case errors.Is(err, router.ErrUnknownModel):
		WriteError(w, http.StatusBadRequest, "invalid_request_error", "invalid_model",
			fmt.Sprintf("unknown model %q", modelName))
	case errors.Is(err, router.ErrNoHealthyProviders):
		handleNoHealthyProviders(w)
	default:
		if decision.Attempts > 1 {
			handleFallbackExhausted(w, decision.Attempts)
			return
		}
		handleProviderError(w, err)
	}
}

// isMaxBytesError reports whether err was caused by exceeding the MaxBytesReader limit.
func isMaxBytesError(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr)
}
