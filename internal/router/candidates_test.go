package router_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

func TestRoute_PricingMapPopulatesConcreteCandidate(t *testing.T) {
	reg := newRegistry(t, map[string]string{"gpt-4o": "openai"})
	cfg := config.RoutingConfig{
		DefaultStrategy: "priority",
		Pricing: map[string]config.CandidatePricing{
			"gpt-4o": {CostPer1kInputUSD: 0.0025, CostPer1kOutputUSD: 0.01},
		},
	}
	cap := &fixedStrategy{name: "priority", pickIndex: 0}
	rtr, err := router.NewRouter(reg, cfg, providersConfig, func(string) (router.Strategy, error) { return cap, nil })
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: "gpt-4o", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	_, _, err = rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	require.Len(t, cap.lastList, 1)
	assert.InDelta(t, 0.0025, cap.lastList[0].CostPer1kInputUSD, 1e-9)
	assert.InDelta(t, 0.01, cap.lastList[0].CostPer1kOutputUSD, 1e-9)
}

func TestRoute_AllUnhealthyReturnsNoHealthyProviders(t *testing.T) {
	// Two providers, both wrapped in HealthTrackingProvider and both
	// driven unhealthy via repeated failures so IsHealthy() returns
	// false on the next consult.
	openai := provider.NewHealthTrackingProvider(stubProvider{name: "openai"},
		provider.HealthConfig{FailureThreshold: 1, CooldownPeriod: time.Hour},
		provider.RetryConfig{},
	)
	anthropic := provider.NewHealthTrackingProvider(stubProvider{name: "anthropic"},
		provider.HealthConfig{FailureThreshold: 1, CooldownPeriod: time.Hour},
		provider.RetryConfig{},
	)
	// Trip both circuit breakers.
	openai.Health.RecordFailure(assertableErr("boom"))
	anthropic.Health.RecordFailure(assertableErr("boom"))

	reg := provider.NewRegistry()
	reg.Register(openai, []string{"gpt-4o"})
	reg.Register(anthropic, []string{"claude-sonnet-4-20250514"})

	cfg := config.RoutingConfig{
		DefaultStrategy: "priority",
		ModelGroups: map[string]config.ModelGroupConfig{
			"smart": {
				Candidates: []config.CandidateConfig{
					{Provider: "openai", Model: "gpt-4o"},
					{Provider: "anthropic", Model: "claude-sonnet-4-20250514"},
				},
			},
		},
	}
	rtr, err := router.NewRouter(reg, cfg, providersConfig, buildFixed("priority"))
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: "smart", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	_, _, err = rtr.Route(context.Background(), req, router.RequestMeta{})
	require.ErrorIs(t, err, router.ErrNoHealthyProviders)
}

// assertableErr is a value-typed error for the failure-record path so the
// "boom" string appears in ProviderHealth.lastError without us having to
// wrap a *model.ProviderError.
type assertableErr string

func (e assertableErr) Error() string { return string(e) }
