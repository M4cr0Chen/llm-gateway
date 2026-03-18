package handler_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/handler"
	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// mockProvider implements provider.Provider for testing.
type mockProvider struct {
	name       string
	models     []string
	resp       *model.ChatCompletionResponse
	chunks     []model.ChatCompletionChunk
	streamErr  error
	chatErr    error
}

func (m *mockProvider) Name() string    { return m.name }
func (m *mockProvider) Models() []string { return m.models }

func (m *mockProvider) ChatCompletion(_ context.Context, _ *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	if m.chatErr != nil {
		return nil, m.chatErr
	}
	return m.resp, nil
}

func (m *mockProvider) ChatCompletionStream(_ context.Context, _ *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	if m.streamErr != nil {
		return nil, m.streamErr
	}
	ch := make(chan provider.StreamEvent, len(m.chunks))
	for i := range m.chunks {
		ch <- provider.StreamEvent{Chunk: &m.chunks[i]}
	}
	close(ch)
	return ch, nil
}

func newTestRegistry(p provider.Provider) *provider.Registry {
	reg := provider.NewRegistry()
	reg.Register(p, p.Models())
	return reg
}

func validRequest() model.ChatCompletionRequest {
	return model.ChatCompletionRequest{
		Model:    "test-model",
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}
}

func sampleResponse() *model.ChatCompletionResponse {
	return &model.ChatCompletionResponse{
		ID:      "chatcmpl-123",
		Object:  "chat.completion",
		Created: 1700000000,
		Model:   "test-model",
		Choices: []model.Choice{
			{Index: 0, Message: model.Message{Role: "assistant", Content: "hi"}, FinishReason: "stop"},
		},
		Usage: model.Usage{PromptTokens: 5, CompletionTokens: 1, TotalTokens: 6},
	}
}

func sampleChunks() []model.ChatCompletionChunk {
	stop := "stop"
	return []model.ChatCompletionChunk{
		{ID: "chatcmpl-123", Object: "chat.completion.chunk", Created: 1700000000, Model: "test-model",
			Choices: []model.ChunkChoice{{Index: 0, Delta: model.DeltaMessage{Role: "assistant"}}}},
		{ID: "chatcmpl-123", Object: "chat.completion.chunk", Created: 1700000000, Model: "test-model",
			Choices: []model.ChunkChoice{{Index: 0, Delta: model.DeltaMessage{Content: "hi"}, FinishReason: &stop}}},
	}
}

func doRequest(t *testing.T, h http.HandlerFunc, req model.ChatCompletionRequest) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	require.NoError(t, err)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHandleChatCompletion_NonStreaming(t *testing.T) {
	mock := &mockProvider{name: "test", models: []string{"test-model"}, resp: sampleResponse()}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	w := doRequest(t, ch.HandleChatCompletion, validRequest())

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "test", w.Header().Get("X-LLM-Gateway-Provider"))

	var resp model.ChatCompletionResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	assert.Equal(t, "chatcmpl-123", resp.ID)
	assert.Equal(t, "hi", resp.Choices[0].Message.Content)
}

func TestHandleChatCompletion_Streaming(t *testing.T) {
	mock := &mockProvider{name: "test", models: []string{"test-model"}, chunks: sampleChunks()}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	req := validRequest()
	req.Stream = true
	w := doRequest(t, ch.HandleChatCompletion, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	assert.Equal(t, "test", w.Header().Get("X-LLM-Gateway-Provider"))

	body := w.Body.String()
	lines := strings.Split(strings.TrimSpace(body), "\n")
	// Should have data lines and a [DONE] sentinel
	var dataLines []string
	for _, line := range lines {
		if strings.HasPrefix(line, "data: ") {
			dataLines = append(dataLines, line)
		}
	}
	require.GreaterOrEqual(t, len(dataLines), 3, "expected at least 2 chunks + [DONE]")
	assert.Equal(t, "data: [DONE]", dataLines[len(dataLines)-1])
}

func TestHandleChatCompletion_InvalidJSON(t *testing.T) {
	mock := &mockProvider{name: "test", models: []string{"test-model"}}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{invalid"))
	w := httptest.NewRecorder()
	ch.HandleChatCompletion(w, r)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var apiErr model.APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &apiErr))
	assert.Equal(t, "invalid_request", apiErr.Error.Code)
}

func TestHandleChatCompletion_MissingModel(t *testing.T) {
	mock := &mockProvider{name: "test", models: []string{"test-model"}}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	req := model.ChatCompletionRequest{Messages: []model.Message{{Role: "user", Content: "hi"}}}
	w := doRequest(t, ch.HandleChatCompletion, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var apiErr model.APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &apiErr))
	assert.Equal(t, "invalid_request", apiErr.Error.Code)
}

