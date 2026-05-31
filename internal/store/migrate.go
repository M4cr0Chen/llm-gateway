package store

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	pgxmigrate "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/M4cr0Chen/llm-gateway/migrations"
)

// RunMigrations applies any pending migrations from the embedded
// migrations FS using the given Postgres DSN. It is idempotent: a
// fully up-to-date database returns nil with no work done.
func RunMigrations(dsn string) error {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return fmt.Errorf("opening embedded migrations: %w", err)
	}

	migrateDSN, err := dsnForMigrate(dsn)
	if err != nil {
		return fmt.Errorf("normalising dsn: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, migrateDSN)
	if err != nil {
		return fmt.Errorf("creating migrate instance: %w", err)
	}
	defer func() {
		// Close releases the source and database handles. Failures here are
		// orthogonal to whether migrations applied, so we log rather than
		// fail the caller.
		if srcErr, dbErr := m.Close(); srcErr != nil || dbErr != nil {
			slog.Default().Warn("releasing migrate handles", "source_err", srcErr, "database_err", dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

// dsnForMigrate normalises the DSN into the URI form expected by the
// pgx5 golang-migrate driver. URL-form DSNs (postgres:// or postgresql://)
// are re-prefixed; libpq key=value DSNs (e.g. "host=localhost dbname=foo")
// are rejected with a clear message because the migrate driver does not
// accept them.
func dsnForMigrate(dsn string) (string, error) {
	const scheme = "pgx5://"
	switch {
	case strings.HasPrefix(dsn, scheme):
		return dsn, nil
	case strings.HasPrefix(dsn, "postgres://"):
		return scheme + strings.TrimPrefix(dsn, "postgres://"), nil
	case strings.HasPrefix(dsn, "postgresql://"):
		return scheme + strings.TrimPrefix(dsn, "postgresql://"), nil
	}
	// A libpq DSN has space-separated key=value pairs and no "://". Detect
	// the shape so the operator gets an actionable error instead of an
	// opaque migrate failure.
	if strings.Contains(dsn, "=") && !strings.Contains(dsn, "://") {
		return "", fmt.Errorf("libpq key=value DSNs are not supported for migrations; use postgres:// URL form")
	}
	return "", fmt.Errorf("unrecognised DSN format; expected postgres:// URL")
}

// Ensure pgx5 driver is registered.
var _ = pgxmigrate.Postgres{}
