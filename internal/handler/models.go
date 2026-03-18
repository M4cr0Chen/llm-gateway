package handler

import (
	"encoding/json"
	"net/http"

	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// ModelsHandler serves the /v1/models endpoint.
type ModelsHandler struct {
	registry *provider.Registry
}

// NewModelsHandler creates a new ModelsHandler.
func NewModelsHandler(registry *provider.Registry) *ModelsHandler {
	return &ModelsHandler{registry: registry}
}

type modelObject struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

type modelsResponse struct {
	Object string        `json:"object"`
	Data   []modelObject `json:"data"`
}

// HandleListModels returns the list of available models.
func (h *ModelsHandler) HandleListModels(w http.ResponseWriter, _ *http.Request) {
	details := h.registry.ListModelDetails()

	data := make([]modelObject, len(details))
	for i, d := range details {
		data[i] = modelObject{
			ID:      d.ModelName,
			Object:  "model",
			OwnedBy: d.ProviderName,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(modelsResponse{
		Object: "list",
		Data:   data,
	})
}
