# Smoke Test: API Key Authentication (Issue 3.1)

End-to-end verification that the auth middleware, admin endpoints, and
embedded migrations work against a real Postgres. Takes about 5 minutes.

## 0. Prerequisites

- Docker, **or** a local Postgres 16+ with `gen_random_uuid()` available.
- An OpenAI API key — only needed for the "use key → 200" path; everything else works without one.
- Two terminals: one for the gateway, one for `curl` / `psql`.

> macOS note: many developers already have Postgres on `:5432` (Postgres.app / Homebrew). The instructions below put the smoke-test container on `:5433` to avoid a port clash.

## 1. Start Postgres

```bash
docker rm -f smoke-pg 2>/dev/null
docker run --rm -d --name smoke-pg \
  -e POSTGRES_USER=test -e POSTGRES_PASSWORD=test -e POSTGRES_DB=llm_gateway \
  -p 5433:5432 \
  postgres:16-alpine

until docker exec smoke-pg pg_isready -U test >/dev/null 2>&1; do sleep 0.5; done
```

## 2. Start the gateway

In terminal 1:

```bash
export GATEWAY_DATABASE__POSTGRES__DSN="postgres://test:test@localhost:5433/llm_gateway?sslmode=disable"
export GATEWAY_DATABASE__POSTGRES__AUTO_MIGRATE=true
export GATEWAY_AUTH__ADMIN_TOKEN="dev-admin-token"
export GATEWAY_PROVIDERS__OPENAI__API_KEY="sk-..."   # your real OpenAI key

make build && ./bin/gateway 2>&1 | tee /tmp/gateway.log
```

Wait for `starting llm-gateway addr=:8080`.

> If `configs/gateway.yaml` declares aliases (`claude`, `gemini`) for providers you haven't supplied API keys for, the gateway logs a WARN and skips them — it does **not** exit. To exercise just OpenAI, no further action is needed.

In terminal 2:

```bash
ADMIN=dev-admin-token
```

## 3. Negative auth tests (all must be 401, health open)

```bash
# 3a. Missing Authorization.
curl -sS -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'

# 3b. Bogus key.
curl -sS -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-gw-not-real" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'

# 3c-d. Admin endpoint with no / wrong token.
curl -sS -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/internal/admin/keys \
  -H "Content-Type: application/json" -d '{}'
curl -sS -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/internal/admin/keys \
  -H "Authorization: Bearer wrong" -H "Content-Type: application/json" -d '{}'

# 3e. Public health open.
curl -sS localhost:8080/health
```

## 4. Seed an org

`psql -At` strips column headers but **not** the trailing `INSERT 0 1`
status. Take the first line so the UUID lands in `$ORG_ID` cleanly:

```bash
ORG_ID=$(docker exec -i smoke-pg psql -At -U test -d llm_gateway \
  -c "INSERT INTO organizations (name) VALUES ('smoke') RETURNING id;" | head -n1)
echo "ORG_ID='$ORG_ID'"   # must be ONE line containing only a UUID
```

## 5. Mint a key

```bash
RESP=$(curl -sS -X POST localhost:8080/internal/admin/keys \
  -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d "{\"org_id\":\"$ORG_ID\",\"name\":\"smoke-key\",\"rate_limit_rpm\":120,\"rate_limit_tpm\":250000}")
echo "$RESP" | jq .
KEY=$(echo "$RESP"     | jq -r .key)
KEY_ID=$(echo "$RESP" | jq -r .id)
```

Verify: response includes `key` starting with `sk-gw-` (38 chars total),
`key_prefix` = first 14 chars, and the rate limits echoed.

## 6. Defaults applied when limits omitted

```bash
curl -sS -X POST localhost:8080/internal/admin/keys \
  -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d "{\"org_id\":\"$ORG_ID\",\"name\":\"default-limits\"}" \
  | jq '.rate_limit_rpm, .rate_limit_tpm'
# expect: 60 / 100000
```

## 7. Positive path

```bash
# Quote the jq filter — zsh globs unquoted brackets.
curl -sS -X POST localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"say hi in 3 words"}]}' \
  | jq '.choices[0].message'
```

Run twice. The second call should produce only a `request completed` log
line, with no auth lookup — the cache hit suppresses the DB query.

