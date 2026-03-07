# ADR-001: Use chi over gin for HTTP routing

## Status
Accepted

## Context
Need to choose an HTTP router framework for the gateway. The framework will be used for all API endpoints, middleware chain, and request handling. Key requirements: high performance, composable middleware, streaming support (SSE).

## Decision
Use [chi](https://github.com/go-chi/chi).

## Rationale
- chi uses standard `net/http` types: `http.Handler`, `http.HandlerFunc`, `http.ResponseWriter`
- Middleware signature is `func(http.Handler) http.Handler` — the Go standard pattern
- Any third-party middleware compatible with `net/http` works out of the box
- Route parameters via `chi.URLParam(r, "id")` — no custom context type
- Performance is comparable to gin in benchmarks
- Lightweight — minimal dependencies

## Consequences
- All middleware must follow `func(http.Handler) http.Handler` signature
- Request/response handled via standard `http.ResponseWriter` and `*http.Request`
- No automatic JSON binding — use `json.NewDecoder(r.Body).Decode(&req)` (this is fine; explicit is better)
- SSE streaming works naturally with `http.Flusher` interface

## Alternatives Rejected

### gin
- Uses custom `gin.Context` which wraps `http.Request` and `http.ResponseWriter`
- Middleware signature is `func(*gin.Context)` — not compatible with stdlib
- JSON binding is convenient but creates vendor lock-in
- Harder to test with standard Go testing patterns

### gorilla/mux
- Archived / no longer actively maintained as of late 2022
- Heavier than chi with similar functionality

### Standard library only (Go 1.22+ ServeMux)
- Go 1.22 added method-based routing (`GET /path/{id}`)
- Viable option, but chi adds useful features: middleware chaining, route groups, URL parameters in nested routes
- Could migrate to stdlib in the future with minimal effort since chi is stdlib-compatible
