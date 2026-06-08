package strategy

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/metrics"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

func TestLatencyOptimized_ColdStartFallsThroughToRoundRobin(t *testing.T) {
	// Reader returns nothing → no EWMA samples → fall through.
	s := newLatencyOptimized(emptyReader, time.Hour, time.Hour)
	defer s.Stop()

	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
		stubCandidate("anthropic", "claude", router.Candidate{}),
	}
	// First fallthrough → round-robin lands on index 0.
	pick1, err := s.Select(candidates, nil, router.RequestMeta{Group: "smart"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o", pick1.Model)

	pick2, err := s.Select(candidates, nil, router.RequestMeta{Group: "smart"})
	require.NoError(t, err)
	assert.Equal(t, "claude", pick2.Model)
}

func TestLatencyOptimized_PicksLowestEWMA(t *testing.T) {
	s := newLatencyOptimized(emptyReader, time.Hour, time.Hour)
	defer s.Stop()

	s.Observe("openai", 1.0)
	s.Observe("anthropic", 0.2)
	s.Observe("google", 0.5)

	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
		stubCandidate("anthropic", "claude", router.Candidate{}),
		stubCandidate("google", "gemini", router.Candidate{}),
	}
	pick, err := s.Select(candidates, nil, router.RequestMeta{Group: "smart"})
	require.NoError(t, err)
	assert.Equal(t, "claude", pick.Model)
}

func TestLatencyOptimized_UnmeasuredCandidateLosesToMeasured(t *testing.T) {
	s := newLatencyOptimized(emptyReader, time.Hour, time.Hour)
	defer s.Stop()

	// Only openai has data — even though it's slow (1s), the unmeasured
	// candidates should not beat it, because "no sample" should not be
	// confused with "instantly fast".
	s.Observe("openai", 1.0)

	candidates := []router.Candidate{
		stubCandidate("openai", "gpt-4o", router.Candidate{}),
		stubCandidate("anthropic", "claude", router.Candidate{}),
	}
	pick, err := s.Select(candidates, nil, router.RequestMeta{Group: "smart"})
	require.NoError(t, err)
	assert.Equal(t, "gpt-4o", pick.Model)
}

func TestLatencyOptimized_EWMADecaysTowardNewSamples(t *testing.T) {
	// With alpha=0.5, EWMA after observing 1.0 then 0.0 is 0.5.
	s := &LatencyOptimized{
		fallback: NewRoundRobin(),
		ewmas:    make(map[string]float64),
		alpha:    0.5,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	close(s.done) // skip the run() goroutine; Stop() will see done already closed.

	s.Observe("openai", 1.0)
	s.Observe("openai", 0.0)
	assert.InDelta(t, 0.5, s.ewmas["openai"], 1e-9)
}

func TestLatencyOptimized_Name(t *testing.T) {
	s := newLatencyOptimized(emptyReader, time.Hour, time.Hour)
	defer s.Stop()
	assert.Equal(t, "latency_optimized", s.Name())
}

func TestLatencyOptimized_PollerObservesDeltaAverages(t *testing.T) {
	// Use a fast poll so the test wall-clock budget is small.
	calls := 0
	reader := func() map[string]metrics.LatencyTotal {
		calls++
		switch calls {
		case 1:
			// Initial snapshot — provider has 0 observations.
			return map[string]metrics.LatencyTotal{"openai": {Sum: 0, Count: 0}}
		case 2:
			// First tick: 2 observations summing to 0.4 → avg 0.2.
			return map[string]metrics.LatencyTotal{"openai": {Sum: 0.4, Count: 2}}
		default:
			return map[string]metrics.LatencyTotal{"openai": {Sum: 0.4, Count: 2}}
		}
	}
	s := newLatencyOptimized(reader, 20*time.Millisecond, time.Hour)
	defer s.Stop()

	require.Eventually(t, func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		v, ok := s.ewmas["openai"]
		return ok && v == 0.2
	}, time.Second, 5*time.Millisecond, "poller should have observed delta average")
}

func TestLatencyOptimized_StopIsIdempotent(t *testing.T) {
	s := newLatencyOptimized(emptyReader, time.Hour, time.Hour)
	s.Stop()
	s.Stop() // must not panic.
}

func TestEwmaAlpha_KnownValue(t *testing.T) {
	got := ewmaAlpha(5*time.Second, 60*time.Second)
	// 1 - exp(-5/60) ≈ 0.0800.
	assert.InDelta(t, 0.0800, got, 1e-3)
}

func emptyReader() map[string]metrics.LatencyTotal {
	return nil
}
