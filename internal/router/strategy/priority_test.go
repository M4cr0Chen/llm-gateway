package strategy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

func TestPriority_Select(t *testing.T) {
	tests := []struct {
		name       string
		candidates []router.Candidate
		wantModel  string
	}{
		{
			name: "lowest priority wins",
			candidates: []router.Candidate{
				stubCandidate("openai", "gpt-4o", router.Candidate{Priority: 2}),
				stubCandidate("anthropic", "claude", router.Candidate{Priority: 0}),
				stubCandidate("google", "gemini", router.Candidate{Priority: 1}),
			},
			wantModel: "claude",
		},
		{
			name: "tie broken by slice order (first occurrence)",
			candidates: []router.Candidate{
				stubCandidate("openai", "gpt-4o", router.Candidate{Priority: 0}),
				stubCandidate("anthropic", "claude", router.Candidate{Priority: 0}),
				stubCandidate("google", "gemini", router.Candidate{Priority: 0}),
			},
			wantModel: "gpt-4o",
		},
		{
			name: "single element",
			candidates: []router.Candidate{
				stubCandidate("openai", "gpt-4o", router.Candidate{Priority: 5}),
			},
			wantModel: "gpt-4o",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPriority()
			pick, err := p.Select(tt.candidates, nil, router.RequestMeta{})
			require.NoError(t, err)
			assert.Equal(t, tt.wantModel, pick.Model)
		})
	}
}

func TestPriority_EmptyCandidates(t *testing.T) {
	_, err := NewPriority().Select(nil, nil, router.RequestMeta{})
	require.Error(t, err)
}

func TestPriority_Name(t *testing.T) {
	assert.Equal(t, "priority", NewPriority().Name())
}
