package router

import (
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

	c := Candidate{
		Provider: p,
		Model:    modelName,
		Weight:   1,
	}
	if price, ok := r.pricing[modelName]; ok {
		c.CostPer1kInputUSD = price.CostPer1kInputUSD
		c.CostPer1kOutputUSD = price.CostPer1kOutputUSD
	}
	return "", []Candidate{c}, nil
}

// expandGroup turns a model_groups entry into a candidate list, in the
// order written in YAML so Priority's tie-break (config order) stays
// stable. Candidates whose Model cannot be resolved by the registry are
// silently skipped — that case only happens when a provider was
// configured but later had its API key blanked, since startup
// validation rejected groups whose Provider field is not in
// cfg.Providers.
func (r *defaultRouter) expandGroup(g config.ModelGroupConfig) []Candidate {
	out := make([]Candidate, 0, len(g.Candidates))
	for _, c := range g.Candidates {
		p, err := r.registry.Resolve(c.Model)
		if err != nil {
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
