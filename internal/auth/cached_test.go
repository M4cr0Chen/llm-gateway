package auth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

type fakeLookup struct {
	mu          sync.Mutex
	getCalls    atomic.Int32
	touchCalls  atomic.Int32
	keysByHash  map[string]*store.APIKey
	getErr      error
}

func newFakeLookup() *fakeLookup {
	return &fakeLookup{keysByHash: make(map[string]*store.APIKey)}
}

func (f *fakeLookup) GetByHash(_ context.Context, hash string) (*store.APIKey, error) {
	f.getCalls.Add(1)
	if f.getErr != nil {
		return nil, f.getErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.keysByHash[hash]; ok {
		return k, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeLookup) TouchLastUsedAt(_ context.Context, _ string) error {
	f.touchCalls.Add(1)
	return nil
}

func TestCached_HitsCacheOnSecondLookup(t *testing.T) {
	plaintext := "sk-gw-known"
	hash := HashKey(plaintext)

	f := newFakeLookup()
	f.keysByHash[hash] = &store.APIKey{
		ID: "k1", OrgID: "o1", Name: "k", RateLimitRPM: 60, RateLimitTPM: 100000,
	}

	c := NewCached(f, time.Minute, 16)

	for i := 0; i < 3; i++ {
		info, err := c.Authenticate(context.Background(), plaintext)
		require.NoError(t, err)
		require.NotNil(t, info)
		assert.Equal(t, "k1", info.KeyID)
		assert.Equal(t, "o1", info.OrgID)
		assert.Equal(t, 60, info.RPM)
	}

	assert.EqualValues(t, 1, f.getCalls.Load(), "should hit DB exactly once across 3 lookups")
}

func TestCached_NegativeCacheForUnknownKey(t *testing.T) {
	f := newFakeLookup() // no keys

	c := NewCached(f, time.Minute, 16)

	for i := 0; i < 3; i++ {
		_, err := c.Authenticate(context.Background(), "sk-gw-unknown")
		assert.ErrorIs(t, err, ErrInvalidKey)
	}

	assert.EqualValues(t, 1, f.getCalls.Load(), "negative cache should suppress repeat DB hits")
}

func TestCached_PropagatesNonNotFoundError(t *testing.T) {
	f := newFakeLookup()
	f.getErr = errors.New("connection refused")

	c := NewCached(f, time.Minute, 16)

	_, err := c.Authenticate(context.Background(), "sk-gw-anything")
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrInvalidKey, "transient errors must not be cached as invalid")

	_, err = c.Authenticate(context.Background(), "sk-gw-anything")
	require.Error(t, err)
	assert.EqualValues(t, 2, f.getCalls.Load(), "transient errors should retry, not be negatively cached")
}

func TestCached_Invalidate(t *testing.T) {
	plaintext := "sk-gw-revokeme"
	hash := HashKey(plaintext)

	f := newFakeLookup()
	f.keysByHash[hash] = &store.APIKey{ID: "k1", OrgID: "o1", Name: "k", RateLimitRPM: 60}

	c := NewCached(f, time.Minute, 16)

	_, err := c.Authenticate(context.Background(), plaintext)
	require.NoError(t, err)
	assert.EqualValues(t, 1, f.getCalls.Load())

	c.Invalidate(hash)

	_, err = c.Authenticate(context.Background(), plaintext)
	require.NoError(t, err)
	assert.EqualValues(t, 2, f.getCalls.Load(), "after invalidate, next lookup should hit the store")
}

func TestCached_TouchDebounce(t *testing.T) {
	plaintext := "sk-gw-touchme"
	hash := HashKey(plaintext)

	f := newFakeLookup()
	f.keysByHash[hash] = &store.APIKey{ID: "k1", OrgID: "o1", Name: "k"}

	c := NewCached(f, time.Minute, 16)

	for i := 0; i < 5; i++ {
		_, err := c.Authenticate(context.Background(), plaintext)
		require.NoError(t, err)
	}

	// Touch fires in a goroutine; give it a moment.
	deadline := time.Now().Add(500 * time.Millisecond)
	for f.touchCalls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}

	assert.EqualValues(t, 1, f.touchCalls.Load(), "5 quick lookups should debounce to a single touch")
}
