package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserveMiddleware_MatchedRoute(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Observe())
	r.Post("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	before := promCounter(t, "gateway_requests_total",
		labels{"method": "POST", "path_template": "/v1/chat/completions", "status_class": "2xx"})
	beforeHist := promHistogramCount(t, "gateway_request_duration_seconds",
		labels{"method": "POST", "path_template": "/v1/chat/completions"})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, before+1, promCounter(t, "gateway_requests_total",
		labels{"method": "POST", "path_template": "/v1/chat/completions", "status_class": "2xx"}))
	assert.Equal(t, beforeHist+1, promHistogramCount(t, "gateway_request_duration_seconds",
		labels{"method": "POST", "path_template": "/v1/chat/completions"}))
}

func TestObserveMiddleware_404Unknown(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Observe())
	// Need at least one route registered so chi builds the middleware
	// chain — without one, chi short-circuits to NotFoundHandler and
	// never invokes Use middlewares. We register a dummy route on a
	// different path and probe a path that won't match.
	r.Get("/registered", func(w http.ResponseWriter, r *http.Request) {})

	before := promCounter(t, "gateway_requests_total",
		labels{"method": "GET", "path_template": "unknown", "status_class": "4xx"})

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, before+1, promCounter(t, "gateway_requests_total",
		labels{"method": "GET", "path_template": "unknown", "status_class": "4xx"}))
}

func TestObserveMiddleware_5xxStatusClass(t *testing.T) {
	r := chi.NewRouter()
	r.Use(Observe())
	r.Get("/boom", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "kaboom", http.StatusInternalServerError)
	})

	before := promCounter(t, "gateway_requests_total",
		labels{"method": "GET", "path_template": "/boom", "status_class": "5xx"})

	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, before+1, promCounter(t, "gateway_requests_total",
		labels{"method": "GET", "path_template": "/boom", "status_class": "5xx"}))
}

// labels is a small alias for the label map used in test assertions.
type labels map[string]string

// promCounter gathers the default registry and returns the current value
// of the named counter for the given label set, or 0 if not yet recorded.
func promCounter(t *testing.T, name string, want labels) float64 {
	t.Helper()
	m := findMetric(t, name, want)
	if m == nil || m.Counter == nil {
		return 0
	}
	return m.Counter.GetValue()
}

// promHistogramCount returns the histogram sample count for the named
// histogram + label set, or 0 if not yet recorded.
func promHistogramCount(t *testing.T, name string, want labels) uint64 {
	t.Helper()
	m := findMetric(t, name, want)
	if m == nil || m.Histogram == nil {
		return 0
	}
	return m.Histogram.GetSampleCount()
}

func findMetric(t *testing.T, name string, want labels) *dto.Metric {
	t.Helper()
	mfs, err := prometheus.DefaultGatherer.Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.Metric {
			if labelsMatch(m, want) {
				return m
			}
		}
	}
	return nil
}

func labelsMatch(m *dto.Metric, want labels) bool {
	got := make(map[string]string, len(m.Label))
	for _, lp := range m.Label {
		got[lp.GetName()] = lp.GetValue()
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}
