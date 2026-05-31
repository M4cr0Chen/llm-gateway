// Package auth handles API key authentication for the gateway. The public
// surface is the Authenticator interface plus the KeyInfo context helpers;
// implementations may be database-backed (cached) or development-only
// (AllowAll).
package auth

import (
	"context"
	"errors"
)

// KeyInfo describes an authenticated API key. It is attached to the
// request context by RequireAPIKey middleware so downstream handlers can
// scope behaviour to the owning organisation.
type KeyInfo struct {
	KeyID  string
	OrgID  string
	Name   string
	RPM    int
	TPM    int
	Scopes []string
}

// Authenticator resolves a plaintext API key to a KeyInfo. Implementations
// must hash the plaintext internally; callers MUST NOT log it.
type Authenticator interface {
	Authenticate(ctx context.Context, plaintext string) (*KeyInfo, error)
}

// ErrInvalidKey indicates the plaintext key is unknown, revoked, or expired.
// Callers translate this to a 401 response.
var ErrInvalidKey = errors.New("invalid api key")

type ctxKey int

const keyInfoCtxKey ctxKey = iota

// WithKeyInfo returns a context carrying the given KeyInfo.
func WithKeyInfo(ctx context.Context, info *KeyInfo) context.Context {
	return context.WithValue(ctx, keyInfoCtxKey, info)
}

// KeyInfoFromContext returns the KeyInfo stored in ctx, if any.
func KeyInfoFromContext(ctx context.Context) (*KeyInfo, bool) {
	info, ok := ctx.Value(keyInfoCtxKey).(*KeyInfo)
	return info, ok
}
