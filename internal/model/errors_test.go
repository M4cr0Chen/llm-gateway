package model_test

import (
	"testing"
	"time"

	"github.com/M4cr0Chen/llm-gateway/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestParseRetryAfter(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   time.Duration
	}{
		{
			name:   "empty string",
			header: "",
			want:   0,
		},
		{
			name:   "integer seconds",
			header: "120",
			want:   120 * time.Second,
		},
		{
			name:   "zero seconds",
			header: "0",
			want:   0,
		},
		{
			name:   "RFC 1123 date in the future",
			header: time.Now().Add(10 * time.Second).UTC().Format(time.RFC1123),
			want:   10 * time.Second,
		},
		{
			name:   "RFC 1123 date in the past",
			header: time.Now().Add(-10 * time.Second).UTC().Format(time.RFC1123),
			want:   0,
		},
		{
			name:   "garbage input",
			header: "not-a-number-or-date",
			want:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := model.ParseRetryAfter(tt.header)
			if tt.want == 0 {
				assert.Equal(t, time.Duration(0), got)
			} else {
				// Allow 2s tolerance for time-based tests.
				assert.InDelta(t, float64(tt.want), float64(got), float64(2*time.Second),
					"got %v, want ~%v", got, tt.want)
			}
		})
	}
}
