# Architecture

## System Overview

LLM Gateway is a reverse proxy that sits between client applications and LLM providers. It exposes an **OpenAI-compatible API** so clients can use it as a drop-in replacement for direct OpenAI calls, while gaining routing, caching, rate limiting, cost control, and observability features.

## Request Lifecycle

```
1. Client sends POST /v1/chat/completions
   │
2. │→ Request ID Middleware     — assigns X-Request-ID
   │→ Logging Middleware        — attaches slog logger to context
   │→ Auth Middleware           — validates API key, attaches org/key to context
   │→ Rate Limit Middleware     — checks RPM limit (Redis sliding window)
   │→ Cache Middleware          — checks semantic cache (pgvector)
   │       │
   │    [cache hit] → return cached response immediately
   │       │
   │    [cache miss] ↓
   │
3. │→ Chat Handler
   │     │→ Router.Route()      — selects provider via strategy (cost/latency/round-robin)
   │     │    │
   │     │    │→ [circuit breaker open] → try next provider
   │     │    │→ [all circuit breakers open] → return 503
   │     │    │
   │     │→ Provider.ChatCompletion() or Provider.ChatCompletionStream()
   │     │    │
   │     │    │→ Provider Adapter translates request format
   │     │    │→ Sends HTTP request to provider API
   │     │    │→ Translates response back to OpenAI format
   │     │    │
   │     │    │→ [provider error + retryable] → Router tries next provider (max 3)
   │     │    │→ [provider error + non-retryable] → return error to client
   │     │
   │     │→ Token Counter — counts input/output tokens, calculates cost
   │     │→ Cache Store   — stores response in semantic cache (async)
   │     │→ Return response to client
   │
4. │→ Post-response (async)
        │→ Record usage (tokens, cost, latency) → Kafka → PostgreSQL + ClickHouse
        │→ Update TPM rate limit counter (Redis)
        │→ Update budget spend (Redis + PostgreSQL)
        │→ Emit Prometheus metrics
```

> **Implementation Note (post-M3):** Currently implemented: RequestID, request-scoped metrics (`Observe`), panic recovery, request logger, API-key auth, Redis-backed rate limiter, optional DEBUG body capture, Chat Handler with Registry resolution (model name + alias), Provider adapters (OpenAI, Anthropic, Google Gemini), per-provider retry with exponential backoff and jitter via `HealthTrackingProvider` decorator, `GET /internal/health`, `POST/DELETE /internal/admin/keys`, and Prometheus `/metrics` on its own port. Cache, Router strategies, Token Counter / Cost, Budget, and Kafka pipeline are planned for later milestones.

## Component Interaction

```
┌────────────────────────────────────────────────────────────────┐
│                        LLM Gateway Process                     │
│                                                                │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────┐    │
│  │  Config  │  │  Server  │  │ Handler  │  │   Router     │    │
│  │ (koanf)  │→ │  (chi)   │→ │  (chat)  │→ │  (strategy)  │    │
│  └──────────┘  └─────┬────┘  └──────────┘  └──────-┬──────┘    │
│                      │                             │           │
│              ┌───────┴────────┐            ┌───────▼───────┐   │
│              │   Middleware   │            │   Providers   │   │
│              │ ┌────────────┐ │            │ ┌───────────┐ │   │
│              │ │ Auth       │ │            │ │ OpenAI    │ │   │
│              │ │ RateLimit  │ │            │ │ Anthropic │ │   │
│              │ │ Cache      │ │            │ │ Google    │ │   │
│              │ │ Logging    │ │            │ │ SelfHost  │ │   │
│              │ └────────────┘ │            │ └───────────┘ │   │
│              └────────────────┘            └───────────────┘   │
│                                                                │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────┐    │
│  │  Token   │  │  Budget  │  │ Metrics  │  │   Pipeline   │    │
│  │ Counter  │  │ Enforcer │  │ (Prom)   │  │   (Kafka)    │    │
│  └──────────┘  └──────────┘  └──────────┘  └──────────────┘    │
└────────────────────────────────────────────────────────────────┘
         │              │              │              │
         ▼              ▼              ▼              ▼
    ┌─────────┐   ┌──────────┐  ┌──────────┐  ┌──────────┐
    │  Redis  │   │PostgreSQL│  │Prometheus│  │  Kafka   │
    │         │   │(pgvector)│  │          │  │    │     │
    │- rate   │   │- api_keys│  │- metrics │  │    ▼     │
    │  limits │   │- usage   │  │  scrape  │  │ClickHouse│
    │- cache  │   │- cache   │  │          │  │(analytics│
    │  embed  │   │- orgs    │  │          │  │  queries)│
    └─────────┘   └──────────┘  └──────────┘  └──────────┘
```

