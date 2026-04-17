package config

import "time"

// Config is the top-level gateway configuration.
type Config struct {
	Server       ServerConfig              `koanf:"server"`
	Providers    map[string]ProviderConfig `koanf:"providers"`
	ModelAliases map[string]string         `koanf:"model_aliases"`
	Health       HealthConfig              `koanf:"health"`
	Log          LogConfig                 `koanf:"log"`
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
