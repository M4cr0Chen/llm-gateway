package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// RequestLogger returns middleware that logs every request using the provided
// slog.Logger. It attaches a child logger (with request_id) and an empty
// *RequestInfo to the request context so downstream code can retrieve the
// logger via LoggerFromContext and populate request-scoped fields via the
// setter helpers (SetOrgKey, SetModel, SetProvider, SetTokens,
// MarkRateLimited).
//
// After next.ServeHTTP returns, the middleware reads the RequestInfo and
// emits a single INFO "request completed" line. Zero-value fields are
// omitted so unauthenticated and non-LLM requests don't carry empty
// org_id/model/etc. attributes.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			reqID := chimw.GetReqID(r.Context())
			child := logger.With(slog.String("request_id", reqID))

			info := &RequestInfo{}
			ctx := WithLogger(r.Context(), child)
			ctx = WithRequestInfo(ctx, info)
			r = r.WithContext(ctx)

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			if reqID != "" {
				ww.Header().Set("X-Request-Id", reqID)
			}

			next.ServeHTTP(ww, r)

			attrs := []slog.Attr{
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000.0),
				slog.Int("bytes", ww.BytesWritten()),
			}
			attrs = appendRequestInfoAttrs(attrs, info)
			child.LogAttrs(context.Background(), slog.LevelInfo, "request completed", attrs...)
		})
	}
}

// appendRequestInfoAttrs appends non-zero RequestInfo fields to attrs.
// Token counts are emitted only when at least one is non-zero; total is
// derived so dashboards don't need to compute it.
func appendRequestInfoAttrs(attrs []slog.Attr, info *RequestInfo) []slog.Attr {
	if info == nil {
		return attrs
	}
	if info.OrgID != "" {
		attrs = append(attrs, slog.String("org_id", info.OrgID))
	}
	if info.KeyID != "" {
		attrs = append(attrs, slog.String("key_id", info.KeyID))
	}
	if info.Model != "" {
		attrs = append(attrs, slog.String("model", info.Model))
	}
	if info.Provider != "" {
		attrs = append(attrs, slog.String("provider", info.Provider))
	}
	if info.Strategy != "" {
		attrs = append(attrs, slog.String("strategy", info.Strategy))
	}
	if info.Group != "" {
		attrs = append(attrs, slog.String("group", info.Group))
	}
	if info.PromptTokens > 0 || info.CompletionTokens > 0 {
		attrs = append(attrs,
			slog.Int("prompt_tokens", info.PromptTokens),
			slog.Int("completion_tokens", info.CompletionTokens),
			slog.Int("total_tokens", info.PromptTokens+info.CompletionTokens),
		)
	}
	if info.RateLimited {
		attrs = append(attrs, slog.Bool("rate_limited", true))
	}
	return attrs
}
