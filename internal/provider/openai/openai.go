package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

// Config holds configuration for the OpenAI provider adapter.
type Config struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
	Models  []string
}

// Provider implements the provider.Provider interface for OpenAI.
type Provider struct {
	client  *http.Client
	apiKey  string
	baseURL string
	timeout time.Duration
	models  []string
}

// openaiRequest wraps ChatCompletionRequest to control the stream field
// without mutating the caller's request.
type openaiRequest struct {
	*model.ChatCompletionRequest
	Stream bool `json:"stream"`
}

// openaiErrorResponse represents an error response from the OpenAI API.
type openaiErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// New creates a new OpenAI provider with the given configuration.
// The http.Client has no timeout set — non-streaming requests use
// context-based timeouts so that streaming connections are not killed.
func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.openai.com/v1"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	models := make([]string, len(cfg.Models))
	copy(models, cfg.Models)

	return &Provider{
		client:  &http.Client{},
		apiKey:  cfg.APIKey,
		baseURL: cfg.BaseURL,
		timeout: cfg.Timeout,
		models:  models,
	}
}

func (p *Provider) Name() string      { return "openai" }
func (p *Provider) Models() []string   { s := make([]string, len(p.models)); copy(s, p.models); return s }

// ChatCompletion sends a non-streaming chat completion request to OpenAI.
func (p *Provider) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	body, err := json.Marshal(openaiRequest{ChatCompletionRequest: req, Stream: false})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, p.handleErrorResponse(resp)
	}

	var result model.ChatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &result, nil
}

// ChatCompletionStream sends a streaming chat completion request to OpenAI.
// The returned channel emits StreamEvents and is always closed when the stream ends.
func (p *Provider) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	body, err := json.Marshal(openaiRequest{ChatCompletionRequest: req, Stream: true})
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		return nil, p.handleErrorResponse(resp)
	}

	ch := make(chan provider.StreamEvent, 8)

	go func() {
		defer close(ch)
		defer func() { _ = resp.Body.Close() }()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				return
			}

			var chunk model.ChatCompletionChunk
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				select {
				case ch <- provider.StreamEvent{Err: fmt.Errorf("decoding chunk: %w", err)}:
				case <-ctx.Done():
				}
				return
			}

			select {
			case ch <- provider.StreamEvent{Chunk: &chunk}:
			case <-ctx.Done():
				return
			}
		}

		if err := scanner.Err(); err != nil {
			select {
			case ch <- provider.StreamEvent{Err: fmt.Errorf("reading stream: %w", err)}:
			case <-ctx.Done():
			}
		}
	}()

	return ch, nil
}

func (p *Provider) handleErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	retryable := resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= http.StatusInternalServerError

	pe := &model.ProviderError{
		StatusCode: resp.StatusCode,
		Retryable:  retryable,
	}

	var errResp openaiErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		pe.Message = errResp.Error.Message
		pe.Type = errResp.Error.Type
	} else {
		msg := string(body)
		if len(msg) > 200 {
			msg = msg[:200]
		}
		pe.Message = msg
		pe.Type = errorTypeFromStatus(resp.StatusCode)
	}

	return pe
}

func errorTypeFromStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusBadRequest:
		return "invalid_request_error"
	default:
		return "upstream_error"
	}
}
