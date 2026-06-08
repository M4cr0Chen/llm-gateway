package strategy

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuild_AllKnownStrategies(t *testing.T) {
	tests := []string{"cost_optimized", "latency_optimized", "round_robin", "weighted", "priority"}
	for _, name := range tests {
		t.Run(name, func(t *testing.T) {
			s, err := Build(name)
			require.NoError(t, err)
			require.NotNil(t, s)
			assert.Equal(t, name, s.Name())
			// LatencyOptimized spawns a poller goroutine; stop it so the
			// test does not leak a ticker.
			if stopper, ok := s.(interface{ Stop() }); ok {
				stopper.Stop()
			}
		})
	}
}

func TestBuild_UnknownStrategyReturnsError(t *testing.T) {
	_, err := Build("magic-quadrant")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "magic-quadrant")
}

func TestBuilder_MemoizesByName(t *testing.T) {
	// Two Build calls for the same name must return the SAME instance —
	// that's the whole point of Builder: multiple groups picking
	// "latency_optimized" share one poller goroutine and one EWMA map
	// instead of leaking N goroutines per process.
	b := NewBuilder()
	for _, name := range []string{"cost_optimized", "latency_optimized", "round_robin", "weighted", "priority"} {
		t.Run(name, func(t *testing.T) {
			first, err := b.Build(name)
			require.NoError(t, err)
			second, err := b.Build(name)
			require.NoError(t, err)
			assert.Same(t, first, second, "Builder.Build must return memoized instance for %q", name)
		})
	}
	// Stop the latency poller so the goroutine doesn't leak past the test.
	if s, err := b.Build("latency_optimized"); err == nil {
		if stopper, ok := s.(interface{ Stop() }); ok {
			stopper.Stop()
		}
	}
}

func TestBuilder_UnknownStrategyReturnsError(t *testing.T) {
	_, err := NewBuilder().Build("magic-quadrant")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "magic-quadrant")
}

func TestBuilder_FreshBuilderHasIsolatedState(t *testing.T) {
	// Two Builders must not share state — tests rely on this for
	// isolation. We use "round_robin" rather than "priority" because
	// Priority is an empty struct and Go's runtime returns the same
	// address for all zero-size allocations, which would defeat the
	// pointer comparison.
	a := NewBuilder()
	b := NewBuilder()
	sa, err := a.Build("round_robin")
	require.NoError(t, err)
	sb, err := b.Build("round_robin")
	require.NoError(t, err)
	assert.NotSame(t, sa, sb, "distinct Builders must produce distinct instances")
}
