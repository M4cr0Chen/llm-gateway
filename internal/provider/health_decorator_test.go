package provider

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
)

// fakeProvider is a controllable provider for testing the decorator.
type fakeProvider struct {
	name      string
	models    []string
	calls     int
	responses []*model.ChatCompletionResponse
	errors    []error
	// For streaming tests:
	streamCh    <-chan StreamEvent
	streamErr   error
	streamCalls int
}

func (f *fakeProvider) Name() string    { return f.name }
func (f *fakeProvider) Models() []string { return f.models }

func (f *fakeProvider) ChatCompletion(_ context.Context, _ *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	idx := f.calls
	f.calls++
	if idx < len(f.errors) && f.errors[idx] != nil {
		return nil, f.errors[idx]
	}
	if idx < len(f.responses) {
		return f.responses[idx], nil
	}
	return &model.ChatCompletionResponse{ID: "ok"}, nil
}

func (f *fakeProvider) ChatCompletionStream(_ context.Context, _ *model.ChatCompletionRequest) (<-chan StreamEvent, error) {
	f.streamCalls++
	if f.streamErr != nil && f.streamCalls <= len(f.errors) {
		err := f.errors[f.streamCalls-1]
		if err != nil {
			return nil, err
		}
	}
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	return f.streamCh, nil
}

func retryableErr(status int) *model.ProviderError {
	return &model.ProviderError{
		StatusCode: status,
		Retryable:  true,
		Message:    "upstream error",
	}
}

func nonRetryableErr() *model.ProviderError {
	return &model.ProviderError{
		StatusCode: http.StatusBadRequest,
		Retryable:  false,
		Message:    "bad request",
	}
}

func newTestDecorator(fp *fakeProvider, maxRetries int) *HealthTrackingProvider {
	h := NewHealthTrackingProvider(fp, HealthConfig{FailureThreshold: 3}, RetryConfig{
		MaxRetries:   maxRetries,
		RetryBackoff: time.Millisecond, // fast for tests
	})
	h.sleep = func(time.Duration) {} // no-op sleep
	return h
}

func TestDecorator_SuccessNoRetry(t *testing.T) {
	fp := &fakeProvider{name: "test", models: []string{"m1"}}
	dec := newTestDecorator(fp, 2)

	resp, err := dec.ChatCompletion(context.Background(), &model.ChatCompletionRequest{})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.ID)
	assert.Equal(t, 1, fp.calls)
	assert.True(t, dec.Health.IsHealthy())
}

func TestDecorator_RetryOnRetryableError(t *testing.T) {
	fp := &fakeProvider{
		name:   "test",
		models: []string{"m1"},
		errors: []error{retryableErr(http.StatusInternalServerError), nil},
	}
	dec := newTestDecorator(fp, 2)

	resp, err := dec.ChatCompletion(context.Background(), &model.ChatCompletionRequest{})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.ID)
	assert.Equal(t, 2, fp.calls) // 1 fail + 1 success
	assert.True(t, dec.Health.IsHealthy())
}

func TestDecorator_ExhaustsRetries(t *testing.T) {
	fp := &fakeProvider{
		name:   "test",
		models: []string{"m1"},
		errors: []error{
			retryableErr(http.StatusServiceUnavailable),
			retryableErr(http.StatusServiceUnavailable),
			retryableErr(http.StatusServiceUnavailable),
		},
	}
	dec := newTestDecorator(fp, 2)

	_, err := dec.ChatCompletion(context.Background(), &model.ChatCompletionRequest{})
	require.Error(t, err)
	assert.Equal(t, 3, fp.calls) // initial + 2 retries
}

func TestDecorator_NoRetryOnNonRetryable(t *testing.T) {
	fp := &fakeProvider{
		name:   "test",
		models: []string{"m1"},
		errors: []error{nonRetryableErr()},
	}
	dec := newTestDecorator(fp, 2)

	_, err := dec.ChatCompletion(context.Background(), &model.ChatCompletionRequest{})
	require.Error(t, err)
	assert.Equal(t, 1, fp.calls) // no retries
}

func TestDecorator_HealthRecordedOnFailure(t *testing.T) {
	fp := &fakeProvider{
		name:   "test",
		models: []string{"m1"},
		errors: []error{nonRetryableErr(), nonRetryableErr(), nonRetryableErr()},
	}
	dec := newTestDecorator(fp, 0) // no retries

	for i := 0; i < 3; i++ {
		_, _ = dec.ChatCompletion(context.Background(), &model.ChatCompletionRequest{})
	}

	assert.False(t, dec.Health.IsHealthy()) // threshold=3
}

