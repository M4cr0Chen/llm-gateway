package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRequest() *model.ChatCompletionRequest {
	return &model.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []model.Message{
			{Role: "user", Content: "Hello!"},
		},
	}
}

func TestNew_Defaults(t *testing.T) {
	p := New(Config{APIKey: "test-key", Models: []string{"gpt-4o"}})

	assert.Equal(t, "https://api.openai.com/v1", p.baseURL)
	assert.Equal(t, 30*time.Second, p.timeout)
	assert.Equal(t, "openai", p.Name())
	assert.Equal(t, []string{"gpt-4o"}, p.Models())
}

func TestChatCompletion_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify request
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/chat/completions", r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "gpt-4o", body["model"])
		assert.Equal(t, false, body["stream"])

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "chatcmpl-abc123",
			"object": "chat.completion",
			"created": 1700000000,
			"model": "gpt-4o",
			"choices": [{
				"index": 0,
				"message": {"role": "assistant", "content": "Hello! How can I help?"},
				"finish_reason": "stop"
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 7, "total_tokens": 17}
		}`)
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"gpt-4o"},
	})

	resp, err := p.ChatCompletion(context.Background(), testRequest())
	require.NoError(t, err)

	assert.Equal(t, "chatcmpl-abc123", resp.ID)
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, "gpt-4o", resp.Model)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Hello! How can I help?", resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Equal(t, 17, resp.Usage.TotalTokens)
}

func TestChatCompletionStream_Success(t *testing.T) {
	chunks := []string{
		`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`,
		`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}`,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		for _, chunk := range chunks {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"gpt-4o"},
	})

	ch, err := p.ChatCompletionStream(context.Background(), testRequest())
	require.NoError(t, err)

	var events []model.ChatCompletionChunk
	for event := range ch {
		require.NoError(t, event.Err)
		require.NotNil(t, event.Chunk)
		events = append(events, *event.Chunk)
	}

	require.Len(t, events, 3)
	assert.Equal(t, "assistant", events[0].Choices[0].Delta.Role)
	assert.Equal(t, "Hello", events[1].Choices[0].Delta.Content)
	assert.Equal(t, "!", events[2].Choices[0].Delta.Content)
	require.NotNil(t, events[2].Choices[0].FinishReason)
	assert.Equal(t, "stop", *events[2].Choices[0].FinishReason)
}

func TestChatCompletionStream_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		for i := 0; i < 100; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			chunk := fmt.Sprintf(`{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"chunk-%d"},"finish_reason":null}]}`, i)
			_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"gpt-4o"},
	})

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := p.ChatCompletionStream(ctx, testRequest())
	require.NoError(t, err)

	// Read one event, then cancel
	event := <-ch
	require.NoError(t, event.Err)
	cancel()

	// Channel should close within 200ms
	timer := time.NewTimer(200 * time.Millisecond)
	defer timer.Stop()

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed, test passes
			}
		case <-timer.C:
			t.Fatal("channel not closed within 200ms after context cancellation")
		}
	}
}

func TestChatCompletion_Error429(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = fmt.Fprint(w, `{"error":{"message":"Rate limit exceeded","type":"rate_limit_error"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gpt-4o"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 429, pe.StatusCode)
	assert.True(t, pe.Retryable)
	assert.Contains(t, pe.Message, "Rate limit")
	assert.Equal(t, "rate_limit_error", pe.Type)
}

func TestChatCompletion_Error401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"message":"Invalid API key","type":"authentication_error"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "bad-key", BaseURL: server.URL, Models: []string{"gpt-4o"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 401, pe.StatusCode)
	assert.False(t, pe.Retryable)
	assert.Equal(t, "authentication_error", pe.Type)
}

func TestChatCompletion_Error500_NonJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "internal server error")
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gpt-4o"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 500, pe.StatusCode)
	assert.True(t, pe.Retryable)
	assert.Contains(t, pe.Message, "internal server error")
	assert.Equal(t, "upstream_error", pe.Type)
}
