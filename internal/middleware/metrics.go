package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/M4cr0Chen/llm-gateway/internal/metrics"
)

// Observe returns middleware that records per-request Prometheus metrics
// (counter + latency histogram). It must be registered at the root of the
// chi chain — typically as the second middleware after RequestID — so it
// captures every request, including those rejected downstream by auth or
// rate limiting (401/429).
//
// The chi-matched route pattern is read via chi.RouteContext after the
// inner handler returns. A request that 404s has no matched pattern; we
// label it "unknown" to keep cardinality bounded.
func Observe() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

			next.ServeHTTP(ww, r)

			pathTemplate := "unknown"
			if rc := chi.RouteContext(r.Context()); rc != nil {
				if p := rc.RoutePattern(); p != "" {
					pathTemplate = p
				}
			}
			status := ww.Status()
			if status == 0 {
				// Handler returned without calling WriteHeader; net/http
				// will flush a 200 to the wire by default.
				status = http.StatusOK
			}
			metrics.RecordRequest(r.Method, pathTemplate, status, time.Since(start))
		})
	}
}
