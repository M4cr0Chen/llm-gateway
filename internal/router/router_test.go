package router_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
	_, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Equal(t, "openai", dec.Provider)
	assert.Equal(t, "gpt-4o", dec.Model)
	assert.Equal(t, "priority", dec.Strategy)
	assert.Equal(t, "", dec.Group, "concrete model requests carry no group")
	assert.Equal(t, 1, dec.Attempts, "first-try success records Attempts=1")
}

func TestRoute_Alias(t *testing.T) {
	reg := newRegistry(t, map[string]string{"claude-sonnet-4-20250514": "anthropic"})
	require.NoError(t, reg.RegisterAlias("claude", "claude-sonnet-4-20250514"))

	rtr, err := router.NewRouter(reg, config.RoutingConfig{DefaultStrategy: "priority"}, providersConfig, buildFixed("priority"))
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: "claude", Messages: []model.Message{{Role: "user", Content: "hi"}}}
	_, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Equal(t, "anthropic", dec.Provider)
	assert.Equal(t, "claude-sonnet-4-20250514", dec.Model, "alias resolves to the canonical model name so handler can rewrite req.Model before calling upstream")
	assert.Equal(t, "claude-sonnet-4-20250514", req.Model, "Router rewrites req.Model to canonical so the upstream call uses a name the provider API recognises")
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
	_, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Equal(t, "openai", dec.Provider)
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

