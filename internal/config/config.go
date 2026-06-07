package config

import "time"

// Config is the top-level gateway configuration.
type Config struct {
	Server       ServerConfig              `koanf:"server"`
	Providers    map[string]ProviderConfig `koanf:"providers"`
	ModelAliases map[string]string         `koanf:"model_aliases"`
	Health       HealthConfig              `koanf:"health"`
	Log          LogConfig                 `koanf:"log"`
	Database     DatabaseConfig            `koanf:"database"`
	Auth         AuthConfig                `koanf:"auth"`
	RateLimit    RateLimitConfig           `koanf:"rate_limit"`
	Metrics      MetricsConfig             `koanf:"metrics"`
	Routing      RoutingConfig             `koanf:"routing"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port         int           `koanf:"port"`
	ReadTimeout  time.Duration `koanf:"read_timeout"`
	WriteTimeout time.Duration `koanf:"write_timeout"`
}

// ProviderConfig holds settings for a single LLM provider.
type ProviderConfig struct {
	APIKey       string        `koanf:"api_key"`
	BaseURL      string        `koanf:"base_url"`
	Timeout      time.Duration `koanf:"timeout"`
	Models       []string      `koanf:"models"`
	MaxRetries   int           `koanf:"max_retries"`
	RetryBackoff time.Duration `koanf:"retry_backoff"`
}

// HealthConfig holds settings for provider health tracking.
type HealthConfig struct {
	FailureThreshold int           `koanf:"failure_threshold"`
	CooldownPeriod   time.Duration `koanf:"cooldown_period"`
}

// LogConfig holds logging settings.
type LogConfig struct {
	Level       string `koanf:"level"`
	Format      string `koanf:"format"`
	DebugBodies bool   `koanf:"debug_bodies"`
}

// DatabaseConfig holds backing-store connection settings.
type DatabaseConfig struct {
	Postgres PostgresConfig `koanf:"postgres"`
	Redis    RedisConfig    `koanf:"redis"`
}

// RedisConfig holds settings for the Redis client used by the rate
// limiter. DSN is a secret and must be supplied via
// GATEWAY_DATABASE__REDIS__DSN; it accepts both redis:// and rediss://.
type RedisConfig struct {
	DSN string `koanf:"dsn"`
}

// PostgresConfig holds settings for the Postgres connection pool. The DSN
// is a secret and must be supplied via GATEWAY_DATABASE__POSTGRES__DSN;
// AutoMigrate is an opt-in startup behaviour for development.
type PostgresConfig struct {
	DSN            string `koanf:"dsn"`
	AutoMigrate    bool   `koanf:"auto_migrate"`
	MaxConnections int    `koanf:"max_connections"`
}

// AuthConfig holds API key authentication settings. AdminToken is a secret
// and must be supplied via GATEWAY_AUTH__ADMIN_TOKEN.
type AuthConfig struct {
	Enabled    bool          `koanf:"enabled"`
	AdminToken string        `koanf:"admin_token"`
	CacheTTL   time.Duration `koanf:"cache_ttl"`
	CacheSize  int           `koanf:"cache_size"`
}

// RateLimitConfig holds the per-API-key request and token throttling
// settings. DefaultRPM and DefaultTPM apply when a KeyInfo has 0 in the
// corresponding field; non-zero KeyInfo values override them.
type RateLimitConfig struct {
	Enabled    bool `koanf:"enabled"`
	DefaultRPM int  `koanf:"default_rpm"`
	DefaultTPM int  `koanf:"default_tpm"`
}

// MetricsConfig holds Prometheus scrape-endpoint settings. When Enabled
// is true the gateway starts a second HTTP server on Port serving
// /metrics (Prometheus exposition) and /health. The scrape server is
// intentionally separate from the public API server so scrape traffic
// can have a different network policy.
type MetricsConfig struct {
	Enabled bool `koanf:"enabled"`
	Port    int  `koanf:"port"`
}

// RoutingConfig holds the router's strategy defaults and the model-group
// definitions used for candidate fan-out. Experiment and per-org
// override fields are intentionally absent — those land in M4.3 and M6
// respectively.
type RoutingConfig struct {
	DefaultStrategy string                      `koanf:"default_strategy"`
	MaxAttempts     int                         `koanf:"max_attempts"`
	ModelGroups     map[string]ModelGroupConfig `koanf:"model_groups"`
	Pricing         map[string]CandidatePricing `koanf:"pricing"`
}

// ModelGroupConfig describes one named model group: an ordered list of
// candidate providers plus an optional per-group strategy override that
// supersedes RoutingConfig.DefaultStrategy when set.
type ModelGroupConfig struct {
	Strategy   string            `koanf:"strategy"`
	Candidates []CandidateConfig `koanf:"candidates"`
}

// CandidateConfig is one (provider, model) entry inside a model group.
// Cost figures are USD per 1k tokens; Priority lower = preferred;
// Weight is used only by the weighted strategy.
type CandidateConfig struct {
	Provider           string  `koanf:"provider"`
	Model              string  `koanf:"model"`
	CostPer1kInputUSD  float64 `koanf:"cost_per_1k_input"`
	CostPer1kOutputUSD float64 `koanf:"cost_per_1k_output"`
	Priority           int     `koanf:"priority"`
	Weight             int     `koanf:"weight"`
}

// CandidatePricing supplies cost-per-1k figures for concrete model and
// alias requests (the one-element candidate path). When the requested
// model is not in this map the candidate is built with zero costs and
// CostOptimized treats it as "no cost data".
type CandidatePricing struct {
	CostPer1kInputUSD  float64 `koanf:"cost_per_1k_input"`
	CostPer1kOutputUSD float64 `koanf:"cost_per_1k_output"`
}
