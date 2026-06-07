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