// counterValueByLabels reads the named CounterVec's value for the
// given labels off prometheus.DefaultGatherer. Returns 0 for an absent
// series, matching testutil.ToFloat64 semantics for brand-new label
// combinations. The router_test package can't reach the unexported
// CounterVecs in internal/metrics directly, so the gatherer is the
// cleanest abstraction the standard testing surface offers.
func counterValueByLabels(t *testing.T, name string, labels map[string]string) float64 {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			actual := map[string]string{}
			for _, lp := range m.GetLabel() {
				actual[lp.GetName()] = lp.GetValue()
			}
			if len(actual) != len(labels) {
				continue
			}
			matched := true
			for k, v := range labels {
				if actual[k] != v {
					matched = false
					break
				}
			}
			if matched {
				return m.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// scriptedTracked wraps p in a HealthTrackingProvider with MaxRetries=0
// (no in-decorator retry — the Router's fallback is what we're testing)
// and a high failure threshold so a single failure doesn't trip the
// breaker. The returned HealthTrackingProvider's *Health is exposed so
// callers that want to seed unhealthy state can do so.
func scriptedTracked(p provider.Provider, cfg provider.HealthConfig) *provider.HealthTrackingProvider {
	if cfg.FailureThreshold == 0 {
		cfg.FailureThreshold = 5
	}
	return provider.NewHealthTrackingProvider(p, cfg, provider.RetryConfig{MaxRetries: 0})
}

// fallbackGroup builds a config.RoutingConfig with a single model-group
// "<prefix>-group" containing two candidates with distinct model names
// — distinct because Registry maps each model name to exactly one
// provider, so co-registering two providers under the same name would
// silently overwrite the first. Priority 0 = primary, priority 1 =
// secondary (the fallback target under the fixed-strategy used here).
func fallbackGroup(prefix, primary, primaryModel, secondary, secondaryModel string, maxAttempts int) config.RoutingConfig {
	return config.RoutingConfig{
		DefaultStrategy: "priority",
		MaxAttempts:     maxAttempts,
		ModelGroups: map[string]config.ModelGroupConfig{
			prefix + "-group": {
				Candidates: []config.CandidateConfig{
					{Provider: primary, Model: primaryModel, Priority: 0, Weight: 1},
					{Provider: secondary, Model: secondaryModel, Priority: 1, Weight: 1},
				},
			},
		},
	}
}

func TestRoute_FallbackOnRetryableError(t *testing.T) {
	const prefix = "ft-fallback"
	primaryName, secondaryName := prefix+"-primary", prefix+"-secondary"
	primaryModel, secondaryModel := prefix+"-pmodel", prefix+"-smodel"
	groupName := prefix + "-group"

	wantResp := &model.ChatCompletionResponse{
		ID: "chatcmpl-fallback", Model: secondaryModel,
		Choices: []model.Choice{{Index: 0, Message: model.Message{Role: "assistant", Content: "ok"}}},
	}
	primary := &scriptedProvider{name: primaryName, chatErr: &model.ProviderError{
		StatusCode: http.StatusServiceUnavailable, Type: "upstream_error", Message: "503 boom", Retryable: true,
	}}
	secondary := &scriptedProvider{name: secondaryName, resp: wantResp}

	primaryTracked := scriptedTracked(primary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour})
	secondaryTracked := scriptedTracked(secondary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour})

	reg := provider.NewRegistry()
	reg.Register(primaryTracked, []string{primaryModel})
	reg.Register(secondaryTracked, []string{secondaryModel})

	providersCfg := map[string]config.ProviderConfig{primaryName: {}, secondaryName: {}}
	rtr, err := router.NewRouter(reg, fallbackGroup(prefix, primaryName, primaryModel, secondaryName, secondaryModel, 3), providersCfg, buildFixed("priority"))
	require.NoError(t, err)

	beforeFallback := counterValueByLabels(t, "gateway_router_fallback_total",
		map[string]string{"from_provider": primaryName, "to_provider": secondaryName, "reason": "5xx"})
	beforeDecision := counterValueByLabels(t, "gateway_router_decisions_total",
		map[string]string{"strategy": "priority", "provider": secondaryName, "group": groupName, "outcome": "fallback"})

	req := &model.ChatCompletionRequest{Model: groupName, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	resp, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Same(t, wantResp, resp)
	assert.Equal(t, 2, dec.Attempts, "fallback succeeded on attempt 2")
	assert.Equal(t, secondaryName, dec.Provider)
	assert.Equal(t, secondaryModel, dec.Model)
	assert.Equal(t, groupName, dec.Group)
	assert.Equal(t, int32(1), primary.callCount.Load())
	assert.Equal(t, int32(1), secondary.callCount.Load())

	afterFallback := counterValueByLabels(t, "gateway_router_fallback_total",
		map[string]string{"from_provider": primaryName, "to_provider": secondaryName, "reason": "5xx"})
	afterDecision := counterValueByLabels(t, "gateway_router_decisions_total",
		map[string]string{"strategy": "priority", "provider": secondaryName, "group": groupName, "outcome": "fallback"})
	assert.InDelta(t, beforeFallback+1, afterFallback, 0.0001)
	assert.InDelta(t, beforeDecision+1, afterDecision, 0.0001)
}

func TestRoute_AllProvidersFailReturnsTerminalError(t *testing.T) {
	const prefix = "ft-allfail"
	primaryName, secondaryName := prefix+"-primary", prefix+"-secondary"
	primaryModel, secondaryModel := prefix+"-pmodel", prefix+"-smodel"
	groupName := prefix + "-group"

	primaryErr := &model.ProviderError{StatusCode: 503, Type: "upstream_error", Message: "p down", Retryable: true}
	secondaryErr := &model.ProviderError{StatusCode: 503, Type: "upstream_error", Message: "s down", Retryable: true}

	primary := &scriptedProvider{name: primaryName, chatErr: primaryErr}
	secondary := &scriptedProvider{name: secondaryName, chatErr: secondaryErr}

	reg := provider.NewRegistry()
	reg.Register(scriptedTracked(primary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour}), []string{primaryModel})
	reg.Register(scriptedTracked(secondary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour}), []string{secondaryModel})

	providersCfg := map[string]config.ProviderConfig{primaryName: {}, secondaryName: {}}
	rtr, err := router.NewRouter(reg, fallbackGroup(prefix, primaryName, primaryModel, secondaryName, secondaryModel, 3), providersCfg, buildFixed("priority"))
	require.NoError(t, err)

	beforeError := counterValueByLabels(t, "gateway_router_decisions_total",
		map[string]string{"strategy": "priority", "provider": secondaryName, "group": groupName, "outcome": "error"})

	req := &model.ChatCompletionRequest{Model: groupName, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	resp, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, 2, dec.Attempts, "attempts == providers tried (2), not max_attempts (3)")
	assert.Equal(t, secondaryName, dec.Provider, "Decision reflects the last attempted provider")
	// Underlying error is the LAST attempt's error, surfaced for handler inspection.
	var pe *model.ProviderError
	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "s down", pe.Message)

	afterError := counterValueByLabels(t, "gateway_router_decisions_total",
		map[string]string{"strategy": "priority", "provider": secondaryName, "group": groupName, "outcome": "error"})
	assert.InDelta(t, beforeError+1, afterError, 0.0001)
}

