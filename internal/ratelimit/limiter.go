// Package ratelimit implements per-API-key request- and token-rate
// throttling against Redis using sliding-window sorted sets.
//
// Data model (matches docs/data-model.md §Redis):
//
//	ratelimit:{org_id}:{key_id}:rpm  ZSET, score=now_ms, member=<request_id>
//	ratelimit:{org_id}:{key_id}:tpm  ZSET, score=now_ms, member=<request_id>:<tokens>
//
// A single Lua script (check.lua) runs per AllowRequest call so the RPM
// and TPM pre-checks plus the RPM tick are atomic; in particular, a
// TPM-rejected request never consumes an RPM slot. RecordTokens appends a
// single member to the TPM ZSET after a non-streaming response completes.
//
// TPM aggregation embeds the token count in the sorted-set member name
// rather than maintaining a parallel hash. The trade-off is Lua-side
// string parsing during the per-request TPM sum; for typical limits
// (e.g., 60 RPM × moderate TPM) the iteration is bounded by RPM and
// cheap. The alternative (fixed-window INCRBY counters) was rejected to
// keep RPM and TPM on the same algorithm and make the X-RateLimit-Reset
// semantics consistent.
//
// All Redis errors fail-open: AllowRequest returns allowed=true with the
// raw error so the middleware can log a throttled WARN and forward the
// request. RecordTokens swallows errors the same way at its callsite.
package ratelimit

import (
	"context"
	_ "embed"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/config"
)

//go:embed check.lua
var checkLua string

//go:embed tpm_record.lua
var tpmRecordLua string

// KeyInfo aliases auth.KeyInfo so callers can pass the request-context
// KeyInfo without an extra conversion and the middleware can avoid
// importing both packages.
type KeyInfo = auth.KeyInfo

// Limiter is the canonical gateway rate-limit interface. Middleware
// generally uses the richer Reserver below; Limiter is preserved so other
// callers (e.g., a future budget enforcer) can depend on the minimal
// allow/deny shape.
type Limiter interface {
	AllowRequest(ctx context.Context, key KeyInfo) (allowed bool, retryAfter time.Duration, err error)
	RecordTokens(ctx context.Context, key KeyInfo, tokens int) error
}

// Reservation captures the post-decision state of a rate-limit check and
// is the payload consumed by the rate-limit middleware when populating
// the X-RateLimit-* response headers.
type Reservation struct {
	Allowed    bool
	Limit      int           // configured RPM for this key
	Remaining  int           // max(0, Limit - rpm_used)
	Reset      time.Duration // time until the oldest live RPM entry ages out
	RetryAfter time.Duration // > 0 only when Allowed == false
}

// Reserver is the richer interface used by the middleware. Reserve gates
// the request just like Limiter.AllowRequest but also returns the
// post-state needed for the response headers.
type Reserver interface {
	Limiter
	Reserve(ctx context.Context, key KeyInfo) (Reservation, error)
}

// redisLimiter is a Reserver backed by Redis. The two embedded Lua
// scripts are wrapped in *redis.Script which transparently uses EvalSha
// after first load and falls back to Eval on NOSCRIPT.
type redisLimiter struct {
	client       *redis.Client
	checkScript  *redis.Script
	recordScript *redis.Script
	window       time.Duration
	defaultRPM   int
	defaultTPM   int
}

// NewRedis constructs a Reserver backed by the given Redis client. The
// caller owns the client and is responsible for closing it on shutdown.
func NewRedis(client *redis.Client, cfg config.RateLimitConfig) Reserver {
	return &redisLimiter{
		client:       client,
		checkScript:  redis.NewScript(checkLua),
		recordScript: redis.NewScript(tpmRecordLua),
		window:       60 * time.Second,
		defaultRPM:   cfg.DefaultRPM,
		defaultTPM:   cfg.DefaultTPM,
	}
}

func (r *redisLimiter) effectiveRPM(key KeyInfo) int {
	if key.RPM > 0 {
		return key.RPM
	}
	return r.defaultRPM
}

func (r *redisLimiter) effectiveTPM(key KeyInfo) int {
	if key.TPM > 0 {
		return key.TPM
	}
	return r.defaultTPM
}

func (r *redisLimiter) rpmKey(key KeyInfo) string {
	return fmt.Sprintf("ratelimit:%s:%s:rpm", key.OrgID, key.KeyID)
}

func (r *redisLimiter) tpmKey(key KeyInfo) string {
	return fmt.Sprintf("ratelimit:%s:%s:tpm", key.OrgID, key.KeyID)
}

// reqIDCounter prevents collisions between concurrent AllowRequest calls
// that share a millisecond on the clock — without the suffix, ZADD would
// silently dedupe and the RPM count would skew low under high concurrency.
var reqIDCounter atomic.Uint64

