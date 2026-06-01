// Package metrics declares and registers all Prometheus collectors used by
// the gateway, and exposes small typed helpers so callers never import the
// Prometheus types directly.
//
// Cardinality plan — keep total active series under ~10k.
//
//	gateway_requests_total{method, path_template, status_class}
//	  method:        bounded ~7 (GET, POST, PUT, DELETE, PATCH, HEAD, OPTIONS)
//	  path_template: bounded by chi-matched routes (~6: /health,
//	                 /v1/chat/completions, /v1/models, /internal/health,
//	                 /internal/admin/keys, /internal/admin/keys/{id}),
//	                 plus "unknown" for 404s
//	  status_class:  bounded 5 (2xx, 3xx, 4xx, 5xx, other)
//
//	gateway_provider_requests_total{provider, status_class}
//	  provider:      bounded by configured providers (~3: openai, anthropic, google)
//	  status_class:  bounded 5
//
//	gateway_ratelimit_hits_total{outcome}
//	  outcome:       allowed | throttled | failopen
//
//	gateway_auth_failures_total{reason}
//	  reason:        missing | malformed | unknown | revoked | expired
//
//	gateway_request_duration_seconds{method, path_template}
//	  bounded as gateway_requests_total minus status_class
//
//	gateway_provider_request_duration_seconds{provider}
//	  bounded by provider count
//
//	gateway_provider_health{provider}
//	  bounded by provider count
//
// Rule: any new label must keep the total series product under ~10k.
// `model` is intentionally NOT a top-level label — it is high-cardinality
// and can be queried via logs. Do not add request_id, user-supplied text,
// or per-key labels.
package metrics

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// durationBuckets are the histogram buckets used for request and provider
// latency. Values are in seconds, matching the issue's spec.
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

var (
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_requests_total",
		Help: "Total HTTP requests handled by the gateway, labelled by method, chi path template, and status class.",
	}, []string{"method", "path_template", "status_class"})

	requestDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_request_duration_seconds",
		Help:    "Duration of HTTP requests handled by the gateway, in seconds.",
		Buckets: durationBuckets,
	}, []string{"method", "path_template"})

	providerRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_provider_requests_total",
		Help: "Total outbound requests to upstream LLM providers, labelled by provider and status class.",
	}, []string{"provider", "status_class"})

	providerRequestDurationSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "gateway_provider_request_duration_seconds",
		Help:    "Duration of outbound requests to upstream LLM providers, in seconds.",
		Buckets: durationBuckets,
	}, []string{"provider"})

	rateLimitHitsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_ratelimit_hits_total",
		Help: "Rate-limiter decisions: allowed (passed), throttled (429), or failopen (Redis unavailable).",
	}, []string{"outcome"})

	authFailuresTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "gateway_auth_failures_total",
		Help: "Authentication failures classified by reason.",
	}, []string{"reason"})

	providerHealth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "gateway_provider_health",
		Help: "Provider health: 1 healthy, 0 unhealthy (set after the failure threshold is reached).",
	}, []string{"provider"})
)

// RecordRequest emits the gateway-level counter and latency histogram for
// one HTTP request. pathTemplate is the chi-matched route pattern or
// "unknown" when no route matched.
func RecordRequest(method, pathTemplate string, status int, duration time.Duration) {
	cls := statusClass(status)
	requestsTotal.WithLabelValues(method, pathTemplate, cls).Inc()
	requestDurationSeconds.WithLabelValues(method, pathTemplate).Observe(duration.Seconds())
}

// RecordProviderRequest emits the provider-level counter and latency
// histogram for one outbound call (after retries, if the wrapped provider
// is a HealthTrackingProvider). status is the upstream HTTP status, or
// 200 on success / 500 when no status is available.
func RecordProviderRequest(provider string, status int, duration time.Duration) {
	cls := statusClass(status)
	providerRequestsTotal.WithLabelValues(provider, cls).Inc()
	providerRequestDurationSeconds.WithLabelValues(provider).Observe(duration.Seconds())
}

// RecordRateLimit increments the rate-limit decision counter. outcome
// must be one of "allowed", "throttled", "failopen".
func RecordRateLimit(outcome string) {
	rateLimitHitsTotal.WithLabelValues(outcome).Inc()
}

// RecordAuthFailure increments the auth-failure counter. reason must be
// one of the documented values: "missing", "malformed", "unknown",
// "revoked", "expired".
func RecordAuthFailure(reason string) {
	authFailuresTotal.WithLabelValues(reason).Inc()
}

// SetProviderHealth sets the provider's health gauge to 1 (healthy) or 0
// (unhealthy). Callers should pass the underlying healthy flag, not the
// cooldown-permissive view, so the gauge reflects threshold trips.
func SetProviderHealth(provider string, healthy bool) {
	v := 0.0
	if healthy {
		v = 1.0
	}
	providerHealth.WithLabelValues(provider).Set(v)
}

// statusClass bins an HTTP status code into "2xx", "3xx", "4xx", "5xx",
// or "other" (for 1xx and out-of-range values). Five values keeps the
// label cardinality bounded.
func statusClass(status int) string {
	switch {
	case status >= 200 && status < 300:
		return "2xx"
	case status >= 300 && status < 400:
		return "3xx"
	case status >= 400 && status < 500:
		return "4xx"
	case status >= 500 && status < 600:
		return "5xx"
	default:
		return "other"
	}
}
