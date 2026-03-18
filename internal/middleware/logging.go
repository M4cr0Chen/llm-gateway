package middleware

import (
	"log/slog"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// RequestLogger returns middleware that logs every request using the provided
// slog.Logger. It attaches a child logger (with request_id) to the request
// context so downstream handlers can retrieve it via LoggerFromContext.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			reqID := chimw.GetReqID(r.Context())
			child := logger.With(slog.String("request_id", reqID))

			ctx := WithLogger(r.Context(), child)
			r = r.WithContext(ctx)

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			if reqID != "" {
				ww.Header().Set("X-Request-Id", reqID)
			}

			next.ServeHTTP(ww, r)

			child.Info("request completed",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000.0),
				slog.Int("bytes", ww.BytesWritten()),
			)
		})
	}
}
