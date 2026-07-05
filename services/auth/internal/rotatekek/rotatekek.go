// Package rotatekek implements registry-auth's `rotate-kek` subcommand.
// It re-encrypts oauth_client_secret_enc on the current global_sso_config table
// and, when present, the legacy auth_providers table (RED-FU-015). auth's DSN
// comes from AUTH_DB_DSN. global_sso_config has a TEXT primary key
// (provider_id) — handled uniformly by the sweep's ::text PK casting.
//
// registry-auth holds TWO independent KEK domains, each with its own key
// material, so they are rotated by SEPARATE invocations (never one sweep):
//
//   - default (no flag): the SSO-credential KEK (SSO_CREDENTIAL_KEY_HEX) —
//     rotates oauth_client_secret_enc (global_sso_config + legacy auth_providers).
//   - `--mfa`:           the MFA-secret KEK (MFA_SECRET_KEY_HEX) — rotates
//     users.mfa_secret_enc (TOTP shared secrets, Tier-1 #1).
//
// The shared rekey CLI (libs/crypto/rekey) applies a SINGLE KEK_OLD_HEX/
// KEK_NEW_HEX pair to every spec in one run, so the two domains cannot be
// combined: decrypting an MFA secret with the SSO KEK (or vice-versa) is a GCM
// authentication failure. Whichever domain is selected, KEK_OLD_HEX/KEK_NEW_HEX
// must carry that domain's old/new key material.
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

// specs returns the table sweep plan for auth's SSO-credential KEK rotation
// (SSO_CREDENTIAL_KEY_HEX). The current global_sso_config table is always
// swept; the legacy auth_providers table is marked Optional so the sweep skips
// it cleanly when the deployment never had it.
//
// NOTE: users.mfa_secret_enc is intentionally NOT listed here. TOTP MFA secrets
// are encrypted under a SEPARATE KEK (MFA_SECRET_KEY_HEX), and the shared rekey
// sweep applies a single KEK_OLD_HEX/KEK_NEW_HEX pair to every spec — so
// folding MFA into this list would attempt to decrypt MFA secrets with the SSO
// KEK and fail with a GCM authentication error. MFA rotation lives in
// mfaSpecs(), reached via the `--mfa` flag as an independent invocation with
// the MFA key material.
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

// mfaSpecs returns the sweep plan for the MFA-secret KEK domain
// (MFA_SECRET_KEY_HEX). It is DELIBERATELY separate from specs() — see the NOTE
// there — because MFA secrets use a different KEK than the SSO credentials.
// Selected by the `--mfa` flag; the operator runs it with the OLD/NEW MFA key
// material in KEK_OLD_HEX/KEK_NEW_HEX.
func mfaSpecs() []rekey.TableSpec {
	return []rekey.TableSpec{
		{
			// TOTP MFA shared secrets on the users table (Tier-1 #1). Rows
			// without MFA enrolled have a NULL mfa_secret_enc; the sweep's
			// candidate query filters `WHERE mfa_secret_enc IS NOT NULL`, so
			// NULL rows are skipped automatically (no per-column Optional flag
			// needed).
			Table:         "users",
			PKColumn:      "id",
			VersionColumn: "mfa_secret_kek_version",
			Columns: []rekey.CipherColumn{
				{Name: "mfa_secret_enc", Encoding: rekey.EncodingBytea},
			},
		},
	}
}

// Run is the subcommand entry point. args is os.Args[2:]. DSN comes from
// AUTH_DB_DSN. It delegates the actual sweep/verify logic to the shared
// rekey.RunCLI helper.
//
// A leading/anywhere `--mfa` (or `-mfa`) token selects the MFA-secret KEK
// domain (users.mfa_secret_enc) instead of the default SSO-credential domain.
// The flag is consumed here — the shared RunCLI FlagSet does not know it — and
// the remaining args (e.g. --verify, --dry-run, --to-version) are forwarded
// unchanged. All other rekey flags/env vars (KEK_OLD_HEX/KEK_NEW_HEX) behave
// identically; they simply carry the selected domain's key material.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	sel := specs()
	// Strip the auth-local --mfa selector before handing the rest to RunCLI.
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--mfa" || a == "-mfa" {
			sel = mfaSpecs()
			continue
		}
		rest = append(rest, a)
	}
	return rekey.RunCLI(ctx, rest, "AUTH_DB_DSN", sel, stdout)
}
