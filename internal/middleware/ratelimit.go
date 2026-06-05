package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/apierr"
	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/metrics"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/ratelimit"
)

// failOpenWarnInterval throttles the WARN log emitted when Redis is
// unreachable. The first failure logs immediately; subsequent failures
// within this window are suppressed so an outage doesn't flood the log.
// Declared as a var (not a const) so tests can shorten the window without
// resorting to clock injection.
var failOpenWarnInterval = 5 * time.Second

// captureBodyLimit caps the response buffer used to parse `usage.total_tokens`
// out of non-streaming responses. Beyond this limit we stop buffering and
// skip token accounting for the request (still flushing the response).
const captureBodyLimit = 1 << 20 // 1 MiB

// RateLimit returns middleware that enforces per-key RPM and TPM limits
// using the given Reserver. The middleware MUST be registered after
// RequireAPIKey because it reads `*auth.KeyInfo` from the request
// context. When reserver is nil, an identity middleware is returned (the
// chain is unchanged), which lets the caller drop the limiter without
// branching at server-construction time.
//
// Behaviour on the hot path:
//
//   - Pre-handler: call Reserve. On Redis error, log a throttled WARN and
//     fail-open (let the request through). On reject, write the 429 with
//     Retry-After + X-RateLimit-* headers. On allow, set X-RateLimit-*
//     headers and wrap the ResponseWriter.
//   - Post-handler: if the response was non-streaming JSON, parse
//     `usage.total_tokens` and call RecordTokens. Streaming responses
//     pass through with a TODO(M8) marker — mid-stream token accounting
//     is the M8 (Streaming Enhancements) milestone.
func RateLimit(reserver ratelimit.Reserver) func(http.Handler) http.Handler {
	if reserver == nil {
		return func(next http.Handler) http.Handler { return next }
	}
	state := &rateLimitState{reserver: reserver}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			state.serve(next, w, r)
		})
	}
}

type rateLimitState struct {
	reserver       ratelimit.Reserver
	lastWarnUnixNs atomic.Int64
}

func (s *rateLimitState) serve(next http.Handler, w http.ResponseWriter, r *http.Request) {
	info, ok := auth.KeyInfoFromContext(r.Context())
	if !ok {
		// No KeyInfo means auth either ran in AllowAll mode (dev) or the
		// chain is misconfigured. Either way, there's nothing to throttle
		// against — pass through unchanged.
		next.ServeHTTP(w, r)
		return
	}

	res, err := s.reserver.Reserve(r.Context(), *info)
	if err != nil {
		// Fail-open: log a throttled WARN and let the request through. We
		// deliberately don't emit X-RateLimit-* headers here — without a
		// successful check the values would be guesses, and an absent
		// header is a clearer "limits unavailable right now" signal than
		// a stale one.
		metrics.RecordRateLimit("failopen")
		s.warnFailOpen(r, err)
		next.ServeHTTP(w, r)
		return
	}

	setRateLimitHeaders(w, res)

	if !res.Allowed {
		metrics.RecordRateLimit("throttled")
		MarkRateLimited(r.Context())
		apierr.WriteRateLimit(w, res.RetryAfter, "")
		return
	}

	metrics.RecordRateLimit("allowed")
	cw := &usageCaptureWriter{ResponseWriter: w}
	next.ServeHTTP(cw, r)
	cw.finish(r, s.reserver, *info)
}

func (s *rateLimitState) warnFailOpen(r *http.Request, err error) {
	now := time.Now().UnixNano()
	prev := s.lastWarnUnixNs.Load()
	if prev != 0 && time.Duration(now-prev) < failOpenWarnInterval {
		return
	}
	if !s.lastWarnUnixNs.CompareAndSwap(prev, now) {
		return // another goroutine already won the race; let it log.
	}
	LoggerFromContext(r.Context()).Warn(
		"rate limiter fail-open: redis error",
		"err", err.Error(),
	)
}

// setRateLimitHeaders emits the OpenAI-compatible rate-limit headers for
// both dimensions present in the Reservation. RPM headers are written iff
// res.Limit > 0; TPM headers are written iff res.TpmLimit > 0 — so a key
// throttled on requests only (or tokens only) sees only the relevant set.
func setRateLimitHeaders(w http.ResponseWriter, res ratelimit.Reservation) {
	h := w.Header()
	if res.Limit > 0 {
		h.Set("X-RateLimit-Limit-Requests", strconv.Itoa(res.Limit))
		h.Set("X-RateLimit-Remaining-Requests", strconv.Itoa(clampNonNegative(res.Remaining)))
		h.Set("X-RateLimit-Reset-Requests", roundDurationToSecond(res.Reset).String())
	}
	if res.TpmLimit > 0 {
		h.Set("X-RateLimit-Limit-Tokens", strconv.Itoa(res.TpmLimit))
		h.Set("X-RateLimit-Remaining-Tokens", strconv.Itoa(clampNonNegative(res.TpmRemaining)))
		h.Set("X-RateLimit-Reset-Tokens", roundDurationToSecond(res.TpmReset).String())
	}
}

func clampNonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// roundDurationToSecond rounds the duration up to the nearest whole
// second so the header reads like "30s" rather than "30.214s". A zero or
// negative duration becomes "0s".
func roundDurationToSecond(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	secs := int64(d / time.Second)
	if d%time.Second != 0 {
		secs++
	}
	return time.Duration(secs) * time.Second
}

// usageCaptureWriter wraps a ResponseWriter to record `usage.total_tokens`
// from a non-streaming JSON response after the handler completes. It
// detects the mode lazily from the response Content-Type when WriteHeader
// (or the first Write, implicitly) fires:
//
//   - text/event-stream → streaming; pass writes straight through. The
//     M8 milestone adds mid-stream token accounting; for now we
//     deliberately skip it. // TODO(M8)
//   - application/json → buffer the body up to captureBodyLimit; flush at
//     handler exit after parsing usage.
//   - anything else → passthrough, no token accounting.
type usageCaptureWriter struct {
	http.ResponseWriter
	status        int
	wroteHeader   bool
	mode          captureMode
	buf           bytes.Buffer
	exceededLimit bool
}

type captureMode int

const (
	modeUndecided captureMode = iota
	modeBuffer
	modeStream
	modePassthrough
)

func (cw *usageCaptureWriter) WriteHeader(status int) {
	if cw.wroteHeader {
		return
	}
	cw.status = status
	cw.wroteHeader = true
	cw.decideMode()

	switch cw.mode {
	case modeStream, modePassthrough:
		cw.ResponseWriter.WriteHeader(status)
	case modeBuffer:
		// Defer writing the header until finish() so we can also drop the
		// captured body if needed. The handler-set headers stay queued on
		// the underlying header map and ship together when we flush.
	}
}

func (cw *usageCaptureWriter) Write(b []byte) (int, error) {
	if !cw.wroteHeader {
		cw.WriteHeader(http.StatusOK)
	}
	switch cw.mode {
	case modeBuffer:
		if cw.exceededLimit {
			return cw.ResponseWriter.Write(b)
		}
		if cw.buf.Len()+len(b) > captureBodyLimit {
			cw.exceededLimit = true
			// Replay any buffered prefix, then pass this write through.
			if cw.buf.Len() > 0 {
				cw.ResponseWriter.WriteHeader(cw.status)
				if _, err := cw.ResponseWriter.Write(cw.buf.Bytes()); err != nil {
					return 0, err
				}
				cw.buf.Reset()
			} else {
				cw.ResponseWriter.WriteHeader(cw.status)
			}
			return cw.ResponseWriter.Write(b)
		}
		return cw.buf.Write(b)
	default:
		return cw.ResponseWriter.Write(b)
	}
}

// Flush forwards to the underlying writer when it supports http.Flusher.
// In buffer mode flush is a no-op until finish() runs — the handler is
// JSON and doesn't need incremental delivery.
func (cw *usageCaptureWriter) Flush() {
	if cw.mode == modeBuffer {
		return
	}
	if f, ok := cw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (cw *usageCaptureWriter) decideMode() {
	ct := cw.ResponseWriter.Header().Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		cw.mode = modeStream // TODO(M8): count streaming tokens via final chunk usage
	case strings.HasPrefix(ct, "application/json"):
		cw.mode = modeBuffer
	default:
		cw.mode = modePassthrough
	}
}

// finish flushes the buffered body (in buffer mode) and records the
// response's reported token usage. Called once after the inner handler
// returns.
func (cw *usageCaptureWriter) finish(r *http.Request, reserver ratelimit.Reserver, key auth.KeyInfo) {
	if !cw.wroteHeader {
		// Handler wrote nothing — treat as no-op.
		return
	}
	if cw.mode != modeBuffer {
		return
	}
	body := cw.buf.Bytes()
	cw.buf = bytes.Buffer{}

	// Only attempt token parsing on 2xx — error bodies don't carry
	// `usage` and we don't want to record tokens for failed requests.
	//
	// RecordTokens is called synchronously *before* the response flushes:
	// this guarantees the next concurrent request from the same key sees
	// this request's tokens in its TPM sum, which keeps the limit honest
	// under burst traffic. The cost is one extra Redis round-trip on the
	// success path; for a TPM check we'd otherwise lose a window's worth
	// of accuracy. If that latency becomes a problem we can move this to
	// a background goroutine with a derived context, accepting looser
	// TPM bounds during bursts.
	if cw.status >= 200 && cw.status < 300 && !cw.exceededLimit && len(body) > 0 {
		var parsed struct {
			Usage model.Usage `json:"usage"`
		}
		if err := json.Unmarshal(body, &parsed); err == nil && parsed.Usage.TotalTokens > 0 {
			if rerr := reserver.RecordTokens(r.Context(), key, parsed.Usage.TotalTokens); rerr != nil {
				LoggerFromContext(r.Context()).Debug(
					"rate limiter: failed to record tokens",
					"err", rerr.Error(),
				)
			}
		}
	}

	// Flush header + buffered body to the real writer.
	if !cw.exceededLimit {
		cw.ResponseWriter.WriteHeader(cw.status)
		if len(body) > 0 {
			_, _ = cw.ResponseWriter.Write(body)
		}
	}
}
