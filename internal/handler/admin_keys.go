package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/M4cr0Chen/llm-gateway/internal/apierr"
	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/middleware"
	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

// adminBodyLimit caps admin request bodies. They are tiny JSON objects;
// 1 MB is overkill but matches the chat handler's defence-in-depth posture.
const adminBodyLimit = 1 << 20

// defaultRateLimitRPM and defaultRateLimitTPM are applied when an admin
// omits the limits in the create request. They match the SQL DEFAULTs in
// migrations/002_create_api_keys.up.sql.
const (
	defaultRateLimitRPM = 60
	defaultRateLimitTPM = 100000
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
// Pointer fields distinguish "omitted" (apply default) from "explicitly 0".
type CreateKeyRequest struct {
	OrgID        string     `json:"org_id"`
	Name         string     `json:"name"`
	RateLimitRPM *int       `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM *int       `json:"rate_limit_tpm,omitempty"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// CreateKeyResponse is the JSON body returned by POST /internal/admin/keys.
// The plaintext Key is returned once and never persisted. The rate-limit
// fields are echoed so callers can confirm the values that were stored.
type CreateKeyResponse struct {
	ID           string     `json:"id"`
	Key          string     `json:"key"`
	KeyPrefix    string     `json:"key_prefix"`
	Name         string     `json:"name"`
	RateLimitRPM int        `json:"rate_limit_rpm"`
	RateLimitTPM int        `json:"rate_limit_tpm"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// Create mints a new API key and returns the plaintext exactly once. The
// plaintext MUST NOT be logged at any level.
func (h *AdminKeysHandler) Create(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, adminBodyLimit)

	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apierr.Write(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"invalid request body")
		return
	}
	if req.OrgID == "" || req.Name == "" {
		apierr.Write(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"org_id and name are required")
		return
	}

	rpm := defaultRateLimitRPM
	if req.RateLimitRPM != nil {
		rpm = *req.RateLimitRPM
	}
	tpm := defaultRateLimitTPM
	if req.RateLimitTPM != nil {
		tpm = *req.RateLimitTPM
	}

	plaintext, err := generatePlaintextKey()
	if err != nil {
		middleware.LoggerFromContext(r.Context()).Error("generating api key", "err", err)
		apierr.Write(w, http.StatusInternalServerError, "internal_error", "internal_error",
			"failed to generate key")
		return
	}
	hashHex := auth.HashKey(plaintext)
	prefix := plaintext[:keyPrefixLen]

	row, err := h.store.Create(r.Context(), &store.NewKey{
		OrgID:        req.OrgID,
		Name:         req.Name,
		RateLimitRPM: rpm,
		RateLimitTPM: tpm,
		ExpiresAt:    req.ExpiresAt,
	}, hashHex, prefix)
	if err != nil {
		middleware.LoggerFromContext(r.Context()).Error("creating api key", "err", err)
		apierr.Write(w, http.StatusInternalServerError, "internal_error", "internal_error",
			"failed to create key")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(CreateKeyResponse{
		ID:           row.ID,
		Key:          plaintext,
		KeyPrefix:    row.KeyPrefix,
		Name:         row.Name,
		RateLimitRPM: row.RateLimitRPM,
		RateLimitTPM: row.RateLimitTPM,
		ExpiresAt:    row.ExpiresAt,
		CreatedAt:    row.CreatedAt,
	})
}

// Revoke marks the key with the given id inactive. Idempotent: 204 on
// success regardless of whether the key existed or whether the id is
// well-formed (a non-UUID id is treated as a missing row).
func (h *AdminKeysHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		apierr.Write(w, http.StatusBadRequest, "invalid_request_error", "invalid_request",
			"id is required")
		return
	}

	// Look up the hash first so we can invalidate the cache after Revoke.
	// A missing or malformed id is treated as a no-op so Revoke stays
	// idempotent across both well-formed and well-formed-but-unknown ids.
	var hashHex string
	if row, err := h.store.GetByID(r.Context(), id); err == nil {
		hashHex = row.KeyHash
	} else if !errors.Is(err, store.ErrNotFound) {
		middleware.LoggerFromContext(r.Context()).Error("looking up key for revoke", "err", err)
		apierr.Write(w, http.StatusInternalServerError, "internal_error", "internal_error",
			"failed to look up key")
		return
	}

	if err := h.store.Revoke(r.Context(), id); err != nil {
		middleware.LoggerFromContext(r.Context()).Error("revoking key", "err", err)
		apierr.Write(w, http.StatusInternalServerError, "internal_error", "internal_error",
			"failed to revoke key")
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

// keyPrefixLen is the length of the human-visible prefix stored alongside
// the hash for identification. "sk-gw-" + 8 random chars = 14 chars, giving
// 62^8 ≈ 2.2e14 prefix combos — collision-resistant even at scale.
const keyPrefixLen = 14

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
