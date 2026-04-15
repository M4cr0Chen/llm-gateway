package anthropic

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
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRequest() *model.ChatCompletionRequest {
	return &model.ChatCompletionRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []model.Message{
			{Role: "user", Content: "Hello!"},
		},
	}
}

func testRequestWithSystem() *model.ChatCompletionRequest {
	return &model.ChatCompletionRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []model.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello!"},
		},
	}
}

func TestNew_Defaults(t *testing.T) {
	p := New(Config{APIKey: "test-key", Models: []string{"claude-sonnet-4-20250514"}})

	assert.Equal(t, defaultBaseURL, p.baseURL)
	assert.Equal(t, defaultTimeout, p.timeout)
	assert.Equal(t, "anthropic", p.Name())
	assert.Equal(t, []string{"claude-sonnet-4-20250514"}, p.Models())
}

func TestChatCompletion_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, messagesEndpoint, r.URL.Path)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-key", r.Header.Get("x-api-key"))
		assert.Equal(t, apiVersion, r.Header.Get("anthropic-version"))
		// Anthropic uses x-api-key, NOT Authorization: Bearer
		assert.Empty(t, r.Header.Get("Authorization"))

		var body anthropicRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "claude-sonnet-4-20250514", body.Model)
		assert.False(t, body.Stream)
		assert.Equal(t, defaultMaxTokens, body.MaxTokens)
		require.Len(t, body.Messages, 1)
		assert.Equal(t, "user", body.Messages[0].Role)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "msg_abc123",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Hello! How can I help?"}],
			"model": "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"stop_sequence": null,
			"usage": {"input_tokens": 10, "output_tokens": 7}
		}`)
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"claude-sonnet-4-20250514"},
	})

	resp, err := p.ChatCompletion(context.Background(), testRequest())
	require.NoError(t, err)

	assert.Equal(t, "msg_abc123", resp.ID)
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, "claude-sonnet-4-20250514", resp.Model)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Equal(t, "Hello! How can I help?", resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
	assert.Equal(t, 10, resp.Usage.PromptTokens)
	assert.Equal(t, 7, resp.Usage.CompletionTokens)
	assert.Equal(t, 17, resp.Usage.TotalTokens)
}

func TestChatCompletion_SystemMessage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body anthropicRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		// System message should be extracted to top-level field.
		assert.Equal(t, "You are helpful.", body.System)
		// Only user message should remain in messages array.
		require.Len(t, body.Messages, 1)
		assert.Equal(t, "user", body.Messages[0].Role)
		assert.Equal(t, "Hello!", body.Messages[0].Content)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "msg_abc123",
			"type": "message",
			"role": "assistant",
			"content": [{"type": "text", "text": "Hi!"}],
			"model": "claude-sonnet-4-20250514",
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 10, "output_tokens": 2}
		}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

	_, err := p.ChatCompletion(context.Background(), testRequestWithSystem())
	require.NoError(t, err)
}

func TestChatCompletion_MaxTokensPassedThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body anthropicRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, 100, body.MaxTokens)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "msg_abc123", "type": "message", "role": "assistant",
			"content": [{"type": "text", "text": "Hi"}],
			"model": "claude-sonnet-4-20250514", "stop_reason": "max_tokens",
			"usage": {"input_tokens": 5, "output_tokens": 100}
		}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

	maxTokens := 100
	req := testRequest()
	req.MaxTokens = &maxTokens

	resp, err := p.ChatCompletion(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "length", resp.Choices[0].FinishReason)
}

func TestChatCompletion_StopSequence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body anthropicRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, []string{"END", "STOP"}, body.StopSequences)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"id": "msg_abc123", "type": "message", "role": "assistant",
			"content": [{"type": "text", "text": "output"}],
			"model": "claude-sonnet-4-20250514", "stop_reason": "stop_sequence",
			"stop_sequence": "END",
			"usage": {"input_tokens": 5, "output_tokens": 3}
		}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

	req := testRequest()
	req.Stop = []string{"END", "STOP"}

	resp, err := p.ChatCompletion(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)
}

func TestChatCompletionStream_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		events := []string{
			"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_abc123\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-20250514\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n",
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello\"}}\n",
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"!\"}}\n",
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n",
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":5}}\n",
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n",
		}
		for _, event := range events {
			_, _ = fmt.Fprint(w, event)
			_, _ = fmt.Fprint(w, "\n")
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"claude-sonnet-4-20250514"},
	})

	ch, err := p.ChatCompletionStream(context.Background(), testRequest())
	require.NoError(t, err)

	var events []provider.StreamEvent
	for event := range ch {
		events = append(events, event)
	}

	// Expect: 2 content deltas ("Hello", "!") + 1 message_delta (finish_reason + usage) = 3 chunks
	require.Len(t, events, 3)

	// Content deltas
	require.NoError(t, events[0].Err)
	assert.Equal(t, "Hello", events[0].Chunk.Choices[0].Delta.Content)
	assert.Equal(t, "msg_abc123", events[0].Chunk.ID)
	assert.Equal(t, "chat.completion.chunk", events[0].Chunk.Object)

	require.NoError(t, events[1].Err)
	assert.Equal(t, "!", events[1].Chunk.Choices[0].Delta.Content)

	// Message delta with finish reason and usage
	require.NoError(t, events[2].Err)
	require.NotNil(t, events[2].Chunk.Choices[0].FinishReason)
	assert.Equal(t, "stop", *events[2].Chunk.Choices[0].FinishReason)
	require.NotNil(t, events[2].Chunk.Usage)
	assert.Equal(t, 10, events[2].Chunk.Usage.PromptTokens)
	assert.Equal(t, 5, events[2].Chunk.Usage.CompletionTokens)
	assert.Equal(t, 15, events[2].Chunk.Usage.TotalTokens)
}

func TestChatCompletionStream_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		// Send message_start first
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_abc123\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-20250514\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
		flusher.Flush()

		for i := 0; i < 100; i++ {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			_, _ = fmt.Fprintf(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"chunk-%d\"}}\n\n", i)
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"claude-sonnet-4-20250514"},
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
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"rate_limit_error","message":"Rate limit exceeded"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

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
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"Invalid API key"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "bad-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

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

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 500, pe.StatusCode)
	assert.True(t, pe.Retryable)
	assert.Contains(t, pe.Message, "internal server error")
	assert.Equal(t, "upstream_error", pe.Type)
}

func TestChatCompletion_Error529_Overloaded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(529)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"Anthropic is temporarily overloaded"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 529, pe.StatusCode)
	assert.True(t, pe.Retryable)
	assert.Equal(t, "overloaded_error", pe.Type)
}

func TestChatCompletionStream_UnexpectedEOF(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		// Send message_start and one content delta, then close without message_stop
		_, _ = fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_abc123\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet-4-20250514\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\n")
		flusher.Flush()

		_, _ = fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hi\"}}\n\n")
		flusher.Flush()
		// Connection closes without message_stop
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

	ch, err := p.ChatCompletionStream(context.Background(), testRequest())
	require.NoError(t, err)

	var events []provider.StreamEvent
	for event := range ch {
		events = append(events, event)
	}

	require.Len(t, events, 2)
	assert.Nil(t, events[0].Err)
	assert.Equal(t, "Hi", events[0].Chunk.Choices[0].Delta.Content)
	require.Error(t, events[1].Err)
	assert.Contains(t, events[1].Err.Error(), "unexpected EOF without message_stop")
}

func TestChatCompletionStream_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: Required"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"claude-sonnet-4-20250514"}})

	_, err := p.ChatCompletionStream(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 400, pe.StatusCode)
	assert.False(t, pe.Retryable)
	assert.Equal(t, "invalid_request_error", pe.Type)
}
