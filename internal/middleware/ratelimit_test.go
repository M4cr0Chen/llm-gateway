package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/ratelimit"
)

// fakeReserver lets each test pre-program the Reserve outcome and assert
// what RecordTokens was called with afterward.
type fakeReserver struct {
	mu          sync.Mutex
	nextRes     ratelimit.Reservation
	nextErr     error
	recordedTok []int
	recordErr   error
}

func (f *fakeReserver) Reserve(_ context.Context, _ ratelimit.KeyInfo) (ratelimit.Reservation, error) {
	return f.nextRes, f.nextErr
}

func (f *fakeReserver) AllowRequest(_ context.Context, _ ratelimit.KeyInfo) (bool, time.Duration, error) {
	return f.nextRes.Allowed, f.nextRes.RetryAfter, f.nextErr
}

func (f *fakeReserver) RecordTokens(_ context.Context, _ ratelimit.KeyInfo, tokens int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recordedTok = append(f.recordedTok, tokens)
	return f.recordErr
}

func (f *fakeReserver) recordedSnapshot() []int {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]int, len(f.recordedTok))
	copy(out, f.recordedTok)
	return out
}

func withKeyInfo(req *http.Request) *http.Request {
	info := &auth.KeyInfo{KeyID: "k1", OrgID: "o1"}
	return req.WithContext(auth.WithKeyInfo(req.Context(), info))
}

func TestRateLimit_AllowedSetsXRateLimitHeaders(t *testing.T) {
	fr := &fakeReserver{nextRes: ratelimit.Reservation{
		Allowed:   true,
		Limit:     60,
		Remaining: 42,
		Reset:     30 * time.Second,
	}}
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"usage":{"total_tokens":1234}}`))
	}))

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, withKeyInfo(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "60", rec.Header().Get("X-RateLimit-Limit-Requests"))
	assert.Equal(t, "42", rec.Header().Get("X-RateLimit-Remaining-Requests"))
	assert.Equal(t, "30s", rec.Header().Get("X-RateLimit-Reset-Requests"))
	assert.Empty(t, rec.Header().Get("Retry-After"), "Retry-After only on 429")
	assert.JSONEq(t, `{"usage":{"total_tokens":1234}}`, rec.Body.String())
	assert.Equal(t, []int{1234}, fr.recordedSnapshot(), "non-streaming JSON usage is recorded")
}

func TestRateLimit_RejectedReturns429WithRetryAfter(t *testing.T) {
	fr := &fakeReserver{nextRes: ratelimit.Reservation{
		Allowed:    false,
		Limit:      60,
		Remaining:  0,
		Reset:      27 * time.Second,
		RetryAfter: 27 * time.Second,
	}}
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("downstream handler must not run when rejected")
	}))

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, withKeyInfo(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "60", rec.Header().Get("X-RateLimit-Limit-Requests"))
	assert.Equal(t, "0", rec.Header().Get("X-RateLimit-Remaining-Requests"))
	assert.Equal(t, "27s", rec.Header().Get("X-RateLimit-Reset-Requests"))
	assert.Equal(t, "27", rec.Header().Get("Retry-After"))

	var body struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
	assert.Equal(t, "rate_limit_error", body.Error.Type)
	assert.Equal(t, "rate_limit_exceeded", body.Error.Code)
	assert.Contains(t, body.Error.Message, "27s")
}

func TestRateLimit_StreamingResponseSkipsTokenRecording(t *testing.T) {
	fr := &fakeReserver{nextRes: ratelimit.Reservation{Allowed: true, Limit: 60, Remaining: 59}}
	chunks := []string{
		`data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n\n",
		`data: {"choices":[{"delta":{"content":" there"}}]}` + "\n\n",
		`data: [DONE]` + "\n\n",
	}
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, c := range chunks {
			_, _ = io.WriteString(w, c)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		}
	}))

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, withKeyInfo(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Equal(t, strings.Join(chunks, ""), rec.Body.String())
	assert.Empty(t, fr.recordedSnapshot(), "streaming responses skip RecordTokens (TODO M8)")
}

