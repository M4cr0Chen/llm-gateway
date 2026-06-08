// Package strategy ships the five candidate-ranking strategies the
// Router consults: cost_optimized, latency_optimized, round_robin,
// weighted, and priority. Each concrete strategy implements
// router.Strategy via structural typing — the interface itself lives in
// the parent router package to keep the import graph acyclic.
//
// Construction is funnelled through Build so config validation
// (router.NewRouter) has a single place to look up strategies by name
// and so callers cannot accidentally pull in only some of the five.
package strategy

import (
	"fmt"
	"sync"

	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// Builder memoizes strategy instances by name so multiple groups that
// pick the same strategy share one instance per Builder. This matters
// most for LatencyOptimized: each instance starts a background poller
// and maintains its own EWMA map populated from the same Prometheus
// metrics. Without memoization, a process with N groups overriding to
// "latency_optimized" leaks N goroutines for state that should be one.
//
// Sharing is also correct for the other four strategies — RoundRobin
// keys its counters by meta.Group, CostOptimized's warn-once is keyed
// by (group, candidate-set), Weighted has only an RNG, and Priority is
// stateless — so the same instance is safe to use as the default
// strategy AND as a per-group override.
//
// Production wires one Builder in main.go and passes Builder.Build into
// router.NewRouter. Tests that need isolation construct a fresh
// Builder, or call the free Build directly to get a brand-new
// instance.
type Builder struct {
	mu sync.Mutex
	by map[string]router.Strategy
}

// NewBuilder returns an empty Builder.
func NewBuilder() *Builder {
	return &Builder{by: make(map[string]router.Strategy)}
}

// Build returns the memoized strategy for name, constructing it on
// first use. Subsequent calls for the same name return the same
// instance.
func (b *Builder) Build(name string) (router.Strategy, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.by[name]; ok {
		return s, nil
	}
	s, err := Build(name)
	if err != nil {
		return nil, err
	}
	b.by[name] = s
	return s, nil
}

// Build returns a freshly-constructed strategy identified by name. The
// five known names are kept in one switch so an "unknown routing
// strategy" error fires from both the default_strategy and per-group
// override paths.
//
// Each call returns a NEW instance — use *Builder.Build for production
// memoization where multiple groups share one strategy. LatencyOptimized
// in particular starts a background poller; callers responsible for
// long-lived ownership should arrange to call Stop.
func Build(name string) (router.Strategy, error) {
	switch name {
	case "cost_optimized":
		return NewCostOptimized(), nil
	case "latency_optimized":
		return NewLatencyOptimized(), nil
	case "round_robin":
		return NewRoundRobin(), nil
	case "weighted":
		return NewWeighted(), nil
	case "priority":
		return NewPriority(), nil
	default:
		return nil, fmt.Errorf("unknown routing strategy %q", name)
	}
}
