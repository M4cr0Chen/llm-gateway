package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
)

type fakeAuthenticator struct {
	want    string
	info    *auth.KeyInfo
	failErr error
}

func (f *fakeAuthenticator) Authenticate(_ context.Context, plaintext string) (*auth.KeyInfo, error) {
	if f.failErr != nil {
		return nil, f.failErr
	}
	if plaintext == f.want {
		return f.info, nil
	}
	return nil, auth.ErrInvalidKey
}

func newDownstream(t *testing.T, wantInfo *auth.KeyInfo) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := auth.KeyInfoFromContext(r.Context())
		assert.True(t, ok, "downstream handler should see KeyInfo")
		assert.Equal(t, wantInfo, got)
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequireAPIKey(t *testing.T) {
	info := &auth.KeyInfo{KeyID: "k1", OrgID: "o1"}
	a := &fakeAuthenticator{want: "sk-gw-good", info: info}

	cases := []struct {
		name       string
		authHeader string
		wantStatus int
		wantCode   string
	}{
		{"missing header", "", http.StatusUnauthorized, "invalid_api_key"},
		{"non-bearer", "Basic something", http.StatusUnauthorized, "invalid_api_key"},
		{"empty bearer", "Bearer ", http.StatusUnauthorized, "invalid_api_key"},
		{"unknown key", "Bearer sk-gw-bad", http.StatusUnauthorized, "invalid_api_key"},
		{"valid key", "Bearer sk-gw-good", http.StatusOK, ""},
		{"lowercase bearer", "bearer sk-gw-good", http.StatusOK, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mw := RequireAPIKey(a)(newDownstream(t, info))

			req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)

			assert.Equal(t, tc.wantStatus, rec.Code)
			if tc.wantCode != "" {
				var body struct {
					Error struct {
						Code string `json:"code"`
						Type string `json:"type"`
					} `json:"error"`
				}
				require.NoError(t, json.NewDecoder(rec.Body).Decode(&body))
				assert.Equal(t, tc.wantCode, body.Error.Code)
				assert.Equal(t, "authentication_error", body.Error.Type)
			}
		})
	}
}

func TestRequireAPIKey_BackendError(t *testing.T) {
	a := &fakeAuthenticator{failErr: errors.New("db down")}

	mw := RequireAPIKey(a)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("downstream handler should not run on backend error")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	req.Header.Set("Authorization", "Bearer sk-gw-anything")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestRequireAdminToken(t *testing.T) {
	mw := RequireAdminToken("admin-secret")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{"missing header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer not-the-secret", http.StatusUnauthorized},
		{"correct token", "Bearer admin-secret", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/internal/admin/keys", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			mw.ServeHTTP(rec, req)
			assert.Equal(t, tc.wantStatus, rec.Code)
		})
	}
}

func TestRequireAdminToken_EmptyExpectedRejectsAll(t *testing.T) {
	mw := RequireAdminToken("")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("empty admin token must fail closed")
	}))
	req := httptest.NewRequest(http.MethodPost, "/internal/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