> **Current (post-M3):** Config, Server, Handler, OpenAI/Anthropic/Google providers, the `HealthTrackingProvider` decorator, Auth + RateLimit middleware (Redis-backed), and Prometheus Metrics are implemented. Router strategies, Cache middleware, Token Counter, Budget Enforcer, and Kafka Pipeline are planned.

## Streaming Data Flow

For streaming requests (`"stream": true`):

```
Client ◄──SSE──┐
               │
         Gateway Stream Interceptor
               │
               ├── Receives chunks from provider via <-chan StreamEvent
               ├── For each chunk:
               │     ├── Forward to client as SSE: "data: {json}\n\n"
               │     ├── Accumulate text for token counting
               │     └── Periodically update TPM counter (every 50 tokens)
               ├── On last chunk:
               │     ├── Inject usage event chunk (token counts)
               │     ├── Send "data: [DONE]\n\n"
               │     └── Record final usage asynchronously
               └── On client disconnect:
                     ├── Cancel upstream context
                     ├── Record partial usage
                     └── Clean up resources
```

## Middleware Chain Order

The order of middleware matters. Defined in `internal/server/server.go`:

```
Global (mounted on all routes):
1. RequestID      — first, so all subsequent middleware/handlers have a request ID
2. Observe        — request-scoped Prometheus metrics + per-request mutable fields
                    (model, provider, token counts) read by the logger and rate limiter
3. PanicRecovery  — catch panics, return 500, log stack trace
4. RequestLogger  — log request start/end, attach slog logger to context

Scoped to /v1/* (public API):
5. RequireAPIKey  — validate API key, inject KeyInfo into context, reject 401 if invalid
6. RateLimit      — RPM check via Redis sliding window; reject 429 with Retry-After
7. DebugBodies    — (opt-in via log.debug_bodies) capture request body at DEBUG
8. CacheCheck     — check semantic cache, short-circuit on hit                  [planned M5]
9. [Handler]      — chat completions / models

Scoped to /internal/admin/*:
- RequireAdminToken — separate token-based auth, gated on configured admin token
```

> **Current (post-M3):** Steps 1–7 are implemented. CacheCheck is planned for M5.
> The Router (M4) sits between the Handler and the Provider Registry — it does not add
> an HTTP middleware; it replaces the direct `registry.Resolve(model)` call inside
> `ChatHandler` with a strategy-aware `Router.Route(ctx, req, meta)`.

## Ports and Endpoints

| Port | Purpose | Endpoints |
|------|---------|-----------|
| 8080 | Public API | `POST /v1/chat/completions`, `GET /v1/models`, `GET /health` |
| 9090 | Internal metrics | `GET /metrics` (Prometheus) |
| 8080 | Internal admin | `GET /internal/health`, `POST /internal/admin/keys`, `GET /internal/admin/usage`, etc. |

Admin endpoints are on the same port but protected by a separate admin token, not regular API keys.

## Configuration Hierarchy

```
configs/gateway.yaml        ← base configuration (committed to repo)
     ▲
     │ overridden by
     │
Environment variables       ← per-environment overrides (GATEWAY_SERVER__PORT, GATEWAY_PROVIDERS__OPENAI__API_KEY, etc.)
     ▲
     │ overridden by
     │
CLI flags (future)          ← per-invocation overrides
```

## Error Handling Strategy

All errors returned to clients follow the OpenAI error format:

```json
{
  "error": {
    "message": "Rate limit exceeded. Try again in 30s.",
    "type": "rate_limit_error",
    "code": "rate_limit_exceeded"
  }
}
```

Internal errors are logged with full context but returned to clients as generic 500 errors (no stack traces, no internal details leaked).

## Graceful Shutdown

On SIGTERM / SIGINT:

1. Stop accepting new connections
2. Wait for active non-streaming requests to complete (timeout: 10s)
3. Send error chunk to active streams, then close (timeout: 30s)
4. Flush pending Kafka events
5. Flush pending usage records to database
6. Close database and Redis connections
7. Exit
