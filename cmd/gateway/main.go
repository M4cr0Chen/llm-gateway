package main

import (
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/provider/openai"
	"github.com/M4cr0Chen/llm-gateway/internal/server"
)

func main() {
	configPath := os.Getenv("GATEWAY_CONFIG_PATH")
	if configPath == "" {
		configPath = "configs/gateway.yaml"
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}

	setupLogger(cfg.Log)

	registry := provider.NewRegistry()
	for name, provCfg := range cfg.Providers {
		if provCfg.APIKey == "" {
			slog.Warn("skipping provider with no API key", "provider", name)
			continue
		}
		switch name {
		case "openai":
			p := openai.New(openai.Config{
				APIKey:  provCfg.APIKey,
				BaseURL: provCfg.BaseURL,
				Timeout: provCfg.Timeout,
				Models:  provCfg.Models,
			})
			registry.Register(p, p.Models())
		default:
			slog.Warn("unknown provider, skipping", "provider", name)
		}
	}

	srv := server.New(registry, slog.Default())

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
