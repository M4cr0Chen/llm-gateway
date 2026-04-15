package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// Load reads configuration from a YAML file and environment variables.
// Environment variables with the GATEWAY_ prefix override YAML values.
// A missing config file is not an error — defaults and env vars are used.
func Load(path string) (*Config, error) {
	k := koanf.New(".")

	// 1. Load from YAML file (missing file is OK).
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("loading config file: %w", err)
		}
	}

	// 2. Load from environment variables (override YAML).
	// Double underscore (__) separates hierarchy levels; single underscore
	// is kept literal so that compound key names (api_key, read_timeout) work.
	//   GATEWAY_SERVER__PORT=9090            → server.port = 9090
	//   GATEWAY_PROVIDERS__OPENAI__API_KEY=… → providers.openai.api_key = …
	//   GATEWAY_LOG__DEBUG_BODIES=true       → log.debug_bodies = true
	if err := k.Load(env.Provider("GATEWAY_", ".", func(s string) string {
		key := strings.ToLower(strings.TrimPrefix(s, "GATEWAY_"))
		return strings.ReplaceAll(key, "__", ".")
	}), nil); err != nil {
		return nil, fmt.Errorf("loading env vars: %w", err)
	}

	// 3. Unmarshal into Config struct.
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshaling config: %w", err)
	}

	// 4. Apply defaults for zero values.
	applyDefaults(&cfg)

	// 5. Validate required fields.
	if err := validate(&cfg); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.ReadTimeout == 0 {
		cfg.Server.ReadTimeout = 30 * time.Second
	}
	if cfg.Server.WriteTimeout == 0 {
		cfg.Server.WriteTimeout = 120 * time.Second
	}
	if cfg.Log.Level == "" {
		cfg.Log.Level = "info"
	}
	if cfg.Log.Format == "" {
		cfg.Log.Format = "json"
	}
	for name, p := range cfg.Providers {
		if p.Timeout == 0 {
			p.Timeout = 30 * time.Second
			cfg.Providers[name] = p
		}
	}
}

func validate(cfg *Config) error {
	hasProvider := false
	for _, p := range cfg.Providers {
		if p.APIKey != "" {
			hasProvider = true
			break
		}
	}
	if !hasProvider {
		return fmt.Errorf("at least one provider must be configured with an API key")
	}
	return nil
}
