# LLM Gateway

A smart proxy layer between clients and LLM providers (OpenAI, Anthropic, Google, self-hosted),
handling unified API, routing, caching, rate limiting, cost control, and observability.

## Architecture

```
Clients (internal services, web apps)
          │
          ▼
    Load Balancer
          │
          ▼
  ┌─── LLM Gateway ───────────────────────────────────────────┐
  │                                                            │
  │  Auth → Rate Limit → Cache Check → Router → Provider      │
  │  Middleware   (Redis)   (pgvector)   (strategy)  Adapter   │
  │                                                    │       │
  │  Token Accounting ← Response Streaming ◄───────────┘       │
  │       │                                                    │
  └───────┼────────────────────────────────────────────────────┘
          │                          │
          ▼                          ▼
    Async Pipeline             LLM Providers
    (Kafka → ClickHouse)       ├── OpenAI
          │                    ├── Anthropic
          ▼                    ├── Google Gemini
    PostgreSQL + Redis         └── Self-hosted (vLLM/Ollama)
    (API keys, usage,
     cache, rate limits)
```

## Tech Stack

| Component | Choice | Notes |
|-----------|--------|-------|
| Language | Go 1.23+ | |
| HTTP Router | chi | stdlib-compatible, `func(http.Handler) http.Handler` middleware |
| Config | koanf | YAML file + env var overlay |
| Database | PostgreSQL 16 + pgvector | API keys, usage records, semantic cache vectors |
| Cache / Rate Limit | Redis 7 | Sliding window counters, cached embeddings |
| Message Queue | Kafka | segmentio/kafka-go (pure Go, no CGo) |
| Analytics DB | ClickHouse | Time-series aggregation for usage dashboards |
| Monitoring | Prometheus + Grafana | Metrics on port 9090 |
| Logging | slog (stdlib) | JSON structured logging |
| DB Migration | golang-migrate | SQL migrations in migrations/ |
| HTTP Client | net/http | No SDK — direct control over streaming, timeouts, cancellation |
| Token Counting | tiktoken-go | Accurate for OpenAI, reasonable for others |

## Project Structure

```
llm-gateway/
├── cmd/gateway/main.go          # Entry point: wires config, providers, router, server
├── internal/
│   ├── config/                  # Configuration loading (YAML + env)
│   ├── server/                  # HTTP server setup, middleware chain
│   ├── handler/                 # HTTP handlers (chat completions, models, health)
│   ├── middleware/              # Auth, rate limiting, request ID, logging
│   ├── model/                   # Domain types: Request, Response, Message, Usage
│   ├── provider/                # Provider adapters
│   │   ├── provider.go          # Provider interface + StreamEvent + Registry
│   │   ├── openai/              # OpenAI adapter
│   │   ├── anthropic/           # Anthropic Claude adapter
│   │   ├── google/              # Google Gemini adapter
│   │   └── selfhosted/          # vLLM / Ollama adapter
│   ├── router/                  # Smart routing: strategies, fallback, circuit breaker
│   │   └── router.go            # Router interface + Strategy interface
│   ├── cache/                   # Semantic cache
│   │   ├── embedder.go          # Embedder interface
│   │   └── store.go             # CacheStore interface (pgvector)
│   ├── ratelimit/               # Redis-backed rate limiter
│   │   └── limiter.go           # Limiter interface
│   ├── auth/                    # API key validation, org resolution
│   ├── token/                   # Token counting, cost calculation
│   ├── budget/                  # Budget enforcement per org
│   ├── streaming/               # SSE proxy, mid-stream token counting
│   ├── pipeline/                # Kafka producers / consumers
│   ├── metrics/                 # Prometheus metric collectors
│   └── store/                   # Database access layer (PostgreSQL, ClickHouse)
├── pkg/openaicompat/            # Exported OpenAI-compatible types (importable by clients)
├── migrations/                  # SQL migration files (golang-migrate)
├── deployments/
│   ├── docker/                  # Dockerfile, docker-compose.yml
│   └── k8s/                     # Kubernetes manifests (Kustomize)
├── scripts/                     # Helper scripts (seed data, load tests)
├── tests/
│   ├── integration/             # End-to-end tests
│   └── load/                    # k6 / vegeta load test scripts
├── configs/gateway.yaml         # Default configuration
├── docs/                        # Design documents (see below)
├── go.mod
├── go.sum
└── Makefile
```

## Key Interfaces

### Provider (internal/provider/provider.go)

