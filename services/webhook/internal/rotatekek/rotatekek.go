// Package rotatekek implements registry-webhook's `rotate-kek` subcommand.
// It re-encrypts webhook_endpoints.secret_enc from KEK_OLD_HEX to KEK_NEW_HEX
// (RED-FU-015). secret_enc is stored as hex-encoded TEXT (not BYTEA), so this
// column is declared with EncodingHexText — the sweep hex-decodes before
// decrypt and hex-encodes after encrypt.
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

// specs describes the single hex-TEXT cipher column re-encrypted by this
// subcommand. webhook_endpoints is keyed by id and versioned via kek_version.
func specs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "webhook_endpoints",
		PKColumn:      "id",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			// secret_enc is stored as hex-encoded TEXT, the one hex-TEXT
			// column on the platform — hence EncodingHexText.
			{Name: "secret_enc", Encoding: rekey.EncodingHexText},
		},
	}}
}

// Run is the subcommand entry point. args is os.Args[2:]. DSN comes from DB_DSN.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return rekey.RunCLI(ctx, args, "DB_DSN", specs(), stdout)
}
