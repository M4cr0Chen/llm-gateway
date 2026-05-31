package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"sort"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/handler"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/provider/anthropic"
	"github.com/M4cr0Chen/llm-gateway/internal/provider/google"
	"github.com/M4cr0Chen/llm-gateway/internal/provider/openai"
	"github.com/M4cr0Chen/llm-gateway/internal/server"
	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

func main() {
	configPath := os.Getenv("GATEWAY_CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/gateway.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		// Use log.Fatalf because slog is not configured until after config is loaded.
		log.Fatalf("failed to load config: %v", err)
	}

	setupLogger(cfg.Log)

	registry, healthProviders := buildProviders(cfg)

	authenticator, adminKeys, closer := buildAuth(cfg)
	if closer != nil {
		defer closer()
	}

	srv := server.New(registry, server.Options{
		HealthProviders: healthProviders,
		Authenticator:   authenticator,
		AdminToken:      cfg.Auth.AdminToken,
		AdminKeys:       adminKeys,
	}, slog.Default())

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv.Handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	slog.Info("starting llm-gateway", "addr", addr)
	if err := httpServer.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func buildProviders(cfg *config.Config) (*provider.Registry, map[string]*provider.HealthTrackingProvider) {
	healthCfg := provider.HealthConfig{
		FailureThreshold: cfg.Health.FailureThreshold,
		CooldownPeriod:   cfg.Health.CooldownPeriod,
	}

	registry := provider.NewRegistry()
	healthProviders := make(map[string]*provider.HealthTrackingProvider)

	for name, provCfg := range cfg.Providers {
		if provCfg.APIKey == "" {
			slog.Warn("skipping provider with no API key", "provider", name)
			continue
		}

		retryCfg := provider.RetryConfig{
			MaxRetries:   provCfg.MaxRetries,
			RetryBackoff: provCfg.RetryBackoff,
		}

		var base provider.Provider
		switch name {
		case "openai":
			base = openai.New(openai.Config{
				APIKey:  provCfg.APIKey,
				BaseURL: provCfg.BaseURL,
				Timeout: provCfg.Timeout,
				Models:  provCfg.Models,
			})
		case "anthropic":
			base = anthropic.New(anthropic.Config{
				APIKey:  provCfg.APIKey,
				BaseURL: provCfg.BaseURL,
				Timeout: provCfg.Timeout,
				Models:  provCfg.Models,
			})
		case "google":
			base = google.New(google.Config{
				APIKey:  provCfg.APIKey,
				BaseURL: provCfg.BaseURL,
				Timeout: provCfg.Timeout,
				Models:  provCfg.Models,
			})
		default:
			slog.Warn("unknown provider, skipping", "provider", name)
			continue
		}

		wrapped := provider.NewHealthTrackingProvider(base, healthCfg, retryCfg)
		healthProviders[name] = wrapped
		registry.Register(wrapped, wrapped.Models())
	}

	aliasKeys := make([]string, 0, len(cfg.ModelAliases))
	for alias := range cfg.ModelAliases {
		aliasKeys = append(aliasKeys, alias)
	}
	sort.Strings(aliasKeys)

	for _, alias := range aliasKeys {
		target := cfg.ModelAliases[alias]
		if err := registry.RegisterAlias(alias, target); err != nil {
			log.Fatalf("failed to register model alias %q -> %q: %v", alias, target, err)
		}
		slog.Info("registered model alias", "alias", alias, "target", target)
	}

	return registry, healthProviders
}

// buildAuth constructs the authenticator and admin handler based on
// cfg.Auth.Enabled. When disabled, returns an AllowAll dev escape hatch
// and a nil admin handler (so admin routes are not mounted). Returns a
// closer that releases the database pool (nil when auth is disabled).
func buildAuth(cfg *config.Config) (auth.Authenticator, *handler.AdminKeysHandler, func()) {
	if !cfg.Auth.Enabled {
		return auth.NewAllowAll(), nil, nil
	}

	ctx := context.Background()
	pool, err := store.NewPool(ctx, cfg.Database.Postgres.DSN, cfg.Database.Postgres.MaxConnections)
	if err != nil {
		log.Fatalf("failed to connect to postgres: %v", err)
	}

	if cfg.Database.Postgres.AutoMigrate {
		if err := store.RunMigrations(cfg.Database.Postgres.DSN); err != nil {
			pool.Close()
			log.Fatalf("failed to apply migrations: %v", err)
		}
		slog.Info("database migrations applied")
	}

	keysStore := store.NewAPIKeyStore(pool)
	cached := auth.NewCached(keysStore, cfg.Auth.CacheTTL, cfg.Auth.CacheSize)
	adminHandler := handler.NewAdminKeysHandler(keysStore, cached.Invalidate)
	return cached, adminHandler, pool.Close
}

func setupLogger(cfg config.LogConfig) {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	var handler slog.Handler
	if cfg.Format == "text" {
		handler = slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	}
	slog.SetDefault(slog.New(handler))
}
