package strategy

import (
	"errors"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// Priority picks the candidate with the lowest Priority integer.
// Ties are broken by config order: a single forward scan stops at the
// first candidate matching the minimum, so the earliest-listed wins.
type Priority struct{}

// NewPriority builds the stateless Priority strategy. The default
// strategy in M4.1 is "priority"; callers that want it as a per-group
// override get the same instance shape.
func NewPriority() *Priority { return &Priority{} }

// Name reports the canonical strategy id used in config and the
// X-LLM-Gateway-Strategy response header.
func (Priority) Name() string { return "priority" }

// Select returns the lowest-priority candidate. The scan is O(n) and
// preserves slice order on ties so router behavior matches the YAML
// reading order.
func (Priority) Select(candidates []router.Candidate, _ *model.ChatCompletionRequest, _ router.RequestMeta) (router.Candidate, error) {
	if len(candidates) == 0 {
		return router.Candidate{}, errors.New("priority: empty candidate list")
	}
	best := 0
	for i := 1; i < len(candidates); i++ {
		if candidates[i].Priority < candidates[best].Priority {
			best = i
		}
	}
	return candidates[best], nil
}
