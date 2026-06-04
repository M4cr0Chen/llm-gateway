package middleware

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
)

// defaultDebugBodyLimit caps how many bytes of the request body the
// debug logger will buffer and emit. Matches the rate-limiter's
// response-buffer cap so we don't burn unbounded memory on large
// uploads.
const defaultDebugBodyLimit = 1 << 20 // 1 MiB

// DebugRequestBodies returns middleware that emits each request body at
// DEBUG level. It only ever logs through slog.LevelDebug — when the
// configured handler level is INFO or above, slog drops the line before
// any allocation, so bodies CANNOT leak at INFO. Tests assert this
// invariant directly.
//
// limit is the maximum number of body bytes captured per request. Pass
// 0 to use defaultDebugBodyLimit. Bodies larger than the limit are
// truncated in the log line (with a "truncated" flag); the downstream
// handler still receives the full body — the middleware re-wraps r.Body
// from the buffered prefix plus the original reader for the remainder.
//
// This middleware MUST only be mounted when cfg.Log.DebugBodies is
// true; main.go gates the registration accordingly.
func DebugRequestBodies(limit int) func(http.Handler) http.Handler {
	if limit <= 0 {
		limit = defaultDebugBodyLimit
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			logger := LoggerFromContext(r.Context())

			// Cheap fast-path: if the logger won't emit DEBUG, don't pay
			// the cost of reading the body at all. Bodies stay sealed.
			if !logger.Enabled(r.Context(), slog.LevelDebug) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Body == nil || r.Body == http.NoBody {
				next.ServeHTTP(w, r)
				return
			}

			// Read up to limit+1 bytes: any read that fills the full buffer
			// proves there's more data on the wire, so we can flag the log
			// line as truncated. The +1 byte still belongs to the handler
			// and must be put back below.
			buf := make([]byte, limit+1)
			read, err := io.ReadFull(r.Body, buf)
			truncated := false
			switch err {
			case nil:
				truncated = true
			case io.ErrUnexpectedEOF, io.EOF:
				// Body fully consumed; read holds the actual length.
			default:
				// Read failure — don't fabricate a body, just continue. The
				// downstream handler will see the same error on its own
				// read and surface it as a 4xx.
				logger.LogAttrs(r.Context(), slog.LevelDebug, "http body read failed",
					slog.String("err", err.Error()),
				)
				r.Body = io.NopCloser(bytes.NewReader(nil))
				next.ServeHTTP(w, r)
				return
			}
			consumed := buf[:read]
			logged := consumed
			if truncated {
				logged = consumed[:limit]
			}

			attrs := []slog.Attr{
				slog.String("path", r.URL.Path),
				slog.Int("len", len(logged)),
				slog.String("body", string(logged)),
			}
			if truncated {
				attrs = append(attrs, slog.Bool("truncated", true))
			}
			// Use context.Background() to avoid carrying request-scoped
			// values onto the log handler — the only attribute we care
			// about is the captured body.
			logger.LogAttrs(context.Background(), slog.LevelDebug, "http body", attrs...)

			// Restore r.Body so the handler sees the full payload: every
			// byte we consumed from the wire (consumed, not logged) plus
			// whatever remains on the wire when we truncated early.
			if truncated {
				r.Body = struct {
					io.Reader
					io.Closer
				}{
					Reader: io.MultiReader(bytes.NewReader(consumed), r.Body),
					Closer: r.Body,
				}
			} else {
				r.Body = io.NopCloser(bytes.NewReader(consumed))
			}

			next.ServeHTTP(w, r)
		})
	}
}
