package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a lookup matches no rows.
var ErrNotFound = errors.New("not found")

// mapPgError normalises a few Postgres errors that should look like
// "no such row" to callers. Right now that's just SQLSTATE 22P02
// (invalid_text_representation), which fires when a non-UUID string is
// bound to a UUID column. Letting callers treat it as ErrNotFound keeps
// admin endpoints idempotent even when given a malformed id.
func mapPgError(err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "22P02" {
		return ErrNotFound
	}
	return err
}
