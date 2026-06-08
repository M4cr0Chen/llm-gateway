package strategy

import (
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

func intPtr(i int) *int { return &i }

func TestCostOptimized_PicksLowestCost(t *testing.T) {
	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{CostPer1kInputUSD: 0.0025, CostPer1kOutputUSD: 0.01}),
		stubCandidate("anthropic", "claude", router.Candidate{CostPer1kInputUSD: 0.003, CostPer1kOutputUSD: 0.015}),
		stubCandidate("google", "gemini-flash", router.Candidate{CostPer1kInputUSD: 0.0001, CostPer1kOutputUSD: 0.0004}),
	}
	req := &model.ChatCompletionRequest{
		Messages:  []model.Message{{Role: "user", Content: "hello world"}},
		MaxTokens: intPtr(256),
	}
	pick, err := NewCostOptimized().Select(candidates, req, router.RequestMeta{Group: "smart"})
	require.NoError(t, err)
	assert.Equal(t, "gemini-flash", pick.Model)
}

func TestCostOptimized_DeprioritisesZeroCostCandidates(t *testing.T) {
	// gpt-4o has no cost data; gemini-flash has cost data → gemini wins
	// despite being later in slice order.
	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
		stubCandidate("google", "gemini-flash", router.Candidate{CostPer1kInputUSD: 0.0001, CostPer1kOutputUSD: 0.0004}),
	}
	req := &model.ChatCompletionRequest{
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	}
	pick, err := NewCostOptimized().Select(candidates, req, router.RequestMeta{Group: "smart"})
	require.NoError(t, err)
	assert.Equal(t, "gemini-flash", pick.Model)
}

func TestCostOptimized_AllZeroCostFallsThroughToRoundRobin(t *testing.T) {
	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
		stubCandidate("anthropic", "claude", router.Candidate{}),
	}
	req := &model.ChatCompletionRequest{
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	}

	var warnCount atomic.Int64
	s := NewCostOptimized()
	s.warn = func(string, []router.Candidate) { warnCount.Add(1) }

	// First pick rolls the round-robin from 0 → index 0.
	pick1, err := s.Select(candidates, req, router.RequestMeta{Group: "smart"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o", pick1.Model)
	// Second pick wraps to index 1, and crucially does NOT re-warn —
	// the (group, model-set) key is unchanged across the two calls.
	pick2, err := s.Select(candidates, req, router.RequestMeta{Group: "smart"})
	require.NoError(t, err)
	assert.Equal(t, "claude", pick2.Model)

	assert.Equal(t, int64(1), warnCount.Load(), "WARN should fire once per (group, candidate-set)")

	// A different group triggers another WARN.
	_, _ = s.Select(candidates, req, router.RequestMeta{Group: "fast"})
	assert.Equal(t, int64(2), warnCount.Load())
}

func TestCostOptimized_EstimateCostUsesMaxTokensWhenSet(t *testing.T) {
	c := router.Candidate{CostPer1kInputUSD: 1, CostPer1kOutputUSD: 1}
	// Input "abcd" (len=4) → 4/4/1000 = 0.001 per-1k inTokens.
	// MaxTokens=2000 → 2.0 per-1k outTokens.
	// Cost ≈ 0.001*1 + 2.0*1 = 2.001.
	req := &model.ChatCompletionRequest{
		Messages:  []model.Message{{Role: "user", Content: "abcd"}},
		MaxTokens: intPtr(2000),
	}
	got := estimateCost(c, req)
	assert.InDelta(t, 2.001, got, 1e-9)
}

func TestCostOptimized_EstimateCostFallsBackToDefaultMaxTokens(t *testing.T) {
	c := router.Candidate{CostPer1kInputUSD: 1, CostPer1kOutputUSD: 1}
	// MaxTokens unset → uses defaultMaxTokens (256). outTokens = 0.256.
	req := &model.ChatCompletionRequest{
		Messages: []model.Message{{Role: "user", Content: ""}},
	}
	got := estimateCost(c, req)
	assert.InDelta(t, 0.256, got, 1e-9)
}

func TestCostOptimized_EmptyCandidates(t *testing.T) {
	_, err := NewCostOptimized().Select(nil, &model.ChatCompletionRequest{}, router.RequestMeta{})
	require.Error(t, err)
}

func TestCostOptimized_ConcreteModelPathWarnsPerDistinctModel(t *testing.T) {
	// Concrete-model requests arrive with group="" and a one-element
	// candidate list. Each distinct unpriced model must produce its own
	// warning — keying solely on group would collapse them all under
	// the empty key and silence everything after the first.
	var got []string
	s := NewCostOptimized()
	s.warn = func(_ string, cands []router.Candidate) {
		for _, c := range cands {
			got = append(got, c.Model)
		}
	}
	req := &model.ChatCompletionRequest{Messages: []model.Message{{Role: "user", Content: "hi"}}}

	// gpt-4o twice → one warning.
	_, _ = s.Select([]router.Candidate{stubCandidate("openai", "gpt-4o", router.Candidate{})}, req, router.RequestMeta{})
	_, _ = s.Select([]router.Candidate{stubCandidate("openai", "gpt-4o", router.Candidate{})}, req, router.RequestMeta{})
	// claude once → second warning.
	_, _ = s.Select([]router.Candidate{stubCandidate("anthropic", "claude", router.Candidate{})}, req, router.RequestMeta{})

	assert.Equal(t, []string{"gpt-4o", "claude"}, got)
}

func TestCostOptimized_WarnKeyIsOrderIndependent(t *testing.T) {
	// Two candidate lists with the same models in different order
	// must share one warn-once slot — otherwise a reordering in YAML
	// (or in expandGroup) would double-warn.
	a := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
		stubCandidate("anthropic", "claude", router.Candidate{}),
	}
	b := []router.Candidate{
		stubCandidate("anthropic", "claude", router.Candidate{}),
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
	}
	assert.Equal(t, warnKey("smart", a), warnKey("smart", b))
}

func TestCostOptimized_Name(t *testing.T) {
	assert.Equal(t, "cost_optimized", NewCostOptimized().Name())
}
