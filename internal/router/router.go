// Package router decides which provider serves an incoming request and
// runs the inter-provider fallback loop. It replaces the direct
// registry.Resolve(req.Model) call inside ChatHandler with a
// strategy-aware Route/RouteStream that fans a "model group" out to a
// list of candidates, filters by health, asks the configured Strategy
// to pick one, and retries the **next** candidate when the chosen
// provider returns a retryable error.
//
// Layering rule (ADR-006 / docs/routing-strategies.md):
//
//   - Per-target retry with backoff lives inside
//     provider.HealthTrackingProvider (M2). The Router does not retry
//     the same target.
//   - The Router only switches **between** providers, bounded by
//     routing.max_attempts.
//
// The circuit breaker is the existing 3-state provider.ProviderHealth —
// the Router consults IsHealthy() to filter candidates before each
// attempt, so a breaker that trips mid-request is honoured immediately.
//
// A/B routing (M4.3) plugs into the same interfaces:
// RequestMeta.TriedNames and Decision.ExperimentName/Variant are the
// scaffolding for that follow-up.
package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/metrics"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// Router selects a provider for one chat-completion request and runs
// the fallback loop. Implementations own the actual ChatCompletion /
// ChatCompletionStream call so they can switch providers on retryable
// failures without leaking attempt state to the caller.
type Router interface {
	// Route resolves the request's model field, picks a provider via
	// the chosen Strategy, calls ChatCompletion, and falls back to the
	// next candidate on retryable errors up to routing.max_attempts.
	// Returns the final response (from whichever attempt succeeded)
	// and a Decision describing the path taken.
	Route(ctx context.Context, req *model.ChatCompletionRequest, meta RequestMeta) (*model.ChatCompletionResponse, Decision, error)

	// RouteStream is the streaming variant. It can fall back **before
	// the first byte** is established (i.e., before
	// ChatCompletionStream returns a channel); once a channel is
	// returned, the Router exits — mid-stream errors propagate via the
	// channel to the client and are not retried, matching the existing
	// HealthTrackingProvider contract.
	RouteStream(ctx context.Context, req *model.ChatCompletionRequest, meta RequestMeta) (<-chan provider.StreamEvent, Decision, error)
}

// Strategy ranks a candidate list and returns the candidate to try.
// Strategies MUST be safe for concurrent use; internal counters or RNGs
// they own (round-robin, weighted) must be guarded by atomics or mutexes.
//
// The interface lives in the router package so the concrete *router
// implementation can hold strategies as fields without an import cycle
// (the strategy/ subpackage imports this package for Candidate and
// RequestMeta).
type Strategy interface {
	Name() string
	Select(candidates []Candidate, req *model.ChatCompletionRequest, meta RequestMeta) (Candidate, error)
}

// RequestMeta carries per-request context the strategy may need beyond
// the OpenAI request body. The handler builds it from the auth and
// request middleware; the Router fills Group right before Strategy.Select
// and increments Attempt + appends to TriedNames per fallback hop.
type RequestMeta struct {
	OrgID      string
	KeyID      string
	Attempt    int      // 0 on the first try; the fallback loop increments this to attempt+1 before calling the provider.
	TriedNames []string // provider names already attempted this request.
	Group      string   // model-group name; "" for concrete or alias requests. Set by Router.
}

// Decision describes the routing choice that handler middleware stamps
// onto response headers and the access log. ExperimentName/Variant stay
// empty until M4.3.
//
// Attempts is filled on every return path:
//   - 1 on first-try success.
//   - N when N-th attempt finally succeeded.
//   - On terminal failure: the number of attempts made (≤ maxAttempts).
//   - On ErrNoHealthyProviders: 0 (no candidate was ever tried).
type Decision struct {
	Provider       string
	Model          string
	Strategy       string
	Group          string
	Attempts       int
	ExperimentName string
	Variant        string
}

