package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Router-level Prometheus collectors. These describe what the M4 router
// decided for each request — distinct from the provider-level collectors
// in metrics.go, which describe the upstream call itself.
//
// Cardinality:
//   - strategy: bounded by the five known strategy names (cost_optimized,
//     latency_optimized, round_robin, weighted, priority).
//   - provider / from_provider / to_provider: bounded by configured
//     providers (~3–5). to_provider may also be "" when the fallback hop
//     has no remaining candidate to switch to.
//   - group: bounded by configured model_groups, plus "" for concrete /
//     alias requests.
//   - outcome: enum primary | fallback | error.
//   - reason: enum 429 | 5xx | network.
var (
	routerDecisionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_router_decisions_total",
		Help: "Final routing decisions, one increment per Route call. outcome=primary when the first attempt succeeded, fallback when a later attempt succeeded, error on terminal failure.",
	}, []string{"strategy", "provider", "group", "outcome"})

	routerFallbackTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_router_fallback_total",
		Help: "Inter-provider fallback hops. One increment per retryable failure that the Router switched away from. reason is derived from the upstream status: 429, 5xx, or network.",
	}, []string{"from_provider", "to_provider", "reason"})

	routerNoHealthyProvidersTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_router_no_healthy_providers_total",
		Help: "Requests rejected with 503 all_providers_down because every candidate's circuit breaker was open at the start of routing.",
	}, []string{"group"})
)

// RecordRouterDecision emits one entry on the final router decision.
// outcome must be one of "primary", "fallback", "error".
func RecordRouterDecision(strategy, providerName, group, outcome string) {
	routerDecisionsTotal.WithLabelValues(strategy, providerName, group, outcome).Inc()
}

// RecordRouterFallback emits one entry per fallback hop. toProvider may be
// "" when the previous attempt failed and no healthy candidate remains
// (i.e., the hop is the terminal one).
func RecordRouterFallback(fromProvider, toProvider, reason string) {
	routerFallbackTotal.WithLabelValues(fromProvider, toProvider, reason).Inc()
}

// RecordRouterNoHealthy increments the 503 all_providers_down counter for
// the given group. group is "" for concrete-model and alias requests.
func RecordRouterNoHealthy(group string) {
	routerNoHealthyProvidersTotal.WithLabelValues(group).Inc()
}
