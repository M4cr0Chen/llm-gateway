# ADR-006: Reuse ProviderHealth for circuit breaker

## Status
Accepted

## Context
M4 introduces a fallback chain: when a provider call fails with a retryable
error, the Router tries the next candidate. To avoid hammering a provider
that is clearly down, the Router needs a circuit-breaker view of each
provider — "this one is open, skip it" — and a way to recover once the
provider comes back.

The natural reach is for an in-process circuit breaker library such as
`sony/gobreaker` or `cep21/circuit`. But the codebase already grew its own
3-state health tracker in M2 (`internal/provider/health.go`,
`HealthTrackingProvider` decorator) to support per-provider retry with
exponential backoff and the `GET /internal/health` admin endpoint. The
existing tracker semantics are exactly classic Closed / Open / Half-Open:

| State       | Existing behaviour (`ProviderHealth`)                    | Classic breaker behaviour |
|-------------|----------------------------------------------------------|---------------------------|
| Closed      | `healthy = true`; `RecordSuccess` resets fail counter    | Closed |
| Open        | `consecutive_fails >= threshold` → `healthy = false`     | Open |
| Half-Open   | `IsHealthy()` returns true when `now - last_failure >= cooldown` despite `healthy = false` | Half-Open probe |

A success in half-open calls `RecordSuccess()`, which flips `healthy = true`
— same as closing the breaker. A failure in half-open calls
`RecordFailure()` which keeps it open and updates `last_failure`, delaying
the next probe by another full cooldown — same as a failed probe.

So we have two paths:
1. Use the M2 tracker as-is for M4 routing decisions.
2. Adopt `sony/gobreaker` (or equivalent) and migrate the M2 tracker to it.

## Decision
Use the existing `provider.ProviderHealth` as the Router's circuit
breaker. The Router calls `IsHealthy()` to filter candidates before invoking
the strategy. No new library is added.

## Rationale
- **One source of truth.** Two breakers per provider (one inside the
  decorator, one inside the router) would have independent state machines.
  The decorator already mutates fail counts on every call; replicating
  that in a parallel breaker means every success/failure has to flow
  through both, and any drift produces ghost-open behaviour that is
  miserable to debug.
- **The semantics already match.** Three states, threshold-triggered open,
  cooldown-triggered probe, success-closes / failure-reopens — all there.
  Adopting a library would mean writing an adapter to map between
  identical concepts.
- **`/internal/health` reuse.** The M3 admin endpoint exposes
  `ProviderHealth.Status()` directly. If we'd split the breaker out, the
  admin endpoint would need to merge two states (`healthy` from the
  health tracker, `open` from the breaker) to give an honest answer.
- **Half-Open is one call, not a window.** `sony/gobreaker` defaults to
  "open for X, then allow N probe requests in half-open." The M2 tracker
  is simpler: "allow one probe after cooldown; success closes, failure
  re-opens." For a small set of providers (3–5) with full traffic
  hitting them, this is the right tradeoff — windowed probing only pays
  off when probe traffic is rare relative to total traffic.
- **No CGo, no new transitive deps.** Keeps `go.sum` short.

## Consequences
- The Router consults `provider.HealthTrackingProvider.Health.IsHealthy()`
  during candidate filtering. The Router does **not** call
  `RecordFailure`/`RecordSuccess` — those stay inside the decorator, which
  observes every call regardless of how it was selected.
- If we ever want richer breaker semantics (rolling-window failure rate
  instead of consecutive count, multiple probe slots in half-open,
  half-open success quorum), we revisit. The migration cost is one method
  rename plus the adapter shim.
- The breaker's tuning knobs (`failure_threshold`, `cooldown_period`) stay
  in the `health:` config section; the Router does not introduce a parallel
  `circuit_breaker:` section.

## Alternatives Rejected

### `sony/gobreaker`
- Mature, widely used, simple API (`cb.Execute(func() (any, error) {...})`).
- Defaults are reasonable but tuned for client-side breakers protecting an
  RPC dependency, not for routing decisions over a small candidate pool.
- Would require either wrapping every `Provider.ChatCompletion` call in a
  `cb.Execute` (duplicating the decorator's job) or extracting just the
  state machine from the library (which is what we already have in
  `ProviderHealth`).

### `cep21/circuit` (Hystrix-style)
- Powerful (rolling stats, fallbacks, command pattern) but heavyweight for
  a 200-line state machine that we've already written and unit-tested.
- Brings a metrics surface that competes with our Prometheus collectors.

### Per-call retry budget (e.g. `failsafe-go`)
- Solves a different problem (token-bucket retry budgets) — orthogonal to
  whether a specific provider is currently usable.
