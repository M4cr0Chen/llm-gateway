package store

import "errors"

// ErrNotFound is returned when a lookup matches no rows.
var ErrNotFound = errors.New("not found")
