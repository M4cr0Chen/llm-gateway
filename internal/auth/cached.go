package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hashicorp/golang-lru/v2/expirable"

	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

// negativeCacheTTL keeps unknown-hash misses for a short window so an
// attacker flooding random keys doesn't repeatedly hit Postgres.
const negativeCacheTTL = 30 * time.Second

// touchDebounce caps how often we update api_keys.last_used_at for a
// single key, so a hot key doesn't generate a write per request. The
// touched-at LRU uses the same TTL so its entries clear naturally.
const touchDebounce = 60 * time.Second

// touchWriteTimeout caps how long the background TouchLastUsedAt write may
// run. Set well above typical pgx round-trips while still bounding a stuck
// connection so the goroutine never leaks.
const touchWriteTimeout = 5 * time.Second

// APIKeyLookup is the subset of *store.APIKeyStore that the cache depends
// on. It exists for testability — tests can swap in a fake without standing
// up Postgres.
type APIKeyLookup interface {
	GetByHash(ctx context.Context, hashHex string) (*store.APIKey, error)
	TouchLastUsedAt(ctx context.Context, id string) error
}

// Cached is an Authenticator that hashes plaintext keys and looks them up
// via an APIKeyLookup, caching results in an expiring LRU. The empty value
// is not usable — construct with NewCached.
type Cached struct {
	store APIKeyLookup
	pos   *expirable.LRU[string, *KeyInfo]
	neg   *expirable.LRU[string, struct{}]
	// touched bounds the per-key debounce table so it can't grow without
	// bound under churn. Entries expire after touchDebounce, which is also
	// the value the debounce check uses, so eviction and policy stay aligned.
	touched *expirable.LRU[string, time.Time]
}

// NewCached returns a Cached authenticator backed by store. ttl applies to
// successful lookups; the negative cache uses a fixed shorter TTL.
func NewCached(s APIKeyLookup, ttl time.Duration, size int) *Cached {
	return &Cached{
		store:   s,
		pos:     expirable.NewLRU[string, *KeyInfo](size, nil, ttl),
		neg:     expirable.NewLRU[string, struct{}](size, nil, negativeCacheTTL),
		touched: expirable.NewLRU[string, time.Time](size, nil, touchDebounce),
	}
}

// Authenticate hashes plaintext and resolves it to a KeyInfo. Cache misses
// fall through to the store; unknown keys are negatively cached. Cache
// hits are re-checked against ExpiresAt so a short-lived key never lives
// past its real expiry, regardless of the cache TTL.
func (c *Cached) Authenticate(ctx context.Context, plaintext string) (*KeyInfo, error) {
	hash := HashKey(plaintext)
	if info, ok := c.pos.Get(hash); ok {
		if info.ExpiresAt != nil && !time.Now().Before(*info.ExpiresAt) {
			c.pos.Remove(hash)
			c.neg.Add(hash, struct{}{})
			return nil, ErrInvalidKey
		}
		c.maybeTouch(info.KeyID)
		return info, nil
	}
	if _, ok := c.neg.Get(hash); ok {
		return nil, ErrInvalidKey
	}

	row, err := c.store.GetByHash(ctx, hash)
	if errors.Is(err, store.ErrNotFound) {
		c.neg.Add(hash, struct{}{})
		return nil, ErrInvalidKey
	}
	if err != nil {
		return nil, err
	}

	info := &KeyInfo{
		KeyID:     row.ID,
		OrgID:     row.OrgID,
		Name:      row.Name,
		RPM:       row.RateLimitRPM,
		TPM:       row.RateLimitTPM,
		Scopes:    row.Scopes,
		ExpiresAt: row.ExpiresAt,
	}
	c.pos.Add(hash, info)
	c.maybeTouch(info.KeyID)
	return info, nil
}

// Invalidate drops a cached lookup. Called by the admin handler after
// revoking a key so the change is reflected without waiting for the TTL.
func (c *Cached) Invalidate(hashHex string) {
	c.pos.Remove(hashHex)
	c.neg.Remove(hashHex)
}

// maybeTouch fires a debounced TouchLastUsedAt for the given keyID. The
// background goroutine uses its own bounded context so a stuck Postgres
// connection can't leak the goroutine forever.
func (c *Cached) maybeTouch(keyID string) {
	if _, ok := c.touched.Get(keyID); ok {
		return
	}
	c.touched.Add(keyID, time.Now())

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), touchWriteTimeout)
		defer cancel()
		if err := c.store.TouchLastUsedAt(ctx, keyID); err != nil {
			slog.Default().Warn("touching api_key last_used_at failed", "key_id", keyID, "err", err)
		}
	}()
}
