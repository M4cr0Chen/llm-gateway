package provider

import (
	"context"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
)

// Provider is the interface that all LLM provider adapters must implement.
type Provider interface {
	Name() string
	ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error)
	ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error)
	Models() []string
}

// StreamEvent carries either a chunk or an error from the streaming provider.
type StreamEvent struct {
	Chunk *model.ChatCompletionChunk
	Err   error
}
