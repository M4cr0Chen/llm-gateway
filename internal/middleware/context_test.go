package middleware

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRequestInfoFromContext_AbsentReturnsNil(t *testing.T) {
	assert.Nil(t, RequestInfoFromContext(context.Background()))
}

func TestSetters_NoHolder_DoNotPanic(t *testing.T) {
	ctx := context.Background()
	// All setters should be silent no-ops when no RequestInfo is attached
	// (e.g., admin routes that don't run under RequestLogger).
	assert.NotPanics(t, func() {
		SetModel(ctx, "gpt-4o")
		SetProvider(ctx, "openai")
		SetTokens(ctx, 10, 20)
		MarkCached(ctx)
		MarkRateLimited(ctx)
	})
}

func TestSetters_MutateAttachedHolder(t *testing.T) {
	info := &RequestInfo{}
	ctx := WithRequestInfo(context.Background(), info)

	SetModel(ctx, "gpt-4o")
	SetProvider(ctx, "openai")
	SetTokens(ctx, 12, 34)
	MarkCached(ctx)
	MarkRateLimited(ctx)

	assert.Equal(t, "gpt-4o", info.Model)
	assert.Equal(t, "openai", info.Provider)
	assert.Equal(t, 12, info.PromptTokens)
	assert.Equal(t, 34, info.CompletionTokens)
	assert.True(t, info.Cached)
	assert.True(t, info.RateLimited)
}

func TestWithRequestInfo_RoundTrip(t *testing.T) {
	want := &RequestInfo{OrgID: "org-1", KeyID: "key-1"}
	ctx := WithRequestInfo(context.Background(), want)
	got := RequestInfoFromContext(ctx)
	assert.Same(t, want, got, "RequestInfoFromContext must return the same pointer")
}
