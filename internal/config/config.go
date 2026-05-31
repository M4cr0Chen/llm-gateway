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
