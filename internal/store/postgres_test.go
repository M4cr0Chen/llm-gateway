package store_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/M4cr0Chen/llm-gateway/internal/auth"
	"github.com/M4cr0Chen/llm-gateway/internal/store"
)

// startPostgres spins up a Postgres container and returns the DSN. If
// Docker is not reachable the test is skipped (testcontainers errors out
// rather than hanging).
func startPostgres(t *testing.T) string {
	t.Helper()

	if os.Getenv("SKIP_DOCKER_TESTS") == "1" {
		t.Skip("SKIP_DOCKER_TESTS=1")
	}

	ctx := context.Background()
	pg, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("llm_gateway"),
		postgres.WithUsername("test"),
		postgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Skipf("docker not available, skipping store integration test: %v", err)
	}
	t.Cleanup(func() {
		if err := pg.Terminate(ctx); err != nil {
			t.Logf("terminating container: %v", err)
		}
	})

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	return dsn
}

func TestAPIKeyStore_CreateGetRevoke(t *testing.T) {
	dsn := startPostgres(t)

	require.NoError(t, store.RunMigrations(dsn))

	ctx := context.Background()
	pool, err := store.NewPool(ctx, dsn, 4)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Seed an organisation directly — there is no admin endpoint for orgs in 3.1.
	var orgID string
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO organizations (name) VALUES ($1) RETURNING id",
		"test-org",
	).Scan(&orgID))

	keys := store.NewAPIKeyStore(pool)

	plaintext := "sk-gw-AbCdEfGhIjKlMnOpQrStUvWxYz012345"
	hashHex := auth.HashKey(plaintext)
	prefix := plaintext[:8]

	created, err := keys.Create(ctx, &store.NewKey{
		OrgID: orgID, Name: "k1", RateLimitRPM: 60, RateLimitTPM: 100000,
	}, hashHex, prefix)
	require.NoError(t, err)
	assert.NotEmpty(t, created.ID)
	assert.Equal(t, hashHex, created.KeyHash)
	assert.Equal(t, prefix, created.KeyPrefix)
	assert.True(t, created.IsActive)

	got, err := keys.GetByHash(ctx, hashHex)
	require.NoError(t, err)
	assert.Equal(t, created.ID, got.ID)
	assert.Equal(t, orgID, got.OrgID)
	assert.Equal(t, 60, got.RateLimitRPM)

	require.NoError(t, keys.TouchLastUsedAt(ctx, created.ID))

	// Revoke should make GetByHash return ErrNotFound (revoked keys are filtered at the SQL level).
	require.NoError(t, keys.Revoke(ctx, created.ID))

	_, err = keys.GetByHash(ctx, hashHex)
	assert.True(t, errors.Is(err, store.ErrNotFound), "expected ErrNotFound after revoke, got %v", err)

	// GetByID should still return the revoked row.
	gotByID, err := keys.GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.False(t, gotByID.IsActive, "row should still exist but be inactive")
}

func TestAPIKeyStore_GetByHash_ExpiredKey(t *testing.T) {
	dsn := startPostgres(t)
	require.NoError(t, store.RunMigrations(dsn))

	ctx := context.Background()
	pool, err := store.NewPool(ctx, dsn, 4)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	var orgID string
	require.NoError(t, pool.QueryRow(ctx,
		"INSERT INTO organizations (name) VALUES ('exp-org') RETURNING id",
	).Scan(&orgID))

	keys := store.NewAPIKeyStore(pool)
	past := time.Now().Add(-1 * time.Hour)

	plaintext := "sk-gw-ExpiredKeyXXXXXXXXXXXXXXXXXXXX1"
	hashHex := auth.HashKey(plaintext)
	_, err = keys.Create(ctx, &store.NewKey{
		OrgID: orgID, Name: "expired", RateLimitRPM: 60, RateLimitTPM: 100000,
		ExpiresAt: &past,
	}, hashHex, plaintext[:8])
	require.NoError(t, err)

	_, err = keys.GetByHash(ctx, hashHex)
	assert.True(t, errors.Is(err, store.ErrNotFound), "expired keys should look like missing rows")
}
