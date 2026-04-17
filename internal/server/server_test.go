package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/M4cr0Chen/llm-gateway/internal/server"
)

type mockProvider struct {
	name   string
	models []string
}

func (m *mockProvider) Name() string    { return m.name }
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

func newTestServer() *server.Server {
	reg := provider.NewRegistry()
	mock := &mockProvider{name: "test", models: []string{"test-model"}}
	reg.Register(mock, mock.Models())
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return server.New(reg, nil, logger)
}

func TestHealth(t *testing.T) {
	srv := newTestServer()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestModels(t *testing.T) {
	srv := newTestServer()
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
	srv := newTestServer()
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
	srv := newTestServer()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	srv.Handler.ServeHTTP(w, r)

	assert.NotEmpty(t, w.Header().Get("X-Request-Id"))
}
