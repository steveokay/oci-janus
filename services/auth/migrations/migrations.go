// Package migrations embeds the SQL migration files for registry-auth so the
// binary contains them at compile time and goose can run them without a
// filesystem dependency on the working directory.
package migrations

import "embed"

// FS contains all .sql migration files in this directory.
//
//go:embed *.sql
var FS embed.FS