func TestRateLimit_FailOpenOnReserveError(t *testing.T) {
	fr := &fakeReserver{nextErr: errors.New("redis dial: connection refused")}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	handlerRan := false
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerRan = true
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))

	req := withKeyInfo(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	req = req.WithContext(WithLogger(req.Context(), logger))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	assert.True(t, handlerRan, "fail-open lets the request through")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, logBuf.String(), "fail-open")
	assert.Contains(t, logBuf.String(), "redis dial")
}

func TestRateLimit_FailOpenWarnIsThrottled(t *testing.T) {
	fr := &fakeReserver{nextErr: errors.New("redis dial: connection refused")}

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 5; i++ {
		req := withKeyInfo(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
		req = req.WithContext(WithLogger(req.Context(), logger))
		mw.ServeHTTP(httptest.NewRecorder(), req)
	}

	count := strings.Count(logBuf.String(), "fail-open")
	assert.Equal(t, 1, count, "throttle suppresses additional WARNs within the 5s window")
}

func TestRateLimit_MissingKeyInfoPassesThrough(t *testing.T) {
	fr := &fakeReserver{}
	handlerRan := false
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerRan = true
		w.WriteHeader(http.StatusOK)
	}))

	// No KeyInfo on the context (e.g., AllowAll dev path).
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))

	assert.True(t, handlerRan)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, fr.recordedSnapshot(), "no KeyInfo → no record call")
}

func TestRateLimit_NilReserverIsIdentity(t *testing.T) {
	handlerRan := false
	mw := RateLimit(nil)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		handlerRan = true
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models", nil))

	assert.True(t, handlerRan)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("X-RateLimit-Limit-Requests"))
}

func TestRateLimit_NonJSONResponseSkipsTokenRecording(t *testing.T) {
	fr := &fakeReserver{nextRes: ratelimit.Reservation{Allowed: true, Limit: 60, Remaining: 59}}
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, withKeyInfo(httptest.NewRequest(http.MethodGet, "/v1/models", nil)))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "ok", rec.Body.String())
	assert.Empty(t, fr.recordedSnapshot(), "non-JSON responses are not parsed for usage")
}

func TestRateLimit_ErrorResponseDoesNotRecordTokens(t *testing.T) {
	fr := &fakeReserver{nextRes: ratelimit.Reservation{Allowed: true, Limit: 60, Remaining: 59}}
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"error":{"message":"oops"}}`))
	}))

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, withKeyInfo(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Empty(t, fr.recordedSnapshot(), "non-2xx → no usage parse")
}

func TestRateLimit_ResetRoundsUpToWholeSeconds(t *testing.T) {
	fr := &fakeReserver{nextRes: ratelimit.Reservation{
		Allowed:   true,
		Limit:     100,
		Remaining: 50,
		Reset:     12500 * time.Millisecond, // 12.5s
	}}
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, withKeyInfo(httptest.NewRequest(http.MethodGet, "/v1/models", nil)))

	got := rec.Header().Get("X-RateLimit-Reset-Requests")
	got = strings.TrimSpace(got)
	// 12.5s rounds up to 13s
	if got != "13s" {
		t.Fatalf("X-RateLimit-Reset-Requests = %q, want 13s", got)
	}
}

func TestRateLimit_RetryAfterAlwaysAtLeastOneSecond(t *testing.T) {
	// Verifies WriteRateLimit's lower-bound clamp: a sub-second retry
	// rounds to 1, not 0, so clients always see a sane Retry-After.
	fr := &fakeReserver{nextRes: ratelimit.Reservation{
		Allowed:    false,
		Limit:      60,
		RetryAfter: 200 * time.Millisecond,
	}}
	mw := RateLimit(fr)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("must not run")
	}))

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, withKeyInfo(httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)))

	got, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	require.NoError(t, err)
	assert.GreaterOrEqual(t, got, 1)
}
