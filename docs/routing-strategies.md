# Routing Strategies

How the gateway selects a provider for each request, falls back when a provider
fails, and runs A/B experiments between models.

The Router replaces the direct `registry.Resolve(model)` call inside
`ChatHandler`. It does **not** add an HTTP middleware — routing happens inside
the handler, after auth/rate-limit have already gated the request.

```
ChatHandler ──► Router.Route(ctx, req, meta) ──► Provider
                  │
                  ├── Candidate resolution (model OR model-group → []Candidate)
                  ├── Experiment override (deterministic A/B by org_id)
                  ├── Strategy.Select(candidates, req, meta) → ordered chain
                  └── Fallback chain (try-next on retryable failure)
```

> **Layering rule.** Within-provider retry on 429/5xx stays inside
> `HealthTrackingProvider` (the decorator added in M2). The Router's fallback
> only switches to a **different** provider. The two never stack retries on
> the same target.

## Interfaces

### Router

```go
// internal/router/router.go

type Router interface {
    // Route selects a provider for the request. Returns the chosen Provider
    // and a Decision describing why it was picked (used for headers, metrics,
    // and logging). Returns an error if no candidate could be selected
    // (e.g., model not found, all providers unhealthy).
    Route(ctx context.Context, req *model.ChatCompletionRequest, meta RequestMeta) (provider.Provider, Decision, error)
}

// RequestMeta carries the per-request context the Router needs that is not
// part of the OpenAI request body. The handler builds this from auth and
// request middleware state.
type RequestMeta struct {
    OrgID      string   // from auth.KeyInfo.OrgID
    KeyID      string   // from auth.KeyInfo.KeyID
    Attempt    int      // 0 on first try; incremented by the fallback loop
    TriedNames []string // provider names already attempted in this request
}

// Decision is what Route returns alongside the provider so the handler can
// stamp response headers and metrics without re-running the strategy.
type Decision struct {
    Provider       string // chosen provider name
    Model          string // canonical model name (after group/alias resolution)
    Strategy       string // strategy that picked this candidate
    Group          string // model-group name (empty if request was a concrete model)
    ExperimentName string // empty if no experiment applied
    Variant        string // empty if no experiment applied
}
```

### Strategy

```go
// internal/router/strategy.go

// Strategy ranks a candidate list and returns the highest-priority candidate
// that is currently routable. Strategies MUST be deterministic given the
// same inputs (the only non-determinism allowed is round-robin's internal
// counter and weighted's RNG, both of which are owned by the strategy).
type Strategy interface {
    Name() string

    // Select returns the first candidate to try. Callers handle fallback by
    // re-invoking Select with the failed candidate filtered out (see
    // RequestMeta.TriedNames).
    Select(candidates []Candidate, req *model.ChatCompletionRequest, meta RequestMeta) (Candidate, error)
}

// Candidate is a (provider, model) pair plus the pricing/latency metadata
// the strategies use to rank.
type Candidate struct {
    Provider           provider.Provider
    Model              string  // canonical model name (post-alias resolution)
    CostPer1kInputUSD  float64 // 0 if unknown — strategy treats as "no cost data"
    CostPer1kOutputUSD float64
    Priority           int     // 0 = highest; used by Priority strategy
    Weight             int     // used by Weighted strategy
}
```

The Router calls `Strategy.Select` once to pick the first candidate. If that
provider fails with a retryable error, the Router filters it out of
`candidates` and calls `Select` again, recording the result in `Decision`.
This keeps strategy logic pure — no fallback state lives inside the strategy.

## Strategies

| Strategy           | Pick rule                                                              | Notes |
|--------------------|------------------------------------------------------------------------|-------|
| `cost_optimized`   | Lowest `(in_tokens·costIn + estOut·costOut)`; estOut ≈ `max_tokens` or 256 | Skip candidates with missing cost metadata; warn-log at startup |
| `latency_optimized`| Lowest EWMA of `provider_request_duration_seconds` over a 60s window   | Falls back to round-robin on cold-start |
| `round_robin`      | Atomic counter mod `len(candidates)`                                   | Counter is per-group |
| `weighted`         | Random sample weighted by `Candidate.Weight`                           | Deterministic-per-request if `meta.OrgID` is hashed in (used by A/B) |
| `priority`         | Lowest `Candidate.Priority`, ties broken by config order               | Default for `prefer_selfhosted: true` |

