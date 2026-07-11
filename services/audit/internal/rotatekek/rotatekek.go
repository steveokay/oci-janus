// Package rotatekek implements registry-audit's `rotate-kek` subcommand.
//
// registry-audit holds THREE independent KEK domains, each with its own key
// material, so they are rotated by SEPARATE invocations (never one sweep). The
// shared rekey CLI (libs/crypto/rekey) applies a SINGLE KEK_OLD_HEX/KEK_NEW_HEX
// pair to every spec in one run, so decrypting one domain's ciphertext with
// another domain's key is a GCM authentication failure. Whichever domain is
// selected, KEK_OLD_HEX/KEK_NEW_HEX must carry THAT domain's old/new key.
//
//   - default (no flag):   the audit-export KEK (runtime env
//     AUDIT_EXPORT_SECRETS_KEY_HEX) — rotates audit_export_configs.hmac_secret
//     and .bearer_token (both on one spec, one transaction).
//   - `--notify-webhook`:  the webhook-notification-channel KEK (runtime env
//     NOTIFY_WEBHOOK_KEY_HEX, FUT-019) — rotates
//     notification_webhook_config.secret_enc.
//   - `--notify-email`:    the email-notification-channel KEK (runtime env
//     NOTIFY_EMAIL_KEY_HEX, FUT-019) — rotates email_transport_config's two
//     provider-secret columns (resend_api_key_enc, smtp_password_enc).
//
// All three domains share the audit DSN (DB_DSN); only the key material and the
// swept tables differ. This closes the RED-FU-015 follow-up "notification-channel
// secret sweep gap": before this change rotate-kek only swept audit_export_configs,
// so the FUT-019 email/webhook channel secrets were effectively un-rotatable.
package rotatekek

import (
	"context"
	"io"

	"github.com/steveokay/oci-janus/libs/crypto/rekey"
)

// specs declares the default audit-export domain: the single audit_export_configs
// table with its two BYTEA cipher columns. Both columns share one TableSpec so the
// sweep re-encrypts them within the table's single transaction and bumps
// kek_version once.
//
// NOTE: the FUT-019 notification-channel secrets are intentionally NOT listed
// here. They are sealed under SEPARATE KEKs (NOTIFY_WEBHOOK_KEY_HEX /
// NOTIFY_EMAIL_KEY_HEX), and the shared rekey sweep applies a single
// KEK_OLD_HEX/KEK_NEW_HEX pair to every spec — folding them in would attempt to
// decrypt a channel secret with the audit-export KEK and fail with a GCM
// authentication error. They live in webhookSpecs()/emailSpecs(), reached via the
// `--notify-webhook` / `--notify-email` flags as independent invocations.
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

// webhookSpecs returns the sweep plan for the webhook-notification-channel KEK
// domain (NOTIFY_WEBHOOK_KEY_HEX, FUT-019). It is DELIBERATELY separate from
// specs() — see the NOTE there — because the channel secret uses a different KEK
// than the audit-export secrets. Selected by the `--notify-webhook` flag; the
// operator runs it with the OLD/NEW webhook-channel key material in
// KEK_OLD_HEX/KEK_NEW_HEX. The table's primary key is tenant_id (one config row
// per tenant); rows whose secret has never been set have a NULL secret_enc and
// are skipped automatically (the sweep's candidate query filters NULLs).
func webhookSpecs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "notification_webhook_config",
		PKColumn:      "tenant_id",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			{Name: "secret_enc", Encoding: rekey.EncodingBytea},
		},
	}}
}

// emailSpecs returns the sweep plan for the email-notification-channel KEK
// domain (NOTIFY_EMAIL_KEY_HEX, FUT-019). Separate from specs() for the same
// KEK-isolation reason. Selected by the `--notify-email` flag.
//
// email_transport_config carries TWO provider-secret columns — resend_api_key_enc
// (provider='resend') and smtp_password_enc (provider='smtp'). They are MUTUALLY
// EXCLUSIVE per row: exactly one is non-NULL depending on the configured provider.
// Declaring both on ONE TableSpec is correct: the sweep's candidate query selects
// rows where `resend_api_key_enc IS NOT NULL OR smtp_password_enc IS NOT NULL`, and
// rotateTable/applyUpdate skip NULL cells per-column — so the provider's live
// secret rotates while the NULL sibling column is left untouched.
func emailSpecs() []rekey.TableSpec {
	return []rekey.TableSpec{{
		Table:         "email_transport_config",
		PKColumn:      "tenant_id",
		VersionColumn: "kek_version",
		Columns: []rekey.CipherColumn{
			{Name: "resend_api_key_enc", Encoding: rekey.EncodingBytea},
			{Name: "smtp_password_enc", Encoding: rekey.EncodingBytea},
		},
	}}
}

// Run is the subcommand entry point. args is os.Args[2:]. DSN comes from DB_DSN.
// It delegates the actual sweep/verify logic to the shared rekey.RunCLI helper.
//
// A `--notify-webhook` or `--notify-email` selector (either spelling `-x`/`--x`)
// picks the corresponding FUT-019 channel-secret KEK domain instead of the default
// audit-export domain. The selector is consumed here — the shared RunCLI FlagSet
// does not know it — and the remaining args (e.g. --verify, --dry-run,
// --to-version) are forwarded unchanged. The two selectors are mutually exclusive:
// each KEK domain uses its own key material and rotates in a separate run, so
// combining them is rejected rather than silently applying one KEK to both.
func Run(ctx context.Context, args []string, stdout io.Writer) error {
	// domain == "" means the default audit-export domain.
	domain := ""
	rest := make([]string, 0, len(args))
	for _, a := range args {
		var d string
		switch a {
		case "--notify-webhook", "-notify-webhook":
			d = "--notify-webhook"
		case "--notify-email", "-notify-email":
			d = "--notify-email"
		default:
			rest = append(rest, a)
			continue
		}
		// Repeating the same selector is harmless; mixing two is a hard error.
		// Use a rekey.ValidationError (not a plain fmt.Errorf) so the audit
		// main.go dispatch maps this operator-input mistake to exit code 2,
		// matching the sibling --dry-run/--verify conflict check in RunCLI.
		if domain != "" && domain != d {
			return rekey.NewValidationError(
				"rotate-kek: %s and %s are mutually exclusive — each KEK domain uses its own key material and must rotate in a separate run",
				domain, d)
		}
		domain = d
	}

	sel := specs()
	switch domain {
	case "--notify-webhook":
		sel = webhookSpecs()
	case "--notify-email":
		sel = emailSpecs()
	}
	return rekey.RunCLI(ctx, rest, "DB_DSN", sel, stdout)
}
