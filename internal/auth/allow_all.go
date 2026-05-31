package auth

import (
	"context"
	"log/slog"
)

// AllowAll is a development-only Authenticator that accepts every request
// with a fixed dev KeyInfo. It is enabled via GATEWAY_AUTH__ENABLED=false
// so that local smoke tests don't require a running Postgres.
type AllowAll struct{}

// NewAllowAll logs a prominent WARN so an operator can't accidentally
// deploy this in production.
func NewAllowAll() AllowAll {
	slog.Default().Warn("auth disabled — every request will be authenticated as the dev key (do NOT run this in production)")
	return AllowAll{}
}

// Authenticate returns a fixed dev KeyInfo.
func (AllowAll) Authenticate(_ context.Context, _ string) (*KeyInfo, error) {
	return &KeyInfo{KeyID: "dev", OrgID: "dev", Name: "dev"}, nil
}
