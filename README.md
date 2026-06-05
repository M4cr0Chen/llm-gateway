# LLM Gateway

A smart proxy layer between clients and LLM providers (OpenAI, Anthropic,
Google, self-hosted), handling a unified API, routing, caching, rate
limiting, cost control, and observability.

Design rationale, architecture diagrams, and roadmap live in
[`CLAUDE.md`](CLAUDE.md), [`docs/architecture.md`](docs/architecture.md),
and [`ROADMAP.md`](ROADMAP.md).

## Quickstart

Bring up the full local stack — gateway, Postgres, Redis, Prometheus,
Grafana — with one make target:

```bash
cp .env.example .env
# Edit .env and set GATEWAY_PROVIDERS__OPENAI__API_KEY (or another
# provider key). Defaults wired into the compose stack handle the rest.

make compose-up
```

Five containers should report healthy within ~10 seconds. The gateway
runs database migrations automatically on first boot.

### Smoke test

```bash
# 1. Gateway health
curl -s localhost:8080/health
# → {"status":"ok"}

# 2. Seed an organization, then mint an API key for it. The admin token
#    is preset to "dev-admin-token" in deployments/docker/docker-compose.yml.
docker compose -f deployments/docker/docker-compose.yml exec postgres \
  psql -U gateway -d gateway -c \
  "INSERT INTO organizations (id, name) VALUES ('org-dev', 'dev') ON CONFLICT DO NOTHING;"

curl -s -X POST localhost:8080/internal/admin/keys \
  -H "Authorization: Bearer dev-admin-token" \
  -H "Content-Type: application/json" \
  -d '{"org_id":"org-dev","name":"smoke"}'
# → {"id":"key-…","plaintext":"sk-…","org_id":"org-dev",…}

# 3. Issue a chat completion with the minted key.
curl -s -X POST localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer <plaintext-from-step-2>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'
```

### Observability

- **Prometheus** → http://localhost:9091 (queries against
  `gateway_requests_total`, `gateway_request_duration_seconds`, etc.)
- **Grafana** → http://localhost:3000 (`admin` / `admin`). The
  *LLM Gateway* dashboard is auto-provisioned with request rate,
  latency P50/P95/P99, error rate, and provider health panels.

### Tear down

```bash
make compose-down   # stops services AND drops volumes
```

Use `docker compose -f deployments/docker/docker-compose.yml down` (no
`-v`) to preserve Postgres / Grafana state between runs.

## Development

```bash
make build          # → bin/gateway
make run            # build and run with configs/gateway.yaml + env
make test           # full test suite (-race)
make lint           # golangci-lint
make docker-build   # build the gateway image
```

### Database migrations

The gateway applies pending migrations on startup
(`GATEWAY_DATABASE__POSTGRES__AUTO_MIGRATE=true`), so most workflows
need nothing extra. For authoring new migrations:

```bash
# One-time install:
go install -tags 'postgres' \
  github.com/golang-migrate/migrate/v4/cmd/migrate@latest

make migrate-create NAME=add_widgets_table
# → migrations/000N_add_widgets_table.{up,down}.sql

# Apply or roll back manually against a running DB:
export GATEWAY_DATABASE__POSTGRES__DSN=postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable
make migrate-up
make migrate-down
```

### Where things live

| Area | Pointer |
|------|---------|
| Architecture overview | [`docs/architecture.md`](docs/architecture.md) |
| API contract | [`docs/api-spec.md`](docs/api-spec.md) |
| Adding a provider | [`docs/provider-adapter.md`](docs/provider-adapter.md) |
| Data model | [`docs/data-model.md`](docs/data-model.md) |
| Technical decisions | [`docs/adr/`](docs/adr/) |
| Coding conventions | [`CLAUDE.md`](CLAUDE.md) |
| Milestone status | [`ROADMAP.md`](ROADMAP.md) |