## 8. `last_used_at` was updated

```bash
docker exec -i smoke-pg psql -U test -d llm_gateway \
  -c "SELECT id, name, last_used_at FROM api_keys WHERE id = '$KEY_ID';"
```

## 9. Revoke invalidates the cache immediately

```bash
curl -sS -o /dev/null -w "%{http_code}\n" \
  -X DELETE "localhost:8080/internal/admin/keys/$KEY_ID" \
  -H "Authorization: Bearer $ADMIN"     # 204

curl -sS -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'     # 401
```

## 10. Revoke is idempotent on bogus ids

```bash
curl -sS -o /dev/null -w "%{http_code}\n" \
  -X DELETE "localhost:8080/internal/admin/keys/not-a-uuid" \
  -H "Authorization: Bearer $ADMIN"     # 204 (NOT 500 — SQLSTATE 22P02 is mapped)

curl -sS -o /dev/null -w "%{http_code}\n" \
  -X DELETE "localhost:8080/internal/admin/keys/00000000-0000-0000-0000-000000000000" \
  -H "Authorization: Bearer $ADMIN"     # 204
```

## 11. Plaintext never reaches the log

```bash
grep -F "$KEY" /tmp/gateway.log && echo "LEAK!" || echo "OK - plaintext not in log"
```

## 12. Expired key is rejected

```bash
RESP=$(curl -sS -X POST localhost:8080/internal/admin/keys \
  -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d "{\"org_id\":\"$ORG_ID\",\"name\":\"shortlived\",\"expires_at\":\"2020-01-01T00:00:00Z\"}")
EXP_KEY=$(echo "$RESP" | jq -r .key)

curl -sS -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $EXP_KEY" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}'   # 401
```

## 13. Graceful shutdown

In terminal 1, `Ctrl-C` once. Expect a `shutdown signal received, draining`
INFO line, then clean exit. Confirm:

```bash
lsof -i :8080     # no output → port released
```

## 14. Dev escape hatch (no Postgres needed)

```bash
unset GATEWAY_DATABASE__POSTGRES__DSN GATEWAY_DATABASE__POSTGRES__AUTO_MIGRATE
export GATEWAY_AUTH__ENABLED=false

./bin/gateway 2>&1 | tee /tmp/gateway-dev.log    # do NOT pipe to `head` — SIGPIPE kills the gateway
```

Wait for `starting llm-gateway`. The startup log includes a prominent WARN
that auth is disabled. Then in terminal 2:

```bash
# Admin routes are not mounted when auth is off → 404.
curl -sS -o /dev/null -w "%{http_code}\n" -X POST localhost:8080/internal/admin/keys \
  -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" -d '{}'

# Any Bearer token passes; the real provider call then succeeds.
curl -sS -X POST localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer anything" -H "Content-Type: application/json" \
  -d '{"model":"gpt-4o-mini","messages":[{"role":"user","content":"hi"}]}' \
  | jq '.choices[0].message'
```

`Ctrl-C` when done.

## 15. Cleanup

```bash
docker rm -f smoke-pg
rm -f /tmp/gateway.log /tmp/gateway-dev.log
unset GATEWAY_AUTH__ENABLED GATEWAY_AUTH__ADMIN_TOKEN GATEWAY_PROVIDERS__OPENAI__API_KEY \
      GATEWAY_DATABASE__POSTGRES__DSN GATEWAY_DATABASE__POSTGRES__AUTO_MIGRATE
unset ADMIN ORG_ID KEY KEY_ID EXP_KEY RESP
```

## Common gotchas

| Symptom | Cause |
|---|---|
| `Invalid admin token` despite setting `ADMIN` | Terminal restarted between steps; re-export `ADMIN=dev-admin-token`. |
| `invalid request body` on key creation | `$ORG_ID` captured the `INSERT 0 1` status line — see step 4's `head -n1`. |
| Empty reply / connection refused after `\| head -N` | SIGPIPE killed the gateway when `head` closed the pipe. Use `tee` instead. |
| `zsh: no matches found: .choices[0].message` | zsh globs unquoted `[`. Wrap the jq filter in single quotes. |
| `role "test" does not exist (SQLSTATE 28000)` | DSN is hitting your local Postgres on `:5432`, not the Docker container. Use `:5433`. |
