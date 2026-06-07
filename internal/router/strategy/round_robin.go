package strategy

import (
	"errors"
	"sync"
	"sync/atomic"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// RoundRobin rotates through candidates with a per-group atomic counter.
// Concrete-model and alias requests share the "" key, which is harmless
// because their candidate list has length 1 (every pick lands on index 0).
type RoundRobin struct {
	counters sync.Map // map[string]*atomic.Uint64
}

// NewRoundRobin builds a fresh RoundRobin with empty per-group counters.
// Callers that need a per-strategy reset (tests) construct a new value
// instead of mutating an existing one.
func NewRoundRobin() *RoundRobin { return &RoundRobin{} }

// Name reports the canonical strategy id.
func (*RoundRobin) Name() string { return "round_robin" }

// Select returns candidates[idx % len(candidates)] where idx is the
// per-group counter incremented atomically. The first call to a fresh
// group lands on index 0 so behaviour is deterministic in tests that
// reset the strategy.
func (r *RoundRobin) Select(candidates []router.Candidate, _ *model.ChatCompletionRequest, meta router.RequestMeta) (router.Candidate, error) {
	if len(candidates) == 0 {
		return router.Candidate{}, errors.New("round_robin: empty candidate list")
	}
	counter := r.counterFor(meta.Group)
	idx := counter.Add(1) - 1
	return candidates[idx%uint64(len(candidates))], nil
}

// counterFor returns the atomic counter for the given group, creating
// one on first use. sync.Map's LoadOrStore guarantees only one counter
// ever exists per key even under concurrent first writes.
func (r *RoundRobin) counterFor(group string) *atomic.Uint64 {
	if v, ok := r.counters.Load(group); ok {
		return v.(*atomic.Uint64)
	}
	fresh := &atomic.Uint64{}
	actual, _ := r.counters.LoadOrStore(group, fresh)
	return actual.(*atomic.Uint64)
}
