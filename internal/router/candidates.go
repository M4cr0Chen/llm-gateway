package router

import (
	"log/slog"
	"sync"

	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// resolveCandidates expands the incoming model field into the candidate
// list that the strategy will rank.
//
// Resolution order:
//  1. Model groups (routing.model_groups[name]) — fan out to every
//     candidate the group defines, in config order. Returns the group
//     name so the Router can apply a per-group strategy override and
//     stamp X-LLM-Gateway-Group.
//  2. Concrete model name or alias — the registry resolves it to a
//     single provider; we wrap that in a one-element candidate list with
//     pricing pulled from routing.pricing when present, else zero. The
//     returned group name is empty for this branch.
//
// A name that matches neither path is ErrUnknownModel.
func (r *defaultRouter) resolveCandidates(modelName string) (string, []Candidate, error) {
	if group, ok := r.groups[modelName]; ok {
		return modelName, r.expandGroup(group), nil
	}

	p, err := r.registry.Resolve(modelName)
	if err != nil {
		return "", nil, ErrUnknownModel
	}

	// Use the canonical name for the candidate so the handler can rewrite
	// req.Model before calling upstream — provider APIs do not recognise
	// our local alias forms. For non-aliases this is a no-op.
	canonical := r.registry.CanonicalModel(modelName)
	c := Candidate{
		Provider: p,
		Model:    canonical,
		Weight:   1,
	}
	if price, ok := r.pricing[canonical]; ok {
		c.CostPer1kInputUSD = price.CostPer1kInputUSD
		c.CostPer1kOutputUSD = price.CostPer1kOutputUSD
	}
	return "", []Candidate{c}, nil
}

// expandGroup turns a model_groups entry into a candidate list, in the
// order written in YAML so Priority's tie-break (config order) stays
// stable. Candidates whose Model cannot be resolved by the registry are
// skipped with a one-shot WARN per (provider, model) pair — that case
// usually means a provider was configured in YAML but its API key was
// blanked at startup, so without the warning the request would surface
// as a misleading 503 all_providers_down.
func (r *defaultRouter) expandGroup(g config.ModelGroupConfig) []Candidate {
	out := make([]Candidate, 0, len(g.Candidates))
	for _, c := range g.Candidates {
		p, err := r.registry.Resolve(c.Model)
		if err != nil {
			r.warnSkipOnce(c.Provider, c.Model)
			continue
		}
		out = append(out, Candidate{
			Provider:           p,
			Model:              c.Model,
			CostPer1kInputUSD:  c.CostPer1kInputUSD,
			CostPer1kOutputUSD: c.CostPer1kOutputUSD,
			Priority:           c.Priority,
			Weight:             c.Weight,
		})
	}
	return out
}

// warnSkipOnce emits exactly one WARN for a given (provider, model)
// pair across the process lifetime. The skip path is observability-only
// — we don't want a hot request to spam logs once an operator has seen
// the issue, but the first occurrence has to be visible.
func (r *defaultRouter) warnSkipOnce(providerName, modelName string) {
	key := providerName + "\x00" + modelName
	v, ok := r.skipWarnOnce.Load(key)
	if !ok {
		v, _ = r.skipWarnOnce.LoadOrStore(key, &sync.Once{})
	}
	v.(*sync.Once).Do(func() {
		r.skipWarn(providerName, modelName)
	})
}

// defaultSkipWarn is the production WARN sink for warnSkipOnce. Tests
// swap it via the unexported skipWarn field to capture the calls
// without scraping slog output.
func defaultSkipWarn(providerName, modelName string) {
	slog.Warn("router: group candidate skipped — model not registered (provider unconfigured or API key missing?)",
		"provider", providerName,
		"model", modelName,
	)
}

// filterHealthy drops candidates whose provider's circuit breaker is
// open. Providers that are not health-tracked (HealthFor returns nil)
// are passed through — that case is primarily tests using bare mocks,
// where assuming "healthy" is the right default.
//
// The result reuses the input slice's underlying array to avoid an
// allocation on the hot path. Callers must not retain the input slice
// after filterHealthy returns.
func filterHealthy(candidates []Candidate) []Candidate {
	out := candidates[:0]
	for _, c := range candidates {
		h := provider.HealthFor(c.Provider)
		if h == nil || h.IsHealthy() {
			out = append(out, c)
		}
	}
	return out
}
