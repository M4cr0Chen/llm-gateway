# API Specification

The gateway exposes an **OpenAI-compatible API**. Any client that works with the OpenAI API should work with this gateway by only changing the base URL.

## Implementation Status

| Endpoint | Status | Milestone |
|----------|--------|-----------|
| `POST /v1/chat/completions` | Implemented | M1 |
| `GET /v1/models` | Implemented | M1 |
| `GET /health` | Implemented | M1 |
| `GET /internal/health` | Planned | M2 |
| `POST /internal/admin/keys` | Planned | M3 |
| `DELETE /internal/admin/keys/{id}` | Planned | M3 |
| `GET /internal/admin/usage` | Planned | M6 |
| `GET /internal/admin/cache/stats` | Planned | M5 |
| `GET /internal/admin/budgets` | Planned | M6 |
| `PUT /internal/admin/budgets/{org_id}` | Planned | M6 |
| `GET /internal/admin/experiments/{name}/stats` | Planned | M4 |
| `GET /internal/admin/analytics/usage` | Planned | M7 |

## Public Endpoints

### POST /v1/chat/completions

Create a chat completion. Supports both streaming and non-streaming modes.

**Request Headers:**
```
Authorization: Bearer sk-gateway-xxxx    # validated by Auth middleware (planned M3)
Content-Type: application/json
X-Cache-Control: no-cache                # optional, bypass semantic cache (planned M5)
```

**Request Body:**
```json
{
  "model": "gpt-4o",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ],
  "temperature": 0.7,
  "max_tokens": 1000,
  "stream": false,
  "top_p": 1.0,
  "n": 1,
  "stop": ["\n"],
  "presence_penalty": 0.0,
  "frequency_penalty": 0.0,
  "user": "user-123"
}
```

**Model field behavior:**
- Specific model name (e.g., `"gpt-4o"`, `"claude-sonnet-4-20250514"`) → routes to the provider that serves this model (implemented M1)
- Model alias (e.g., `"claude"`) → resolves via `model_aliases` config (planned M2)
- Model group (e.g., `"fast"`, `"smart"`) → router selects best provider based on strategy (planned M4)

**Non-streaming response (200 OK):**
```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4o",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help you today?"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 20,
    "completion_tokens": 9,
    "total_tokens": 29
  },
  "system_fingerprint": "fp_abc123"
}
```

**Streaming response (200 OK, Content-Type: text/event-stream):**
```
data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}

data: {"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}

data: [DONE]
```

**Gateway-specific response headers:**
```
X-Request-ID: req-uuid-here               # implemented (M1)
X-LLM-Gateway-Provider: openai            # implemented (M1)
X-LLM-Gateway-Attempts: 1                 # planned (M4, fallback routing)
X-Cache: HIT | MISS | BYPASS              # planned (M5, semantic cache)
X-Cache-Lookup-Time: 5ms                  # planned (M5)
X-Cost-USD: 0.000375                      # planned (M6, token accounting)
X-Tokens-Input: 20                        # planned (M6)
X-Tokens-Output: 9                        # planned (M6)
X-RateLimit-Limit-Requests: 60            # planned (M3, rate limiting)
X-RateLimit-Remaining-Requests: 42        # planned (M3)
X-RateLimit-Reset-Requests: 30s           # planned (M3)
X-Budget-Warning: 85% of monthly budget used  # planned (M6)
```

### GET /v1/models

List all available models across all configured providers.

**Response (200 OK):**
```json
{
  "object": "list",
  "data": [
    {"id": "gpt-4o", "object": "model", "owned_by": "openai"},
    {"id": "gpt-4o-mini", "object": "model", "owned_by": "openai"},
    {"id": "claude-sonnet-4-20250514", "object": "model", "owned_by": "anthropic"},
    {"id": "gemini-2.0-flash", "object": "model", "owned_by": "google"}
  ]
}
```

### GET /health

Health check endpoint. No authentication required.

**Response (200 OK):**
```json
{"status": "ok"}
```

## Error Responses

All errors follow the OpenAI error format:

```json
{
  "error": {
    "message": "Human-readable error description",
    "type": "error_type",
    "code": "error_code"
  }
}
```

| Status | Type | Code | When |
|--------|------|------|------|
| 400 | `invalid_request_error` | `invalid_model` | Unknown model name |
| 400 | `invalid_request_error` | `invalid_request` | Malformed request body |
| 401 | `authentication_error` | `invalid_api_key` | Missing or invalid API key |
| 402 | `billing_error` | `budget_exceeded` | Organization budget exhausted |
| 413 | `invalid_request_error` | `request_too_large` | Request body > 10MB |
| 429 | `rate_limit_error` | `rate_limit_exceeded` | RPM or TPM limit exceeded |
| 500 | `internal_error` | `internal_error` | Unexpected server error |
| 502 | `upstream_error` | `provider_error` | All providers returned errors |
| 503 | `service_unavailable` | `all_providers_down` | All provider circuit breakers open |

**429 response includes:**
```
Retry-After: 30
```

**402 response body includes:**
```json
{
  "error": {
    "message": "Monthly budget exceeded. Current spend: $500.00, Budget: $500.00. Resets: 2025-02-01T00:00:00Z",
    "type": "billing_error",
    "code": "budget_exceeded"
  }
}
```

## Admin Endpoints

All admin endpoints require the admin token: `Authorization: Bearer <GATEWAY_ADMIN_TOKEN>`.
These are prefixed with `/internal/admin/`.

### GET /internal/health

Detailed health check showing provider status.