func newReqID(nowMs int64) string {
	n := reqIDCounter.Add(1)
	return fmt.Sprintf("%d-%d", nowMs, n)
}

// AllowRequest satisfies Limiter; it discards the headers payload.
func (r *redisLimiter) AllowRequest(ctx context.Context, key KeyInfo) (bool, time.Duration, error) {
	res, err := r.Reserve(ctx, key)
	if err != nil {
		return true, 0, err
	}
	return res.Allowed, res.RetryAfter, nil
}

// Reserve atomically checks RPM and TPM, adds an RPM tick on success, and
// returns the post-state. A non-nil error from Redis surfaces as a
// fail-open signal to the middleware (Allowed=false is *only* used for
// genuine rate-limit rejections).
func (r *redisLimiter) Reserve(ctx context.Context, key KeyInfo) (Reservation, error) {
	rpm := r.effectiveRPM(key)
	tpm := r.effectiveTPM(key)
	if rpm <= 0 && tpm <= 0 {
		return Reservation{Allowed: true, Limit: 0}, nil
	}

	nowMs := time.Now().UnixMilli()
	windowMs := r.window.Milliseconds()

	out, err := r.checkScript.Run(ctx, r.client,
		[]string{r.rpmKey(key), r.tpmKey(key)},
		nowMs, windowMs, rpm, tpm, newReqID(nowMs),
	).Slice()
	if err != nil {
		return Reservation{}, err
	}

	parsed, perr := parseCheckResult(out)
	if perr != nil {
		return Reservation{}, perr
	}

	res := Reservation{
		Limit:   rpm,
		Allowed: parsed.allowed,
		Reset:   computeReset(nowMs, parsed.oldestMs, windowMs),
	}
	if parsed.used > rpm {
		res.Remaining = 0
	} else {
		res.Remaining = rpm - parsed.used
	}
	if !parsed.allowed {
		// Both rejection paths surface the RPM-window reset as Retry-After.
		// For TPM-driven rejection this is an over-estimate (true wait would
		// be the time for enough tokens to age out), but the simpler shape
		// keeps the script lean and clients only need an upper bound to retry.
		res.RetryAfter = res.Reset
		if res.RetryAfter <= 0 {
			res.RetryAfter = r.window
		}
	}
	return res, nil
}

type checkResult struct {
	allowed  bool
	code     int   // 0 ok, 1 rpm rejected, 2 tpm rejected
	used     int   // rpm count (post-add if allowed, pre-add if rejected)
	oldestMs int64 // oldest live RPM entry's score
	tpmUsed  int64
}

func parseCheckResult(out []interface{}) (checkResult, error) {
	if len(out) != 5 {
		return checkResult{}, fmt.Errorf("check script: got %d values, want 5", len(out))
	}
	a, ok := out[0].(int64)
	if !ok {
		return checkResult{}, fmt.Errorf("check script: allowed not int64 (%T)", out[0])
	}
	c, ok := out[1].(int64)
	if !ok {
		return checkResult{}, fmt.Errorf("check script: code not int64 (%T)", out[1])
	}
	u, ok := out[2].(int64)
	if !ok {
		return checkResult{}, fmt.Errorf("check script: used not int64 (%T)", out[2])
	}
	o, ok := out[3].(int64)
	if !ok {
		return checkResult{}, fmt.Errorf("check script: oldest not int64 (%T)", out[3])
	}
	t, ok := out[4].(int64)
	if !ok {
		return checkResult{}, fmt.Errorf("check script: tpm not int64 (%T)", out[4])
	}
	return checkResult{
		allowed:  a == 1,
		code:     int(c),
		used:     int(u),
		oldestMs: o,
		tpmUsed:  t,
	}, nil
}

func computeReset(nowMs, oldestMs, windowMs int64) time.Duration {
	elapsed := nowMs - oldestMs
	remaining := windowMs - elapsed
	if remaining < 0 {
		remaining = 0
	}
	return time.Duration(remaining) * time.Millisecond
}

// RecordTokens appends a sized entry to the TPM sorted set so the next
// AllowRequest's TPM sum reflects this request's usage. tokens<=0 and
// unlimited-TPM keys are no-ops; Redis errors are returned to the caller
// (which logs them at DEBUG since the response has already shipped).
func (r *redisLimiter) RecordTokens(ctx context.Context, key KeyInfo, tokens int) error {
	if tokens <= 0 {
		return nil
	}
	if r.effectiveTPM(key) <= 0 {
		return nil
	}
	nowMs := time.Now().UnixMilli()
	windowMs := r.window.Milliseconds()
	return r.recordScript.Run(ctx, r.client,
		[]string{r.tpmKey(key)},
		nowMs, windowMs, newReqID(nowMs), tokens,
	).Err()
}
