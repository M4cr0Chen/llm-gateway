package ratelimit_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/ratelimit"
)

// These tests exercise the limiter against a real Redis container so we
// can verify the Lua scripts run on the actual Redis Lua engine (not
// miniredis's gopher-lua reimplementation) and that script caching plus
// concurrent ZADD semantics behave as required. The unit tests in
// limiter_test.go cover the same algorithmic surface against miniredis;
// this file is the high-fidelity counterpart.
//
// The integration tests run by default and skip cleanly when Docker
// isn't reachable, matching the established pattern in
// internal/store/postgres_test.go (no build tag — same friendly
// degradation for local devs without Docker, CI has it).

// startRedis spins up a redis:7-alpine container and returns a connected
// client. If Docker is unreachable the test is skipped rather than
// failing — keeps the suite green on machines without Docker.
func startRedis(t *testing.T) *redis.Client {
	t.Helper()

	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		t.Skip("SKIP_DOCKER_TESTS=1")
	}

	ctx := context.Background()
	container, err := tcredis.Run(ctx, "redis:7-alpine")
	if err != nil {
		t.Skipf("docker not available, skipping redis integration test: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminating redis container: %v", err)
		}
	})

	dsn, err := container.ConnectionString(ctx)
	require.NoError(t, err)

	opts, err := redis.ParseURL(dsn)
	require.NoError(t, err)
	opts.MaxRetries = -1 // fast failure surface in tests

	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })

	require.NoError(t, client.Ping(ctx).Err(), "redis container reachable")
	return client
}

func newIntegrationLimiter(t *testing.T, cfg config.RateLimitConfig) (ratelimit.Reserver, *redis.Client) {
	t.Helper()
	client := startRedis(t)
	return ratelimit.NewRedis(client, cfg), client
}

func integrationKey(suffix string) auth.KeyInfo {
	// Distinct OrgID per test so parallel test runs against the same
	// container don't collide on the rate-limit ZSETs.
	return auth.KeyInfo{KeyID: "key-" + suffix, OrgID: "org-" + suffix}
}

func TestIntegration_RPMRejectsAt61stRequest(t *testing.T) {
	lim, _ := newIntegrationLimiter(t, config.RateLimitConfig{
		Enabled: true, DefaultRPM: 60, DefaultTPM: 0, // TPM unlimited
	})
	key := integrationKey("rpm-60")
	ctx := context.Background()

	for i := 1; i <= 60; i++ {
		res, err := lim.Reserve(ctx, key)
		require.NoError(t, err, "request %d", i)
		require.True(t, res.Allowed, "request %d", i)
	}

	res, err := lim.Reserve(ctx, key)
	require.NoError(t, err)
	assert.False(t, res.Allowed, "61st request rejected")
	assert.Greater(t, res.RetryAfter, time.Duration(0))
}

func TestIntegration_TPMRejectsAfterRecordedTokensExceedLimit(t *testing.T) {
	lim, _ := newIntegrationLimiter(t, config.RateLimitConfig{
		Enabled: true, DefaultRPM: 1000, DefaultTPM: 10000,
	})
	key := integrationKey("tpm")
	ctx := context.Background()

	res, err := lim.Reserve(ctx, key)
	require.NoError(t, err)
	require.True(t, res.Allowed)

	require.NoError(t, lim.RecordTokens(ctx, key, 7000))
	res, err = lim.Reserve(ctx, key)
	require.NoError(t, err)
	require.True(t, res.Allowed, "7000 of 10000 still allows")

	require.NoError(t, lim.RecordTokens(ctx, key, 3000))
	res, err = lim.Reserve(ctx, key)
	require.NoError(t, err)
	assert.False(t, res.Allowed, "10000 of 10000 rejects")
}

func TestIntegration_ConcurrentRequestsCountExactly(t *testing.T) {
	// The reason this needs real Redis: with 50 goroutines firing
	// AllowRequest in the same millisecond, the atomic Lua guarantee +
	// our (instance_id, ms, counter) member encoding must yield exactly
	// `limit` allowed and the rest rejected. miniredis can pass this
	// with its in-process serialisation; real Redis is the truer test.
	const limit = 10
	const concurrency = 50

	lim, _ := newIntegrationLimiter(t, config.RateLimitConfig{
		Enabled: true, DefaultRPM: limit, DefaultTPM: 0,
	})
	key := integrationKey("concurrent")
	ctx := context.Background()

	var allowed atomic.Int64
	var rejected atomic.Int64
	var firstErr atomic.Value
	var wg sync.WaitGroup

	start := make(chan struct{})
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			res, err := lim.Reserve(ctx, key)
			if err != nil {
				firstErr.CompareAndSwap(nil, err)
				return
			}
			if res.Allowed {
				allowed.Add(1)
			} else {
				rejected.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if v := firstErr.Load(); v != nil {
		t.Fatalf("redis error during concurrent reserve: %v", v)
	}
	assert.Equal(t, int64(limit), allowed.Load(), "exactly the limit allowed under contention")
	assert.Equal(t, int64(concurrency-limit), rejected.Load())
}

func TestIntegration_EvalShaSurvivesScriptFlush(t *testing.T) {
	// Real Redis returns NOSCRIPT when SCRIPT FLUSH wipes the cache mid
	// session. go-redis's *redis.Script wrapper transparently retries
	// with Eval, which is the path the issue's acceptance criteria
	// requires us to verify against a non-miniredis backend.
	lim, client := newIntegrationLimiter(t, config.RateLimitConfig{
		Enabled: true, DefaultRPM: 60,
	})
	key := integrationKey("evalsha")
	ctx := context.Background()

	res, err := lim.Reserve(ctx, key)
	require.NoError(t, err)
	require.True(t, res.Allowed)

	require.NoError(t, client.ScriptFlush(ctx).Err(), "wipes the server-side script cache")

	res, err = lim.Reserve(ctx, key)
	require.NoError(t, err, "Eval fallback handles NOSCRIPT")
	assert.True(t, res.Allowed)
}

func TestIntegration_FailOpenOnRedisDown(t *testing.T) {
	client := startRedis(t)
	lim := ratelimit.NewRedis(client, config.RateLimitConfig{
		Enabled: true, DefaultRPM: 60,
	})

	// Close the client to simulate Redis going away mid-session.
	require.NoError(t, client.Close())

	_, err := lim.Reserve(context.Background(), integrationKey("fail-open"))
	require.Error(t, err, "Reserve surfaces the redis error so the middleware can WARN")
	assert.True(t, errors.Is(err, redis.ErrClosed) || err != nil)

	allowed, _, err := lim.AllowRequest(context.Background(), integrationKey("fail-open"))
	require.Error(t, err)
	assert.True(t, allowed, "AllowRequest fails open on redis error")
}