```go
type Provider interface {
    Name() string
    ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error)
    ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error)
    Models() []string
}

type StreamEvent struct {
    Chunk *model.ChatCompletionChunk
    Err   error
}

type Registry struct { /* maps model names to providers */ }
func (r *Registry) Resolve(modelName string) (Provider, error)
func (r *Registry) Register(p Provider, models []string)
```

### Router (internal/router/router.go)

```go
type Router interface {
    Route(ctx context.Context, req *model.ChatCompletionRequest, meta RequestMeta) (provider.Provider, error)
}

type Strategy interface {
    Select(candidates []provider.Provider, req *model.ChatCompletionRequest, meta RequestMeta) (provider.Provider, error)
}
```

### CacheStore (internal/cache/store.go)

```go
type CacheStore interface {
    Get(ctx context.Context, normalizedInput string, embedding []float32, model string) (*model.ChatCompletionResponse, bool, error)
    Set(ctx context.Context, normalizedInput string, embedding []float32, req *model.ChatCompletionRequest, resp *model.ChatCompletionResponse, ttl time.Duration) error
    Invalidate(ctx context.Context, inputHash string) error
}
```

### Limiter (internal/ratelimit/limiter.go)

```go
type Limiter interface {
    AllowRequest(ctx context.Context, key KeyInfo) (allowed bool, retryAfter time.Duration, err error)
    RecordTokens(ctx context.Context, key KeyInfo, tokens int) error
}
```

## Coding Conventions

- **Error handling**: Return errors, never panic. Wrap with `fmt.Errorf("doing X: %w", err)`.
- **Naming**: Go standard — camelCase unexported, PascalCase exported. No stuttering (e.g., `provider.Provider`, not `provider.ProviderInterface`).
- **Context**: Always pass `context.Context` as the first parameter.
- **Logging**: Use `slog` from request context. Never log message content (PII) at INFO level. DEBUG only with `log.debug_bodies: true`.
- **Testing**: Table-driven tests. Use `testify/assert`. Mock HTTP servers with `httptest.Server`. Use `testify/require` for fatal assertions.
- **Middleware**: Standard Go signature `func(http.Handler) http.Handler`.
- **Configuration**: All config via `configs/gateway.yaml` + env var overrides. Env vars use `__` (double underscore) as hierarchy separator: `GATEWAY_SERVER__PORT`, `GATEWAY_PROVIDERS__OPENAI__API_KEY`, etc. Secrets (API keys) must only be set via env vars, never in YAML.
- **Dependencies**: Prefer stdlib. Only add external dependencies when they provide clear value.
- **Streaming**: Use `<-chan StreamEvent` for provider → handler communication. Always respect `ctx.Done()`.

## Common Commands

```bash
make build           # Build binary to ./bin/gateway
make run             # Build and run locally
make test            # Run all tests
make lint            # Run golangci-lint
make docker-build    # Build Docker image
docker compose up    # Start full local stack (gateway + PG + Redis + Prometheus + Grafana)
docker compose down  # Stop all services
make migrate-up      # Run database migrations
make migrate-down    # Rollback last migration
make load-test       # Run load tests (k6)
```

## Development Guides

Before working on a specific area, read the corresponding design document:

| Task | Read first |
|------|-----------|
| Implement a new provider adapter | `docs/provider-adapter.md` |
| Modify routing logic | `docs/routing-strategies.md` |
| Change database schema | `docs/data-model.md` |
| Work on semantic cache | `docs/semantic-cache.md` |
| Work on streaming | `docs/streaming.md` |
| Work on Kafka pipeline | `docs/kafka-pipeline.md` |
| Understand a technical decision | `docs/adr/` |
| Understand the full architecture | `docs/architecture.md` |
| Understand the API contract | `docs/api-spec.md` |

Side Notes on Development:

- Don't add signature in commit messages/pr. The messages should only focus on the work, not authorship.

## Current Status

- [x] Milestone 0: Documentation Bootstrap
- [x] Milestone 1: Transparent Proxy (Foundation)
- [ ] Milestone 2: Multi-Provider Support ← CURRENT
- [ ] Milestone 3: Auth, Rate Limiting & Observability
- [ ] Milestone 4: Smart Router with Fallback & A/B Routing
- [ ] Milestone 5: Semantic Cache
- [ ] Milestone 6: Token Accounting & Budget Enforcement
- [ ] Milestone 7: Async Pipeline (Kafka)
- [ ] Milestone 8: Streaming Enhancements
- [ ] Milestone 9: Self-Hosted Model Support
- [ ] Milestone 10: Production Hardening

See `ROADMAP.md` for the full development roadmap with issue details.