**Cost estimate for `cost_optimized`.** Token count is unknown at routing
time. The strategy uses `len(prompt_chars)/4` as a cheap upper-bound for
input tokens (tiktoken precision lands in M6) and `req.MaxTokens` (or 256 if
unset) as the output bound. This is intentionally rough; the strategy only
needs to **rank** candidates, not bill them.

**Latency EWMA.** The router subscribes to provider request durations via the
existing `gateway_provider_request_duration_seconds` histogram (no new
counter needed). The half-life is 60s — providers that just degraded fall
back to second place within ~1 min without needing an explicit signal.

## Candidate resolution

The `model` field on the incoming request can be:

1. **Concrete model name** (`gpt-4o`, `claude-sonnet-4-20250514`)
2. **Alias** (`claude` → `claude-sonnet-4-20250514`), resolved via the
   existing `registry.RegisterAlias` mechanism from M2
3. **Model group** (`fast`, `smart`), resolved via `routing.model_groups` to
   a list of `(provider, model, cost, weight, priority)` candidates

Aliases and concrete model names always produce a one-element candidate
list. The strategy is still consulted (to record the decision and apply
A/B) but the choice is forced.

The candidate list is also filtered through `ProviderHealth.IsHealthy()`
(see Circuit Breaker section). If filtering leaves zero candidates the
Router returns a typed `ErrNoHealthyProviders`, which `ChatHandler` maps to
`503 service_unavailable` / `all_providers_down`.

## Fallback chain

```go
// Pseudocode for Router.Route.
candidates := resolveCandidates(req.Model)
candidates = filterHealthy(candidates)
if len(candidates) == 0 { return nil, _, ErrNoHealthyProviders }

for meta.Attempt = 0; meta.Attempt < maxAttempts; meta.Attempt++ {
    pick, err := strategy.Select(candidates, req, meta)
    if err != nil { return nil, _, err }
    provider := pick.Provider

    resp, err := provider.ChatCompletion(ctx, req)   // ← HealthTrackingProvider retries inside
    if err == nil { return resp, decisionFor(pick, meta), nil }

    if !isRouterFallbackable(err) { return nil, _, err }   // 4xx client errors stay 4xx
    meta.TriedNames = append(meta.TriedNames, provider.Name())
    candidates = removeByName(candidates, provider.Name())
    if len(candidates) == 0 { break }
}
return nil, _, &model.ProviderError{StatusCode: 502, ...}
```

**Bounds.** `maxAttempts = 3` by default; configurable via
`routing.max_attempts`. This counts **distinct provider attempts**, not the
inner retry attempts of `HealthTrackingProvider`.

**Fallbackable errors.** Same predicate as `HealthTrackingProvider`'s
`isRetryable`: only `ProviderError.Retryable` is true (429 / 5xx /
network). 4xx client errors (400 bad request, 401 invalid API key on the
upstream side, 413 too large) propagate to the client untouched.

**Streaming.** `ChatCompletionStream` can fall back **before the first
chunk** is written to the client (i.e., if the initial call to the wrapped
provider returns an error). Once the response has begun streaming, the
gateway must not retry — the client has already observed the chunk
sequence. The decorator already enforces this; the Router inherits it.

## Circuit breaker

The M4 router does not introduce a new circuit breaker. It reuses the
3-state machine already living in `internal/provider/health.go`:

| State       | Trigger                                                | Router behaviour                      |
|-------------|--------------------------------------------------------|---------------------------------------|
| Closed      | Default; reset on `RecordSuccess()`                    | Provider is a normal candidate        |
| Open        | `consecutive_fails >= failure_threshold` (default 5)   | `IsHealthy()` returns false → filtered out |
| Half-Open   | Open + `now - last_failure >= cooldown` (default 30s)  | `IsHealthy()` returns true → router tries it again; one success closes it, one failure re-opens |

This is the same semantics as a classic three-state breaker. The reasons we
do not introduce `sony/gobreaker` are documented in `docs/adr/006-reuse-providerhealth-for-circuit-breaker.md`.

**Cooldown after probe.** When a half-open probe fails, the decorator calls
`RecordFailure()` which keeps the breaker open and updates `last_failure`,
so the next probe is delayed another `cooldown_period`. No additional
back-pressure is needed.

