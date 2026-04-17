package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/M4cr0Chen/llm-gateway/internal/handler"
	"github.com/M4cr0Chen/llm-gateway/internal/middleware"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// Server holds the HTTP handler and its dependencies.
type Server struct {
	Handler http.Handler
}

// New creates a Server with middleware and routes wired up.
func New(registry *provider.Registry, healthProviders map[string]*provider.HealthTrackingProvider, logger *slog.Logger) *Server {
	r := chi.NewRouter()

	// Middleware chain
	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(middleware.RequestLogger(logger))

	// Handlers
	chatHandler := handler.NewChatHandler(registry)
	modelsHandler := handler.NewModelsHandler(registry)
	internalHealthHandler := handler.NewInternalHealthHandler(healthProviders)

	// Routes
	r.Post("/v1/chat/completions", chatHandler.HandleChatCompletion)
	r.Get("/v1/models", modelsHandler.HandleListModels)
	r.Get("/health", handler.HandleHealth)
	r.Get("/internal/health", internalHealthHandler.HandleInternalHealth)

	return &Server{Handler: r}
}
