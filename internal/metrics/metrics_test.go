package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecordRequest(t *testing.T) {
	const method, path = "POST", "/v1/chat/completions"

	before2xx := testutil.ToFloat64(requestsTotal.WithLabelValues(method, path, "2xx"))
	before4xx := testutil.ToFloat64(requestsTotal.WithLabelValues(method, path, "4xx"))
	before5xx := testutil.ToFloat64(requestsTotal.WithLabelValues(method, path, "5xx"))

	histCountBefore := histogramSampleCount(t, requestDurationSeconds.WithLabelValues(method, path))

	RecordRequest(method, path, 200, 25*time.Millisecond)
	RecordRequest(method, path, 429, 5*time.Millisecond)
	RecordRequest(method, path, 502, 1500*time.Millisecond)

	assert.Equal(t, before2xx+1, testutil.ToFloat64(requestsTotal.WithLabelValues(method, path, "2xx")))
	assert.Equal(t, before4xx+1, testutil.ToFloat64(requestsTotal.WithLabelValues(method, path, "4xx")))
	assert.Equal(t, before5xx+1, testutil.ToFloat64(requestsTotal.WithLabelValues(method, path, "5xx")))
	assert.Equal(t, histCountBefore+3, histogramSampleCount(t, requestDurationSeconds.WithLabelValues(method, path)))
}

func TestRecordProviderRequest(t *testing.T) {
	const provider = "test-provider-RecordProviderRequest"

	before2xx := testutil.ToFloat64(providerRequestsTotal.WithLabelValues(provider, "2xx"))
	before5xx := testutil.ToFloat64(providerRequestsTotal.WithLabelValues(provider, "5xx"))
	histCountBefore := histogramSampleCount(t, providerRequestDurationSeconds.WithLabelValues(provider))

	RecordProviderRequest(provider, 200, 100*time.Millisecond)
	RecordProviderRequest(provider, 503, 30*time.Millisecond)

	assert.Equal(t, before2xx+1, testutil.ToFloat64(providerRequestsTotal.WithLabelValues(provider, "2xx")))
	assert.Equal(t, before5xx+1, testutil.ToFloat64(providerRequestsTotal.WithLabelValues(provider, "5xx")))
	assert.Equal(t, histCountBefore+2, histogramSampleCount(t, providerRequestDurationSeconds.WithLabelValues(provider)))
}

func TestRecordRateLimit(t *testing.T) {
	beforeAllowed := testutil.ToFloat64(rateLimitHitsTotal.WithLabelValues("allowed"))
	beforeThrottled := testutil.ToFloat64(rateLimitHitsTotal.WithLabelValues("throttled"))
	beforeFailOpen := testutil.ToFloat64(rateLimitHitsTotal.WithLabelValues("failopen"))

	RecordRateLimit("allowed")
	RecordRateLimit("throttled")
	RecordRateLimit("throttled")
	RecordRateLimit("failopen")

	assert.Equal(t, beforeAllowed+1, testutil.ToFloat64(rateLimitHitsTotal.WithLabelValues("allowed")))
	assert.Equal(t, beforeThrottled+2, testutil.ToFloat64(rateLimitHitsTotal.WithLabelValues("throttled")))
	assert.Equal(t, beforeFailOpen+1, testutil.ToFloat64(rateLimitHitsTotal.WithLabelValues("failopen")))
}

func TestRecordAuthFailure(t *testing.T) {
	beforeMissing := testutil.ToFloat64(authFailuresTotal.WithLabelValues("missing"))
	beforeMalformed := testutil.ToFloat64(authFailuresTotal.WithLabelValues("malformed"))
	beforeUnknown := testutil.ToFloat64(authFailuresTotal.WithLabelValues("unknown"))

	RecordAuthFailure("missing")
	RecordAuthFailure("malformed")
	RecordAuthFailure("unknown")
	RecordAuthFailure("unknown")

	assert.Equal(t, beforeMissing+1, testutil.ToFloat64(authFailuresTotal.WithLabelValues("missing")))
	assert.Equal(t, beforeMalformed+1, testutil.ToFloat64(authFailuresTotal.WithLabelValues("malformed")))
	assert.Equal(t, beforeUnknown+2, testutil.ToFloat64(authFailuresTotal.WithLabelValues("unknown")))
}

