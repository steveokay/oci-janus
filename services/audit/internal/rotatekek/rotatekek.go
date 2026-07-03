// Package rotatekek implements registry-audit's `rotate-kek` subcommand.
// audit_export_configs carries two KEK-encrypted BYTEA columns (hmac_secret,
// bearer_token); both are declared on one TableSpec so they rotate together in
// the table's single transaction (RED-FU-015).
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

// specs declares the single audit_export_configs table with its two BYTEA
// cipher columns. Both columns share one TableSpec so the sweep re-encrypts
// them within the table's single transaction and bumps kek_version once.
func specs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "audit_export_configs",
		PKColumn:      "id",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			{Name: "hmac_secret", Encoding: rekey.EncodingBytea},
			{Name: "bearer_token", Encoding: rekey.EncodingBytea},
		},
	}}
}

// Run is the subcommand entry point. args is os.Args[2:]. DSN comes from DB_DSN.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return rekey.RunCLI(ctx, args, "DB_DSN", specs(), stdout)
}
