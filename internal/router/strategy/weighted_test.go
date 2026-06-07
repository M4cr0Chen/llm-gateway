package strategy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

func TestWeighted_DeterministicPick(t *testing.T) {
	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{Weight: 1}),
		stubCandidate("anthropic", "claude", router.Candidate{Weight: 3}),
		stubCandidate("google", "gemini", router.Candidate{Weight: 6}),
	}
	// Total weight 10; cumulative thresholds: 1, 4, 10.
	tests := []struct {
		name      string
		randFloat float64
		want      string
	}{
		{"first bucket (pick < 1)", 0.05, "gpt-4o"},
		{"second bucket (1 <= pick < 4)", 0.25, "claude"},
		{"third bucket (4 <= pick < 10)", 0.85, "gemini"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newWeightedWithRand(func() float64 { return tt.randFloat })
			pick, err := w.Select(candidates, nil, router.RequestMeta{})
			require.NoError(t, err)
			assert.Equal(t, tt.want, pick.Model)
		})
	}
}

func TestWeighted_ZeroWeightsFallsBackToUniform(t *testing.T) {
	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{Weight: 0}),
		stubCandidate("anthropic", "claude", router.Candidate{Weight: 0}),
		stubCandidate("google", "gemini", router.Candidate{Weight: 0}),
	}
	// randFloat=0.5 with 3 candidates → index 1.
	w := newWeightedWithRand(func() float64 { return 0.5 })
	pick, err := w.Select(candidates, nil, router.RequestMeta{})
	require.NoError(t, err)
	assert.Equal(t, "claude", pick.Model)
}

func TestWeighted_EmptyCandidates(t *testing.T) {
	w := newWeightedWithRand(func() float64 { return 0.5 })
	_, err := w.Select(nil, nil, router.RequestMeta{})
	require.Error(t, err)
}

func TestWeighted_Name(t *testing.T) {
	assert.Equal(t, "weighted", NewWeighted().Name())
}
