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

func TestRegistry_RegisterAlias(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(&mockProvider{name: "openai"}, []string{"gpt-4o"})
	r.Register(&mockProvider{name: "anthropic"}, []string{"claude-sonnet-4-20250514"})

	tests := []struct {
		name         string
		alias        string
		target       string
		wantErr      bool
		wantProvider string
	}{
		{
			name:         "alias resolves to correct provider",
			alias:        "gpt4",
			target:       "gpt-4o",
			wantProvider: "openai",
		},
		{
			name:         "alias to different provider",
			alias:        "claude",
			target:       "claude-sonnet-4-20250514",
			wantProvider: "anthropic",
		},
		{
			name:    "alias to unregistered model returns error",
			alias:   "bad",
			target:  "nonexistent-model",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := r.RegisterAlias(tt.alias, tt.target)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.target)
				return
			}
			require.NoError(t, err)

			got, err := r.Resolve(tt.alias)
			require.NoError(t, err)
			assert.Equal(t, tt.wantProvider, got.Name())
		})
	}
}

func TestRegistry_AliasConflictsWithCanonicalModel(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(&mockProvider{name: "openai"}, []string{"gpt-4o"})
	r.Register(&mockProvider{name: "anthropic"}, []string{"claude-sonnet-4-20250514"})

	err := r.RegisterAlias("gpt-4o", "claude-sonnet-4-20250514")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflicts")
}

func TestRegistry_AliasAppearsInListModels(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(&mockProvider{name: "openai"}, []string{"gpt-4o"})

	err := r.RegisterAlias("gpt4", "gpt-4o")
	require.NoError(t, err)

	models := r.ListModels()
	assert.Contains(t, models, "gpt4")
	assert.Contains(t, models, "gpt-4o")
}

func TestRegistry_AliasAppearsInListModelDetails(t *testing.T) {
	r := provider.NewRegistry()
	r.Register(&mockProvider{name: "openai"}, []string{"gpt-4o"})

	err := r.RegisterAlias("gpt4", "gpt-4o")
	require.NoError(t, err)

	details := r.ListModelDetails()
	found := false
	for _, d := range details {
		if d.ModelName == "gpt4" {
			assert.Equal(t, "openai", d.ProviderName)
			found = true
		}
	}
	assert.True(t, found, "alias should appear in ListModelDetails")
}
