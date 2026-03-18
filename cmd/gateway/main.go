package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/provider/openai"
	"github.com/M4cr0Chen/llm-gateway/internal/server"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		logger.Error("OPENAI_API_KEY is required")
		os.Exit(1)
	}

	registry := provider.NewRegistry()

	openaiProvider := openai.New(openai.Config{
		APIKey: apiKey,
		Models: []string{"gpt-4o", "gpt-4o-mini"},
	})
	registry.Register(openaiProvider, openaiProvider.Models())

	srv := server.New(registry, logger)

	logger.Info("starting llm-gateway", slog.String("addr", ":8080"))
	if err := http.ListenAndServe(":8080", srv.Handler); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
