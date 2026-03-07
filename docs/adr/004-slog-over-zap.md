# ADR-004: Use slog (stdlib) over zap/zerolog for logging

## Status
Accepted

## Context
Need structured JSON logging with request-scoped context fields (request ID, org ID, model, provider). Logging is on every request's critical path, so performance matters.

## Decision
Use [slog](https://pkg.go.dev/log/slog) from Go standard library (Go 1.21+).

## Rationale
- Part of the standard library — zero external dependencies
- Structured by default with `slog.With("key", "value")` pattern
- `slog.Handler` interface allows custom backends if needed
- Performance is sufficient for our throughput targets (< 100ns per log call with JSON handler)
- Context-aware: `slog.InfoContext(ctx, "msg")` integrates naturally with request context
- Industry direction — slog is becoming the standard, third-party libraries will converge on it

## Consequences
- Use `slog.New(slog.NewJSONHandler(os.Stdout, opts))` for JSON output
- Attach logger to request context in logging middleware
- Retrieve via helper: `func LoggerFromContext(ctx context.Context) *slog.Logger`
- Log levels: DEBUG, INFO, WARN, ERROR (standard slog levels)
- No colored output in development — use `jq` for local log reading

## Alternatives Rejected

### zap (uber-go/zap)
- Extremely fast (zero-allocation in hot path)
- But: adds dependency, custom API (`zap.String("key", "value")`), heavier
- Performance difference vs slog is negligible at our scale (thousands, not millions of req/s)

### zerolog
- Also very fast, similar to zap
- Fluent API (`log.Info().Str("key", "value").Msg("hello")`) is different from slog's convention
- Adds dependency for marginal benefit

### logrus
- Older library, largely superseded by structured logging libraries
- Slower than slog/zap/zerolog
- No reason to choose over slog in a new project
