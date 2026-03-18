package provider_test

import (
	"context"
	"testing"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type mockProvider struct {
	name   string
	models []string
}

func (m *mockProvider) Name() string { return m.name }
func (m *mockProvider) Models() []string { return m.models }
func (m *mockProvider) ChatCompletion(_ context.Context, _ *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return nil, nil
}
func (m *mockProvider) ChatCompletionStream(_ context.Context, _ *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	return nil, nil
}

func TestRegistry_RegisterAndResolve(t *testing.T) {
	r := provider.NewRegistry()
	p := &mockProvider{name: "openai", models: []string{"gpt-4o", "gpt-4o-mini"}}
	r.Register(p, p.Models())

	got, err := r.Resolve("gpt-4o")
	require.NoError(t, err)
	assert.Equal(t, "openai", got.Name())

	got, err = r.Resolve("gpt-4o-mini")
	require.NoError(t, err)
	assert.Equal(t, "openai", got.Name())
}

func TestRegistry_ResolveUnknown(t *testing.T) {
	r := provider.NewRegistry()
	p := &mockProvider{name: "openai", models: []string{"gpt-4o"}}
	r.Register(p, p.Models())

	_, err := r.Resolve("unknown-model")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown-model")
	assert.Contains(t, err.Error(), "gpt-4o")
}

func TestRegistry_ListModels(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(&mockProvider{name: "openai"}, []string{"gpt-4o", "gpt-4o-mini"})
	r.Register(&mockProvider{name: "anthropic"}, []string{"claude-sonnet"})

	models := r.ListModels()
	assert.Equal(t, []string{"claude-sonnet", "gpt-4o", "gpt-4o-mini"}, models)
}

func TestRegistry_RegisterOverwrite(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(&mockProvider{name: "provider-a"}, []string{"shared-model"})
	r.Register(&mockProvider{name: "provider-b"}, []string{"shared-model"})

	got, err := r.Resolve("shared-model")
	require.NoError(t, err)
	assert.Equal(t, "provider-b", got.Name())
}