func TestRoute_NonRetryableErrorDoesNotFallback(t *testing.T) {
	const prefix = "ft-nonretry"
	primaryName, secondaryName := prefix+"-primary", prefix+"-secondary"
	primaryModel, secondaryModel := prefix+"-pmodel", prefix+"-smodel"

	primaryErr := &model.ProviderError{StatusCode: 400, Type: "invalid_request_error", Message: "bad", Retryable: false}
	primary := &scriptedProvider{name: primaryName, chatErr: primaryErr}
	secondary := &scriptedProvider{name: secondaryName, resp: &model.ChatCompletionResponse{}}

	reg := provider.NewRegistry()
	reg.Register(scriptedTracked(primary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour}), []string{primaryModel})
	reg.Register(scriptedTracked(secondary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour}), []string{secondaryModel})

	providersCfg := map[string]config.ProviderConfig{primaryName: {}, secondaryName: {}}
	rtr, err := router.NewRouter(reg, fallbackGroup(prefix, primaryName, primaryModel, secondaryName, secondaryModel, 3), providersCfg, buildFixed("priority"))
	require.NoError(t, err)
	groupName := prefix + "-group"

	beforeFallback := counterValueByLabels(t, "gateway_router_fallback_total",
		map[string]string{"from_provider": primaryName, "to_provider": secondaryName, "reason": "5xx"})

	req := &model.ChatCompletionRequest{Model: groupName, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	resp, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, 1, dec.Attempts, "non-retryable error must not fall back")
	assert.Equal(t, int32(1), primary.callCount.Load())
	assert.Equal(t, int32(0), secondary.callCount.Load(), "secondary must not be called for a 4xx primary")
	var pe *model.ProviderError
	require.ErrorAs(t, err, &pe)
	assert.False(t, pe.Retryable)

	afterFallback := counterValueByLabels(t, "gateway_router_fallback_total",
		map[string]string{"from_provider": primaryName, "to_provider": secondaryName, "reason": "5xx"})
	assert.InDelta(t, beforeFallback, afterFallback, 0.0001, "no fallback counter increment on non-retryable error")
}

func TestRoute_AllCircuitBrokenAtStart(t *testing.T) {
	const prefix = "ft-broken"
	primaryName, secondaryName := prefix+"-primary", prefix+"-secondary"
	primaryModel, secondaryModel := prefix+"-pmodel", prefix+"-smodel"
	groupName := prefix + "-group"

	primary := &scriptedProvider{name: primaryName, resp: &model.ChatCompletionResponse{}}
	secondary := &scriptedProvider{name: secondaryName, resp: &model.ChatCompletionResponse{}}

	primaryTracked := scriptedTracked(primary, provider.HealthConfig{FailureThreshold: 1, CooldownPeriod: time.Hour})
	secondaryTracked := scriptedTracked(secondary, provider.HealthConfig{FailureThreshold: 1, CooldownPeriod: time.Hour})

	// Trip both breakers before the request runs.
	primaryTracked.Health.RecordFailure(errors.New("preboot"))
	secondaryTracked.Health.RecordFailure(errors.New("preboot"))

	reg := provider.NewRegistry()
	reg.Register(primaryTracked, []string{primaryModel})
	reg.Register(secondaryTracked, []string{secondaryModel})

	providersCfg := map[string]config.ProviderConfig{primaryName: {}, secondaryName: {}}
	rtr, err := router.NewRouter(reg, fallbackGroup(prefix, primaryName, primaryModel, secondaryName, secondaryModel, 3), providersCfg, buildFixed("priority"))
	require.NoError(t, err)

	beforeNoHealthy := counterValueByLabels(t, "gateway_router_no_healthy_providers_total", map[string]string{"group": groupName})

	req := &model.ChatCompletionRequest{Model: groupName, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	resp, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.ErrorIs(t, err, router.ErrNoHealthyProviders)
	assert.Nil(t, resp)
	assert.Equal(t, 0, dec.Attempts)
	assert.Equal(t, int32(0), primary.callCount.Load(), "open breaker means we never even call the wrapped provider")
	assert.Equal(t, int32(0), secondary.callCount.Load())

	afterNoHealthy := counterValueByLabels(t, "gateway_router_no_healthy_providers_total", map[string]string{"group": groupName})
	assert.InDelta(t, beforeNoHealthy+1, afterNoHealthy, 0.0001)
}

func TestRoute_HalfOpenRecovery(t *testing.T) {
	const prefix = "ft-halfopen"
	primaryName, secondaryName := prefix+"-primary", prefix+"-secondary"
	primaryModel, secondaryModel := prefix+"-pmodel", prefix+"-smodel"
	groupName := prefix + "-group"

	wantResp := &model.ChatCompletionResponse{ID: "chatcmpl-halfopen", Model: primaryModel}
	primary := &scriptedProvider{name: primaryName, resp: wantResp}
	secondary := &scriptedProvider{name: secondaryName, resp: &model.ChatCompletionResponse{}}

	// CooldownPeriod = 1ns simulates a fully-elapsed cooldown without
	// depending on real time. (CooldownPeriod = 0 falls back to the
	// 30s default in NewProviderHealth, which would keep the breaker
	// open for the duration of the test.) A nanosecond is below the
	// resolution of any real clock advance between RecordFailure and
	// IsHealthy, so the half-open check passes on the first call.
	primaryTracked := scriptedTracked(primary, provider.HealthConfig{FailureThreshold: 1, CooldownPeriod: time.Nanosecond})
	secondaryTracked := scriptedTracked(secondary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour})

	primaryTracked.Health.RecordFailure(errors.New("prior"))
	require.False(t, primaryTracked.Health.HealthyStrict(), "primary starts strictly unhealthy")
	require.True(t, primaryTracked.Health.IsHealthy(), "cooldown elapsed → half-open allows a probe")

	reg := provider.NewRegistry()
	reg.Register(primaryTracked, []string{primaryModel})
	reg.Register(secondaryTracked, []string{secondaryModel})

	providersCfg := map[string]config.ProviderConfig{primaryName: {}, secondaryName: {}}
	rtr, err := router.NewRouter(reg, fallbackGroup(prefix, primaryName, primaryModel, secondaryName, secondaryModel, 3), providersCfg, buildFixed("priority"))
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: groupName, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	resp, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	assert.Same(t, wantResp, resp, "half-open probe succeeded; response is from primary")
	assert.Equal(t, 1, dec.Attempts)
	assert.Equal(t, primaryName, dec.Provider)
	assert.True(t, primaryTracked.Health.HealthyStrict(), "successful probe closes the breaker via RecordSuccess in the decorator")
}

