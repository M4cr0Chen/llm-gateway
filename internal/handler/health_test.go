package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/handler"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// dummyProvider satisfies provider.Provider for wrapping in HealthTrackingProvider.
type dummyProvider struct {
	name   string
	models []string
}

func (d *dummyProvider) Name() string    { return d.name }
func (d *dummyProvider) Models() []string { return d.models }
func (d *dummyProvider) ChatCompletion(_ context.Context, _ *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return nil, nil
}
func (d *dummyProvider) ChatCompletionStream(_ context.Context, _ *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	return nil, nil
}

func makeHealthProvider(name string) *provider.HealthTrackingProvider {
	return provider.NewHealthTrackingProvider(
		&dummyProvider{name: name, models: []string{name + "-model"}},
		provider.HealthConfig{FailureThreshold: 2, CooldownPeriod: 0},
		provider.RetryConfig{},
	)
}

func TestHandleHealth(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.HandleHealth(w, r)

	assert.Equal(t, http.StatusOK, w.Code)
	var resp map[string]string
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp["status"])
}

func TestInternalHealth_AllHealthy(t *testing.T) {
	providers := map[string]*provider.HealthTrackingProvider{
		"openai":    makeHealthProvider("openai"),
		"anthropic": makeHealthProvider("anthropic"),
	}
	h := handler.NewInternalHealthHandler(providers)

	r := httptest.NewRequest(http.MethodGet, "/internal/health", nil)
	w := httptest.NewRecorder()
	h.HandleInternalHealth(w, r)

	assert.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Status    string                           `json:"status"`
		Providers map[string]provider.HealthStatus `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "ok", resp.Status)
	assert.True(t, resp.Providers["openai"].Healthy)
	assert.True(t, resp.Providers["anthropic"].Healthy)
}

func TestInternalHealth_Degraded(t *testing.T) {
	providers := map[string]*provider.HealthTrackingProvider{
		"openai":    makeHealthProvider("openai"),
		"anthropic": makeHealthProvider("anthropic"),
	}
	// Make anthropic unhealthy.
	providers["anthropic"].Health.RecordFailure(errors.New("err1"))
	providers["anthropic"].Health.RecordFailure(errors.New("err2"))

	h := handler.NewInternalHealthHandler(providers)

	r := httptest.NewRequest(http.MethodGet, "/internal/health", nil)
	w := httptest.NewRecorder()
	h.HandleInternalHealth(w, r)

	var resp struct {
		Status    string                           `json:"status"`
		Providers map[string]provider.HealthStatus `json:"providers"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "degraded", resp.Status)
	assert.True(t, resp.Providers["openai"].Healthy)
	assert.False(t, resp.Providers["anthropic"].Healthy)
	assert.Equal(t, 2, resp.Providers["anthropic"].ConsecutiveFails)
	assert.Equal(t, "err2", resp.Providers["anthropic"].LastError)
}

func TestInternalHealth_Down(t *testing.T) {
	providers := map[string]*provider.HealthTrackingProvider{
		"openai": makeHealthProvider("openai"),
	}
	providers["openai"].Health.RecordFailure(errors.New("err1"))
	providers["openai"].Health.RecordFailure(errors.New("err2"))

	h := handler.NewInternalHealthHandler(providers)

	r := httptest.NewRequest(http.MethodGet, "/internal/health", nil)
	w := httptest.NewRecorder()
	h.HandleInternalHealth(w, r)

	var resp struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "down", resp.Status)
}

func TestInternalHealth_NoProviders(t *testing.T) {
	h := handler.NewInternalHealthHandler(map[string]*provider.HealthTrackingProvider{})

	r := httptest.NewRequest(http.MethodGet, "/internal/health", nil)
	w := httptest.NewRecorder()
	h.HandleInternalHealth(w, r)

	var resp struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	// 0 out of 0 healthy → "ok"
	assert.Equal(t, "ok", resp.Status)
}
