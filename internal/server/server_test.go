package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/config"
	"github.com/M4cr0Chen/llm-gateway/internal/handler"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/router"
	"github.com/M4cr0Chen/llm-gateway/internal/router/strategy"
	"github.com/M4cr0Chen/llm-gateway/internal/server"
	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

type mockProvider struct {
	name   string
	models []string
}

func (m *mockProvider) Name() string     { return m.name }
func (m *mockProvider) Models() []string { return m.models }

func (m *mockProvider) ChatCompletion(_ context.Context, _ *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return &model.ChatCompletionResponse{
		ID:      "chatcmpl-test",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "test-model",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "hello"}, FinishReason: "stop"},
		},
	}, nil
}

func (m *mockProvider) ChatCompletionStream(_ context.Context, _ *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent)
	close(ch)
	return ch, nil
}

func newRegistry() *provider.Registry {
	reg := provider.NewRegistry()
	mock := &mockProvider{name: "test", models: []string{"test-model"}}
	reg.Register(mock, mock.Models())
	return reg
}

func newRouter(t *testing.T, reg *provider.Registry) router.Router {
	t.Helper()
	rtr, err := router.NewRouter(reg, config.RoutingConfig{DefaultStrategy: "priority"}, nil, strategy.Build)
	require.NoError(t, err)
	return rtr
}

func newTestServer(t *testing.T) *server.Server {
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	reg := newRegistry()
	return server.New(reg, newRouter(t, reg), server.Options{}, logger)
}

// --- pre-existing routes (unaffected by adding auth) ---

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestModels(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 1)
	assert.Equal(t, "test-model", resp.Data[0].ID)
	assert.Equal(t, "test", resp.Data[0].OwnedBy)
}

func TestChatCompletions(t *testing.T) {
	srv := newTestServer(t)
	body, _ := json.Marshal(model.ChatCompletionRequest{
		Model:    "test-model",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "test", w.Header().Get("X-LLM-Gateway-Provider"))
}

func TestRequestIDHeader(t *testing.T) {
	srv := newTestServer(t)
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)

	assert.NotEmpty(t, w.Header().Get("X-Request-Id"))
}

// --- auth-protected routes ---

type fixedAuth struct {
	want string
	info *auth.KeyInfo
}

func (f *fixedAuth) Authenticate(_ context.Context, plaintext string) (*auth.KeyInfo, error) {
	if plaintext == f.want {
		return f.info, nil
	}
	return nil, auth.ErrInvalidKey
}

type recordingStore struct{}

func (recordingStore) Create(_ context.Context, k *store.NewKey, hashHex, prefix string) (*store.APIKey, error) {
	return &store.APIKey{ID: "k", KeyHash: hashHex, KeyPrefix: prefix, OrgID: k.OrgID, Name: k.Name, IsActive: true}, nil
}
func (recordingStore) GetByID(context.Context, string) (*store.APIKey, error) {
	return nil, store.ErrNotFound
}
func (recordingStore) Revoke(context.Context, string) error { return nil }

func newAuthedServer(t *testing.T) *server.Server {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	fa := &fixedAuth{want: "sk-gw-good", info: &auth.KeyInfo{KeyID: "k", OrgID: "o"}}
	adm := handler.NewAdminKeysHandler(recordingStore{}, nil)
	reg := newRegistry()
	return server.New(reg, newRouter(t, reg), server.Options{
		Authenticator: fa,
		AdminToken:    "admin-secret",
		AdminKeys:     adm,
	}, logger)
}

func TestAPIRoutes_RequireAuth_When_Authenticator_Set(t *testing.T) {
	srv := newAuthedServer(t)

	// Missing Bearer → 401.
	r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// Valid Bearer → 200.
	r = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer sk-gw-good")
	w = httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAdminRoutes_RequireAdminToken(t *testing.T) {
	srv := newAuthedServer(t)

	body := `{"org_id":"o1","name":"k1"}`

	// Missing admin token → 401.
	r := httptest.NewRequest(http.MethodPost, "/internal/admin/keys", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// Correct admin token → 201.
	r = httptest.NewRequest(http.MethodPost, "/internal/admin/keys", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer admin-secret")
	w = httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusCreated, w.Code)
}

func TestAdminRoutes_NotMounted_When_Disabled(t *testing.T) {
	srv := newTestServer(t) // no admin token / handler

	r := httptest.NewRequest(http.MethodPost, "/internal/admin/keys", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusNotFound, w.Code, "admin routes should not be mounted when disabled")
}

func TestPublicHealthStays_Unauthenticated_When_AuthEnabled(t *testing.T) {
	srv := newAuthedServer(t)
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)
	assert.Equal(t, http.StatusOK, w.Code)
}
