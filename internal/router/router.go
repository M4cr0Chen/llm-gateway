// Package router decides which provider serves an incoming request. It
// replaces the direct registry.Resolve(req.Model) call inside ChatHandler
// with a strategy-aware Route(ctx, req, meta) that fans a "model group"
// out to a list of candidates, filters by health, and asks the
// configured Strategy to pick one.
//
// This package is the M4.1 surface. Fallback across providers (M4.2) and
// A/B routing (M4.3) plug into the same interfaces without reshaping
// them — RequestMeta.TriedNames and Decision.ExperimentName/Variant are
// the scaffolding for those follow-ups.
package router

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// Router selects a provider for one chat-completion request.
type Router interface {
	// Route resolves the request's model field to a candidate list,
	// filters by health, asks the chosen Strategy to pick one candidate,
	// and returns the picked Provider with a Decision describing why.
	Route(ctx context.Context, req *model.ChatCompletionRequest, meta RequestMeta) (provider.Provider, Decision, error)
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
// request middleware; the Router fills Group right before Strategy.Select.
type RequestMeta struct {
	OrgID      string
	KeyID      string
	Attempt    int      // 0 on the first try; the fallback loop (M4.2) increments this.
	TriedNames []string // provider names already attempted this request; populated by M4.2.
	Group      string   // model-group name; "" for concrete or alias requests. Set by Router.
}

// Decision describes the routing choice that handler middleware stamps
// onto response headers and the access log. ExperimentName/Variant stay
// empty until M4.3.
type Decision struct {
	Provider       string
	Model          string
	Strategy       string
	Group          string
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

	// ErrNoHealthyProviders — every resolved candidate is circuit-broken.
	// Handler returns 503 all_providers_down.
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

	return &defaultRouter{
		registry:        registry,
		groups:          cfg.ModelGroups,
		pricing:         cfg.Pricing,
		defaultStrategy: defaultStrategy,
		groupStrategies: groupStrategies,
		skipWarn:        defaultSkipWarn,
	}, nil
}

// Route is a single-attempt routing in 4.1 — resolve, filter healthy,
// ask the strategy once, return. The fallback loop lands in 4.2; the
// signature does not change there, but Route gains a retry loop inside.
func (r *defaultRouter) Route(ctx context.Context, req *model.ChatCompletionRequest, meta RequestMeta) (provider.Provider, Decision, error) {
	_ = ctx // reserved for the M4.2 fallback loop where ctx.Done() short-circuits retries.

	if req == nil {
		return nil, Decision{}, errors.New("router: nil request")
	}

	group, candidates, err := r.resolveCandidates(req.Model)
	if err != nil {
		return nil, Decision{}, err
	}

	candidates = filterHealthy(candidates)
	if len(candidates) == 0 {
		return nil, Decision{}, ErrNoHealthyProviders
	}

	strategy := r.defaultStrategy
	if group != "" {
		if s, ok := r.groupStrategies[group]; ok {
			strategy = s
		}
	}

	meta.Group = group
	picked, err := strategy.Select(candidates, req, meta)
	if err != nil {
		return nil, Decision{}, fmt.Errorf("router: strategy %q: %w", strategy.Name(), err)
	}

	return picked.Provider, Decision{
		Provider: picked.Provider.Name(),
		Model:    picked.Model,
		Strategy: strategy.Name(),
		Group:    group,
	}, nil
}