// Candidate is one (provider, model) pair with the metadata that
// strategies rank by. CostPer1k* of zero means "no cost data" —
// CostOptimized deprioritises such entries instead of preferring them.
type Candidate struct {
	Provider           provider.Provider
	Model              string
	CostPer1kInputUSD  float64
	CostPer1kOutputUSD float64
	Priority           int
	Weight             int
}

// Typed errors the handler maps to specific HTTP status codes.
var (
	// ErrUnknownModel — the request's model field matches no group,
	// concrete model, or alias. Handler returns 400 invalid_model.
	ErrUnknownModel = errors.New("unknown model")

	// ErrNoHealthyProviders — every resolved candidate is circuit-broken
	// at the start of routing. Handler returns 503 all_providers_down.
	ErrNoHealthyProviders = errors.New("no healthy providers")
)

// defaultRouter is the package-private Router implementation. It is
// constructed once at startup with a fully-validated strategy set so
// Route is allocation-light on the hot path.
type defaultRouter struct {
	registry        *provider.Registry
	groups          map[string]config.ModelGroupConfig
	pricing         map[string]config.CandidatePricing
	defaultStrategy Strategy
	groupStrategies map[string]Strategy // per-group override; nil entry → use default
	maxAttempts     int

	skipWarnOnce sync.Map // map[string]*sync.Once — keyed by "provider\x00model"
	skipWarn     func(providerName, modelName string)
}

// NewRouter validates the routing config against the registry and builds
// a *router. It returns an error (so main.go can fail startup) when:
//   - DefaultStrategy names a strategy that doesn't exist
//   - a per-group strategy override names a strategy that doesn't exist
//   - a model_group candidate references a provider name not present in
//     cfg.Providers (we validate against the config rather than the
//     registry because some providers may legitimately have no API key
//     wired in and be skipped at registration — that case still warrants
//     a fail-fast)
//
// build must produce a Strategy for each of the five known names; it is
// passed in (instead of imported) so this package doesn't depend on
// internal/router/strategy and the dependency graph stays flat.
func NewRouter(
	registry *provider.Registry,
	cfg config.RoutingConfig,
	providers map[string]config.ProviderConfig,
	build func(name string) (Strategy, error),
) (Router, error) {
	if registry == nil {
		return nil, errors.New("router: registry is required")
	}
	if build == nil {
		return nil, errors.New("router: strategy builder is required")
	}

	defaultStrategy, err := build(cfg.DefaultStrategy)
	if err != nil {
		return nil, fmt.Errorf("router: %w", err)
	}

	groupStrategies := make(map[string]Strategy, len(cfg.ModelGroups))
	for groupName, g := range cfg.ModelGroups {
		for i, c := range g.Candidates {
			if c.Provider == "" {
				return nil, fmt.Errorf("router: model_groups[%q].candidates[%d]: provider is empty", groupName, i)
			}
			if c.Model == "" {
				return nil, fmt.Errorf("router: model_groups[%q].candidates[%d]: model is empty", groupName, i)
			}
			if _, ok := providers[c.Provider]; !ok {
				return nil, fmt.Errorf("router: model_groups[%q].candidates[%d]: provider %q is not configured", groupName, i, c.Provider)
			}
		}
		if g.Strategy != "" {
			s, err := build(g.Strategy)
			if err != nil {
				return nil, fmt.Errorf("router: model_groups[%q]: %w", groupName, err)
			}
			groupStrategies[groupName] = s
		}
	}

	maxAttempts := cfg.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	return &defaultRouter{
		registry:        registry,
		groups:          cfg.ModelGroups,
		pricing:         cfg.Pricing,
		defaultStrategy: defaultStrategy,
		groupStrategies: groupStrategies,
		maxAttempts:     maxAttempts,
		skipWarn:        defaultSkipWarn,
	}, nil
}