func TestHandleChatCompletion_EmptyMessages(t *testing.T) {
	mock := &mockProvider{name: "test", models: []string{"test-model"}}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	req := model.ChatCompletionRequest{Model: "test-model"}
	w := doRequest(t, ch.HandleChatCompletion, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var apiErr model.APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &apiErr))
	assert.Equal(t, "invalid_request", apiErr.Error.Code)
}

func TestHandleChatCompletion_UnknownModel(t *testing.T) {
	mock := &mockProvider{name: "test", models: []string{"test-model"}}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	req := model.ChatCompletionRequest{
		Model:    "nonexistent",
		Messages: []model.Message{{Role: "user", Content: "hi"}},
	}
	w := doRequest(t, ch.HandleChatCompletion, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
	var apiErr model.APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &apiErr))
	assert.Equal(t, "invalid_model", apiErr.Error.Code)
}

func TestHandleChatCompletion_ProviderError(t *testing.T) {
	provErr := &model.ProviderError{
		StatusCode: http.StatusTooManyRequests,
		Type:       "rate_limit_error",
		Message:    "rate limited",
	}
	mock := &mockProvider{name: "test", models: []string{"test-model"}, chatErr: provErr}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	w := doRequest(t, ch.HandleChatCompletion, validRequest())

	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	var apiErr model.APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &apiErr))
	assert.Equal(t, "rate limited", apiErr.Error.Message)
}

func TestHandleChatCompletion_ProviderError5xx(t *testing.T) {
	provErr := &model.ProviderError{
		StatusCode: http.StatusInternalServerError,
		Type:       "upstream_error",
		Message:    "internal error",
	}
	mock := &mockProvider{name: "test", models: []string{"test-model"}, chatErr: provErr}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	w := doRequest(t, ch.HandleChatCompletion, validRequest())

	// 5xx from provider should be mapped to 502
	assert.Equal(t, http.StatusBadGateway, w.Code)
}

func TestHandleChatCompletion_GenericError(t *testing.T) {
	mock := &mockProvider{name: "test", models: []string{"test-model"}, chatErr: fmt.Errorf("connection refused")}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	w := doRequest(t, ch.HandleChatCompletion, validRequest())

	assert.Equal(t, http.StatusBadGateway, w.Code)
	var apiErr model.APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &apiErr))
	assert.Equal(t, "upstream provider error", apiErr.Error.Message, "should not leak internal error details")
}

func TestHandleChatCompletion_RequestTooLarge(t *testing.T) {
	mock := &mockProvider{name: "test", models: []string{"test-model"}}
	ch := handler.NewChatHandler(newTestRegistry(mock))

	// Build a valid-shaped JSON body that exceeds 10MB via a large content field.
	padding := strings.Repeat("x", 11<<20)
	body := fmt.Sprintf(`{"model":"test-model","messages":[{"role":"user","content":"%s"}]}`, padding)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	ch.HandleChatCompletion(w, r)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	var apiErr model.APIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &apiErr))
	assert.Equal(t, "request_too_large", apiErr.Error.Code)
}

func TestHandleChatCompletion_StreamMidStreamError(t *testing.T) {
	stop := "stop"
	chunks := []model.ChatCompletionChunk{
		{ID: "chatcmpl-123", Object: "chat.completion.chunk", Created: 1700000000, Model: "test-model",
			Choices: []model.ChunkChoice{{Index: 0, Delta: model.DeltaMessage{Content: "hi"}, FinishReason: &stop}}},
	}
	mock := &mockProvider{name: "test", models: []string{"test-model"}}

	// Override ChatCompletionStream to inject an error mid-stream.
	reg := newTestRegistry(mock)
	errMock := &midStreamErrProvider{chunks: chunks, midErr: fmt.Errorf("connection reset")}
	reg.Register(errMock, errMock.Models())

	ch := handler.NewChatHandler(reg)
	req := validRequest()
	req.Stream = true
	w := doRequest(t, ch.HandleChatCompletion, req)

	assert.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	// Should contain the first chunk and then [DONE], not hang
	assert.Contains(t, body, "data: ")
	assert.Contains(t, body, "data: [DONE]")
}

// midStreamErrProvider sends chunks then an error event.
type midStreamErrProvider struct {
	chunks []model.ChatCompletionChunk
	midErr error
}

func (m *midStreamErrProvider) Name() string    { return "test" }
func (m *midStreamErrProvider) Models() []string { return []string{"test-model"} }

func (m *midStreamErrProvider) ChatCompletion(_ context.Context, _ *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *midStreamErrProvider) ChatCompletionStream(_ context.Context, _ *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	ch := make(chan provider.StreamEvent, len(m.chunks)+1)
	for i := range m.chunks {
		ch <- provider.StreamEvent{Chunk: &m.chunks[i]}
	}
	ch <- provider.StreamEvent{Err: m.midErr}
	close(ch)
	return ch, nil
}
