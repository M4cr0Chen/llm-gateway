package provider

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
)

// maxBackoff is the upper bound for exponential backoff duration.
const maxBackoff = 30 * time.Second

// RetryConfig holds per-provider retry settings.
type RetryConfig struct {
	MaxRetries   int           `koanf:"max_retries"`
	RetryBackoff time.Duration `koanf:"retry_backoff"`
}

// HealthTrackingProvider wraps a Provider to track health and retry on
// retryable errors with exponential backoff.
type HealthTrackingProvider struct {
	wrapped Provider
	Health  *ProviderHealth
	retry   RetryConfig
	timer   func(time.Duration) <-chan time.Time // for testing
	rand    func() float64                       // for testing; returns [0.0, 1.0)
}

// NewHealthTrackingProvider creates a decorator that wraps the given provider
// with health tracking and retry logic.
func NewHealthTrackingProvider(p Provider, healthCfg HealthConfig, retryCfg RetryConfig) *HealthTrackingProvider {
	return &HealthTrackingProvider{
		wrapped: p,
		Health:  NewProviderHealth(healthCfg),
		retry:   retryCfg,
		timer: func(d time.Duration) <-chan time.Time {
			return time.NewTimer(d).C
		},
		rand: rand.Float64,
	}
}

func (h *HealthTrackingProvider) Name() string    { return h.wrapped.Name() }
func (h *HealthTrackingProvider) Models() []string { return h.wrapped.Models() }

// ChatCompletion delegates to the wrapped provider with retry logic and
// records the outcome for health tracking.
func (h *HealthTrackingProvider) ChatCompletion(ctx context.Context, req *model.ChatCompletionRequest) (*model.ChatCompletionResponse, error) {
	var lastErr error

	for attempt := 0; attempt <= h.retry.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := h.backoffDuration(attempt, lastErr)
			slog.DebugContext(ctx, "retrying provider call",
				"provider", h.wrapped.Name(),
				"attempt", attempt+1,
				"backoff", backoff,
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-h.timer(backoff):
			}
		}

		resp, err := h.wrapped.ChatCompletion(ctx, req)
		if err == nil {
			h.Health.RecordSuccess()
			return resp, nil
		}

		lastErr = err
		if !isRetryable(err) {
			h.Health.RecordFailure(err)
			return nil, err
		}

		if attempt == h.retry.MaxRetries {
			break
		}
	}

	h.Health.RecordFailure(lastErr)
	return nil, lastErr
}

// ChatCompletionStream delegates to the wrapped provider with retry logic.
// Retries only occur before the first byte is sent (i.e., if the initial
// call to the wrapped provider returns an error).
func (h *HealthTrackingProvider) ChatCompletionStream(ctx context.Context, req *model.ChatCompletionRequest) (<-chan StreamEvent, error) {
	var lastErr error

	for attempt := 0; attempt <= h.retry.MaxRetries; attempt++ {
		if attempt > 0 {
			backoff := h.backoffDuration(attempt, lastErr)
			slog.DebugContext(ctx, "retrying provider stream call",
				"provider", h.wrapped.Name(),
				"attempt", attempt+1,
				"backoff", backoff,
			)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-h.timer(backoff):
			}
		}

		ch, err := h.wrapped.ChatCompletionStream(ctx, req)
		if err == nil {
			return h.trackStreamHealth(ctx, ch), nil
		}

		lastErr = err
		if !isRetryable(err) {
			h.Health.RecordFailure(err)
			return nil, err
		}

		if attempt == h.retry.MaxRetries {
			break
		}
	}

	h.Health.RecordFailure(lastErr)
	return nil, lastErr
}

// trackStreamHealth wraps a stream channel to record success/failure based
// on whether the stream completes without error.
func (h *HealthTrackingProvider) trackStreamHealth(ctx context.Context, in <-chan StreamEvent) <-chan StreamEvent {
	out := make(chan StreamEvent, cap(in))
	go func() {
		defer close(out)
		sawError := false
		var lastStreamErr error
		for evt := range in {
			if evt.Err != nil {
				sawError = true
				lastStreamErr = evt.Err
			}
			select {
			case out <- evt:
			case <-ctx.Done():
				// Context cancellation (e.g. client disconnect) is not a
				// provider fault, so we intentionally skip recording health.
				return
			}
		}
		if sawError {
			h.Health.RecordFailure(lastStreamErr)
		} else {
			h.Health.RecordSuccess()
		}
	}()
	return out
}

// backoffDuration calculates the backoff for the given attempt, respecting
// Retry-After headers from 429 responses. For computed backoffs it applies
// random jitter in [0.5, 1.0) of the base duration to avoid thundering herd.
func (h *HealthTrackingProvider) backoffDuration(attempt int, err error) time.Duration {
	if pe, ok := asProviderError(err); ok && pe.StatusCode == http.StatusTooManyRequests {
		if ra := pe.RetryAfter; ra > 0 {
			return ra
		}
	}

	base := h.retry.RetryBackoff
	if base <= 0 {
		base = time.Second
	}
	// Exponential backoff: base * 2^(attempt-1), capped at maxBackoff.
	multiplier := math.Pow(2, float64(attempt-1))
	d := time.Duration(float64(base) * multiplier)
	if d > maxBackoff {
		d = maxBackoff
	}
	// Apply jitter: uniform random in [0.5*d, d).
	jitter := 0.5 + h.rand()*0.5
	return time.Duration(float64(d) * jitter)
}

// isRetryable returns true if the error represents a retryable condition
// (429 or 5xx).
func isRetryable(err error) bool {
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		return pe.Retryable
	}
	return false
}

func asProviderError(err error) (*model.ProviderError, bool) {
	var pe *model.ProviderError
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// Ensure HealthTrackingProvider satisfies Provider at compile time.
var _ Provider = (*HealthTrackingProvider)(nil)
