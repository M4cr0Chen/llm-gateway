package provider

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProviderHealth_Defaults(t *testing.T) {
	h := NewProviderHealth(HealthConfig{})
	assert.True(t, h.IsHealthy())
	assert.Equal(t, 5, h.failureThreshold)
	assert.Equal(t, 30*time.Second, h.cooldownPeriod)
}

func TestNewProviderHealth_CustomConfig(t *testing.T) {
	h := NewProviderHealth(HealthConfig{
		FailureThreshold: 3,
		CooldownPeriod:   10 * time.Second,
	})
	assert.Equal(t, 3, h.failureThreshold)
	assert.Equal(t, 10*time.Second, h.cooldownPeriod)
}

func TestRecordSuccess_ResetsFailures(t *testing.T) {
	h := NewProviderHealth(HealthConfig{FailureThreshold: 3})
	now := time.Now()
	h.now = func() time.Time { return now }

	// Record 2 failures (just below threshold).
	h.RecordFailure(errors.New("err1"))
	h.RecordFailure(errors.New("err2"))
	assert.True(t, h.IsHealthy())

	// A success resets the counter.
	h.RecordSuccess()
	assert.Equal(t, 0, h.consecutiveFails)
	assert.True(t, h.IsHealthy())

	s := h.Status()
	assert.Equal(t, now, s.LastSuccess)
}

func TestRecordFailure_MarksUnhealthy(t *testing.T) {
	h := NewProviderHealth(HealthConfig{FailureThreshold: 3, CooldownPeriod: time.Minute})
	now := time.Now()
	h.now = func() time.Time { return now }

	for i := 0; i < 3; i++ {
		h.RecordFailure(errors.New("boom"))
	}

	assert.False(t, h.IsHealthy())
	s := h.Status()
	assert.False(t, s.Healthy)
	assert.Equal(t, 3, s.ConsecutiveFails)
	assert.Equal(t, "boom", s.LastError)
	assert.Equal(t, now, s.LastFailure)
}

func TestCooldown_RestoresHealthy(t *testing.T) {
	h := NewProviderHealth(HealthConfig{FailureThreshold: 2, CooldownPeriod: 10 * time.Second})
	failTime := time.Now()
	h.now = func() time.Time { return failTime }

	h.RecordFailure(errors.New("err"))
	h.RecordFailure(errors.New("err"))
	assert.False(t, h.IsHealthy())

	// Advance past cooldown.
	h.now = func() time.Time { return failTime.Add(10 * time.Second) }
	assert.True(t, h.IsHealthy())

	s := h.Status()
	assert.True(t, s.Healthy)
	// ConsecutiveFails is still 2 — it resets on the next RecordSuccess.
	assert.Equal(t, 2, s.ConsecutiveFails)
}

func TestCooldown_NotEnoughTime(t *testing.T) {
	h := NewProviderHealth(HealthConfig{FailureThreshold: 1, CooldownPeriod: 30 * time.Second})
	failTime := time.Now()
	h.now = func() time.Time { return failTime }

	h.RecordFailure(errors.New("err"))
	assert.False(t, h.IsHealthy())

	// Only 15s elapsed — still unhealthy.
	h.now = func() time.Time { return failTime.Add(15 * time.Second) }
	assert.False(t, h.IsHealthy())
}

func TestSuccessAfterUnhealthy_RestoresHealth(t *testing.T) {
	h := NewProviderHealth(HealthConfig{FailureThreshold: 1, CooldownPeriod: time.Minute})
	h.RecordFailure(errors.New("err"))
	assert.False(t, h.IsHealthy())

	// A manual success call (e.g., after cooldown retry) restores health.
	h.RecordSuccess()
	assert.True(t, h.IsHealthy())
	assert.Equal(t, 0, h.consecutiveFails)
}

func TestConcurrentAccess(t *testing.T) {
	h := NewProviderHealth(HealthConfig{FailureThreshold: 100})

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			h.RecordFailure(errors.New("err"))
		}()
		go func() {
			defer wg.Done()
			h.RecordSuccess()
		}()
	}

	wg.Add(50)
	for i := 0; i < 50; i++ {
		go func() {
			defer wg.Done()
			_ = h.IsHealthy()
			_ = h.Status()
		}()
	}

	wg.Wait()
	// No panics or data races — pass if we get here.
	require.NotNil(t, h)
}
