package google

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/M4cr0Chen/llm-gateway/internal/provider"
)

const (
	defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
	defaultTimeout = 30 * time.Second
)

// Config holds configuration for the Google Gemini provider adapter.
type Config struct {
	APIKey  string
	BaseURL string
	Timeout time.Duration
	Models  []string
}

// Provider implements the provider.Provider interface for Google Gemini.
type Provider struct {
	client  *http.Client
	apiKey  string
	baseURL string
	timeout time.Duration
	models  []string
}

// New creates a new Google Gemini provider with the given configuration.
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

func (p *Provider) Name() string    { return "google" }
func (p *Provider) Models() []string { s := make([]string, len(p.models)); copy(s, p.models); return s }

// --- Gemini API types ---

type geminiRequest struct {
	Contents          []geminiContent          `json:"contents"`
	SystemInstruction *geminiSystemInstruction `json:"systemInstruction,omitempty"`
	GenerationConfig  *geminiGenerationConfig  `json:"generationConfig,omitempty"`
}

type geminiSystemInstruction struct {
	Parts []geminiPart `json:"parts"`
}

type geminiContent struct {
	Role  string       `json:"role"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenerationConfig struct {
	Temperature    *float64 `json:"temperature,omitempty"`
	TopP           *float64 `json:"topP,omitempty"`
	MaxOutputTokens *int    `json:"maxOutputTokens,omitempty"`
	StopSequences  []string `json:"stopSequences,omitempty"`
	CandidateCount *int     `json:"candidateCount,omitempty"`
}

type geminiResponse struct {
	Candidates    []geminiCandidate    `json:"candidates"`
	UsageMetadata *geminiUsageMetadata `json:"usageMetadata,omitempty"`
}

type geminiCandidate struct {
	Content      geminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
}

type geminiUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type geminiErrorResponse struct {
	Error struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
	} `json:"error"`
}

// --- Request / Response translation ---

func (p *Provider) translateRequest(req *model.ChatCompletionRequest) geminiRequest {
	gr := geminiRequest{}

	var systemParts []geminiPart
	for _, msg := range req.Messages {
		switch msg.Role {
		case "system":
			systemParts = append(systemParts, geminiPart{Text: msg.Content})
		default:
			role := msg.Role
			if role == "assistant" {
				role = "model"
			}
			gr.Contents = append(gr.Contents, geminiContent{
				Role:  role,
				Parts: []geminiPart{{Text: msg.Content}},
			})
		}
	}

	if len(systemParts) > 0 {
		gr.SystemInstruction = &geminiSystemInstruction{Parts: systemParts}
	}

	// Build generationConfig if any parameters are set.
	if req.Temperature != nil || req.TopP != nil || req.MaxTokens != nil || len(req.Stop) > 0 || req.N != nil {
		gc := &geminiGenerationConfig{
			Temperature:    req.Temperature,
			TopP:           req.TopP,
			MaxOutputTokens: req.MaxTokens,
			StopSequences:  req.Stop,
		}
		if req.N != nil {
			gc.CandidateCount = req.N
		}
		gr.GenerationConfig = gc
	}

	return gr
}

func mapFinishReason(reason string) string {
	switch reason {
	case "STOP":
		return "stop"
	case "MAX_TOKENS":
		return "length"
	case "SAFETY":
		return "content_filter"
	case "RECITATION":
		return "content_filter"
	default:
		return "stop"
	}
}

func generateID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("chatcmpl-%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func (p *Provider) translateResponse(resp geminiResponse, reqModel string) *model.ChatCompletionResponse {
	result := &model.ChatCompletionResponse{
		ID:      generateID(),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   reqModel,
	}

	for i, candidate := range resp.Candidates {
		var content string
		for _, part := range candidate.Content.Parts {
			content += part.Text
		}
		result.Choices = append(result.Choices, model.Choice{
			Index: i,
			Message: model.Message{
				Role:    "assistant",
				Content: content,
			},
			FinishReason: mapFinishReason(candidate.FinishReason),
		})
	}

	if resp.UsageMetadata != nil {
		result.Usage = model.Usage{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		}
	}

	return result
}

// --- URL helpers ---

// Note: Gemini requires the API key as a query parameter. Avoid logging
// these URLs, as the key will be visible in any access logs or debug output.
func (p *Provider) generateContentURL(modelName string) string {
	return fmt.Sprintf("%s/models/%s:generateContent?key=%s", p.baseURL, modelName, p.apiKey)
}

func (p *Provider) streamGenerateContentURL(modelName string) string {
	return fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", p.baseURL, modelName, p.apiKey)
}

// --- ChatCompletion (non-streaming) ---

func (p *Provider) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	gr := p.translateRequest(req)

	body, err := json.Marshal(gr)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.generateContentURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, p.handleErrorResponse(resp)
	}

	var gemResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gemResp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}

	return p.translateResponse(gemResp, req.Model), nil
}

// --- ChatCompletionStream (streaming) ---

func (p *Provider) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
	gr := p.translateRequest(req)

	body, err := json.Marshal(gr)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.streamGenerateContentURL(req.Model), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
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
	streamID := generateID()

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

			var chunk geminiResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				sendErr(ctx, ch, fmt.Errorf("decoding chunk: %w", err))
				return
			}

			oaiChunk := p.translateChunk(chunk, streamID, req.Model)
			select {
			case ch <- provider.StreamEvent{Chunk: oaiChunk}:
			case <-ctx.Done():
				return
			}
		}

		// Gemini streaming ends on EOF — no explicit [DONE] signal.
		// A clean EOF (scanner.Err() == nil) is the normal termination.
		if err := scanner.Err(); err != nil {
			sendErr(ctx, ch, fmt.Errorf("reading stream: %w", err))
		}
	}()

	return ch, nil
}

func (p *Provider) translateChunk(resp geminiResponse, streamID, reqModel string) *model.ChatCompletionChunk {
	chunk := &model.ChatCompletionChunk{
		ID:      streamID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   reqModel,
	}

	for i, candidate := range resp.Candidates {
		var content string
		for _, part := range candidate.Content.Parts {
			content += part.Text
		}

		cc := model.ChunkChoice{
			Index: i,
			Delta: model.DeltaMessage{
				Content: content,
			},
		}

		if candidate.FinishReason != "" {
			fr := mapFinishReason(candidate.FinishReason)
			cc.FinishReason = &fr
		}

		chunk.Choices = append(chunk.Choices, cc)
	}

	if resp.UsageMetadata != nil {
		chunk.Usage = &model.Usage{
			PromptTokens:     resp.UsageMetadata.PromptTokenCount,
			CompletionTokens: resp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      resp.UsageMetadata.TotalTokenCount,
		}
	}

	return chunk
}

// --- Helpers ---

func sendErr(ctx context.Context, ch chan<- provider.StreamEvent, err error) {
	select {
	case ch <- provider.StreamEvent{Err: err}:
	case <-ctx.Done():
	}
}

func (p *Provider) handleErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	retryable := resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode >= http.StatusInternalServerError

	pe := &model.ProviderError{
		StatusCode: resp.StatusCode,
		Retryable:  retryable,
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
	}

	var errResp geminiErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		pe.Message = errResp.Error.Message
		pe.Type = errResp.Error.Status
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

func parseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	secs, err := strconv.Atoi(header)
	if err != nil {
		return 0
	}
	return time.Duration(secs) * time.Second
}

func errorTypeFromStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication_error"
	case http.StatusForbidden:
		return "authentication_error"
	case http.StatusTooManyRequests:
		return "rate_limit_error"
	case http.StatusBadRequest:
		return "invalid_request_error"
	default:
		return "upstream_error"
	}
}
