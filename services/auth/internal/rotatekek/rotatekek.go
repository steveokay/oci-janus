// Package rotatekek implements registry-auth's `rotate-kek` subcommand.
// It re-encrypts oauth_client_secret_enc on the current global_sso_config table
// and, when present, the legacy auth_providers table (RED-FU-015). auth's DSN
// comes from AUTH_DB_DSN. global_sso_config has a TEXT primary key
// (provider_id) — handled uniformly by the sweep's ::text PK casting.
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

// specs returns the table sweep plan for auth's KEK rotation. The current
// global_sso_config table is always swept; the legacy auth_providers table is
// marked Optional so the sweep skips it cleanly when the deployment never had
// it.
func specs() []rekey.TableSpec {
	return []rekey.TableSpec{
		{
			// Current SSO config. TEXT primary key (provider_id) — the sweep
			// casts PKs to ::text uniformly, so a TEXT PK needs no special case.
			Table:         "global_sso_config",
			PKColumn:      "provider_id",
			VersionColumn: "kek_version",
			Columns: []rekey.CipherColumn{
				{Name: "oauth_client_secret_enc", Encoding: rekey.EncodingBytea},
			},
		},
		{
			// Legacy per-tenant providers table with a UUID primary key.
			Table:         "auth_providers",
			PKColumn:      "id",
			VersionColumn: "kek_version",
			Columns: []rekey.CipherColumn{
				{Name: "oauth_client_secret_enc", Encoding: rekey.EncodingBytea},
			},
			Optional: true, // legacy — skip cleanly if the table is absent
		},
	}
}

// Run is the subcommand entry point. args is os.Args[2:]. DSN comes from
// AUTH_DB_DSN. It delegates the actual sweep/verify logic to the shared
// rekey.RunCLI helper.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	return rekey.RunCLI(ctx, args, "AUTH_DB_DSN", specs(), stdout)
}
