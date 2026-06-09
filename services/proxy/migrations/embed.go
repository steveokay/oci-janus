// Package proxymigrations embeds the SQL migration files for registry-proxy.
package proxymigrations

import "embed"

//go:embed *.sql
var FS embed.FS
