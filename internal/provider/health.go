package provider

import (
	"sync"
	"time"
)

// HealthConfig holds configuration for provider health tracking.
type HealthConfig struct {
	FailureThreshold int           `koanf:"failure_threshold"`
	CooldownPeriod   time.Duration `koanf:"cooldown_period"`
}

// HealthStatus is a point-in-time snapshot of a provider's health state.
type HealthStatus struct {
	Healthy          bool      `json:"healthy"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	LastSuccess      time.Time `json:"last_success,omitempty"`
	LastFailure      time.Time `json:"last_failure,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
}

// ProviderHealth tracks the health of a single provider.
// It is safe for concurrent use.
type ProviderHealth struct {
	mu               sync.RWMutex
	consecutiveFails int
	lastSuccess      time.Time
	lastFailure      time.Time
	lastError        string
	healthy          bool

	failureThreshold int
	cooldownPeriod   time.Duration
	now              func() time.Time // for testing
}

// NewProviderHealth creates a new ProviderHealth with the given configuration.
func NewProviderHealth(cfg HealthConfig) *ProviderHealth {
	threshold := cfg.FailureThreshold
	if threshold <= 0 {
		threshold = 5
	}
	cooldown := cfg.CooldownPeriod
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &ProviderHealth{
		healthy:          true,
		failureThreshold: threshold,
		cooldownPeriod:   cooldown,
		now:              time.Now,
	}
}

// RecordSuccess records a successful call and resets consecutive failures.
func (h *ProviderHealth) RecordSuccess() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.consecutiveFails = 0
	h.lastSuccess = h.now()
	h.healthy = true
}

// RecordFailure records a failed call. If consecutive failures reach the
// threshold, the provider is marked unhealthy.
func (h *ProviderHealth) RecordFailure(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.consecutiveFails++
	h.lastFailure = h.now()
	if err != nil {
		h.lastError = err.Error()
	}
	if h.consecutiveFails >= h.failureThreshold {
		h.healthy = false
	}
}

// IsHealthy returns whether the provider is currently considered healthy.
// An unhealthy provider becomes healthy again after the cooldown period
// elapses since the last failure, allowing a retry.
func (h *ProviderHealth) IsHealthy() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if h.healthy {
		return true
	}
	// Allow retry after cooldown period.
	return h.now().Sub(h.lastFailure) >= h.cooldownPeriod
}

// Status returns a point-in-time snapshot of the provider's health.
func (h *ProviderHealth) Status() HealthStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()

	healthy := h.healthy
	if !healthy && h.now().Sub(h.lastFailure) >= h.cooldownPeriod {
		healthy = true
	}

	return HealthStatus{
		Healthy:          healthy,
		ConsecutiveFails: h.consecutiveFails,
		LastSuccess:      h.lastSuccess,
		LastFailure:      h.lastFailure,
		LastError:        h.lastError,
	}
}