// prepare runs the candidate resolution + strategy selection that is
// shared between Route and RouteStream. The caller then drives its own
// attempt loop around the resolved (strategy, candidates, group).
func (r *defaultRouter) prepare(req *model.ChatCompletionRequest) (strategy Strategy, candidates []Candidate, group string, err error) {
	if req == nil {
		return nil, nil, "", errors.New("router: nil request")
	}
	group, candidates, err = r.resolveCandidates(req.Model)
	if err != nil {
		return nil, nil, "", err
	}
	strategy = r.defaultStrategy
	if group != "" {
		if s, ok := r.groupStrategies[group]; ok {
			strategy = s
		}
	}
	return strategy, candidates, group, nil
}

// Route runs the fallback loop for non-streaming chat completions.
// The loop is bounded by routing.max_attempts (default 3); each
// iteration consults ProviderHealth.IsHealthy() so a breaker that
// trips mid-request is honoured immediately.
func (r *defaultRouter) Route(ctx context.Context, req *model.ChatCompletionRequest, meta RequestMeta) (*model.ChatCompletionResponse, Decision, error) {
	strategy, candidates, group, err := r.prepare(req)
	if err != nil {
		return nil, Decision{}, err
	}
	meta.Group = group

	dec := Decision{Strategy: strategy.Name(), Group: group}
	tried := map[string]struct{}{}
	var (
		lastErr  error
		lastPick Candidate
		attempt  int
	)

	for attempt = 0; attempt < r.maxAttempts; attempt++ {
		healthy := filterHealthy(candidates, tried)
		if len(healthy) == 0 {
			if attempt == 0 {
				metrics.RecordRouterNoHealthy(group)
				return nil, dec, ErrNoHealthyProviders
			}
			break
		}
		pick, sErr := strategy.Select(healthy, req, meta)
		if sErr != nil {
			return nil, dec, fmt.Errorf("router: strategy %q: %w", strategy.Name(), sErr)
		}
		meta.Attempt = attempt + 1
		req.Model = pick.Model

		resp, callErr := pick.Provider.ChatCompletion(ctx, req)
		if callErr == nil {
			dec.Provider = pick.Provider.Name()
			dec.Model = pick.Model
			dec.Attempts = meta.Attempt
			outcome := "primary"
			if attempt > 0 {
				outcome = "fallback"
			}
			metrics.RecordRouterDecision(strategy.Name(), dec.Provider, group, outcome)
			return resp, dec, nil
		}

		lastErr = callErr
		lastPick = pick

		if !isRouterFallbackable(ctx, callErr) {
			dec.Provider = pick.Provider.Name()
			dec.Model = pick.Model
			dec.Attempts = meta.Attempt
			metrics.RecordRouterDecision(strategy.Name(), dec.Provider, group, "error")
			return nil, dec, callErr
		}

		tried[pick.Provider.Name()] = struct{}{}
		meta.TriedNames = append(meta.TriedNames, pick.Provider.Name())

		var toName string
		if attempt+1 < r.maxAttempts {
			if nextHealthy := filterHealthy(candidates, tried); len(nextHealthy) > 0 {
				toName = nextHealthy[0].Provider.Name()
			}
		}
		metrics.RecordRouterFallback(pick.Provider.Name(), toName, reasonFor(callErr))
	}

	// Loop exhausted (max_attempts reached) OR ran out of healthy
	// candidates mid-flight. attempt is the number of attempts made.
	dec.Provider = lastPick.Provider.Name()
	dec.Model = lastPick.Model
	dec.Attempts = attempt
	metrics.RecordRouterDecision(strategy.Name(), dec.Provider, group, "error")
	return nil, dec, lastErr
}

