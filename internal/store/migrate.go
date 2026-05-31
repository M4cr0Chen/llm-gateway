package store

import (
	"errors"
	"fmt"
	"log/slog"

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

	m, err := migrate.NewWithSourceInstance("iofs", src, dsnForMigrate(dsn))
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

// dsnForMigrate prefixes the DSN with the pgx/v5 scheme expected by
// the golang-migrate driver. Standard "postgres://" URIs are accepted
// as-is by re-prefixing with "pgx5://".
func dsnForMigrate(dsn string) string {
	// pgxmigrate registers itself under the "pgx5" scheme.
	const scheme = "pgx5://"
	for _, prefix := range []string{"postgres://", "postgresql://"} {
		if len(dsn) > len(prefix) && dsn[:len(prefix)] == prefix {
			return scheme + dsn[len(prefix):]
		}
	}
	if len(dsn) > len(scheme) && dsn[:len(scheme)] == scheme {
		return dsn
	}
	return scheme + dsn
}

// Ensure pgx5 driver is registered.
var _ = pgxmigrate.Postgres{}