## A/B routing for model evaluation

Goal: send a deterministic, configurable fraction of an organization's
traffic for a given group to a non-default variant, and measure outcomes.

### Config schema

```yaml
routing:
  experiments:
    - name: "smart-claude-vs-gpt4o"
      enabled: true
      group: "smart"                     # which incoming model/group triggers it
      hash_key: "org_id"                 # org_id | key_id | request_id (latter = pure random)
      variants:
        - name: "control"
          weight: 50
          model: "gpt-4o"                # rewrite to this concrete model
        - name: "treatment"
          weight: 50
          model: "claude-sonnet-4-20250514"
      # Optional: scope to specific orgs. Empty = applies to all.
      include_orgs: []
      exclude_orgs: []
```

### Selection algorithm

```go
// internal/router/experiment.go

// pickVariant returns the variant name and the rewritten model. Deterministic
// in (experiment.Name, meta.<HashKey>): the same org always gets the same
// variant for the same experiment until weights or variant list change.
func pickVariant(e Experiment, meta RequestMeta) (variantName, model string) {
    seed := e.Name + "|" + meta.fieldByHashKey(e.HashKey)
    h := fnv.New64a()
    h.Write([]byte(seed))
    // Bucket into [0, totalWeight)
    bucket := h.Sum64() % uint64(totalWeight(e.Variants))
    var cursor uint64
    for _, v := range e.Variants {
        cursor += uint64(v.Weight)
        if bucket < cursor { return v.Name, v.Model }
    }
    // Unreachable when totalWeight > 0.
}
```

**Why FNV-1a.** Cheap, well-distributed for short inputs, no security
sensitivity (the bucketing is observable by definition). The standard
library `hash/fnv` is sufficient — we do not need a crypto hash.

**Distribution check.** With ~1000 unique org_ids and 50/50 weights, the
expected variance per variant is √(n·p·q) = √250 ≈ 16, so we accept ≤ ±5%
deviation (50 buckets). The 4.3 acceptance test asserts this.

### Lifecycle in `Route`

1. `resolveCandidates` returns the candidate list for the **original** model
   or group.
2. If `routing.experiments` has an enabled entry where
   `entry.group == req.Model` (or matches the resolved group), call
   `pickVariant`.
3. Replace `req.Model` with `variant.model` (in-place is fine — the request
   is owned by the handler at this point) and re-resolve candidates.
4. Set `Decision.ExperimentName` and `Decision.Variant`. The handler stamps
   these as `X-LLM-Gateway-Experiment` and `X-LLM-Gateway-Variant` on the
   response, and `RequestInfo.SetExperiment` records them on the access log.

### Stats endpoint

```
GET /internal/admin/experiments/{name}/stats
```

In-process counters only for M4 (no DB). Backed by Prometheus counters
inspected via `metrics.GatherExperiment(name)`:

```json
{
  "name": "smart-claude-vs-gpt4o",
  "enabled": true,
  "variants": [
    { "name": "control", "weight": 50, "requests": 482, "errors": 3, "p50_ms": 420, "p99_ms": 1830 },
    { "name": "treatment", "weight": 50, "requests": 518, "errors": 5, "p50_ms": 510, "p99_ms": 2100 }
  ]
}
```

Per-variant latency comes from a labelled histogram
`gateway_experiment_request_duration_seconds{experiment,variant}` (added in
4.3). For richer analytics (P50/P95 by org, cost diff), wait for the M7
ClickHouse pipeline.

## Response headers

```
X-LLM-Gateway-Provider: anthropic                    # always, including fallback
X-LLM-Gateway-Attempts: 2                            # 1 if first try succeeded
X-LLM-Gateway-Strategy: cost_optimized               # which strategy ran
X-LLM-Gateway-Group: smart                           # empty header omitted
X-LLM-Gateway-Experiment: smart-claude-vs-gpt4o      # omitted if no experiment
X-LLM-Gateway-Variant: treatment                     # omitted if no experiment
```

`X-LLM-Gateway-Provider` is already stamped by `ChatHandler` today; in M4 it
becomes the **final** provider after fallback, not the first one tried.

## Metrics

