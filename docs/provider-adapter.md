# Provider Adapter Guide

This document explains how to implement a new LLM provider adapter. Follow this guide when adding support for a new provider (e.g., Cohere, Mistral, AWS Bedrock).

## Provider Interface

Defined in `internal/provider/provider.go`:

```go
type Provider interface {
    // Name returns the provider identifier (e.g., "openai", "anthropic")
    Name() string

    // ChatCompletion sends a non-streaming chat completion request.
    // The request is in OpenAI-compatible format. The adapter translates
    // it to the provider's native format, calls the API, and translates
    // the response back to OpenAI format.
    ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error)

    // ChatCompletionStream sends a streaming request.
    // Returns a channel that emits StreamEvent values.
    // The channel MUST be closed when the stream ends (success or error).
    // The adapter MUST respect ctx.Done() and stop consuming from the
    // provider when the context is canceled.
    ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error)

    // Models returns the list of model names this provider serves.
    Models() []string
}

type StreamEvent struct {
    Chunk *model.ChatCompletionChunk  // nil if Err is set
    Err   error                       // nil if Chunk is set
}
```

## Step-by-Step Implementation

### 1. Create the package

```
internal/provider/newprovider/
├── newprovider.go       # Adapter implementation
└── newprovider_test.go  # Tests with mock HTTP server
```

### 2. Define the adapter struct

```go
package newprovider

import (
    "context"
    "net/http"
    "time"

    "github.com/M4cr0Chen/llm-gateway/internal/model"
    "github.com/M4cr0Chen/llm-gateway/internal/provider"
)

type Provider struct {
    client  *http.Client
    apiKey  string
    baseURL string
    timeout time.Duration
    models  []string
}

type Config struct {
    APIKey  string
    BaseURL string
    Timeout time.Duration
    Models  []string
}

// New creates a new provider adapter.
// IMPORTANT: Do NOT set Timeout on http.Client — that would kill streaming
// connections. Instead, use context.WithTimeout in ChatCompletion for
// non-streaming requests, leaving streaming connections unbounded.
func New(cfg Config) *Provider {
    if cfg.Timeout == 0 {
        cfg.Timeout = 30 * time.Second
    }
    return &Provider{
        client:  &http.Client{},
        apiKey:  cfg.APIKey,
        baseURL: cfg.BaseURL,
        timeout: cfg.Timeout,
        models:  cfg.Models,
    }
}

func (p *Provider) Name() string { return "newprovider" }
func (p *Provider) Models() []string { return p.models }
```

### 3. Implement request translation

Translate from OpenAI format to the provider's native format:

```go
// translateRequest converts our OpenAI-compatible request to provider format.
func (p *Provider) translateRequest(req *model.ChatCompletionRequest) (providerRequest, error) {
    // Key translations to handle:
    //
    // 1. Message format differences
    //    - OpenAI: messages array with role/content
    //    - Anthropic: system is top-level, messages array for user/assistant only
    //    - Google: "parts" instead of "content", "model" instead of "assistant"
    //
    // 2. System message handling
    //    - Some providers have system as a separate field
    //    - Extract system messages from the messages array if needed
    //
    // 3. Parameter name mapping
    //    - max_tokens vs max_output_tokens vs maxOutputTokens
    //    - stop vs stop_sequences
    //
    // 4. Model name mapping
    //    - If the provider uses different model identifiers
}
```

### 4. Implement response translation

Translate from provider's response to OpenAI format:

```go
// translateResponse converts provider response to our OpenAI-compatible format.
func (p *Provider) translateResponse(resp providerResponse) *model.ChatCompletionResponse {
    // Key translations:
    //
    // 1. finish_reason mapping:
    //    OpenAI uses: "stop", "length", "content_filter"
    //    Map provider-specific values to these.
    //    Common mappings:
    //      Anthropic: "end_turn" → "stop", "max_tokens" → "length"
    //      Google:    "STOP" → "stop", "MAX_TOKENS" → "length", "SAFETY" → "content_filter"
    //
    // 2. Usage mapping:
    //    OpenAI: prompt_tokens, completion_tokens, total_tokens
    //    Map provider-specific field names to these.
    //
    // 3. Response ID:
    //    Generate one if provider doesn't return it.
    //    Format: "chatcmpl-{uuid}"
    //
    // 4. Created timestamp:
    //    Use provider's timestamp or time.Now().Unix()
}
```

### 5. Implement ChatCompletion (non-streaming)

