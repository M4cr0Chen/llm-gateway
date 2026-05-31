package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/M4cr0Chen/llm-gateway/internal/apierr"
	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

// APIKeyStore is the subset of *store.APIKeyStore the admin handler relies
// on. It is an interface so tests can supply a fake without standing up
// Postgres.
type APIKeyStore interface {
	Create(ctx context.Context, k *store.NewKey, hashHex, prefix string) (*store.APIKey, error)
	GetByID(ctx context.Context, id string) (*store.APIKey, error)
	Revoke(ctx context.Context, id string) error
}

// AdminKeysHandler implements POST/DELETE /internal/admin/keys.
type AdminKeysHandler struct {
	store      APIKeyStore
	invalidate func(hashHex string) // optional cache hook
}

// NewAdminKeysHandler constructs an AdminKeysHandler. invalidate may be
// nil for tests; in production the cached authenticator's Invalidate is
// passed in so revoked keys take effect immediately.
func NewAdminKeysHandler(s APIKeyStore, invalidate func(hashHex string)) *AdminKeysHandler {
	return &AdminKeysHandler{store: s, invalidate: invalidate}
}

// CreateKeyRequest is the JSON body for POST /internal/admin/keys.
type CreateKeyRequest struct {
	OrgID        string     `json:"org_id"`
	Name         string     `json:"name"`
	RateLimitRPM int        `json:"rate_limit_rpm"`
	RateLimitTPM int        `json:"rate_limit_tpm"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// CreateKeyResponse is the JSON body returned by POST /internal/admin/keys.
// The plaintext Key is returned once and never persisted.
type CreateKeyResponse struct {
	ID        string    `json:"id"`
	Key       string    `json:"key"`
	KeyPrefix string    `json:"key_prefix"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Create mints a new API key and returns the plaintext exactly once. The
// plaintext MUST NOT be logged at any level.
func (h *AdminKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if req.OrgID == "" || req.Name == "" {
		apierr.Write(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"org_id and name are required")
		return
	}

	plaintext, err := generatePlaintextKey()
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, "internal_error", "internal_error",
			"failed to generate key")
		return
	}
	hashHex := auth.HashKey(plaintext)
	prefix := plaintext[:8]

	row, err := h.store.Create(r.Context(), &store.NewKey{
		OrgID:        req.OrgID,
		Name:         req.Name,
		RateLimitRPM: req.RateLimitRPM,
		RateLimitTPM: req.RateLimitTPM,
		ExpiresAt:    req.ExpiresAt,
	}, hashHex, prefix)
	if err != nil {
		apierr.Write(w, http.StatusInternalServerError, "internal_error", "internal_error",
			fmt.Sprintf("creating key: %v", err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CreateKeyResponse{
		ID:        row.ID,
		Key:       plaintext,
		KeyPrefix: row.KeyPrefix,
		Name:      row.Name,
		CreatedAt: row.CreatedAt,
	})
}

// Revoke marks the key with the given id inactive. Idempotent: 204 on
// success regardless of whether the key existed.
func (h *AdminKeysHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		apierr.Write(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"id is required")
		return
	}

	// Look up the hash first so we can invalidate the cache after Revoke.
	// If the row is gone, that's fine — Revoke is still idempotent.
	var hashHex string
	if row, err := h.store.GetByID(r.Context(), id); err == nil {
		hashHex = row.KeyHash
	} else if !errors.Is(err, store.ErrNotFound) {
		apierr.Write(w, http.StatusInternalServerError, "internal_error", "internal_error",
			fmt.Sprintf("looking up key: %v", err))
		return
	}

	if err := h.store.Revoke(r.Context(), id); err != nil {
		apierr.Write(w, http.StatusInternalServerError, "internal_error", "internal_error",
			fmt.Sprintf("revoking key: %v", err))
		return
	}

	if hashHex != "" && h.invalidate != nil {
		h.invalidate(hashHex)
	}
	w.WriteHeader(http.StatusNoContent)
}

// keyAlphabet is the set of characters used in generated API keys. 62
// distinct symbols; rejection sampling with a single byte gives an even
// distribution.
const keyAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const keyBodyLen = 32

// keyAcceptCeiling is the largest byte value evenly divisible by len(alphabet).
// Bytes >= ceiling are rejected to avoid modulo bias.
var keyAcceptCeiling = byte((256 / len(keyAlphabet)) * len(keyAlphabet))

func generatePlaintextKey() (string, error) {
	out := make([]byte, keyBodyLen)
	var buf [1]byte
	for i := 0; i < keyBodyLen; {
		if _, err := rand.Read(buf[:]); err != nil {
			return "", err
		}
		if buf[0] < keyAcceptCeiling {
			out[i] = keyAlphabet[int(buf[0])%len(keyAlphabet)]
			i++
		}
	}
	return "sk-gw-" + string(out), nil
}
