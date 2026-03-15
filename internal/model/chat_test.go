package model_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ptr[T any](v T) *T { return &v }

func TestChatCompletionRequest_JSONRoundTrip(t *testing.T) {
	req := model.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []model.Message{
			{Role: "system", Content: "You are a helpful assistant."},
			{Role: "user", Content: "Hello!"},
		},
		Temperature:      ptr(0.7),
		TopP:             ptr(1.0),
		N:                ptr(1),
		Stream:           false,
		Stop:             []string{"\n"},
		MaxTokens:        ptr(1000),
		PresencePenalty:  ptr(0.0),
		FrequencyPenalty: ptr(0.0),
		User:             "user-123",
	}

	data, err := json.Marshal(req)
	require.NoError(t, err)

	var decoded model.ChatCompletionRequest
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, req, decoded)

	// Verify snake_case JSON keys
	raw := string(data)
	assert.Contains(t, raw, `"max_tokens"`)
	assert.Contains(t, raw, `"top_p"`)
	assert.Contains(t, raw, `"presence_penalty"`)
	assert.Contains(t, raw, `"frequency_penalty"`)
}

func TestChatCompletionRequest_OptionalFieldsOmitted(t *testing.T) {
	req := model.ChatCompletionRequest{
		Model: "gpt-4o",
		Messages: []model.Message{
			{Role: "user", Content: "Hello!"},
		},
	}

	data, err := json.Marshal(req)
	require.NoError(t, err)

	raw := string(data)
	assert.NotContains(t, raw, `"temperature"`)
	assert.NotContains(t, raw, `"top_p"`)
	assert.NotContains(t, raw, `"max_tokens"`)
	assert.NotContains(t, raw, `"n"`)
	assert.NotContains(t, raw, `"stop"`)
	assert.NotContains(t, raw, `"presence_penalty"`)
	assert.NotContains(t, raw, `"frequency_penalty"`)
	assert.NotContains(t, raw, `"stream"`)
	assert.NotContains(t, raw, `"user":"`)
}

func TestChatCompletionResponse_UnmarshalOpenAIJSON(t *testing.T) {
	openaiJSON := `{
		"id": "chatcmpl-abc123",
		"object": "chat.completion",
		"created": 1700000000,
		"model": "gpt-4o",
		"choices": [
			{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Hello! How can I help you today?"
				},
				"finish_reason": "stop"
			}
		],
		"usage": {
			"prompt_tokens": 20,
			"completion_tokens": 9,
			"total_tokens": 29
		},
		"system_fingerprint": "fp_abc123"
	}`

	var resp model.ChatCompletionResponse
	err := json.Unmarshal([]byte(openaiJSON), &resp)
	require.NoError(t, err)

	assert.Equal(t, "chatcmpl-abc123", resp.ID)
	assert.Equal(t, "chat.completion", resp.Object)
	assert.Equal(t, int64(1700000000), resp.Created)
	assert.Equal(t, "gpt-4o", resp.Model)
	assert.Equal(t, "fp_abc123", resp.SystemFingerprint)

	require.Len(t, resp.Choices, 1)
	assert.Equal(t, 0, resp.Choices[0].Index)
	assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
	assert.Equal(t, "Hello! How can I help you today?", resp.Choices[0].Message.Content)
	assert.Equal(t, "stop", resp.Choices[0].FinishReason)

	assert.Equal(t, 20, resp.Usage.PromptTokens)
	assert.Equal(t, 9, resp.Usage.CompletionTokens)
	assert.Equal(t, 29, resp.Usage.TotalTokens)
}

func TestChatCompletionChunk_IntermediateChunk(t *testing.T) {
	chunkJSON := `{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}`

	var chunk model.ChatCompletionChunk
	err := json.Unmarshal([]byte(chunkJSON), &chunk)
	require.NoError(t, err)

	assert.Equal(t, "chatcmpl-abc123", chunk.ID)
	assert.Equal(t, "chat.completion.chunk", chunk.Object)

	require.Len(t, chunk.Choices, 1)
	assert.Equal(t, "Hello", chunk.Choices[0].Delta.Content)
	assert.Nil(t, chunk.Choices[0].FinishReason)
	assert.Nil(t, chunk.Usage)
}

func TestChatCompletionChunk_FinalChunk(t *testing.T) {
	chunkJSON := `{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`

	var chunk model.ChatCompletionChunk
	err := json.Unmarshal([]byte(chunkJSON), &chunk)
	require.NoError(t, err)

	require.Len(t, chunk.Choices, 1)
	require.NotNil(t, chunk.Choices[0].FinishReason)
	assert.Equal(t, "stop", *chunk.Choices[0].FinishReason)
}

func TestChatCompletionChunk_MarshalPreservesNullFinishReason(t *testing.T) {
	chunk := model.ChatCompletionChunk{
		ID:      "chatcmpl-abc123",
		Object:  "chat.completion.chunk",
		Created: 1700000000,
		Model:   "gpt-4o",
		Choices: []model.ChunkChoice{
			{
				Index:        0,
				Delta:        model.DeltaMessage{Content: "Hello"},
				FinishReason: nil,
			},
		},
	}

	data, err := json.Marshal(chunk)
	require.NoError(t, err)

	raw := string(data)
	assert.True(t, strings.Contains(raw, `"finish_reason":null`),
		"expected finish_reason:null in JSON, got: %s", raw)
}

func TestChatCompletionChunk_UsageChunk(t *testing.T) {
	chunkJSON := `{"id":"chatcmpl-abc123","object":"chat.completion.chunk","created":1700000000,"model":"gpt-4o","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`

	var chunk model.ChatCompletionChunk
	err := json.Unmarshal([]byte(chunkJSON), &chunk)
	require.NoError(t, err)

	assert.Empty(t, chunk.Choices)
	require.NotNil(t, chunk.Usage)
	assert.Equal(t, 20, chunk.Usage.PromptTokens)
	assert.Equal(t, 2, chunk.Usage.CompletionTokens)
	assert.Equal(t, 22, chunk.Usage.TotalTokens)
}

func TestProviderError_ImplementsErrorInterface(t *testing.T) {
	var _ error = &model.ProviderError{}

	pe := &model.ProviderError{
		StatusCode: 429,
		Type:       "rate_limit_error",
		Message:    "too many requests",
		Retryable:  true,
	}

	errMsg := pe.Error()
	assert.Contains(t, errMsg, "429")
	assert.Contains(t, errMsg, "too many requests")
}

func TestAPIError_JSONRoundTrip(t *testing.T) {
	apiErr := model.APIError{
		Error: model.ErrorDetail{
			Message: "Unknown model: foo",
			Type:    "invalid_request_error",
			Code:    "invalid_model",
		},
	}

	data, err := json.Marshal(apiErr)
	require.NoError(t, err)

	var decoded model.APIError
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, apiErr, decoded)

	// Verify JSON structure
	var raw map[string]any
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)

	errObj, ok := raw["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "Unknown model: foo", errObj["message"])
	assert.Equal(t, "invalid_request_error", errObj["type"])
	assert.Equal(t, "invalid_model", errObj["code"])
}
