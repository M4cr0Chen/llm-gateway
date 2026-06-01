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
	"crypto/rand"
	_ "embed"
	"encoding/hex"
	"fmt"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/config"
)

// reject codes returned by check.lua (see check.lua's header comment).
const (
	rejectOK  = 0
	rejectRPM = 1
	rejectTPM = 2
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
// the X-RateLimit-* response headers. The RPM-side fields are populated
// whenever an RPM limit is configured; the TPM-side fields are populated
// whenever a TPM limit is configured. A zero Limit / TpmLimit means that
// dimension is unlimited and the middleware skips its headers.
type Reservation struct {
	Allowed      bool
	Limit        int           // configured RPM for this key (0 → unlimited)
	Remaining    int           // max(0, Limit - rpm_used)
	Reset        time.Duration // time until the oldest live RPM entry ages out
	TpmLimit     int           // configured TPM for this key (0 → unlimited)
	TpmRemaining int           // max(0, TpmLimit - tpm_used)
	TpmReset     time.Duration // time until the oldest live TPM entry ages out
	RetryAfter   time.Duration // > 0 only when Allowed == false
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

// instanceID is a per-process random prefix attached to every reqID. With
// only a (ms, counter) suffix, two gateway instances sharing the same
// Redis would emit identical members for their first concurrent requests
// (both counters start at 1) and ZADD would silently dedupe them — the
// RPM ZSET would then undercount across the fleet. A 6-byte random prefix
// makes cross-instance collisions astronomically unlikely.
var instanceID = newInstanceID()

func newInstanceID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure at init time is exceedingly unlikely; fall
		// back to a nanosecond-resolution timestamp so we still get a
		// process-specific prefix even in the degenerate case.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func newReqID(nowMs int64) string {
	n := reqIDCounter.Add(1)
	return fmt.Sprintf("%s-%d-%d", instanceID, nowMs, n)
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
		return Reservation{Allowed: true}, nil
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
		Limit:    rpm,
		TpmLimit: tpm,
		Allowed:  parsed.allowed,
		Reset:    computeReset(nowMs, parsed.rpmOldestMs, windowMs),
		TpmReset: computeReset(nowMs, parsed.tpmOldestMs, windowMs),
	}
	res.Remaining = clampRemaining(rpm, parsed.rpmUsed)
	res.TpmRemaining = clampRemaining(tpm, int(parsed.tpmUsed))
	if !parsed.allowed {
		// Retry-After uses the dimension that triggered rejection: RPM
		// reset for RPM rejection, TPM reset for TPM rejection. For TPM
		// this is the time until the oldest token entry ages out — only a
		// lower bound on actual relief, since the next request can still
		// be rejected if the freed tokens don't cover its size. Clients
		// should treat Retry-After as "earliest moment a retry could
		// succeed", not a guarantee.
		switch parsed.code {
		case rejectRPM:
			res.RetryAfter = res.Reset
		case rejectTPM:
			res.RetryAfter = res.TpmReset
		}
		if res.RetryAfter <= 0 {
			res.RetryAfter = r.window
		}
	}
	return res, nil
}

func clampRemaining(limit, used int) int {
	if limit <= 0 || used >= limit {
		return 0
	}
	return limit - used
}

type checkResult struct {
	allowed     bool
	code        int   // 0 ok, 1 rpm rejected, 2 tpm rejected
	rpmUsed     int   // rpm count (post-add if allowed, pre-add if rejected)
	rpmOldestMs int64 // oldest live RPM entry's score (or now if empty)
	tpmUsed     int64
	tpmOldestMs int64 // oldest live TPM entry's score (or now if empty)
}

func parseCheckResult(out []interface{}) (checkResult, error) {
	const want = 6
	if len(out) != want {
		return checkResult{}, fmt.Errorf("check script: got %d values, want %d", len(out), want)
	}
	fields := [want]int64{}
	names := [want]string{"allowed", "code", "rpm_used", "rpm_oldest_ms", "tpm_used", "tpm_oldest_ms"}
	for i, v := range out {
		n, ok := v.(int64)
		if !ok {
			return checkResult{}, fmt.Errorf("check script: %s not int64 (%T)", names[i], v)
		}
		fields[i] = n
	}
	return checkResult{
		allowed:     fields[0] == 1,
		code:        int(fields[1]),
		rpmUsed:     int(fields[2]),
		rpmOldestMs: fields[3],
		tpmUsed:     fields[4],
		tpmOldestMs: fields[5],
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
