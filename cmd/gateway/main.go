package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/handler"
	"github.com/M4cr0Chen/llm-gateway/internal/metrics"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/provider/anthropic"
	"github.com/M4cr0Chen/llm-gateway/internal/provider/google"
	"github.com/M4cr0Chen/llm-gateway/internal/provider/openai"
	"github.com/M4cr0Chen/llm-gateway/internal/ratelimit"
	"github.com/M4cr0Chen/llm-gateway/internal/server"
	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

// shutdownTimeout caps how long graceful shutdown waits for in-flight
// requests to finish before forcibly closing connections.
const shutdownTimeout = 30 * time.Second

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

	limiter, limiterCloser := buildRateLimiter(cfg)
	if limiterCloser != nil {
		defer limiterCloser()
	}

	srv := server.New(registry, server.Options{
		HealthProviders: healthProviders,
		Authenticator:   authenticator,
		AdminToken:      cfg.Auth.AdminToken,
		AdminKeys:       adminKeys,
		RateLimiter:     limiter,
		DebugBodies:     cfg.Log.DebugBodies,
	}, slog.Default())

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      srv.Handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	metricsServer := buildMetricsServer(cfg.Metrics)

	// Wire SIGINT/SIGTERM to a context so the server (and any deferred
	// cleanup, like releasing the Postgres pool) can shut down cleanly
	// instead of being killed mid-request.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("starting llm-gateway", "addr", addr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
		close(serverErr)
	}()

	// metricsErr stays nil when the metrics server is disabled. A receive
	// on a nil channel blocks forever, so the select case below is
	// effectively disabled — closing the channel here instead would make
	// the case fire immediately at startup and the gateway would exit.
	var metricsErr chan error
	if metricsServer != nil {
		metricsErr = make(chan error, 1)
		go func() {
			slog.Info("starting metrics server", "addr", metricsServer.Addr)
			if err := metricsServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				metricsErr <- err
			}
			close(metricsErr)
		}()
	}

	exitCode := 0
	select {
	case err := <-serverErr:
		if err != nil {
			slog.Error("http server stopped with error", "err", err)
			exitCode = 1
		}
	case err := <-metricsErr:
		if err != nil {
			slog.Error("metrics server stopped with error", "err", err)
			exitCode = 1
		}
	case <-ctx.Done():
		slog.Info("shutdown signal received, draining")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	shutdownAll(shutdownCtx, httpServer, metricsServer)

	if exitCode != 0 {
		os.Exit(exitCode)
	}
}

// buildMetricsServer constructs the second HTTP server that exposes the
// Prometheus scrape endpoint on /metrics and a simple /health probe.
// Returns nil when metrics are disabled so the caller can skip starting
// the goroutine entirely.
func buildMetricsServer(cfg config.MetricsConfig) *http.Server {
	if !cfg.Enabled {
		return nil
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}
}

// shutdownAll drains both servers concurrently within the shared context
// timeout so neither one blocks the other.
func shutdownAll(ctx context.Context, servers ...*http.Server) {
	var wg sync.WaitGroup
	for _, s := range servers {
		if s == nil {
			continue
		}
		wg.Add(1)
		go func(s *http.Server) {
			defer wg.Done()
			if err := s.Shutdown(ctx); err != nil {
				slog.Error("graceful shutdown failed", "addr", s.Addr, "err", err)
			}
		}(s)
	}
	wg.Wait()
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
		// Seed the health gauge so the panel shows the provider before any
		// traffic flows. The gauge will be overwritten by the first
		// success/failure observed by the health decorator.
		metrics.SetProviderHealth(name, true)
	}

	aliasKeys := make([]string, 0, len(cfg.ModelAliases))
	for alias := range cfg.ModelAliases {
		aliasKeys = append(aliasKeys, alias)
	}
	sort.Strings(aliasKeys)

	for _, alias := range aliasKeys {
		target := cfg.ModelAliases[alias]
		if err := registry.RegisterAlias(alias, target); err != nil {
			// A dangling alias (target provider intentionally disabled, or
			// not yet configured) shouldn't take the whole gateway down —
			// the alias just won't be resolvable. Surface a WARN so it's
			// visible without killing the process.
			slog.Warn("skipping model alias with unregistered target",
				"alias", alias, "target", target, "err", err)
			continue
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

// buildRateLimiter constructs the Redis-backed rate limiter when the
// `rate_limit` section is enabled, mirroring how buildAuth gates Postgres
// behind cfg.Auth.Enabled. A failure to reach Redis at startup is fatal
// — running with rate limiting silently disabled would let unauthorised
// traffic through and is worse than refusing to boot.
func buildRateLimiter(cfg *config.Config) (ratelimit.Reserver, func()) {
	if !cfg.RateLimit.Enabled {
		return nil, nil
	}
	ctx := context.Background()
	rdb, err := store.NewRedis(ctx, cfg.Database.Redis.DSN)
	if err != nil {
		log.Fatalf("failed to connect to redis: %v", err)
	}
	return ratelimit.NewRedis(rdb, cfg.RateLimit), func() { _ = rdb.Close() }
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