```go
func (p *Provider) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
    // Use context-based timeout (not http.Client.Timeout) so streaming isn't affected.
    ctx, cancel := context.WithTimeout(ctx, p.timeout)
    defer cancel()

    // 1. Translate request
    provReq, err := p.translateRequest(req)
    if err != nil {
        return nil, fmt.Errorf("translating request: %w", err)
    }

    // 2. Marshal and send HTTP request
    body, err := json.Marshal(provReq)
    if err != nil {
        return nil, fmt.Errorf("marshaling request: %w", err)
    }

    httpReq, err := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/endpoint", bytes.NewReader(body))
    if err != nil {
        return nil, fmt.Errorf("creating request: %w", err)
    }
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

    // 3. Execute request
    httpResp, err := p.client.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("sending request: %w", err)
    }
    defer httpResp.Body.Close()

    // 4. Handle error responses
    if httpResp.StatusCode != http.StatusOK {
        return nil, p.handleErrorResponse(httpResp)
    }

    // 5. Decode and translate response
    var provResp providerResponse
    if err := json.NewDecoder(httpResp.Body).Decode(&provResp); err != nil {
        return nil, fmt.Errorf("decoding response: %w", err)
    }

    return p.translateResponse(provResp), nil
}
```

### 6. Implement ChatCompletionStream (streaming)

```go
func (p *Provider) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan provider.StreamEvent, error) {
    // 1. Translate request (set stream: true in provider format)
    provReq, err := p.translateRequest(req)
    if err != nil {
        return nil, fmt.Errorf("translating request: %w", err)
    }
    provReq.Stream = true

    // 2. Send HTTP request
    body, _ := json.Marshal(provReq)
    httpReq, _ := http.NewRequestWithContext(ctx, "POST", p.baseURL+"/endpoint", bytes.NewReader(body))
    httpReq.Header.Set("Content-Type", "application/json")
    httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

    httpResp, err := p.client.Do(httpReq)
    if err != nil {
        return nil, fmt.Errorf("sending request: %w", err)
    }

    if httpResp.StatusCode != http.StatusOK {
        httpResp.Body.Close()
        return nil, p.handleErrorResponse(httpResp)
    }

    // 3. Start goroutine to read stream and emit events
    ch := make(chan provider.StreamEvent, 8) // buffered to prevent blocking
    go func() {
        defer close(ch)
        defer httpResp.Body.Close()

        scanner := bufio.NewScanner(httpResp.Body)
        // Enlarge scanner buffer for large chunks (64KB initial, 1MB max).
        scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

        gotDone := false
        for scanner.Scan() {
            select {
            case <-ctx.Done():
                return // client disconnected, stop reading
            default:
            }

            line := scanner.Text()

            // Handle SSE format: "data: {json}" or "data: [DONE]"
            if !strings.HasPrefix(line, "data: ") {
                continue
            }
            data := strings.TrimPrefix(line, "data: ")
            if data == "[DONE]" {
                gotDone = true
                break
            }

            // Parse provider chunk and translate to OpenAI chunk format
            var provChunk providerChunk
            if err := json.Unmarshal([]byte(data), &provChunk); err != nil {
                select {
                case ch <- provider.StreamEvent{Err: fmt.Errorf("decoding chunk: %w", err)}:
                case <-ctx.Done():
                }
                return
            }

            chunk := p.translateChunk(provChunk)
            select {
            case ch <- provider.StreamEvent{Chunk: chunk}:
            case <-ctx.Done():
                return
            }
        }

        if gotDone {
            return
        }
        // If we didn't get [DONE], report the error.
        if err := scanner.Err(); err != nil {
            select {
            case ch <- provider.StreamEvent{Err: fmt.Errorf("reading stream: %w", err)}:
            case <-ctx.Done():
            }
        } else {
            select {
            case ch <- provider.StreamEvent{Err: fmt.Errorf("reading stream: unexpected EOF without [DONE]")}:
            case <-ctx.Done():
            }
        }
    }()

    return ch, nil
}
```

### 7. Implement error handling

```go
// handleErrorResponse translates provider error responses to gateway errors.
// It tries to parse the provider's JSON error body first, then falls back
// to raw text (truncated at 200 chars).
func (p *Provider) handleErrorResponse(resp *http.Response) error {
    body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit

    retryable := resp.StatusCode == http.StatusTooManyRequests ||
        resp.StatusCode >= http.StatusInternalServerError

    pe := &model.ProviderError{
        StatusCode: resp.StatusCode,
        Retryable:  retryable,
    }

    // Try parsing provider's error JSON. Fall back to raw text if it fails.
    var errResp providerErrorResponse
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

// errorTypeFromStatus maps HTTP status codes to OpenAI error type strings.
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
```

### 8. Register in provider registry

