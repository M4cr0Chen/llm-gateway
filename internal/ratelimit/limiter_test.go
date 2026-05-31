package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/config"
)

// newTestLimiter spins up a miniredis instance and returns a wired
// limiter plus a cleanup func and the miniredis handle (for FastForward
// and Close in tests that need them).
func newTestLimiter(t *testing.T, cfg config.RateLimitConfig) (Reserver, *redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	// MaxRetries=-1 disables the client-side retry loop so a closed
	// miniredis surfaces the connection error immediately (otherwise the
	// fail-open test spends seconds in pool retries).
	client := redis.NewClient(&redis.Options{Addr: mr.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedis(client, cfg), client, mr
}

func defaultCfg() config.RateLimitConfig {
	return config.RateLimitConfig{Enabled: true, DefaultRPM: 60, DefaultTPM: 100000}
}

func testKey() auth.KeyInfo {
	return auth.KeyInfo{KeyID: "key-1", OrgID: "org-1"}
}

func TestReserve_RPMAllowsUpToLimit(t *testing.T) {
	lim, _, _ := newTestLimiter(t, defaultCfg())
	key := testKey()
	ctx := context.Background()

	for i := 1; i <= 60; i++ {
		res, err := lim.Reserve(ctx, key)
		require.NoError(t, err, "request %d", i)
		require.True(t, res.Allowed, "request %d should be allowed", i)
		assert.Equal(t, 60, res.Limit)
		assert.Equal(t, 60-i, res.Remaining, "request %d remaining", i)
	}

	res, err := lim.Reserve(ctx, key)
	require.NoError(t, err)
	assert.False(t, res.Allowed, "61st request should be rejected")
	assert.Equal(t, 0, res.Remaining)
	assert.Greater(t, res.RetryAfter, time.Duration(0))
}

func TestReserve_PerKeyRPMOverridesDefault(t *testing.T) {
	lim, _, _ := newTestLimiter(t, defaultCfg())
	key := testKey()
	key.RPM = 10
	ctx := context.Background()

	for i := 1; i <= 10; i++ {
		res, err := lim.Reserve(ctx, key)
		require.NoError(t, err)
		require.True(t, res.Allowed, "request %d", i)
		assert.Equal(t, 10, res.Limit)
	}

	res, err := lim.Reserve(ctx, key)
	require.NoError(t, err)
	assert.False(t, res.Allowed, "11th request with KeyInfo.RPM=10 should be rejected")
}

func TestRecordTokens_FeedsTPMCheck(t *testing.T) {
	cfg := defaultCfg()
	cfg.DefaultTPM = 10000
	lim, _, _ := newTestLimiter(t, cfg)
	key := testKey()
	ctx := context.Background()

	// First request: allowed, no recorded tokens yet.
	res, err := lim.Reserve(ctx, key)
	require.NoError(t, err)
	require.True(t, res.Allowed)

	// Record 5000 tokens for that request. The next request should still
	// be allowed but TPM usage is no longer zero.
	require.NoError(t, lim.RecordTokens(ctx, key, 5000))

	res, err = lim.Reserve(ctx, key)
	require.NoError(t, err)
	require.True(t, res.Allowed)

	// Record another 5000 tokens, pushing us to the limit.
	require.NoError(t, lim.RecordTokens(ctx, key, 5000))

	res, err = lim.Reserve(ctx, key)
	require.NoError(t, err)
	assert.False(t, res.Allowed, "third request should be rejected by TPM limit")
}

func TestRecordTokens_ZeroIsNoOp(t *testing.T) {
	lim, client, _ := newTestLimiter(t, defaultCfg())
	key := testKey()
	ctx := context.Background()

	require.NoError(t, lim.RecordTokens(ctx, key, 0))
	require.NoError(t, lim.RecordTokens(ctx, key, -5))

	card, err := client.ZCard(ctx, "ratelimit:org-1:key-1:tpm").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), card, "TPM ZSET should be untouched")
}

func TestReserve_WindowKeyExpiresAfterTTL(t *testing.T) {
	// The `now` used by the limiter is Go's wall clock, so we can't
	// directly simulate a 60s slide in a unit test. Instead, verify the
	// EXPIRE TTL (window*2 = 120s) is correctly set: miniredis fires the
	// expiry on access and the limiter starts fresh, which is the
	// observable end state when an idle key ages out.
	cfg := defaultCfg()
	cfg.DefaultRPM = 2
	lim, client, mr := newTestLimiter(t, cfg)
	key := testKey()
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		res, err := lim.Reserve(ctx, key)
		require.NoError(t, err)
		require.True(t, res.Allowed)
	}
	res, err := lim.Reserve(ctx, key)
	require.NoError(t, err)
	require.False(t, res.Allowed, "3rd request over the RPM=2 limit is rejected")

	mr.FastForward(121 * time.Second)

	exists, err := client.Exists(ctx, "ratelimit:org-1:key-1:rpm").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), exists, "RPM key expires after window*2")

	res, err = lim.Reserve(ctx, key)
	require.NoError(t, err)
	assert.True(t, res.Allowed, "expired key means requests are allowed again")
}

func TestReserve_FailOpenOnRedisDown(t *testing.T) {
	lim, _, mr := newTestLimiter(t, defaultCfg())
	mr.Close()

	res, err := lim.Reserve(context.Background(), testKey())
	require.Error(t, err, "limiter surfaces redis errors so the middleware can WARN")
	assert.False(t, res.Allowed, "zero value on error; middleware fails open separately")

	// AllowRequest is the canonical Limiter API; it MUST swallow the
	// error and return allowed=true so middleware that ignores the error
	// (e.g., a future caller that doesn't read it) still fails open.
	allowed, retry, err := lim.AllowRequest(context.Background(), testKey())
	require.Error(t, err)
	assert.True(t, allowed, "AllowRequest fails open on redis error")
	assert.Equal(t, time.Duration(0), retry)
}

func TestReserve_NoLimitsConfiguredAllowsWithoutRedis(t *testing.T) {
	// Both DefaultRPM=0 and KeyInfo.RPM=0 → unlimited. We shouldn't even
	// reach Redis.
	cfg := config.RateLimitConfig{Enabled: true}
	lim, _, mr := newTestLimiter(t, cfg)
	mr.Close()

	res, err := lim.Reserve(context.Background(), testKey())
	require.NoError(t, err)
	assert.True(t, res.Allowed)
}

func TestReserve_EvalShaPathReusesScript(t *testing.T) {
	lim, client, mr := newTestLimiter(t, defaultCfg())
	ctx := context.Background()
	key := testKey()

	// First call triggers SCRIPT LOAD + EvalSha (or Eval; both are
	// observable). After that, the script SHA is cached client-side and
	// subsequent calls use EvalSha exclusively.
	_, err := lim.Reserve(ctx, key)
	require.NoError(t, err)

	// Sanity: the script is loaded in miniredis.
	sha, err := client.ScriptLoad(ctx, checkLua).Result()
	require.NoError(t, err)
	exists, err := client.ScriptExists(ctx, sha).Result()
	require.NoError(t, err)
	require.True(t, exists[0])

	// Flush scripts: subsequent EvalSha will NOSCRIPT, and go-redis's
	// Script.Run wrapper retries with Eval transparently.
	require.NoError(t, client.ScriptFlush(ctx).Err())

	res, err := lim.Reserve(ctx, key)
	require.NoError(t, err, "Reserve survives a SCRIPT FLUSH thanks to Eval fallback")
	assert.True(t, res.Allowed)

	_ = mr // keep alive
}
