package auth

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKeyInfoContext_RoundTrip(t *testing.T) {
	info := &KeyInfo{KeyID: "abc", OrgID: "org-1", Name: "test", RPM: 60, TPM: 100000}

	ctx := WithKeyInfo(context.Background(), info)
	got, ok := KeyInfoFromContext(ctx)

	assert.True(t, ok)
	assert.Equal(t, info, got)
}

func TestKeyInfoContext_Absent(t *testing.T) {
	got, ok := KeyInfoFromContext(context.Background())

	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestHashKey_Deterministic(t *testing.T) {
	h1 := HashKey("sk-gw-abc")
	h2 := HashKey("sk-gw-abc")
	h3 := HashKey("sk-gw-xyz")

	assert.Equal(t, h1, h2)
	assert.NotEqual(t, h1, h3)
	assert.Len(t, h1, 64) // sha256 hex is 64 chars
}
