# Data Model

This document describes all database schemas used by the gateway.

## PostgreSQL

### organizations

Represents a tenant (team, company, or project) that uses the gateway.

```sql
CREATE TABLE organizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    monthly_budget_usd DECIMAL(10, 2),        -- NULL means no budget limit
    total_budget_usd DECIMAL(10, 2),           -- NULL means no total limit
    budget_alert_threshold DECIMAL(3, 2) DEFAULT 0.80,  -- alert at 80%
    budget_action TEXT DEFAULT 'block',         -- 'warn', 'throttle', 'block'
    created_at TIMESTAMPTZ DEFAULT NOW(),
    updated_at TIMESTAMPTZ DEFAULT NOW()
);
```

### api_keys

API keys for authentication. Keys are hashed with SHA-256; plaintext is never stored.

```sql
CREATE TABLE api_keys (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    key_hash TEXT NOT NULL UNIQUE,              -- SHA-256 of the plaintext key
    key_prefix TEXT NOT NULL,                   -- first 8 chars for identification (sk-gw-xxxx)
    org_id UUID NOT NULL REFERENCES organizations(id),
    name TEXT NOT NULL,                         -- human-readable label
    scopes TEXT[] DEFAULT '{}',                 -- future: fine-grained permissions
    rate_limit_rpm INT DEFAULT 60,             -- requests per minute (NULL = use org default)
    rate_limit_tpm INT DEFAULT 100000,         -- tokens per minute (NULL = use org default)
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ,                    -- NULL means no expiration
    last_used_at TIMESTAMPTZ
);

CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);
CREATE INDEX idx_api_keys_org ON api_keys(org_id);
```

**Key format:** `sk-gw-{32 random alphanumeric chars}`

**Key lifecycle:**
1. Admin creates key via `POST /internal/admin/keys`
2. Gateway generates random key, returns plaintext once, stores SHA-256 hash
3. On each request, gateway hashes the provided key and looks up the hash
4. Keys can be revoked via `DELETE /internal/admin/keys/{id}` (sets `is_active = false`)

### usage_records

Per-request usage tracking. Partitioned by month for query performance.

```sql
CREATE TABLE usage_records (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    request_id TEXT NOT NULL,
    org_id UUID NOT NULL REFERENCES organizations(id),
    key_id UUID NOT NULL REFERENCES api_keys(id),
    model TEXT NOT NULL,
    provider TEXT NOT NULL,
    prompt_tokens INT NOT NULL,
    completion_tokens INT NOT NULL,
    total_tokens INT NOT NULL,
    cost_usd DECIMAL(10, 6) NOT NULL,
    cached BOOLEAN DEFAULT FALSE,
    latency_ms INT NOT NULL,
    status_code INT NOT NULL,
    routing_strategy TEXT,
    experiment TEXT,                            -- A/B experiment name if any
    created_at TIMESTAMPTZ DEFAULT NOW()
) PARTITION BY RANGE (created_at);

-- Create partitions for each month
-- Example: CREATE TABLE usage_records_2025_01 PARTITION OF usage_records
--          FOR VALUES FROM ('2025-01-01') TO ('2025-02-01');

CREATE INDEX idx_usage_org_created ON usage_records(org_id, created_at);
CREATE INDEX idx_usage_key_created ON usage_records(key_id, created_at);
CREATE INDEX idx_usage_request_id ON usage_records(request_id);  -- for idempotent inserts
```

### semantic_cache

Stores cached LLM responses with their embeddings for semantic similarity search.

```sql
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE semantic_cache (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    input_hash TEXT NOT NULL,                  -- SHA-256 of normalized input (exact match fast path)
    embedding vector(1536) NOT NULL,           -- pgvector embedding
    model TEXT NOT NULL,                       -- model used for this response
    temperature FLOAT,                         -- temperature used (affects TTL)
    request_body JSONB NOT NULL,               -- original request (for cache validation)
    response_body JSONB NOT NULL,              -- cached response
    token_count INT NOT NULL,                  -- total tokens in cached response
    hit_count INT DEFAULT 0,                   -- number of cache hits
    created_at TIMESTAMPTZ DEFAULT NOW(),
    expires_at TIMESTAMPTZ NOT NULL,
    last_hit_at TIMESTAMPTZ
);

CREATE INDEX idx_cache_hash ON semantic_cache(input_hash);
CREATE INDEX idx_cache_expires ON semantic_cache(expires_at);
CREATE INDEX idx_cache_embedding ON semantic_cache
    USING ivfflat (embedding vector_cosine_ops) WITH (lists = 100);
```