In `cmd/gateway/main.go`, providers are registered from a `map[string]ProviderConfig` via a switch statement:

```go
registry := provider.NewRegistry()

for name, provCfg := range cfg.Providers {
    if provCfg.APIKey == "" {
        slog.Warn("skipping provider with no API key", "provider", name)
        continue
    }
    switch name {
    case "openai":
        p := openai.New(openai.Config{...})
        registry.Register(p, p.Models())
    case "newprovider":
        p := newprovider.New(newprovider.Config{
            APIKey:  provCfg.APIKey,
            BaseURL: provCfg.BaseURL,
            Timeout: provCfg.Timeout,
            Models:  provCfg.Models,
        })
        registry.Register(p, p.Models())
    default:
        slog.Warn("unknown provider, skipping", "provider", name)
    }
}
```

## Testing Pattern

Every adapter MUST include tests with a mock HTTP server:

```go
func TestChatCompletion(t *testing.T) {
    // 1. Create mock server that returns canned response
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // Verify request format (headers, body structure)
        assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
        assert.Equal(t, "Bearer test-key", r.Header.Get("Authorization"))

        var req providerRequest
        json.NewDecoder(r.Body).Decode(&req)
        // Assert request was translated correctly
        assert.Equal(t, expectedProviderModel, req.Model)

        // Return canned response
        json.NewEncoder(w).Encode(cannedProviderResponse)
    }))
    defer server.Close()

    // 2. Create adapter pointing to mock server
    p := New(Config{
        APIKey:  "test-key",
        BaseURL: server.URL,
        Timeout: 5 * time.Second,
        Models:  []string{"test-model"},
    })

    // 3. Send request and verify response translation
    resp, err := p.ChatCompletion(context.Background(), &model.ChatCompletionRequest{
        Model:    "test-model",
        Messages: []model.Message{{Role: "user", Content: "hello"}},
    })
    require.NoError(t, err)
    assert.Equal(t, "assistant", resp.Choices[0].Message.Role)
    assert.Equal(t, "stop", resp.Choices[0].FinishReason)
}

func TestChatCompletionStream(t *testing.T) {
    // Similar, but mock server returns SSE format
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        flusher := w.(http.Flusher)

        chunks := []string{`data: {"chunk": "Hello"}`, `data: {"chunk": "!"}`, `data: [DONE]`}
        for _, chunk := range chunks {
            fmt.Fprintln(w, chunk)
            fmt.Fprintln(w)
            flusher.Flush()
        }
    }))
    defer server.Close()

    // ... create adapter, call ChatCompletionStream, read from channel, verify
}

func TestContextCancellation(t *testing.T) {
    // Verify that canceling context stops the streaming goroutine
    server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "text/event-stream")
        // Send chunks slowly
        for i := 0; i < 100; i++ {
            select {
            case <-r.Context().Done():
                return
            case <-time.After(100 * time.Millisecond):
                fmt.Fprintf(w, "data: {\"chunk\": \"%d\"}\n\n", i)
                w.(http.Flusher).Flush()
            }
        }
    }))
    defer server.Close()

    p := New(Config{APIKey: "test", BaseURL: server.URL, Timeout: 30 * time.Second, Models: []string{"m"}})

    ctx, cancel := context.WithCancel(context.Background())
    ch, err := p.ChatCompletionStream(ctx, &model.ChatCompletionRequest{
        Model:    "m",
        Messages: []model.Message{{Role: "user", Content: "hello"}},
        Stream:   true,
    })
    require.NoError(t, err)

    // Read one chunk
    event := <-ch
    assert.NotNil(t, event.Chunk)

    // Cancel and verify channel closes quickly
    cancel()
    timeout := time.After(200 * time.Millisecond)
    for {
        select {
        case _, ok := <-ch:
            if !ok {
                return // success: channel closed
            }
        case <-timeout:
            t.Fatal("channel did not close within 200ms after cancel")
        }
    }
}
```

## Checklist for New Provider

- [ ] Adapter struct implements `provider.Provider` interface
- [ ] Request translation handles: messages, system prompt, parameters, model name
- [ ] Response translation handles: choices, finish_reason mapping, usage mapping
- [ ] Streaming translation handles: all event types, `[DONE]` signal, error events
- [ ] Error handling maps provider errors to `model.ProviderError` with correct `Retryable` flag
- [ ] Context cancellation stops streaming goroutine within 100ms
- [ ] Unit tests with mock HTTP server for: non-streaming, streaming, errors, context cancel
- [ ] No goroutine leaks (channel always closed, response body always closed)
- [ ] Registered in provider registry with correct model names
- [ ] Config added to `configs/gateway.yaml` and config struct
