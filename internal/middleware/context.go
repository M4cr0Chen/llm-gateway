package middleware

import (
	"context"
	"log/slog"
)

type contextKey int

const (
	loggerKey contextKey = iota
	requestInfoKey
)

// WithLogger returns a new context with the given logger attached.
func WithLogger(ctx context.Context, logger *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey, logger)
}

// LoggerFromContext returns the logger stored in the context, or the
// default slog logger if none is set.
func LoggerFromContext(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// RequestInfo carries fields populated during request handling so the
// access logger can emit them in a single line. Fields are filled in by
// downstream middlewares and handlers via the setter helpers below.
//
// Lifecycle: the *RequestInfo pointer is allocated and attached to the
// context by RequestLogger before next.ServeHTTP runs; downstream code
// mutates it through that pointer; RequestLogger reads it back after
// next.ServeHTTP returns. Reads and writes are serialised by request
// flow (the logger only reads after the handler chain has fully
// returned), so no mutex is required.
type RequestInfo struct {
	OrgID            string
	KeyID            string
	Model            string
	Provider         string
	Strategy         string
	Group            string
	PromptTokens     int
	CompletionTokens int
	RateLimited      bool
}

// WithRequestInfo returns a new context carrying the given RequestInfo
// pointer. The caller retains ownership; downstream code mutates fields
// through the pointer.
func WithRequestInfo(ctx context.Context, info *RequestInfo) context.Context {
	return context.WithValue(ctx, requestInfoKey, info)
}

// RequestInfoFromContext returns the *RequestInfo attached to the
// context, or nil if none is set. Setters silently no-op on a nil
// holder, so callers that may run outside the RequestLogger chain
// (admin routes, tests) don't need to branch.
func RequestInfoFromContext(ctx context.Context) *RequestInfo {
	if ri, ok := ctx.Value(requestInfoKey).(*RequestInfo); ok {
		return ri
	}
	return nil
}

// SetOrgKey records the authenticated organization and API key IDs on
// the request's RequestInfo. Called from the auth middleware so the
// outer RequestLogger can include org_id/key_id in the access line.
// No-op if no RequestInfo is attached.
func SetOrgKey(ctx context.Context, orgID, keyID string) {
	if ri := RequestInfoFromContext(ctx); ri != nil {
		ri.OrgID = orgID
		ri.KeyID = keyID
	}
}

// SetModel records the resolved model name on the request's RequestInfo.
// No-op if no RequestInfo is attached.
func SetModel(ctx context.Context, model string) {
	if ri := RequestInfoFromContext(ctx); ri != nil {
		ri.Model = model
	}
}

// SetProvider records the resolved provider name on the request's
// RequestInfo. No-op if no RequestInfo is attached.
func SetProvider(ctx context.Context, provider string) {
	if ri := RequestInfoFromContext(ctx); ri != nil {
		ri.Provider = provider
	}
}

// SetTokens records prompt and completion token counts on the request's
// RequestInfo. No-op if no RequestInfo is attached.
func SetTokens(ctx context.Context, prompt, completion int) {
	if ri := RequestInfoFromContext(ctx); ri != nil {
		ri.PromptTokens = prompt
		ri.CompletionTokens = completion
	}
}

// SetStrategy records the routing strategy that picked the provider for
// this request. No-op if no RequestInfo is attached.
func SetStrategy(ctx context.Context, strategy string) {
	if ri := RequestInfoFromContext(ctx); ri != nil {
		ri.Strategy = strategy
	}
}

// SetGroup records the model-group name (empty for concrete or alias
// requests) on the RequestInfo. No-op if no RequestInfo is attached.
func SetGroup(ctx context.Context, group string) {
	if ri := RequestInfoFromContext(ctx); ri != nil {
		ri.Group = group
	}
}

// MarkRateLimited flags the request as rejected by the rate limiter.
// Called from RateLimit middleware on the 429 branch.
func MarkRateLimited(ctx context.Context) {
	if ri := RequestInfoFromContext(ctx); ri != nil {
		ri.RateLimited = true
	}
}