**Cache lookup algorithm:**
1. Compute SHA-256 of normalized input → look up by `input_hash` (exact match, fast)
2. If no exact match, compute embedding → query pgvector for nearest neighbor where `cosine_similarity >= 0.95`
3. If match found and not expired → return cached response, increment `hit_count`
4. If no match → cache miss, proceed to provider

## ClickHouse

Used for analytics queries. Data flows from Kafka consumer.

### usage_events

```sql
CREATE TABLE usage_events (
    request_id String,
    timestamp DateTime,
    org_id String,
    key_id String,
    model String,
    provider String,
    prompt_tokens UInt32,
    completion_tokens UInt32,
    total_tokens UInt32,
    cost_usd Float64,
    latency_ms UInt32,
    status_code UInt16,
    cached UInt8,
    routing_strategy String,
    experiment String
) ENGINE = MergeTree()
ORDER BY (org_id, timestamp)
PARTITION BY toYYYYMM(timestamp);
```

## Redis

Redis is used for ephemeral, high-frequency data. No persistence required.

### Key Patterns

| Key Pattern | Type | TTL | Purpose |
|-------------|------|-----|---------|
| `ratelimit:{org_id}:{key_id}:rpm:{window}` | Sorted Set | 2 min | RPM sliding window |
| `ratelimit:{org_id}:{key_id}:tpm:{window}` | Sorted Set | 2 min | TPM sliding window |
| `budget:{org_id}:{year}:{month}` | String (float) | 60s | Current month spend cache |
| `apikey:{key_hash}` | String (JSON) | 5 min | API key metadata cache |
| `embedding:{input_sha256}` | String (binary) | 24h | Cached embedding vectors |
| `provider:health:{name}` | Hash | none | Provider health status |

### Rate Limit Algorithm (Sliding Window)

Using Redis sorted sets with timestamps as scores:

```
ZADD ratelimit:{key}:rpm:{window} {timestamp} {request_id}
ZREMRANGEBYSCORE ratelimit:{key}:rpm:{window} -inf {timestamp - 60s}
ZCARD ratelimit:{key}:rpm:{window}
```

Wrapped in a Lua script for atomicity:

```lua
-- rate_limit.lua
local key = KEYS[1]
local now = tonumber(ARGV[1])
local window = tonumber(ARGV[2])
local limit = tonumber(ARGV[3])
local request_id = ARGV[4]

-- Remove expired entries
redis.call('ZREMRANGEBYSCORE', key, '-inf', now - window)
-- Count current entries
local count = redis.call('ZCARD', key)
if count >= limit then
    return 0  -- rate limited
end
-- Add new entry
redis.call('ZADD', key, now, request_id)
redis.call('EXPIRE', key, window * 2)
return 1  -- allowed
```

## Entity Relationship

```
organizations 1──────N api_keys
      │                    │
      │                    │
      N                    N
usage_records ◄──────── (via org_id, key_id)

semantic_cache (independent, keyed by input content)
```

## Migration Strategy

- Migrations live in `migrations/` directory
- Named: `{sequence}_{description}.up.sql` and `{sequence}_{description}.down.sql`
- Run with `golang-migrate`: `migrate -path migrations -database $DATABASE_URL up`
- Every migration must have a corresponding down migration
- Never modify an existing migration after it has been applied — create a new one

### Migration Files

```
migrations/
├── 001_create_organizations.up.sql
├── 001_create_organizations.down.sql
├── 002_create_api_keys.up.sql
├── 002_create_api_keys.down.sql
├── 003_create_usage_records.up.sql
├── 003_create_usage_records.down.sql
├── 004_create_semantic_cache.up.sql
├── 004_create_semantic_cache.down.sql
├── 005_add_budget_fields.up.sql
└── 005_add_budget_fields.down.sql
```
