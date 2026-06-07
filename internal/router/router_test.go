package router_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// providersConfig is the minimal cfg.Providers map used by NewRouter's
// "candidate provider must be configured" validation. Real ProviderConfig
// fields don't matter — only the keys are inspected.
var providersConfig = map[string]config.ProviderConfig{
	"openai":    {},
	"anthropic": {},
	"google":    {},
}

func newRegistry(t *testing.T, models map[string]string) *provider.Registry {
	t.Helper()
	reg := provider.NewRegistry()
	byProv := map[string][]string{}
	for m, p := range models {
		byProv[p] = append(byProv[p], m)
	}
	for p, ms := range byProv {
		reg.Register(stubProvider{name: p}, ms)
	}
	return reg
}

func TestRoute_ConcreteModel(t *testing.T) {
	reg := newRegistry(t, map[string]string{"gpt-4o": "openai"})
	rtr, err := router.NewRouter(reg, config.RoutingConfig{
		DefaultStrategy: "priority",
	}, providersConfig, buildFixed("priority"))
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: "gpt-4o", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	p, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Equal(t, "openai", p.Name())
	assert.Equal(t, "openai", dec.Provider)
	assert.Equal(t, "gpt-4o", dec.Model)
	assert.Equal(t, "priority", dec.Strategy)
	assert.Equal(t, "", dec.Group, "concrete model requests carry no group")
}

func TestRoute_Alias(t *testing.T) {
	reg := newRegistry(t, map[string]string{"claude-sonnet-4-20250514": "anthropic"})
	require.NoError(t, reg.RegisterAlias("claude", "claude-sonnet-4-20250514"))

	rtr, err := router.NewRouter(reg, config.RoutingConfig{DefaultStrategy: "priority"}, providersConfig, buildFixed("priority"))
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: "claude", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	p, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", p.Name())
	assert.Equal(t, "claude", dec.Model, "alias resolves through the registry; the model field retains the alias")
	assert.Equal(t, "", dec.Group)
}

func TestRoute_ModelGroup(t *testing.T) {
	reg := newRegistry(t, map[string]string{
		"gpt-4o":                   "openai",
		"claude-sonnet-4-20250514": "anthropic",
	})
	cfg := config.RoutingConfig{
		DefaultStrategy: "priority",
		ModelGroups: map[string]config.ModelGroupConfig{
			"smart": {
				Candidates: []config.CandidateConfig{
					{Provider: "openai", Model: "gpt-4o", Priority: 0, Weight: 1},
					{Provider: "anthropic", Model: "claude-sonnet-4-20250514", Priority: 1, Weight: 1},
				},
			},
		},
	}

	// Capture the candidate list the strategy receives.
	cap := &fixedStrategy{name: "priority", pickIndex: 0}
	rtr, err := router.NewRouter(reg, cfg, providersConfig, func(string) (router.Strategy, error) { return cap, nil })
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: "smart", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	p, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Equal(t, "openai", p.Name())
	assert.Equal(t, "smart", dec.Group)
	assert.Equal(t, "smart", cap.lastMeta.Group, "Router must populate meta.Group before calling Strategy.Select")
	require.Len(t, cap.lastList, 2)
	assert.Equal(t, "openai", cap.lastList[0].Provider.Name())
	assert.Equal(t, "anthropic", cap.lastList[1].Provider.Name())
}

func TestRoute_GroupStrategyOverride(t *testing.T) {
	reg := newRegistry(t, map[string]string{"gpt-4o": "openai"})
	defaultCap := &fixedStrategy{name: "priority", pickIndex: 0}
	overrideCap := &fixedStrategy{name: "round_robin", pickIndex: 0}

	build := func(name string) (router.Strategy, error) {
		switch name {
		case "priority":
			return defaultCap, nil
		case "round_robin":
			return overrideCap, nil
		}
		return nil, errors.New("unexpected strategy name " + name)
	}

	cfg := config.RoutingConfig{
		DefaultStrategy: "priority",
		ModelGroups: map[string]config.ModelGroupConfig{
			"fast": {
				Strategy: "round_robin",
				Candidates: []config.CandidateConfig{
					{Provider: "openai", Model: "gpt-4o", Priority: 0, Weight: 1},
				},
			},
		},
	}
	rtr, err := router.NewRouter(reg, cfg, providersConfig, build)
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: "fast", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	_, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Equal(t, "round_robin", dec.Strategy)
	assert.NotNil(t, overrideCap.lastList, "override strategy should have been consulted")
	assert.Nil(t, defaultCap.lastList, "default strategy should not have been consulted")
}

func TestRoute_UnknownModel(t *testing.T) {
	reg := newRegistry(t, map[string]string{"gpt-4o": "openai"})
	rtr, err := router.NewRouter(reg, config.RoutingConfig{DefaultStrategy: "priority"}, providersConfig, buildFixed("priority"))
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: "no-such-model", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	_, _, err = rtr.Route(context.Background(), req, router.RequestMeta{})
	require.ErrorIs(t, err, router.ErrUnknownModel)
}

func TestNewRouter_ValidatesDefaultStrategy(t *testing.T) {
	reg := newRegistry(t, map[string]string{"gpt-4o": "openai"})
	build := func(name string) (router.Strategy, error) {
		return nil, errors.New("unknown routing strategy \"" + name + "\"")
	}
	_, err := router.NewRouter(reg, config.RoutingConfig{DefaultStrategy: "bogus"}, providersConfig, build)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}

func TestNewRouter_ValidatesGroupCandidateProvider(t *testing.T) {
	reg := newRegistry(t, map[string]string{"gpt-4o": "openai"})
	cfg := config.RoutingConfig{
		DefaultStrategy: "priority",
		ModelGroups: map[string]config.ModelGroupConfig{
			"bad": {
				Candidates: []config.CandidateConfig{
					{Provider: "not-configured", Model: "gpt-4o"},
				},
			},
		},
	}
	_, err := router.NewRouter(reg, cfg, providersConfig, buildFixed("priority"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not-configured")
}

func TestNewRouter_ValidatesGroupStrategyOverride(t *testing.T) {
	reg := newRegistry(t, map[string]string{"gpt-4o": "openai"})
	build := func(name string) (router.Strategy, error) {
		if name == "priority" {
			return &fixedStrategy{name: "priority"}, nil
		}
		return nil, errors.New("unknown routing strategy \"" + name + "\"")
	}
	cfg := config.RoutingConfig{
		DefaultStrategy: "priority",
		ModelGroups: map[string]config.ModelGroupConfig{
			"smart": {
				Strategy: "bogus",
				Candidates: []config.CandidateConfig{
					{Provider: "openai", Model: "gpt-4o"},
				},
			},
		},
	}
	_, err := router.NewRouter(reg, cfg, providersConfig, build)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")
}
