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
1. RequestID      — first, so all subsequent middleware/handlers have a request ID
2. PanicRecovery  — catch panics, return 500, log stack trace
3. Logging        — log request start, attach logger to context
4. Auth           — validate API key, reject 401 if invalid
5. RateLimit      — check RPM, reject 429 if exceeded
6. CacheCheck     — check semantic cache, short-circuit on hit
7. [Handler]      — actual request processing
```

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
Environment variables       ← per-environment overrides (GATEWAY_SERVER_PORT, etc.)
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
