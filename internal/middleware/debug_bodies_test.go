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

// mountDebugBodies wires a logger of the given level via RequestLogger
// (so DebugRequestBodies picks it up from context) and returns the
// captured buffer + composed handler.
func mountDebugBodies(level slog.Level, limit int, inner http.Handler) (*bytes.Buffer, http.Handler) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level}))
	chain := RequestLogger(logger)(DebugRequestBodies(limit)(inner))
	return buf, chain
}

// parseLogLines splits a captured slog JSON buffer into one decoded map
// per line.
func parseLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, raw := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		if len(raw) == 0 {
			continue
		}
		m := map[string]any{}
		require.NoError(t, json.Unmarshal(raw, &m))
		out = append(out, m)
	}
	return out
}

func TestDebugRequestBodies_EmitsAtDebug(t *testing.T) {
	body := `{"prompt":"hello world"}`
	innerSaw := ""
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		innerSaw = string(b)
		w.WriteHeader(http.StatusOK)
	})

	buf, h := mountDebugBodies(slog.LevelDebug, 0, inner)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)

	lines := parseLogLines(t, buf)

	var debugLine map[string]any
	for _, ln := range lines {
		if ln["msg"] == "http body" {
			debugLine = ln
			break
		}
	}
	require.NotNil(t, debugLine, "expected a DEBUG 'http body' line")
	assert.Equal(t, body, debugLine["body"])
	assert.Equal(t, float64(len(body)), debugLine["len"])
	assert.NotContains(t, debugLine, "truncated")

	assert.Equal(t, body, innerSaw, "downstream handler must still see the full body")
}

func TestDebugRequestBodies_SuppressedAtInfo(t *testing.T) {
	body := `{"prompt":"hello world"}`
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	})

	buf, h := mountDebugBodies(slog.LevelInfo, 0, inner)
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Only the RequestLogger's INFO "request completed" line should appear;
	// no DEBUG "http body" line, and the body content itself never shows up.
	out := buf.String()
	assert.NotContains(t, out, "\"http body\"")
	assert.NotContains(t, out, "hello world",
		"body content must never appear at INFO even when debug_bodies is mounted")
}

func TestDebugRequestBodies_TruncatedOverLimit(t *testing.T) {
	limit := 16
	body := strings.Repeat("A", limit*4) // 4× the limit
	innerSaw := ""
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		innerSaw = string(b)
		w.WriteHeader(http.StatusOK)
	})

	buf, h := mountDebugBodies(slog.LevelDebug, limit, inner)
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	h.ServeHTTP(httptest.NewRecorder(), req)

	lines := parseLogLines(t, buf)
	var debugLine map[string]any
	for _, ln := range lines {
		if ln["msg"] == "http body" {
			debugLine = ln
			break
		}
	}
	require.NotNil(t, debugLine, "expected a DEBUG 'http body' line")
	assert.Equal(t, true, debugLine["truncated"])
	assert.Equal(t, float64(limit), debugLine["len"])
	assert.Equal(t, body[:limit], debugLine["body"], "captured body must match the limit-bounded prefix")

	assert.Equal(t, body, innerSaw,
		"downstream handler must still see the FULL body even when the log line was truncated")
}

func TestDebugRequestBodies_EmptyBody(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	buf, h := mountDebugBodies(slog.LevelDebug, 0, inner)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.ServeHTTP(httptest.NewRecorder(), req)

	// Empty/no body must not generate a DEBUG "http body" line.
	assert.NotContains(t, buf.String(), "\"http body\"")
}
