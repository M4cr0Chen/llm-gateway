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

	"github.com/M4cr0Chen/llm-gateway/internal/router"
)

// Build returns the strategy identified by name. The five known names
// are kept in one switch so an "unknown routing strategy" error fires
// from both the default_strategy and per-group override paths.
//
// LatencyOptimized starts a background poller for the EWMA values; for
// tests that need to control timing or bypass the goroutine, use
// NewLatencyOptimizedWithReader directly.
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