// RouteStream is the streaming-aware variant of Route. The fallback
// predicate and metric calls mirror Route exactly; the only structural
// difference is that the per-attempt call returns a channel instead of
// a response, and once that channel is established no further fallback
// happens (the mid-stream-error contract belongs to the decorator).
func (r *defaultRouter) RouteStream(ctx context.Context, req *model.ChatCompletionRequest, meta RequestMeta) (<-chan provider.StreamEvent, Decision, error) {
	strategy, candidates, group, err := r.prepare(req)
	if err != nil {
		return nil, Decision{}, err
	}
	meta.Group = group

	dec := Decision{Strategy: strategy.Name(), Group: group}
	tried := map[string]struct{}{}
	var (
		lastErr  error
		lastPick Candidate
		attempt  int
	)

	for attempt = 0; attempt < r.maxAttempts; attempt++ {
		healthy := filterHealthy(candidates, tried)
		if len(healthy) == 0 {
			if attempt == 0 {
				metrics.RecordRouterNoHealthy(group)
				return nil, dec, ErrNoHealthyProviders
			}
			break
		}
		pick, sErr := strategy.Select(healthy, req, meta)
		if sErr != nil {
			return nil, dec, fmt.Errorf("router: strategy %q: %w", strategy.Name(), sErr)
		}
		meta.Attempt = attempt + 1
		req.Model = pick.Model

		ch, callErr := pick.Provider.ChatCompletionStream(ctx, req)
		if callErr == nil {
			dec.Provider = pick.Provider.Name()
			dec.Model = pick.Model
			dec.Attempts = meta.Attempt
			outcome := "primary"
			if attempt > 0 {
				outcome = "fallback"
			}
			metrics.RecordRouterDecision(strategy.Name(), dec.Provider, group, outcome)
			return ch, dec, nil
		}

		lastErr = callErr
		lastPick = pick

		if !isRouterFallbackable(ctx, callErr) {
			dec.Provider = pick.Provider.Name()
			dec.Model = pick.Model
			dec.Attempts = meta.Attempt
			metrics.RecordRouterDecision(strategy.Name(), dec.Provider, group, "error")
			return nil, dec, callErr
		}

		tried[pick.Provider.Name()] = struct{}{}
		meta.TriedNames = append(meta.TriedNames, pick.Provider.Name())

		var toName string
		if attempt+1 < r.maxAttempts {
			if nextHealthy := filterHealthy(candidates, tried); len(nextHealthy) > 0 {
				toName = nextHealthy[0].Provider.Name()
			}
		}
		metrics.RecordRouterFallback(pick.Provider.Name(), toName, reasonFor(callErr))
	}

	dec.Provider = lastPick.Provider.Name()
	dec.Model = lastPick.Model
	dec.Attempts = attempt
	metrics.RecordRouterDecision(strategy.Name(), dec.Provider, group, "error")
	return nil, dec, lastErr
}

// isRouterFallbackable decides whether the Router should switch to the
// next candidate after err. It is the cross-provider analogue of
// HealthTrackingProvider.isRetryable, defined separately so the Router's
// fallback predicate can have its own (slightly broader) rules — namely,
// the ctx-aware DeadlineExceeded defensive check.
//
// Rules:
//   - *model.ProviderError → pe.Retryable (true for 429 / 5xx).
//   - context.Canceled → false (the client cancelled; falling back wastes upstream capacity).
//   - context.DeadlineExceeded → only true when the parent ctx is still
//     live. The decorator wraps every network error into a ProviderError
//     before returning, so reaching this branch implies the timeout
//     fired before any HTTP response — defensive, but cheap.
//   - All other errors → false (don't fall back on errors we don't recognise).
func isRouterFallbackable(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ctx.Err() == nil
	}
	return false
}

// reasonFor maps the failed attempt's error into a low-cardinality label
// for the fallback counter. Unknown shapes bucket into "network" so the
// label set stays the three documented values.
func reasonFor(err error) string {
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		switch {
		case pe.StatusCode == http.StatusTooManyRequests:
			return "429"
		case pe.StatusCode >= 500:
			return "5xx"
		}
	}
	return "network"
}