func TestSetProviderHealth(t *testing.T) {
	const provider = "test-provider-SetProviderHealth"

	SetProviderHealth(provider, true)
	assert.Equal(t, 1.0, testutil.ToFloat64(providerHealth.WithLabelValues(provider)))

	SetProviderHealth(provider, false)
	assert.Equal(t, 0.0, testutil.ToFloat64(providerHealth.WithLabelValues(provider)))

	SetProviderHealth(provider, true)
	assert.Equal(t, 1.0, testutil.ToFloat64(providerHealth.WithLabelValues(provider)))
}

func TestRecordRouterDecision(t *testing.T) {
	const strategy, providerName, group = "priority", "test-provider-RecordRouterDecision", "smart"

	beforePrimary := testutil.ToFloat64(routerDecisionsTotal.WithLabelValues(strategy, providerName, group, "primary"))
	beforeFallback := testutil.ToFloat64(routerDecisionsTotal.WithLabelValues(strategy, providerName, group, "fallback"))
	beforeError := testutil.ToFloat64(routerDecisionsTotal.WithLabelValues(strategy, providerName, group, "error"))

	RecordRouterDecision(strategy, providerName, group, "primary")
	RecordRouterDecision(strategy, providerName, group, "fallback")
	RecordRouterDecision(strategy, providerName, group, "fallback")
	RecordRouterDecision(strategy, providerName, group, "error")

	assert.Equal(t, beforePrimary+1, testutil.ToFloat64(routerDecisionsTotal.WithLabelValues(strategy, providerName, group, "primary")))
	assert.Equal(t, beforeFallback+2, testutil.ToFloat64(routerDecisionsTotal.WithLabelValues(strategy, providerName, group, "fallback")))
	assert.Equal(t, beforeError+1, testutil.ToFloat64(routerDecisionsTotal.WithLabelValues(strategy, providerName, group, "error")))
}

func TestRecordRouterFallback(t *testing.T) {
	const from, to = "test-provider-RecordRouterFallback-from", "test-provider-RecordRouterFallback-to"

	before5xx := testutil.ToFloat64(routerFallbackTotal.WithLabelValues(from, to, "5xx"))
	before429 := testutil.ToFloat64(routerFallbackTotal.WithLabelValues(from, to, "429"))
	beforeNetwork := testutil.ToFloat64(routerFallbackTotal.WithLabelValues(from, "", "network"))

	RecordRouterFallback(from, to, "5xx")
	RecordRouterFallback(from, to, "429")
	RecordRouterFallback(from, "", "network")

	assert.Equal(t, before5xx+1, testutil.ToFloat64(routerFallbackTotal.WithLabelValues(from, to, "5xx")))
	assert.Equal(t, before429+1, testutil.ToFloat64(routerFallbackTotal.WithLabelValues(from, to, "429")))
	assert.Equal(t, beforeNetwork+1, testutil.ToFloat64(routerFallbackTotal.WithLabelValues(from, "", "network")))
}

func TestRecordRouterNoHealthy(t *testing.T) {
	const group, empty = "test-group-RecordRouterNoHealthy", ""

	beforeGroup := testutil.ToFloat64(routerNoHealthyProvidersTotal.WithLabelValues(group))
	beforeEmpty := testutil.ToFloat64(routerNoHealthyProvidersTotal.WithLabelValues(empty))

	RecordRouterNoHealthy(group)
	RecordRouterNoHealthy(group)
	RecordRouterNoHealthy(empty)

	assert.Equal(t, beforeGroup+2, testutil.ToFloat64(routerNoHealthyProvidersTotal.WithLabelValues(group)))
	assert.Equal(t, beforeEmpty+1, testutil.ToFloat64(routerNoHealthyProvidersTotal.WithLabelValues(empty)))
}

func TestStatusClass(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{100, "other"},
		{199, "other"},
		{200, "2xx"},
		{299, "2xx"},
		{301, "3xx"},
		{399, "3xx"},
		{400, "4xx"},
		{499, "4xx"},
		{500, "5xx"},
		{599, "5xx"},
		{600, "other"},
		{0, "other"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, statusClass(c.status), "status %d", c.status)
	}
}

// histogramSampleCount returns the cumulative number of observations
// recorded against the given histogram observer. The HistogramVec child
// returned by WithLabelValues implements prometheus.Metric, so we can
// extract the sample count from its dto.Metric representation.
func histogramSampleCount(t *testing.T, obs prometheus.Observer) uint64 {
	t.Helper()
	m, ok := obs.(prometheus.Metric)
	require.True(t, ok, "observer %T does not implement prometheus.Metric", obs)
	var dtoM dto.Metric
	require.NoError(t, m.Write(&dtoM))
	if dtoM.Histogram == nil {
		return 0
	}
	return dtoM.Histogram.GetSampleCount()
}
