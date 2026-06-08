package strategy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

func TestRoundRobin_RotatesAcrossCandidates(t *testing.T) {
	rr := NewRoundRobin()
	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
		stubCandidate("anthropic", "claude", router.Candidate{}),
		stubCandidate("google", "gemini", router.Candidate{}),
	}
	meta := router.RequestMeta{Group: "smart"}

	got := make([]string, 7)
	for i := range got {
		c, err := rr.Select(candidates, nil, meta)
		require.NoError(t, err)
		got[i] = c.Provider.Name()
	}

	// First call lands on index 0; wraps cleanly at len.
	want := []string{"openai", "anthropic", "google", "openai", "anthropic", "google", "openai"}
	assert.Equal(t, want, got)
}

func TestRoundRobin_PerGroupIsolation(t *testing.T) {
	rr := NewRoundRobin()
	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
		stubCandidate("anthropic", "claude", router.Candidate{}),
	}

	// Two groups with separate counters: each starts at index 0.
	pickA1, _ := rr.Select(candidates, nil, router.RequestMeta{Group: "groupA"})
	pickB1, _ := rr.Select(candidates, nil, router.RequestMeta{Group: "groupB"})
	pickA2, _ := rr.Select(candidates, nil, router.RequestMeta{Group: "groupA"})
	pickB2, _ := rr.Select(candidates, nil, router.RequestMeta{Group: "groupB"})

	assert.Equal(t, "openai", pickA1.Provider.Name())
	assert.Equal(t, "openai", pickB1.Provider.Name())
	assert.Equal(t, "anthropic", pickA2.Provider.Name())
	assert.Equal(t, "anthropic", pickB2.Provider.Name())
}

func TestRoundRobin_EmptyCandidates(t *testing.T) {
	_, err := NewRoundRobin().Select(nil, nil, router.RequestMeta{Group: "x"})
	require.Error(t, err)
}

func TestRoundRobin_Name(t *testing.T) {
	assert.Equal(t, "round_robin", NewRoundRobin().Name())
}