func TestDecorator_ContextCancelDuringRetry(t *testing.T) {
	fp := &fakeProvider{
		name:   "test",
		models: []string{"m1"},
		errors: []error{
			retryableErr(500),
			retryableErr(500),
			retryableErr(500),
			retryableErr(500),
		},
	}
	dec := NewHealthTrackingProvider(fp, HealthConfig{FailureThreshold: 10}, RetryConfig{
		MaxRetries:   3,
		RetryBackoff: time.Second, // long enough that context cancels first
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	_, err := dec.ChatCompletion(ctx, &model.ChatCompletionRequest{})
	require.Error(t, err)
}

func TestDecorator_RetryAfterHonored(t *testing.T) {
	pe := &model.ProviderError{
		StatusCode: http.StatusTooManyRequests,
		Retryable:  true,
		RetryAfter: 5 * time.Second,
		Message:    "rate limited",
	}
	fp := &fakeProvider{
		name:   "test",
		models: []string{"m1"},
		errors: []error{pe, nil},
	}
	dec := newTestDecorator(fp, 1)

	var sleptDuration time.Duration
	dec.sleep = func(d time.Duration) { sleptDuration = d }

	_, err := dec.ChatCompletion(context.Background(), &model.ChatCompletionRequest{})
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, sleptDuration)
}

func TestDecorator_ExponentialBackoff(t *testing.T) {
	fp := &fakeProvider{
		name:   "test",
		models: []string{"m1"},
		errors: []error{
			retryableErr(500),
			retryableErr(500),
			retryableErr(500),
			nil,
		},
	}
	dec := NewHealthTrackingProvider(fp, HealthConfig{FailureThreshold: 10}, RetryConfig{
		MaxRetries:   3,
		RetryBackoff: 100 * time.Millisecond,
	})

	var sleeps []time.Duration
	dec.sleep = func(d time.Duration) { sleeps = append(sleeps, d) }

	_, err := dec.ChatCompletion(context.Background(), &model.ChatCompletionRequest{})
	require.NoError(t, err)
	require.Len(t, sleeps, 3)
	assert.Equal(t, 100*time.Millisecond, sleeps[0]) // 100ms * 2^0
	assert.Equal(t, 200*time.Millisecond, sleeps[1]) // 100ms * 2^1
	assert.Equal(t, 400*time.Millisecond, sleeps[2]) // 100ms * 2^2
}

func TestDecorator_StreamSuccess(t *testing.T) {
	ch := make(chan StreamEvent, 2)
	ch <- StreamEvent{Chunk: &model.ChatCompletionChunk{ID: "c1"}}
	close(ch)

	fp := &fakeProvider{
		name:     "test",
		models:   []string{"m1"},
		streamCh: ch,
	}
	dec := newTestDecorator(fp, 0)

	out, err := dec.ChatCompletionStream(context.Background(), &model.ChatCompletionRequest{})
	require.NoError(t, err)

	var events []StreamEvent
	for evt := range out {
		events = append(events, evt)
	}
	require.Len(t, events, 1)
	assert.Equal(t, "c1", events[0].Chunk.ID)
	// Wait briefly for the goroutine to record health.
	time.Sleep(10 * time.Millisecond)
	assert.True(t, dec.Health.IsHealthy())
}

func TestDecorator_StreamRetryOnInitError(t *testing.T) {
	ch := make(chan StreamEvent, 1)
	close(ch)

	fp := &streamRetryFake{
		name:   "test",
		models: []string{"m1"},
		results: []streamResult{
			{err: retryableErr(500)},
			{ch: ch},
		},
	}
	dec := NewHealthTrackingProvider(fp, HealthConfig{FailureThreshold: 3}, RetryConfig{
		MaxRetries:   1,
		RetryBackoff: time.Millisecond,
	})
	dec.sleep = func(time.Duration) {}

	out, err := dec.ChatCompletionStream(context.Background(), &model.ChatCompletionRequest{})
	require.NoError(t, err)
	for range out {
	}
	assert.Equal(t, 2, fp.calls)
}

func TestDecorator_StreamErrorRecordsFailure(t *testing.T) {
	ch := make(chan StreamEvent, 2)
	ch <- StreamEvent{Err: errors.New("stream broke")}
	close(ch)

	fp := &fakeProvider{
		name:     "test",
		models:   []string{"m1"},
		streamCh: ch,
	}
	dec := NewHealthTrackingProvider(fp, HealthConfig{FailureThreshold: 1}, RetryConfig{})
	dec.sleep = func(time.Duration) {}

	out, err := dec.ChatCompletionStream(context.Background(), &model.ChatCompletionRequest{})
	require.NoError(t, err)
	for range out {
	}
	// Wait for goroutine.
	time.Sleep(10 * time.Millisecond)
	assert.False(t, dec.Health.IsHealthy())
}

func TestDecorator_NameAndModels(t *testing.T) {
	fp := &fakeProvider{name: "openai", models: []string{"gpt-4o", "gpt-3.5"}}
	dec := newTestDecorator(fp, 0)
	assert.Equal(t, "openai", dec.Name())
	assert.Equal(t, []string{"gpt-4o", "gpt-3.5"}, dec.Models())
}

// streamRetryFake supports sequence of stream results for retry testing.
type streamRetryFake struct {
	name    string
	models  []string
	results []streamResult
	calls   int
}

type streamResult struct {
	ch  <-chan StreamEvent
	err error
}

func (f *streamRetryFake) Name() string    { return f.name }
func (f *streamRetryFake) Models() []string { return f.models }

func (f *streamRetryFake) ChatCompletion(_ context.Context, _ *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	return nil, errors.New("not implemented")
}

func (f *streamRetryFake) ChatCompletionStream(_ context.Context, _ *model.ChatCompletionRequest) (<-chan StreamEvent, error) {
	idx := f.calls
	f.calls++
	if idx < len(f.results) {
		r := f.results[idx]
		return r.ch, r.err
	}
	ch := make(chan StreamEvent)
	close(ch)
	return ch, nil
}
