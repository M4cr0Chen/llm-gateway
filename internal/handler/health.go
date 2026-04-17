package handler

import (
	"encoding/json"
	"net/http"

	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// HandleHealth returns a simple health check response.
func HandleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// InternalHealthHandler serves detailed per-provider health information.
type InternalHealthHandler struct {
	providers map[string]*provider.HealthTrackingProvider
}

// NewInternalHealthHandler creates a handler that reports health for the given
// named providers.
func NewInternalHealthHandler(providers map[string]*provider.HealthTrackingProvider) *InternalHealthHandler {
	return &InternalHealthHandler{providers: providers}
}

// internalHealthResponse is the JSON structure for GET /internal/health.
type internalHealthResponse struct {
	Status    string                           `json:"status"`
	Providers map[string]provider.HealthStatus `json:"providers"`
}

// HandleInternalHealth returns per-provider health status with an aggregate
// status of "ok", "degraded", or "down".
func (h *InternalHealthHandler) HandleInternalHealth(w http.ResponseWriter, _ *http.Request) {
	statuses := make(map[string]provider.HealthStatus, len(h.providers))
	healthyCount := 0

	for name, p := range h.providers {
		s := p.Health.Status()
		statuses[name] = s
		if s.Healthy {
			healthyCount++
		}
	}

	var overall string
	switch {
	case healthyCount == len(h.providers):
		overall = "ok"
	case healthyCount == 0:
		overall = "down"
	default:
		overall = "degraded"
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(internalHealthResponse{
		Status:    overall,
		Providers: statuses,
	})
}
