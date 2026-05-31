package server

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/handler"
	"github.com/M4cr0Chen/llm-gateway/internal/middleware"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// Server holds the HTTP handler and its dependencies.
type Server struct {
	Handler http.Handler
}

// Options bundles the optional collaborators wired into the HTTP server.
// Any field may be left nil to disable the corresponding feature surface
// (handy in tests and in dev mode with auth disabled).
type Options struct {
	HealthProviders map[string]*provider.HealthTrackingProvider
	Authenticator   auth.Authenticator       // nil → /v1/* mounted without auth
	AdminToken      string                   // empty → admin routes are NOT mounted
	AdminKeys       *handler.AdminKeysHandler // nil → admin routes are NOT mounted
}

// New constructs the HTTP server. Public routes (/health, /v1/*) are
// always mounted; /internal/health is mounted when HealthProviders is
// supplied; admin routes are mounted only when both AdminToken and
// AdminKeys are present.
func New(registry *provider.Registry, opts Options, logger *slog.Logger) *Server {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.Recoverer)
	r.Use(middleware.RequestLogger(logger))

	chatHandler := handler.NewChatHandler(registry)
	modelsHandler := handler.NewModelsHandler(registry)

	r.Get("/health", handler.HandleHealth)
	if len(opts.HealthProviders) > 0 {
		internalHealth := handler.NewInternalHealthHandler(opts.HealthProviders)
		r.Get("/internal/health", internalHealth.HandleInternalHealth)
	}

	r.Group(func(api chi.Router) {
		if opts.Authenticator != nil {
			api.Use(middleware.RequireAPIKey(opts.Authenticator))
		}
		api.Post("/v1/chat/completions", chatHandler.HandleChatCompletion)
		api.Get("/v1/models", modelsHandler.HandleListModels)
	})

	if opts.AdminToken != "" && opts.AdminKeys != nil {
		r.Group(func(adm chi.Router) {
			adm.Use(middleware.RequireAdminToken(opts.AdminToken))
			adm.Post("/internal/admin/keys", opts.AdminKeys.Create)
			adm.Delete("/internal/admin/keys/{id}", opts.AdminKeys.Revoke)
		})
	}

	return &Server{Handler: r}
}
