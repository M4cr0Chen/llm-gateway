package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testRequest() *model.ChatCompletionRequest {
	return &model.ChatCompletionRequest{
		Model: "gemini-2.0-flash",
		Messages: []model.Message{
			{Role: "user", Content: "Hello!"},
		},
	}
}

func testRequestWithSystem() *model.ChatCompletionRequest {
	return &model.ChatCompletionRequest{
		Model: "gemini-2.0-flash",
		Messages: []model.Message{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "Hello!"},
		},
	}
}

func TestNew_Defaults(t *testing.T) {
	p := New(Config{APIKey: "test-key", Models: []string{"gemini-2.0-flash"}})

	assert.Equal(t, defaultBaseURL, p.baseURL)
	assert.Equal(t, defaultTimeout, p.timeout)
	assert.Equal(t, "google", p.Name())
	assert.Equal(t, []string{"gemini-2.0-flash"}, p.Models())
}

func TestChatCompletion_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, r.URL.Path, "/models/gemini-2.0-flash:generateContent")
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		// API key should be in query parameter, not header
		assert.Equal(t, "test-key", r.URL.Query().Get("key"))
		assert.Empty(t, r.Header.Get("Authorization"))

		var body geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Len(t, body.Contents, 1)
		assert.Equal(t, "user", body.Contents[0].Role)
		assert.Equal(t, "Hello!", body.Contents[0].Parts[0].Text)
		assert.Nil(t, body.SystemInstruction)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello! How can I help?"}]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {
				"promptTokenCount": 10,
				"candidatesTokenCount": 7,
				"totalTokenCount": 17
			}
		}`)
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"gemini-2.0-flash"},
	})

	resp, err := p.ChatCompletion(context.Background(), testRequest())
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(resp.ID, "chatcmpl-"))
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, "gemini-2.0-flash", resp.Model)
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
		var body geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		// System message should be extracted to systemInstruction field.
		require.NotNil(t, body.SystemInstruction)
		require.Len(t, body.SystemInstruction.Parts, 1)
		assert.Equal(t, "You are helpful.", body.SystemInstruction.Parts[0].Text)
		// Only user message should remain in contents array.
		require.Len(t, body.Contents, 1)
		assert.Equal(t, "user", body.Contents[0].Role)
		assert.Equal(t, "Hello!", body.Contents[0].Parts[0].Text)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "Hi!"}]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 10, "candidatesTokenCount": 2, "totalTokenCount": 12}
		}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	_, err := p.ChatCompletion(context.Background(), testRequestWithSystem())
	require.NoError(t, err)
}

func TestChatCompletion_RoleMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		// assistant should be mapped to model
		require.Len(t, body.Contents, 3)
		assert.Equal(t, "user", body.Contents[0].Role)
		assert.Equal(t, "model", body.Contents[1].Role)
		assert.Equal(t, "user", body.Contents[2].Role)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "Sure!"}]},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 20, "candidatesTokenCount": 3, "totalTokenCount": 23}
		}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	req := &model.ChatCompletionRequest{
		Model: "gemini-2.0-flash",
		Messages: []model.Message{
			{Role: "user", Content: "Hi"},
			{Role: "assistant", Content: "Hello!"},
			{Role: "user", Content: "Tell me more"},
		},
	}

	_, err := p.ChatCompletion(context.Background(), req)
	require.NoError(t, err)
}

func TestChatCompletion_GenerationConfig(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))

		require.NotNil(t, body.GenerationConfig)
		assert.InDelta(t, 0.7, *body.GenerationConfig.Temperature, 0.001)
		assert.InDelta(t, 0.9, *body.GenerationConfig.TopP, 0.001)
		assert.Equal(t, 100, *body.GenerationConfig.MaxOutputTokens)
		assert.Equal(t, []string{"END", "STOP"}, body.GenerationConfig.StopSequences)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"candidates": [{
				"content": {"role": "model", "parts": [{"text": "output"}]},
				"finishReason": "MAX_TOKENS"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 100, "totalTokenCount": 105}
		}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	temp := 0.7
	topP := 0.9
	maxTokens := 100
	req := testRequest()
	req.Temperature = &temp
	req.TopP = &topP
	req.MaxTokens = &maxTokens
	req.Stop = []string{"END", "STOP"}

	resp, err := p.ChatCompletion(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, "length", resp.Choices[0].FinishReason)
}

func TestChatCompletion_FinishReasonMapping(t *testing.T) {
	tests := []struct {
		geminiReason   string
		expectedReason string
	}{
		{"STOP", "stop"},
		{"MAX_TOKENS", "length"},
		{"SAFETY", "content_filter"},
		{"RECITATION", "content_filter"},
	}

	for _, tc := range tests {
		t.Run(tc.geminiReason, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
					"candidates": [{
						"content": {"role": "model", "parts": [{"text": "output"}]},
						"finishReason": %q
					}],
					"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 3, "totalTokenCount": 8}
				}`, tc.geminiReason)
			}))
			defer server.Close()

			p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

			resp, err := p.ChatCompletion(context.Background(), testRequest())
			require.NoError(t, err)
			assert.Equal(t, tc.expectedReason, resp.Choices[0].FinishReason)
		})
	}
}

