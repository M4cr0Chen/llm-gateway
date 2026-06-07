package strategy

import (
	"errors"
	"math"
	"sync"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/metrics"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

const (
	defaultLatencyPollInterval = 5 * time.Second
	defaultLatencyHalfLife     = 60 * time.Second
)

// LatencyOptimized picks the candidate with the lowest exponentially-
// weighted moving average of provider latency. The EWMA is fed by a
// background poller that periodically reads
// gateway_provider_request_duration_seconds and feeds the delta-average
// between polls into Observe.
//
// Cold start (no provider has any sample yet) falls back to a private
// RoundRobin. Mixed state (some providers measured, some not) treats an
// unmeasured candidate as +infinity so a measured one always wins —
// otherwise a fresh provider would beat a slow but proven one purely
// because it has no data yet, which is the opposite of "least-latency".
type LatencyOptimized struct {
	fallback *RoundRobin

	mu    sync.RWMutex
	ewmas map[string]float64
	alpha float64

	stop chan struct{}
	done chan struct{}
}

// NewLatencyOptimized builds the production-shaped strategy: 5s poll,
// 60s half-life, reading from the real Prometheus histogram. It starts
// the poller goroutine immediately. Production code does not currently
// call Stop — the goroutine exits when the process exits — but tests
// MUST call Stop to avoid leaking the ticker.
func NewLatencyOptimized() *LatencyOptimized {
	return newLatencyOptimized(metrics.ProviderLatencyTotals, defaultLatencyPollInterval, defaultLatencyHalfLife)
}

// newLatencyOptimized is the test-friendly constructor. Pass a reader
// closure so the test can fabricate latency totals without touching the
// global Prometheus registry, and a pollInterval that is short enough
// for test wall-clock budgets.
func newLatencyOptimized(reader func() map[string]metrics.LatencyTotal, pollInterval, halfLife time.Duration) *LatencyOptimized {
	alpha := ewmaAlpha(pollInterval, halfLife)
	s := &LatencyOptimized{
		fallback: NewRoundRobin(),
		ewmas:    make(map[string]float64),
		alpha:    alpha,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	go s.run(reader, pollInterval)
	return s
}

// Name reports the canonical strategy id.
func (*LatencyOptimized) Name() string { return "latency_optimized" }

// Select returns the candidate with the lowest tracked EWMA. Candidates
// without a sample are treated as +Inf; if every candidate is unmeasured
// the strategy delegates to its private RoundRobin so the first request
// after startup still gets routed.
func (s *LatencyOptimized) Select(candidates []router.Candidate, req *model.ChatCompletionRequest, meta router.RequestMeta) (router.Candidate, error) {
	if len(candidates) == 0 {
		return router.Candidate{}, errors.New("latency_optimized: empty candidate list")
	}

	s.mu.RLock()
	bestIdx := -1
	bestEwma := math.Inf(1)
	for i, c := range candidates {
		v, ok := s.ewmas[c.Provider.Name()]
		if !ok {
			continue
		}
		if v < bestEwma {
			bestIdx = i
			bestEwma = v
		}
	}
	s.mu.RUnlock()

	if bestIdx == -1 {
		return s.fallback.Select(candidates, req, meta)
	}
	return candidates[bestIdx], nil
}

// Observe folds one sample into the per-provider EWMA. Exposed so tests
// can drive the strategy without running the poller; the poller itself
// also calls it.
func (s *LatencyOptimized) Observe(provider string, seconds float64) {
	if seconds < 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, ok := s.ewmas[provider]; ok {
		s.ewmas[provider] = s.alpha*seconds + (1-s.alpha)*prev
	} else {
		s.ewmas[provider] = seconds
	}
}

// Stop signals the poller to exit and waits for it. Idempotent; safe to
// call multiple times.
func (s *LatencyOptimized) Stop() {
	select {
	case <-s.stop:
		// already stopped
	default:
		close(s.stop)
	}
	<-s.done
}

// run is the polling loop. Each tick reads the cumulative latency
// totals, takes the delta vs. the previous read, and feeds the average
// of new observations into Observe. Providers with zero new observations
// in the window contribute nothing — their EWMA stays where it was, so
// a quiet provider's history does not decay away to misleading values.
func (s *LatencyOptimized) run(reader func() map[string]metrics.LatencyTotal, pollInterval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	prev := reader()
	for {
		select {
		case <-s.stop:
			return
		case <-ticker.C:
			curr := reader()
			for provider, c := range curr {
				p := prev[provider]
				deltaCount := c.Count - p.Count
				if deltaCount == 0 {
					continue
				}
				deltaSum := c.Sum - p.Sum
				if deltaSum < 0 {
					// Counter reset (e.g., metric re-registration in tests). Reset state.
					deltaSum = 0
				}
				s.Observe(provider, deltaSum/float64(deltaCount))
			}
			prev = curr
		}
	}
}

// ewmaAlpha derives the per-sample weight from the poll interval and the
// configured half-life: alpha = 1 - exp(-poll/halfLife). For the
// production 5s / 60s pair this is ≈ 0.080.
func ewmaAlpha(pollInterval, halfLife time.Duration) float64 {
	if halfLife <= 0 {
		return 1.0
	}
	return 1.0 - math.Exp(-float64(pollInterval)/float64(halfLife))
}
