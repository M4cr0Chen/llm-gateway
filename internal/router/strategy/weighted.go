package strategy

import (
	"errors"
	"math/rand/v2"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// Weighted picks a candidate with probability proportional to its Weight
// field. It uses a per-strategy *rand.Rand seeded from the wall clock so
// it never touches the global RNG (the global is package-shared and
// would make tests flaky).
type Weighted struct {
	randFloat func() float64
}

// NewWeighted builds a Weighted strategy with its own RNG.
func NewWeighted() *Weighted {
	now := uint64(time.Now().UnixNano())
	rng := rand.New(rand.NewPCG(now, now^0x9e3779b97f4a7c15))
	return &Weighted{randFloat: rng.Float64}
}

// newWeightedWithRand is the unexported test seam: callers inject a
// deterministic randFloat to pin the pick.
func newWeightedWithRand(randFloat func() float64) *Weighted {
	return &Weighted{randFloat: randFloat}
}

// Name reports the canonical strategy id.
func (Weighted) Name() string { return "weighted" }

// Select draws a uniform float in [0, 1), scales it to the cumulative
// weight, and returns the first candidate whose running sum exceeds the
// pick. When every weight is zero the strategy falls back to a uniform
// pick across the slice so the configuration mistake (forgot to set
// weights) does not turn into a 5xx.
func (w *Weighted) Select(candidates []router.Candidate, _ *model.ChatCompletionRequest, _ router.RequestMeta) (router.Candidate, error) {
	if len(candidates) == 0 {
		return router.Candidate{}, errors.New("weighted: empty candidate list")
	}
	total := 0
	for _, c := range candidates {
		if c.Weight < 0 {
			continue
		}
		total += c.Weight
	}
	if total == 0 {
		idx := int(w.randFloat() * float64(len(candidates)))
		if idx >= len(candidates) {
			idx = len(candidates) - 1
		}
		return candidates[idx], nil
	}
	pick := w.randFloat() * float64(total)
	cursor := 0.0
	for _, c := range candidates {
		if c.Weight <= 0 {
			continue
		}
		cursor += float64(c.Weight)
		if pick < cursor {
			return c, nil
		}
	}
	// Numerical edge: pick == total. Return the last positive-weight candidate.
	for i := len(candidates) - 1; i >= 0; i-- {
		if candidates[i].Weight > 0 {
			return candidates[i], nil
		}
	}
	return candidates[len(candidates)-1], nil
}
