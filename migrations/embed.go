// Package migrations bundles the SQL migration files into the binary so
// they can be applied at startup without shipping a separate directory.
package migrations

import "embed"

// FS exposes every *.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
