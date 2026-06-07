package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// runRequest spins up a chi-style middleware chain composed of just
// RequestLogger wrapping the provided inner handler, runs one request
// against it, and returns the captured JSON log line decoded.
func runRequest(t *testing.T, level slog.Level, inner http.Handler, req *http.Request) map[string]any {
	t.Helper()

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: level}))
	h := RequestLogger(logger)(inner)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// The middleware emits exactly one INFO line per request.
	line := strings.TrimSpace(buf.String())
	require.NotEmpty(t, line, "expected one log line, got none")
	out := map[string]any{}
	require.NoError(t, json.Unmarshal([]byte(line), &out))
	return out
}

func TestRequestLogger_BaselineFieldsOnly(t *testing.T) {
	// Unauthenticated handler that does nothing — should produce method,
	// path, status, duration_ms, bytes, request_id but no org_id/model/etc.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	got := runRequest(t, slog.LevelInfo, inner, req)

	assert.Equal(t, "GET", got["method"])
	assert.Equal(t, "/health", got["path"])
	assert.Equal(t, float64(http.StatusOK), got["status"])
	assert.Contains(t, got, "duration_ms")
	assert.Contains(t, got, "bytes")

	for _, omit := range []string{"org_id", "key_id", "model", "provider",
		"strategy", "group", "prompt_tokens", "completion_tokens",
		"total_tokens", "rate_limited"} {
		assert.NotContains(t, got, omit, "field %q must be omitted when zero-valued", omit)
	}
}

func TestRequestLogger_EnrichedFieldsFromSetters(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetOrgKey(r.Context(), "org-abc", "key-xyz")
		SetModel(r.Context(), "gpt-4o")
		SetProvider(r.Context(), "openai")
		SetStrategy(r.Context(), "priority")
		SetGroup(r.Context(), "smart")
		SetTokens(r.Context(), 11, 22)
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	got := runRequest(t, slog.LevelInfo, inner, req)

	assert.Equal(t, "org-abc", got["org_id"])
	assert.Equal(t, "key-xyz", got["key_id"])
	assert.Equal(t, "gpt-4o", got["model"])
	assert.Equal(t, "openai", got["provider"])
	assert.Equal(t, "priority", got["strategy"])
	assert.Equal(t, "smart", got["group"])
	assert.Equal(t, float64(11), got["prompt_tokens"])
	assert.Equal(t, float64(22), got["completion_tokens"])
	assert.Equal(t, float64(33), got["total_tokens"], "total_tokens must be prompt+completion")
}

func TestRequestLogger_RateLimitedFlag(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		MarkRateLimited(r.Context())
		w.WriteHeader(http.StatusTooManyRequests)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	got := runRequest(t, slog.LevelInfo, inner, req)

	assert.Equal(t, true, got["rate_limited"])
}

func TestRequestLogger_TokensOmittedWhenZero(t *testing.T) {
	// Setting only model+provider (no tokens, e.g. streaming) must not
	// emit zero-valued prompt_tokens / completion_tokens / total_tokens.
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetModel(r.Context(), "claude-sonnet-4-20250514")
		SetProvider(r.Context(), "anthropic")
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	got := runRequest(t, slog.LevelInfo, inner, req)

	assert.Equal(t, "claude-sonnet-4-20250514", got["model"])
	assert.NotContains(t, got, "prompt_tokens")
	assert.NotContains(t, got, "completion_tokens")
	assert.NotContains(t, got, "total_tokens")
}

func TestRequestLogger_PIIContract_BodiesNeverAtInfo(t *testing.T) {
	// PII contract: with debug_bodies mounted but the configured handler
	// level set to INFO, request body content must NEVER appear in the
	// captured log buffer.
	sentinel := "TOP-SECRET-PROMPT-DO-NOT-LEAK-bc77ea91"

	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Drain so the handler "consumed" the body, mimicking real handler.
		_, _ = io.Copy(io.Discard, r.Body)
		SetModel(r.Context(), "gpt-4o")
		w.WriteHeader(http.StatusOK)
	})

	// Compose the chain the same way main.go would when debug_bodies=true.
	chain := RequestLogger(logger)(DebugRequestBodies(0)(inner))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(sentinel))
	chain.ServeHTTP(httptest.NewRecorder(), req)

	assert.NotContains(t, buf.String(), sentinel,
		"INFO-level logger must never receive DEBUG-only body content")
}