| Metric                                                      | Labels                                | Notes |
|-------------------------------------------------------------|---------------------------------------|-------|
| `gateway_router_decisions_total`                            | `strategy`, `provider`, `group`, `outcome` (`primary`/`fallback`/`error`) | One increment per successful `Route` call |
| `gateway_router_fallback_total`                             | `from_provider`, `to_provider`, `reason` (`5xx`/`429`/`network`) | Increment per fallback hop |
| `gateway_router_no_healthy_providers_total`                 | `group`                               | 503 path |
| `gateway_experiment_assignments_total`                      | `experiment`, `variant`               | Increment per A/B assignment |
| `gateway_experiment_request_duration_seconds`               | `experiment`, `variant`, `outcome`    | Histogram |

The provider-level counters (`gateway_provider_requests_total`,
`gateway_provider_request_duration_seconds`) and the health gauge
(`gateway_provider_healthy`) from M3 are reused as-is; M4 adds only the
**router**-level metrics above.

## Per-org strategy overrides

The default strategy is `routing.default_strategy`. Two override paths:

1. **Per-group override** in YAML:
   ```yaml
   routing:
     default_strategy: cost_optimized
     model_groups:
       smart:
         strategy: priority    # overrides default for this group only
         candidates: [ ... ]
   ```
2. **Per-org override** via the `organizations.routing_strategy` column
   (added in 4.1 migration). Empty/NULL = use default. Per-org overrides
   take precedence over per-group.

For M4 only the YAML-driven paths are wired up at startup. Per-org runtime
edits land in M6 when admin endpoints learn to mutate org rows.

## Config schema (full)

```yaml
routing:
  default_strategy: cost_optimized        # cost_optimized | latency_optimized | round_robin | weighted | priority
  max_attempts: 3                          # max distinct providers per request
  prefer_selfhosted: false                 # M9 — when true, self-hosted candidates win all ties

  model_groups:
    fast:
      strategy: cost_optimized             # optional override
      candidates:
        - { provider: openai,    model: gpt-4o-mini,        cost_per_1k_input: 0.00015, cost_per_1k_output: 0.0006, priority: 0, weight: 1 }
        - { provider: google,    model: gemini-2.0-flash,   cost_per_1k_input: 0.0001,  cost_per_1k_output: 0.0004, priority: 1, weight: 1 }
    smart:
      candidates:
        - { provider: openai,    model: gpt-4o,                       cost_per_1k_input: 0.0025, cost_per_1k_output: 0.01,  priority: 0, weight: 1 }
        - { provider: anthropic, model: claude-sonnet-4-20250514,     cost_per_1k_input: 0.003,  cost_per_1k_output: 0.015, priority: 1, weight: 1 }

  experiments:
    - name: smart-claude-vs-gpt4o
      enabled: true
      group: smart
      hash_key: org_id
      variants:
        - { name: control,   weight: 50, model: gpt-4o }
        - { name: treatment, weight: 50, model: claude-sonnet-4-20250514 }
```

## Error handling

| Condition                                                    | Status | Code                  |
|--------------------------------------------------------------|--------|-----------------------|
| Unknown model, no group match                                | 400    | `invalid_model`        |
| All candidates are circuit-broken                            | 503    | `all_providers_down`   |
| All candidates returned retryable errors after `max_attempts`| 502    | `provider_error`       |
| First candidate returned a 4xx (e.g., 401 from upstream)     | passthrough | upstream code     |
| `max_attempts` exhausted mid-stream after first byte         | n/a — first attempt wins or fails outright |

The 4xx passthrough rule exists because client errors (bad request, invalid
key on the upstream account) are not solved by trying another provider. The
existing `model.ProviderError.Retryable` already encodes this.

## Why not …

- **Why not a generic circuit-breaker library (`sony/gobreaker`)?** See
  ADR-006. We already have a 3-state health tracker; two sources of truth
  would diverge.
- **Why not push strategies into the registry?** The registry is a flat
  model-name map (M1). Routing decisions need cost/health/latency context;
  conflating that with name resolution would block A/B routing (where the
  request's model is rewritten before resolution).
- **Why not retry mid-stream by buffering chunks?** Buffering N chunks
  adds complexity and PII risk (we'd be holding generated content in
  memory). The contract `gateway_first_byte ⇒ provider commits` is
  simpler and lines up with how clients already think about SSE.
- **Why not give experiments their own DB table now?** M4 ships with config
  files only; experiment churn fits in PR review. A table arrives when
  we need runtime mutation (M6) and live stats (M7).
