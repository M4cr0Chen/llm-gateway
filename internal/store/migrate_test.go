package store

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDSNForMigrate(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"postgres scheme", "postgres://user:pw@host/db?sslmode=disable", "pgx5://user:pw@host/db?sslmode=disable", false},
		{"postgresql scheme", "postgresql://user:pw@host/db", "pgx5://user:pw@host/db", false},
		{"already pgx5", "pgx5://user:pw@host/db", "pgx5://user:pw@host/db", false},
		{"libpq key=value rejected", "host=localhost port=5432 dbname=foo user=bar", "", true},
		{"unrecognised rejected", "junk", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := dsnForMigrate(tc.in)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMapPgError_22P02ToNotFound(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "22P02", Message: "invalid input syntax for type uuid"}
	got := mapPgError(pgErr)
	assert.True(t, errors.Is(got, ErrNotFound), "22P02 should map to ErrNotFound")
}

func TestMapPgError_OtherCodeUnchanged(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "23505", Message: "duplicate key"}
	got := mapPgError(pgErr)
	assert.Equal(t, pgErr, got, "unrelated codes should pass through")
}

func TestMapPgError_NilSafe(t *testing.T) {
	assert.NoError(t, mapPgError(nil))
}
