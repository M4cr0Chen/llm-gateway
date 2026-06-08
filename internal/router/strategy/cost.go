package strategy

import (
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// defaultMaxTokens is the assumed output length when req.MaxTokens is
// unset. Picked to match the OpenAI chat-completions default in our
// own contract; 256 is a reasonable upper bound for "short reply" use
// cases and produces a fair ranking between providers.
const defaultMaxTokens = 256

// CostOptimized picks the candidate with the lowest estimated USD cost
// for the request. Cost is `inTokens·costIn + outTokens·costOut` where
// inTokens ≈ sum(len(message.Content))/4 (a cheap upper bound; tiktoken
// arrives in M6) and outTokens ≈ req.MaxTokens (or defaultMaxTokens
// when unset).
//
// Candidates with both costs equal to zero are deprioritised: they only
// win when no priced candidate exists for the group. When every
// candidate lacks cost data, the strategy delegates to an embedded
// RoundRobin and warns once per (group, candidate-set) so each distinct
// unpriced model surfaces — without the model-set component, the
// concrete-model path (group="") would only warn for the first model
// and silence every subsequent unpriced model.
type CostOptimized struct {
	fallback *RoundRobin

	warnOnce sync.Map // map[string]*sync.Once — keyed by warnKey(group, candidates)
	warn     func(group string, candidates []router.Candidate)
}

// NewCostOptimized builds a CostOptimized with a private RoundRobin
// fallback and a slog-backed warner.
func NewCostOptimized() *CostOptimized {
	return &CostOptimized{
		fallback: NewRoundRobin(),
		warn: func(group string, candidates []router.Candidate) {
			models := make([]string, len(candidates))
			for i, c := range candidates {
				models[i] = c.Model
			}
			slog.Warn("cost_optimized: all candidates lack cost data; falling through to round_robin",
				"group", group,
				"models", models,
			)
		},
	}
}

// Name reports the canonical strategy id.
func (*CostOptimized) Name() string { return "cost_optimized" }

// Select ranks priced candidates by estimated cost. Behavior:
//   - At least one priced candidate → lowest-cost wins; ties broken by
//     slice order (a single forward min scan).
//   - All candidates lack cost data → delegate to the embedded
//     RoundRobin (so callers still get a deterministic spread) and emit
//     a one-shot WARN per group.
func (c *CostOptimized) Select(candidates []router.Candidate, req *model.ChatCompletionRequest, meta router.RequestMeta) (router.Candidate, error) {
	if len(candidates) == 0 {
		return router.Candidate{}, errors.New("cost_optimized: empty candidate list")
	}

	bestIdx := -1
	bestCost := 0.0
	for i, cand := range candidates {
		if cand.CostPer1kInputUSD == 0 && cand.CostPer1kOutputUSD == 0 {
			continue
		}
		cost := estimateCost(cand, req)
		if bestIdx == -1 || cost < bestCost {
			bestIdx = i
			bestCost = cost
		}
	}
	if bestIdx == -1 {
		c.warnFor(meta.Group, candidates)
		return c.fallback.Select(candidates, req, meta)
	}
	return candidates[bestIdx], nil
}

// warnFor emits the "no cost data" WARN exactly once per (group, model
// set). For the concrete-model path (group="") each distinct model
// produces its own warning so the operator can identify which models
// need pricing data — keying solely on group would collapse them all
// into one warning under the empty key.
func (c *CostOptimized) warnFor(group string, candidates []router.Candidate) {
	key := warnKey(group, candidates)
	// Load-then-LoadOrStore avoids allocating a sync.Once on every call
	// once the key has been seen.
	v, ok := c.warnOnce.Load(key)
	if !ok {
		v, _ = c.warnOnce.LoadOrStore(key, &sync.Once{})
	}
	v.(*sync.Once).Do(func() { c.warn(group, candidates) })
}

// warnKey returns a stable identifier for the (group, candidate-set)
// pair. Models are sorted so two requests with the same candidates in
// different order share one warn-once slot.
func warnKey(group string, candidates []router.Candidate) string {
	models := make([]string, len(candidates))
	for i, c := range candidates {
		models[i] = c.Model
	}
	sort.Strings(models)
	return group + "\x00" + strings.Join(models, ",")
}

// estimateCost is the rank-only heuristic described above. The math is
// in per-1k units so the multiplications stay on small floats.
func estimateCost(c router.Candidate, req *model.ChatCompletionRequest) float64 {
	inChars := 0
	for _, m := range req.Messages {
		inChars += len(m.Content)
	}
	inTokensPer1k := float64(inChars) / 4.0 / 1000.0

	outTokens := defaultMaxTokens
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		outTokens = *req.MaxTokens
	}
	outTokensPer1k := float64(outTokens) / 1000.0

	return inTokensPer1k*c.CostPer1kInputUSD + outTokensPer1k*c.CostPer1kOutputUSD
}