func TestRoute_MaxAttemptsOne(t *testing.T) {
	const prefix = "ft-maxone"
	primaryName, secondaryName := prefix+"-primary", prefix+"-secondary"
	primaryModel, secondaryModel := prefix+"-pmodel", prefix+"-smodel"
	groupName := prefix + "-group"

	primaryErr := &model.ProviderError{StatusCode: 503, Type: "upstream_error", Message: "p down", Retryable: true}
	primary := &scriptedProvider{name: primaryName, chatErr: primaryErr}
	secondary := &scriptedProvider{name: secondaryName, resp: &model.ChatCompletionResponse{}}

	reg := provider.NewRegistry()
	reg.Register(scriptedTracked(primary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour}), []string{primaryModel})
	reg.Register(scriptedTracked(secondary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour}), []string{secondaryModel})

	providersCfg := map[string]config.ProviderConfig{primaryName: {}, secondaryName: {}}
	rtr, err := router.NewRouter(reg, fallbackGroup(prefix, primaryName, primaryModel, secondaryName, secondaryModel, 1), providersCfg, buildFixed("priority"))
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: groupName, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	resp, dec, err := rtr.Route(context.Background(), req, router.RequestMeta{})
	require.Error(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, 1, dec.Attempts, "max_attempts=1 forbids any fallback")
	assert.Equal(t, int32(1), primary.callCount.Load())
	assert.Equal(t, int32(0), secondary.callCount.Load(), "max_attempts=1 stops the loop after the first failure")
}

func TestRouteStream_FallbackBeforeFirstByte(t *testing.T) {
	const prefix = "ft-stream"
	primaryName, secondaryName := prefix+"-primary", prefix+"-secondary"
	primaryModel, secondaryModel := prefix+"-pmodel", prefix+"-smodel"
	groupName := prefix + "-group"

	primaryErr := &model.ProviderError{StatusCode: 503, Type: "upstream_error", Message: "stream down", Retryable: true}
	chunks := []model.ChatCompletionChunk{{ID: "chunk-1", Model: secondaryModel}}
	primary := &scriptedProvider{name: primaryName, streamErr: primaryErr}
	secondary := &scriptedProvider{name: secondaryName, chunks: chunks}

	reg := provider.NewRegistry()
	reg.Register(scriptedTracked(primary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour}), []string{primaryModel})
	reg.Register(scriptedTracked(secondary, provider.HealthConfig{FailureThreshold: 10, CooldownPeriod: time.Hour}), []string{secondaryModel})

	providersCfg := map[string]config.ProviderConfig{primaryName: {}, secondaryName: {}}
	rtr, err := router.NewRouter(reg, fallbackGroup(prefix, primaryName, primaryModel, secondaryName, secondaryModel, 3), providersCfg, buildFixed("priority"))
	require.NoError(t, err)

	req := &model.ChatCompletionRequest{Model: groupName, Stream: true, Messages: []model.Message{{Role: "user", Content: "hi"}}}
	ch, dec, err := rtr.RouteStream(context.Background(), req, router.RequestMeta{})
	require.NoError(t, err)
	require.NotNil(t, ch)
	assert.Equal(t, 2, dec.Attempts)
	assert.Equal(t, secondaryName, dec.Provider)

	got := []string{}
	for evt := range ch {
		require.NoError(t, evt.Err)
		got = append(got, evt.Chunk.ID)
	}
	require.Len(t, got, 1)
	assert.Equal(t, "chunk-1", got[0])
	assert.Equal(t, int32(1), primary.callCount.Load())
	assert.Equal(t, int32(1), secondary.callCount.Load())
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
