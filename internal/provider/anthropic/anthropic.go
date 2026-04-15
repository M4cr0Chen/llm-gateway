package anthropic

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

const (
	defaultBaseURL   = "https://api.anthropic.com"
	defaultTimeout   = 30 * time.Second
	defaultMaxTokens = 4096
	apiVersion       = "2023-06-01"
	messagesEndpoint = "/v1/messages"
)

// Config holds configuration for the Anthropic provider adapter.
type Config struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
	Models  []string
}

// Provider implements the provider.Provider interface for Anthropic Claude.
type Provider struct {
	client  *http.Client
	apiKey  string
	baseURL string
	timeout time.Duration
	models  []string
}

// New creates a new Anthropic provider with the given configuration.
// The http.Client has no timeout set — non-streaming requests use
// context-based timeouts so that streaming connections are not killed.
func New(cfg Config) *Provider {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
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

func (p *Provider) Name() string    { return "anthropic" }
func (p *Provider) Models() []string { s := make([]string, len(p.models)); copy(s, p.models); return s }

// --- Anthropic API types ---

type anthropicRequest struct {
	Model         string             `json:"model"`
	Messages      []anthropicMessage `json:"messages"`
	System        string             `json:"system,omitempty"`
	MaxTokens     int                `json:"max_tokens"`
	Temperature   *float64           `json:"temperature,omitempty"`
	TopP          *float64           `json:"top_p,omitempty"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
	Stream        bool               `json:"stream"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []anthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        anthropicUsage     `json:"usage"`
}

type anthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type anthropicErrorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// --- Streaming event types ---

type messageStartEvent struct {
	Type    string            `json:"type"`
	Message anthropicResponse `json:"message"`
}

type contentBlockDeltaEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type messageDeltaEvent struct {
	Type  string `json:"type"`
	Delta struct {
		StopReason   string  `json:"stop_reason"`
		StopSequence *string `json:"stop_sequence"`
	} `json:"delta"`
	Usage struct {
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// --- Request / Response translation ---

func (p *Provider) translateRequest(req *model.ChatCompletionRequest) anthropicRequest {
	ar := anthropicRequest{
		Model:       req.Model,
		Temperature: req.Temperature,
		TopP:        req.TopP,
	}

	// Extract system messages to top-level field; keep user/assistant messages.
	var systemParts []string
	for _, msg := range req.Messages {
		if msg.Role == "system" {
			systemParts = append(systemParts, msg.Content)
		} else {
			ar.Messages = append(ar.Messages, anthropicMessage{
				Role:    msg.Role,
				Content: msg.Content,
			})
		}
	}
	if len(systemParts) > 0 {
		ar.System = strings.Join(systemParts, "\n\n")
	}

	// max_tokens is required by Anthropic.
	if req.MaxTokens != nil {
		ar.MaxTokens = *req.MaxTokens
	} else {
		ar.MaxTokens = defaultMaxTokens
	}

	if len(req.Stop) > 0 {
		ar.StopSequences = req.Stop
	}

	return ar
}

func mapStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return "stop"
	case "max_tokens":
		return "length"
	case "stop_sequence":
		return "stop"
	default:
		return reason
	}
}

func (p *Provider) translateResponse(resp anthropicResponse) *model.ChatCompletionResponse {
	// Concatenate all text content blocks. Anthropic may return multiple text
	// blocks when tool_use is added; for now there is typically one.
	var textParts []string
	for _, c := range resp.Content {
		if c.Type == "text" {
			textParts = append(textParts, c.Text)
		}
	}
	content := strings.Join(textParts, "")

	return &model.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   resp.Model,
		Choices: []model.Choice{
			{
				Index: 0,
				Message: model.Message{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: mapStopReason(resp.StopReason),
			},
		},
		Usage: model.Usage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			TotalTokens:      resp.Usage.InputTokens + resp.Usage.OutputTokens,
		},
	}
}

// --- ChatCompletion (non-streaming) ---

func (p *Provider) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	ar := p.translateRequest(req)
	ar.Stream = false

	body, err := json.Marshal(ar)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+messagesEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, p.handleErrorResponse(resp)
	}

	var ar2 anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&ar2); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return p.translateResponse(ar2), nil
}

// --- ChatCompletionStream (streaming) ---

func (p *Provider) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	ar := p.translateRequest(req)
	ar.Stream = true

	body, err := json.Marshal(ar)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+messagesEndpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	p.setHeaders(httpReq)
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

		var (
			msgID       string
			msgModel    string
			inputTokens int
			eventType   string
		)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Text()

			// Parse "event: <type>" lines.
			if val, ok := strings.CutPrefix(line, "event: "); ok {
				eventType = val
				continue
			}

			// Parse "data: <json>" lines.
			data, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}

			switch eventType {
			case "message_start":
				var ev messageStartEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					sendErr(ctx, ch, fmt.Errorf("decoding message_start: %w", err))
					return
				}
				msgID = ev.Message.ID
				msgModel = ev.Message.Model
				inputTokens = ev.Message.Usage.InputTokens

			case "content_block_delta":
				var ev contentBlockDeltaEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					sendErr(ctx, ch, fmt.Errorf("decoding content_block_delta: %w", err))
					return
				}
				if ev.Delta.Type != "text_delta" {
					continue
				}
				chunk := &model.ChatCompletionChunk{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   msgModel,
					Choices: []model.ChunkChoice{
						{
							Index: 0,
							Delta: model.DeltaMessage{
								Content: ev.Delta.Text,
							},
						},
					},
				}
				select {
				case ch <- provider.StreamEvent{Chunk: chunk}:
				case <-ctx.Done():
					return
				}

			case "message_delta":
				var ev messageDeltaEvent
				if err := json.Unmarshal([]byte(data), &ev); err != nil {
					sendErr(ctx, ch, fmt.Errorf("decoding message_delta: %w", err))
					return
				}
				finishReason := mapStopReason(ev.Delta.StopReason)
				totalOutput := ev.Usage.OutputTokens
				chunk := &model.ChatCompletionChunk{
					ID:      msgID,
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   msgModel,
					Choices: []model.ChunkChoice{
						{
							Index:        0,
							FinishReason: &finishReason,
						},
					},
					Usage: &model.Usage{
						PromptTokens:     inputTokens,
						CompletionTokens: totalOutput,
						TotalTokens:      inputTokens + totalOutput,
					},
				}
				select {
				case ch <- provider.StreamEvent{Chunk: chunk}:
				case <-ctx.Done():
					return
				}

			case "message_stop":
				return
			}
		}

		if err := scanner.Err(); err != nil {
			sendErr(ctx, ch, fmt.Errorf("reading stream: %w", err))
		} else {
			sendErr(ctx, ch, fmt.Errorf("reading stream: unexpected EOF without message_stop"))
		}
	}()

	return ch, nil
}

// --- Helpers ---

func (p *Provider) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", apiVersion)
}

func sendErr(ctx context.Context, ch chan<- provider.StreamEvent, err error) {
	select {
	case ch <- provider.StreamEvent{Err: err}:
	case <-ctx.Done():
	}
}

func (p *Provider) handleErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	retryable := resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= http.StatusInternalServerError ||
		resp.StatusCode == 529 // Anthropic overloaded_error

	pe := &model.ProviderError{
		StatusCode: resp.StatusCode,
		Retryable:  retryable,
	}

	var errResp anthropicErrorResponse
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