func TestChatCompletionStream_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Contains(t, r.URL.Path, "/models/gemini-2.0-flash:streamGenerateContent")
		assert.Equal(t, "sse", r.URL.Query().Get("alt"))
		assert.Equal(t, "test-key", r.URL.Query().Get("key"))

		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		require.True(t, ok)

		chunks := []string{
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]},"finishReason":""}],"usageMetadata":null}`,
			`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"!"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}`,
		}
		for _, chunk := range chunks {
			_, _ = fmt.Fprintln(w, chunk)
			_, _ = fmt.Fprintln(w)
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"gemini-2.0-flash"},
	})

	ch, err := p.ChatCompletionStream(context.Background(), testRequest())
	require.NoError(t, err)

	var events []provider.StreamEvent
	for event := range ch {
		events = append(events, event)
	}

	require.Len(t, events, 2)

	// First chunk: content delta
	require.NoError(t, events[0].Err)
	assert.Equal(t, "Hello", events[0].Chunk.Choices[0].Delta.Content)
	assert.True(t, strings.HasPrefix(events[0].Chunk.ID, "chatcmpl-"))
	assert.Equal(t, "chat.completion.chunk", events[0].Chunk.Object)
	assert.Equal(t, "gemini-2.0-flash", events[0].Chunk.Model)

	// Second chunk: content + finish reason + usage
	require.NoError(t, events[1].Err)
	assert.Equal(t, "!", events[1].Chunk.Choices[0].Delta.Content)
	require.NotNil(t, events[1].Chunk.Choices[0].FinishReason)
	assert.Equal(t, "stop", *events[1].Chunk.Choices[0].FinishReason)
	require.NotNil(t, events[1].Chunk.Usage)
	assert.Equal(t, 10, events[1].Chunk.Usage.PromptTokens)
	assert.Equal(t, 5, events[1].Chunk.Usage.CompletionTokens)
	assert.Equal(t, 15, events[1].Chunk.Usage.TotalTokens)

	// Both chunks should share the same stream ID
	assert.Equal(t, events[0].Chunk.ID, events[1].Chunk.ID)
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
			_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"chunk-%d\"}]},\"finishReason\":\"\"}]}\n\n", i)
			flusher.Flush()
		}
	}))
	defer server.Close()

	p := New(Config{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Models:  []string{"gemini-2.0-flash"},
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
		_, _ = fmt.Fprint(w, `{"error":{"code":429,"message":"Resource exhausted","status":"RESOURCE_EXHAUSTED"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 429, pe.StatusCode)
	assert.True(t, pe.Retryable)
	assert.Contains(t, pe.Message, "Resource exhausted")
	assert.Equal(t, "RESOURCE_EXHAUSTED", pe.Type)
}

func TestChatCompletion_Error401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":{"code":401,"message":"API key not valid","status":"UNAUTHENTICATED"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "bad-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 401, pe.StatusCode)
	assert.False(t, pe.Retryable)
	assert.Equal(t, "UNAUTHENTICATED", pe.Type)
}

func TestChatCompletion_Error400(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"code":400,"message":"Invalid request","status":"INVALID_ARGUMENT"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 400, pe.StatusCode)
	assert.False(t, pe.Retryable)
	assert.Equal(t, "INVALID_ARGUMENT", pe.Type)
}

func TestChatCompletion_Error500_NonJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprint(w, "internal server error")
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	_, err := p.ChatCompletion(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 500, pe.StatusCode)
	assert.True(t, pe.Retryable)
	assert.Contains(t, pe.Message, "internal server error")
	assert.Equal(t, "upstream_error", pe.Type)
}

func TestChatCompletion_MultiPartContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"candidates": [{
				"content": {
					"role": "model",
					"parts": [{"text": "Hello "}, {"text": "world"}, {"text": "!"}]
				},
				"finishReason": "STOP"
			}],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 3, "totalTokenCount": 8}
		}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	resp, err := p.ChatCompletion(context.Background(), testRequest())
	require.NoError(t, err)
	require.Len(t, resp.Choices, 1)
	assert.Equal(t, "Hello world!", resp.Choices[0].Message.Content)
}

func TestChatCompletion_MultipleCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body geminiRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.NotNil(t, body.GenerationConfig)
		assert.Equal(t, 3, *body.GenerationConfig.CandidateCount)

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{
			"candidates": [
				{"content": {"role": "model", "parts": [{"text": "Answer A"}]}, "finishReason": "STOP"},
				{"content": {"role": "model", "parts": [{"text": "Answer B"}]}, "finishReason": "STOP"},
				{"content": {"role": "model", "parts": [{"text": "Answer C"}]}, "finishReason": "STOP"}
			],
			"usageMetadata": {"promptTokenCount": 5, "candidatesTokenCount": 9, "totalTokenCount": 14}
		}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	n := 3
	req := testRequest()
	req.N = &n

	resp, err := p.ChatCompletion(context.Background(), req)
	require.NoError(t, err)
	require.Len(t, resp.Choices, 3)
	assert.Equal(t, 0, resp.Choices[0].Index)
	assert.Equal(t, "Answer A", resp.Choices[0].Message.Content)
	assert.Equal(t, 1, resp.Choices[1].Index)
	assert.Equal(t, "Answer B", resp.Choices[1].Message.Content)
	assert.Equal(t, 2, resp.Choices[2].Index)
	assert.Equal(t, "Answer C", resp.Choices[2].Message.Content)
}

func TestChatCompletionStream_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"error":{"code":400,"message":"Invalid request","status":"INVALID_ARGUMENT"}}`)
	}))
	defer server.Close()

	p := New(Config{APIKey: "test-key", BaseURL: server.URL, Models: []string{"gemini-2.0-flash"}})

	_, err := p.ChatCompletionStream(context.Background(), testRequest())
	require.Error(t, err)

	var pe *model.ProviderError
	require.True(t, errors.As(err, &pe))
	assert.Equal(t, 400, pe.StatusCode)
	assert.False(t, pe.Retryable)
	assert.Equal(t, "INVALID_ARGUMENT", pe.Type)
}
