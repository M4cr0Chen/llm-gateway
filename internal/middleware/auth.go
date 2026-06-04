package middleware

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"

	"github.com/M4cr0Chen/llm-gateway/internal/apierr"
	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/metrics"
)

// RequireAPIKey returns middleware that authenticates Bearer-token API keys
// against the given Authenticator. On success the KeyInfo is attached to
// the request context. On failure the response is an OpenAI-compatible 401.
func RequireAPIKey(a auth.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if strings.TrimSpace(header) == "" {
				metrics.RecordAuthFailure("missing")
				writeAuthError(w, "Missing or malformed Authorization header.")
				return
			}
			token, ok := bearerToken(header)
			if !ok {
				metrics.RecordAuthFailure("malformed")
				writeAuthError(w, "Missing or malformed Authorization header.")
				return
			}
			info, err := a.Authenticate(r.Context(), token)
			if errors.Is(err, auth.ErrInvalidKey) {
				metrics.RecordAuthFailure("unknown")
				writeAuthError(w, "Invalid API key.")
				return
			}
			if err != nil {
				// Backend unavailable: surfaced as 503, not an auth
				// failure — deliberately not counted in
				// gateway_auth_failures_total.
				LoggerFromContext(r.Context()).Error("auth lookup failed", "err", err)
				apierr.Write(w, http.StatusServiceUnavailable, "internal_error", "internal_error",
					"authentication backend unavailable")
				return
			}
			ctx := auth.WithKeyInfo(r.Context(), info)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// RequireAdminToken returns middleware that gates a route on a static
// admin token compared in constant time. An empty expected token rejects
// every request (fail-closed). Failure reasons are split into
// missing/malformed/unknown for parity with RequireAPIKey so the metric
// label distribution is comparable.
func RequireAdminToken(expected string) func(http.Handler) http.Handler {
	expectedBytes := []byte(expected)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			if strings.TrimSpace(header) == "" {
				metrics.RecordAuthFailure("missing")
				writeAuthError(w, "Invalid admin token.")
				return
			}
			token, ok := bearerToken(header)
			if !ok {
				metrics.RecordAuthFailure("malformed")
				writeAuthError(w, "Invalid admin token.")
				return
			}
			if len(expectedBytes) == 0 ||
				subtle.ConstantTimeCompare([]byte(token), expectedBytes) != 1 {
				metrics.RecordAuthFailure("unknown")
				writeAuthError(w, "Invalid admin token.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// bearerToken parses an "Authorization: Bearer <token>" header. The prefix
// match is case-insensitive; surrounding whitespace is trimmed.
func bearerToken(header string) (string, bool) {
	header = strings.TrimSpace(header)
	const prefix = "bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

func writeAuthError(w http.ResponseWriter, msg string) {
	apierr.Write(w, http.StatusUnauthorized, "authentication_error", "invalid_api_key", msg)
}
