// Package rotatekek implements registry-proxy's `rotate-kek` subcommand.
// It re-encrypts upstream_registries.password_enc from KEK_OLD_HEX to
// KEK_NEW_HEX (RED-FU-015). All table/column knowledge lives here; the sweep
// engine and CLI plumbing live in libs/crypto/rekey.
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

// specs declares the proxy schema's KEK-encrypted columns.
func specs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "upstream_registries",
		PKColumn:      "upstream_id",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			{Name: "password_enc", Encoding: rekey.EncodingBytea},
		},
	}}
}

// Run is the subcommand entry point. args is os.Args[2:]. The proxy DSN comes
// from DB_DSN.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return rekey.RunCLI(ctx, args, "DB_DSN", specs(), stdout)
}
