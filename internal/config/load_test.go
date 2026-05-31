package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_FromYAML(t *testing.T) {
	yamlContent := `
server:
  port: 9090
  read_timeout: 10s
  write_timeout: 60s

providers:
  openai:
    base_url: "https://api.openai.com/v1"
    timeout: 15s
    models:
      - gpt-4o
      - gpt-4o-mini

log:
  level: debug
  format: json
  debug_bodies: true
`
	path := writeTemp(t, yamlContent)
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Server.Port)
	assert.Equal(t, 10*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 60*time.Second, cfg.Server.WriteTimeout)

	assert.Equal(t, "sk-test", cfg.Providers["openai"].APIKey)
	assert.Equal(t, "https://api.openai.com/v1", cfg.Providers["openai"].BaseURL)
	assert.Equal(t, 15*time.Second, cfg.Providers["openai"].Timeout)
	assert.Equal(t, []string{"gpt-4o", "gpt-4o-mini"}, cfg.Providers["openai"].Models)

	assert.Equal(t, "debug", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.True(t, cfg.Log.DebugBodies)
}

func TestLoad_EnvOverridesYAML(t *testing.T) {
	yamlContent := `
server:
  port: 8080
providers:
  openai:
    base_url: "https://api.openai.com/v1"
`
	path := writeTemp(t, yamlContent)

	t.Setenv("GATEWAY_SERVER__PORT", "9090")
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-env")

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, 9090, cfg.Server.Port)
	assert.Equal(t, "sk-env", cfg.Providers["openai"].APIKey)
}

func TestLoad_DefaultsApplied(t *testing.T) {
	yamlContent := `
providers:
  openai:
    base_url: "https://api.openai.com/v1"
`
	path := writeTemp(t, yamlContent)
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, 8080, cfg.Server.Port)
	assert.Equal(t, 30*time.Second, cfg.Server.ReadTimeout)
	assert.Equal(t, 120*time.Second, cfg.Server.WriteTimeout)
	assert.Equal(t, "info", cfg.Log.Level)
	assert.Equal(t, "json", cfg.Log.Format)
	assert.False(t, cfg.Log.DebugBodies)
	assert.Equal(t, 30*time.Second, cfg.Providers["openai"].Timeout)
}

func TestLoad_MissingFileUsesEnv(t *testing.T) {
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-env-only")

	cfg, err := Load("/nonexistent/path/config.yaml")
	require.NoError(t, err)

	assert.Equal(t, "sk-env-only", cfg.Providers["openai"].APIKey)
	assert.Equal(t, 8080, cfg.Server.Port)
}

func TestLoad_NoProviderAPIKey_Error(t *testing.T) {
	yamlContent := `
server:
  port: 8080
`
	path := writeTemp(t, yamlContent)

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least one provider must be configured with an API key")
}

func TestLoad_ProviderTimeoutDefault(t *testing.T) {
	yamlContent := `
providers:
  openai:
    base_url: "https://api.openai.com/v1"
`
	path := writeTemp(t, yamlContent)
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, 30*time.Second, cfg.Providers["openai"].Timeout)
}

func TestLoad_DatabaseAndAuthFromEnv(t *testing.T) {
	yamlContent := `
providers:
  openai:
    base_url: "https://api.openai.com/v1"
auth:
  enabled: true
`
	path := writeTemp(t, yamlContent)

	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")
	t.Setenv("GATEWAY_DATABASE__POSTGRES__DSN", "postgres://localhost:5432/llm_gateway?sslmode=disable")
	t.Setenv("GATEWAY_DATABASE__POSTGRES__AUTO_MIGRATE", "true")
	t.Setenv("GATEWAY_AUTH__ADMIN_TOKEN", "dev-admin-token")

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, "postgres://localhost:5432/llm_gateway?sslmode=disable", cfg.Database.Postgres.DSN)
	assert.True(t, cfg.Database.Postgres.AutoMigrate)
	assert.Equal(t, 20, cfg.Database.Postgres.MaxConnections) // default
	assert.True(t, cfg.Auth.Enabled)
	assert.Equal(t, "dev-admin-token", cfg.Auth.AdminToken)
	assert.Equal(t, 5*time.Minute, cfg.Auth.CacheTTL)
	assert.Equal(t, 10000, cfg.Auth.CacheSize)
}

func TestLoad_AuthEnabledWithoutDSN_Error(t *testing.T) {
	yamlContent := `
providers:
  openai:
    base_url: "https://api.openai.com/v1"
auth:
  enabled: true
`
	path := writeTemp(t, yamlContent)
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")
	t.Setenv("GATEWAY_AUTH__ADMIN_TOKEN", "dev-admin-token")

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GATEWAY_DATABASE__POSTGRES__DSN")
}

func TestLoad_AuthEnabledWithoutAdminToken_Error(t *testing.T) {
	yamlContent := `
providers:
  openai:
    base_url: "https://api.openai.com/v1"
auth:
  enabled: true
`
	path := writeTemp(t, yamlContent)
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")
	t.Setenv("GATEWAY_DATABASE__POSTGRES__DSN", "postgres://localhost:5432/llm_gateway")

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GATEWAY_AUTH__ADMIN_TOKEN")
}

func TestLoad_RateLimitDefaults(t *testing.T) {
	yamlContent := `
providers:
  openai:
    base_url: "https://api.openai.com/v1"
`
	path := writeTemp(t, yamlContent)
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.Equal(t, 60, cfg.RateLimit.DefaultRPM)
	assert.Equal(t, 100000, cfg.RateLimit.DefaultTPM)
}

func TestLoad_RateLimitEnabledWithoutRedisDSN_Error(t *testing.T) {
	yamlContent := `
providers:
  openai:
    base_url: "https://api.openai.com/v1"
rate_limit:
  enabled: true
`
	path := writeTemp(t, yamlContent)
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")

	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GATEWAY_DATABASE__REDIS__DSN")
}

func TestLoad_RateLimitFromEnv(t *testing.T) {
	yamlContent := `
providers:
  openai:
    base_url: "https://api.openai.com/v1"
rate_limit:
  enabled: true
  default_rpm: 120
  default_tpm: 50000
`
	path := writeTemp(t, yamlContent)
	t.Setenv("GATEWAY_PROVIDERS__OPENAI__API_KEY", "sk-test")
	t.Setenv("GATEWAY_DATABASE__REDIS__DSN", "redis://localhost:6379/0")

	cfg, err := Load(path)
	require.NoError(t, err)

	assert.True(t, cfg.RateLimit.Enabled)
	assert.Equal(t, 120, cfg.RateLimit.DefaultRPM)
	assert.Equal(t, 50000, cfg.RateLimit.DefaultTPM)
	assert.Equal(t, "redis://localhost:6379/0", cfg.Database.Redis.DSN)
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	err := os.WriteFile(path, []byte(content), 0644)
	require.NoError(t, err)
	return path
}