```json
{
  "status": "ok",
  "providers": {
    "openai": {"healthy": true, "latency_p50_ms": 450},
    "anthropic": {"healthy": true, "latency_p50_ms": 520},
    "google": {"healthy": false, "last_error": "timeout", "last_failure": "2025-01-15T10:30:00Z"}
  }
}
```

### POST /internal/admin/keys

Create a new API key. Returns the key only once.

**Request:**
```json
{
  "org_id": "org-uuid",
  "name": "production-key",
  "rate_limit_rpm": 120,
  "rate_limit_tpm": 200000
}
```

**Response (201 Created):**
```json
{
  "id": "key-uuid",
  "key": "sk-gateway-abc123xyz",
  "name": "production-key",
  "created_at": "2025-01-15T10:30:00Z"
}
```

### DELETE /internal/admin/keys/{id}

Revoke an API key. Returns 204 No Content.

### GET /internal/admin/usage

Query usage records.

**Query params:** `org_id`, `key_id`, `start`, `end`, `group_by` (model|key|day)

**Response:**
```json
{
  "data": [
    {"model": "gpt-4o", "requests": 1500, "tokens": 450000, "cost_usd": 3.75},
    {"model": "claude-sonnet-4-20250514", "requests": 800, "tokens": 240000, "cost_usd": 4.32}
  ],
  "summary": {"total_requests": 2300, "total_tokens": 690000, "total_cost_usd": 8.07}
}
```

### GET /internal/admin/cache/stats

Cache statistics.

```json
{
  "entries": 15234,
  "hit_rate": 0.34,
  "avg_lookup_ms": 12,
  "size_mb": 45.2
}
```

### GET /internal/admin/budgets

List all organization budgets.

### PUT /internal/admin/budgets/{org_id}

Update organization budget.

### GET /internal/admin/experiments/{name}/stats

A/B experiment statistics.

### GET /internal/admin/analytics/usage

ClickHouse-backed analytics. Query params: `org_id`, `period` (7d, 30d), `granularity` (1m, 5m, 1h, 1d).

## Configuration Reference

```yaml
server:
  port: 8080                    # API port
  metrics_port: 9090            # Prometheus metrics port
  read_timeout: 30s
  write_timeout: 120s           # long for streaming responses

providers:
  openai:
    # API keys are set via env vars only, never in YAML:
    #   GATEWAY_PROVIDERS__OPENAI__API_KEY=sk-xxx
    base_url: "https://api.openai.com/v1"
    timeout: 30s
    max_retries: 2
    retry_backoff: 1s
    models:
      - gpt-4o
      - gpt-4o-mini
  anthropic:
    # GATEWAY_PROVIDERS__ANTHROPIC__API_KEY=sk-ant-xxx
    base_url: "https://api.anthropic.com"
    timeout: 30s
    max_retries: 2
    models:
      - claude-sonnet-4-20250514
      - claude-haiku-35-20241022
  google:
    # GATEWAY_PROVIDERS__GOOGLE__API_KEY=AIza-xxx
    timeout: 30s
    models:
      - gemini-2.0-flash
      - gemini-2.0-pro
  selfhosted:
    endpoints:
      - name: "vllm-cluster"
        base_url: "http://vllm:8000/v1"
        models: [llama-3.1-70b-instruct]
        max_concurrent: 10

model_aliases:
  "gpt4": "gpt-4o"
  "claude": "claude-sonnet-4-20250514"
  "gemini": "gemini-2.0-flash"

routing:
  default_strategy: "cost_optimized"   # cost_optimized | latency_optimized | round_robin | weighted | priority
  model_groups:
    "fast":
      - { provider: openai, model: gpt-4o-mini, cost_per_1k_input: 0.00015, cost_per_1k_output: 0.0006 }
      - { provider: google, model: gemini-2.0-flash, cost_per_1k_input: 0.0001, cost_per_1k_output: 0.0004 }
    "smart":
      - { provider: openai, model: gpt-4o, cost_per_1k_input: 0.0025, cost_per_1k_output: 0.01 }
      - { provider: anthropic, model: claude-sonnet-4-20250514, cost_per_1k_input: 0.003, cost_per_1k_output: 0.015 }
  prefer_selfhosted: true
  experiments: []

cache:
  enabled: true
  similarity_threshold: 0.95
  ttl: 1h
  max_entries: 100000
  embedding_model: "text-embedding-3-small"
  cacheable_models: [gpt-4o-mini, gemini-2.0-flash]

rate_limit:
  enabled: true
  default_rpm: 60
  default_tpm: 100000

auth:
  admin_token: "${GATEWAY_ADMIN_TOKEN}"

pricing:
  openai:
    gpt-4o: { input_per_1k: 0.0025, output_per_1k: 0.01 }
    gpt-4o-mini: { input_per_1k: 0.00015, output_per_1k: 0.0006 }
  anthropic:
    claude-sonnet-4-20250514: { input_per_1k: 0.003, output_per_1k: 0.015 }
  google:
    gemini-2.0-flash: { input_per_1k: 0.0001, output_per_1k: 0.0004 }

database:
  postgres_url: "${DATABASE_URL}"
  max_connections: 20
  redis_url: "${REDIS_URL}"

kafka:
  brokers: ["localhost:9092"]
  topics:
    usage: "llm-gateway.usage"
    errors: "llm-gateway.errors"
    audit: "llm-gateway.audit"

log:
  level: info                   # debug | info | warn | error
  format: json
  debug_bodies: false           # log request/response bodies (PII risk!)
```
