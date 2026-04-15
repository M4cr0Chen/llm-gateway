# ADR-002: Use koanf over viper for configuration

## Status
Accepted

## Context
Need a configuration library that supports YAML files with environment variable overrides. Configuration includes server settings, provider API keys, routing rules, and feature flags.

## Decision
Use [koanf](https://github.com/knadh/koanf).

## Rationale
- Cleaner, more composable API than viper
- Explicit provider chain: file → env → defaults (no global singleton)
- Lighter dependency tree
- Supports YAML, JSON, TOML, env vars, and more
- Type-safe unmarshaling into structs
- No init() magic or global state — fully instantiated

## Consequences
- Config loading is explicit: create a `koanf.New()` instance, load providers in order
- Environment variables use `__` (double underscore) as hierarchy separator: `GATEWAY_SERVER__PORT` maps to `server.port`. Single underscores are kept literal for compound keys like `api_key`.
- Config struct defined in `internal/config/config.go` with struct tags
- Hot-reload possible via file watcher, but not implemented initially

## Alternatives Rejected

### viper
- Most popular Go config library, but has issues:
  - Global singleton pattern (`viper.Get()`) makes testing harder
  - Heavy dependency tree (pulls in many transitive dependencies)
  - Complex API with many ways to do the same thing
  - Case-insensitive key handling can cause subtle bugs

### envconfig
- Only supports environment variables, no file-based config
- Too limited for our needs (YAML config with structured routing rules, model groups, etc.)

### stdlib only (os.Getenv + YAML parsing)
- Viable but requires writing boilerplate for env var overrides, defaults, validation
- koanf handles this cleanly with minimal overhead
