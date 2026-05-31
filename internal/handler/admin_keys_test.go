package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

type fakeAPIKeyStore struct {
	mu        sync.Mutex
	created   *store.APIKey
	revokedID string
	byID      map[string]*store.APIKey
}

func (f *fakeAPIKeyStore) Create(_ context.Context, k *store.NewKey, hashHex, prefix string) (*store.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &store.APIKey{
		ID: "key-uuid-1", KeyHash: hashHex, KeyPrefix: prefix, OrgID: k.OrgID, Name: k.Name,
		RateLimitRPM: k.RateLimitRPM, RateLimitTPM: k.RateLimitTPM, IsActive: true,
	}
	f.created = out
	if f.byID == nil {
		f.byID = make(map[string]*store.APIKey)
	}
	f.byID[out.ID] = out
	return out, nil
}

func (f *fakeAPIKeyStore) GetByID(_ context.Context, id string) (*store.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.byID[id]; ok {
		return k, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeAPIKeyStore) Revoke(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revokedID = id
	return nil
}

func TestAdminKeys_Create_ReturnsPlaintextAndDoesNotLogIt(t *testing.T) {
	// Capture logs to assert the plaintext never appears.
	var logBuf bytes.Buffer
	prevDefault := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prevDefault) })

	fakeStore := &fakeAPIKeyStore{}
	var invalidatedHash string
	h := NewAdminKeysHandler(fakeStore, func(hash string) { invalidatedHash = hash })

	body := `{"org_id":"00000000-0000-0000-0000-000000000001","name":"prod","rate_limit_rpm":60,"rate_limit_tpm":100000}`
	req := httptest.NewRequest(http.MethodPost, "/internal/admin/keys", strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp CreateKeyResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.NotEmpty(t, resp.Key)
	assert.True(t, strings.HasPrefix(resp.Key, "sk-gw-"), "plaintext key must start with sk-gw-")
	assert.Len(t, resp.Key, len("sk-gw-")+32)
	assert.Equal(t, "key-uuid-1", resp.ID)
	assert.Equal(t, resp.Key[:8], resp.KeyPrefix)

	// The stored hash must be SHA-256 of the plaintext, NOT the plaintext.
	require.NotNil(t, fakeStore.created)
	assert.NotEqual(t, resp.Key, fakeStore.created.KeyHash)
	assert.Len(t, fakeStore.created.KeyHash, 64)

	// The plaintext must NOT appear in any log line.
	assert.NotContains(t, logBuf.String(), resp.Key, "plaintext key leaked into logs")

	// Cache invalidation is NOT triggered on create — only on revoke.
	assert.Empty(t, invalidatedHash)
}

func TestAdminKeys_Revoke_InvalidatesCacheAndReturns204(t *testing.T) {
	fakeStore := &fakeAPIKeyStore{byID: map[string]*store.APIKey{
		"key-uuid-1": {ID: "key-uuid-1", KeyHash: "deadbeef"},
	}}
	var invalidated string
	h := NewAdminKeysHandler(fakeStore, func(hash string) { invalidated = hash })

	r := chi.NewRouter()
	r.Delete("/internal/admin/keys/{id}", h.Revoke)

	req := httptest.NewRequest(http.MethodDelete, "/internal/admin/keys/key-uuid-1", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, "key-uuid-1", fakeStore.revokedID)
	assert.Equal(t, "deadbeef", invalidated)
}

func TestAdminKeys_Revoke_IdempotentOnUnknownID(t *testing.T) {
	fakeStore := &fakeAPIKeyStore{} // no rows
	h := NewAdminKeysHandler(fakeStore, nil)

	r := chi.NewRouter()
	r.Delete("/internal/admin/keys/{id}", h.Revoke)

	req := httptest.NewRequest(http.MethodDelete, "/internal/admin/keys/missing", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNoContent, rec.Code)
}

func TestAdminKeys_Create_InvalidBody_400(t *testing.T) {
	h := NewAdminKeysHandler(&fakeAPIKeyStore{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/internal/admin/keys", strings.NewReader("not-json"))
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAdminKeys_Create_RequiresOrgIDAndName(t *testing.T) {
	h := NewAdminKeysHandler(&fakeAPIKeyStore{}, nil)

	req := httptest.NewRequest(http.MethodPost, "/internal/admin/keys", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	h.Create(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGeneratePlaintextKey_AlphabetAndLength(t *testing.T) {
	for i := 0; i < 50; i++ {
		k, err := generatePlaintextKey()
		require.NoError(t, err)
		require.True(t, strings.HasPrefix(k, "sk-gw-"))
		body := strings.TrimPrefix(k, "sk-gw-")
		require.Len(t, body, 32)
		for _, b := range []byte(body) {
			assert.Contains(t, keyAlphabet, string(b))
		}
	}
}

// Just to use errors import even if no other case in this file does, in
// case the build minimises imports differently across go versions.
var _ = errors.New
