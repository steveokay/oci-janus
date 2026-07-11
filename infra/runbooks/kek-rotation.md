# Runbook — KEK Rotation (RED-FU-015)

Rotate a per-service key-encryption key (KEK) that protects secrets at rest.
Four services own independent KEKs; rotate each one separately. There is **no
single master KEK**. Two services (registry-auth, registry-audit) own **more than
one KEK domain** — each domain has its own key material and its own `rotate-kek`
selector flag, and rotates in a separate run.

| Service | Secrets protected | DSN env | KEK env (runtime) | `rotate-kek` flag |
|---|---|---|---|---|
| registry-auth | SSO OAuth client secrets (`oauth_client_secret_enc`) | `AUTH_DB_DSN` | `SSO_CREDENTIAL_KEY_HEX` | *(none)* |
| registry-auth | TOTP MFA secrets (`users.mfa_secret_enc`) | `AUTH_DB_DSN` | `MFA_SECRET_KEY_HEX` | `--mfa` |
| registry-proxy | upstream registry passwords | `DB_DSN` | `CREDENTIAL_KEY_HEX` | *(none)* |
| registry-webhook | webhook HMAC keys | `DB_DSN` | `CREDENTIAL_KEY_HEX` | *(none)* |
| registry-audit | export HMAC secret + bearer token (`audit_export_configs`) | `DB_DSN` | `AUDIT_EXPORT_SECRETS_KEY_HEX` | *(none)* |
| registry-audit | webhook notification-channel secret (`notification_webhook_config.secret_enc`, FUT-019) | `DB_DSN` | `NOTIFY_WEBHOOK_KEY_HEX` | `--notify-webhook` |
| registry-audit | email notification-channel secrets (`email_transport_config.resend_api_key_enc` / `smtp_password_enc`, FUT-019) | `DB_DSN` | `NOTIFY_EMAIL_KEY_HEX` | `--notify-email` |

> `CREDENTIAL_KEY_HEX` is the *same variable name* in proxy and webhook but a
> **different value per deployment**. Rotating one does not affect the other.

> **registry-auth has TWO independent KEK domains.** Its SSO credentials and its
> TOTP MFA secrets are encrypted under *different* keys
> (`SSO_CREDENTIAL_KEY_HEX` vs `MFA_SECRET_KEY_HEX`) and are rotated by
> **separate invocations**. A single `rotate-kek` run applies one
> `KEK_OLD_HEX`/`KEK_NEW_HEX` pair to whichever domain is selected, so the two
> cannot be combined (decrypting an MFA secret with the SSO key is a GCM auth
> failure). To rotate the MFA KEK, add the `--mfa` flag to every command below
> and set `KEK_OLD_HEX`/`KEK_NEW_HEX` to the old/new **MFA** key material — the
> sweep then targets `users.mfa_secret_enc` / `users.mfa_secret_kek_version`.
> Example: `KEK_OLD_HEX=<old-mfa> KEK_NEW_HEX=<new-mfa> registry-auth rotate-kek --mfa`.
> Unenrolled users have a NULL `mfa_secret_enc` and are skipped automatically.

> **registry-audit has THREE independent KEK domains** (FUT-019 added two). The
> default run rotates the audit-export secrets (`audit_export_configs`) under the
> `AUDIT_EXPORT_SECRETS_KEY_HEX` key material. Add `--notify-webhook` to rotate
> the webhook notification-channel secret (`notification_webhook_config.secret_enc`,
> key material `NOTIFY_WEBHOOK_KEY_HEX`) or `--notify-email` to rotate the email
> notification-channel secrets (`email_transport_config.resend_api_key_enc` and
> `.smtp_password_enc`, key material `NOTIFY_EMAIL_KEY_HEX`). As with `--mfa`, set
> `KEK_OLD_HEX`/`KEK_NEW_HEX` to the **selected domain's** old/new key. The two
> selectors are mutually exclusive — combining them is rejected. Tenants that have
> never configured a channel (NULL secret) are skipped automatically, and an email
> row rotates only its populated provider column (resend *or* smtp), leaving the
> unused NULL column untouched.
> Example: `KEK_OLD_HEX=<old-wh> KEK_NEW_HEX=<new-wh> registry-audit rotate-kek --notify-webhook`.

The `rotate-kek` subcommand reads the OLD and NEW keys from `KEK_OLD_HEX` /
`KEK_NEW_HEX` (never flags — avoids shell-history leakage). Keys are 32-byte
AES-256 keys, hex-encoded (64 hex chars).

## 1. Mint a new key

```bash
<service> rotate-kek --generate
# prints one 64-char hex string on STDOUT — store it in your secrets manager
```

> **Handling the key safely (SEC-072):** the key is the only thing on
> **stdout** so `--generate` is pipeline-friendly (e.g. pipe straight into a
> secrets-manager CLI). A warning is printed to **stderr**, not stdout, so it
> cannot corrupt that pipe. Do **not** run `--generate` in a job whose stdout
> is captured to CI logs or a shared console — treat the output like any raw
> secret. If in doubt, redirect to a file with restrictive permissions and
> load it into your secrets manager from there.

## 2. Pre-flight (no mutation)

```bash
KEK_OLD_HEX=<old> KEK_NEW_HEX=<new> <service> rotate-kek --dry-run
# "would rotate N row(s) to kek_version V"
```

## 3. Stop the service

Rotation runs in a brief maintenance window (tables hold tens of rows — seconds).
Stopping the service ensures no writer encrypts a fresh row under the OLD key
after the sweep has passed it.

## 4. Rotate

```bash
KEK_OLD_HEX=<old> KEK_NEW_HEX=<new> <service> rotate-kek
# "rotated N row(s) to kek_version V"
```

Rotation is **per-table all-or-nothing**: if any cell fails to decrypt under the
OLD key, the transaction rolls back and the tool exits non-zero with the
offending primary key. Nothing is left half-rotated.

**Re-running is safe.** Rotation is idempotent: rows that already decrypt under
the NEW key (from a previous run) are skipped, not re-encrypted. So if a
multi-table service (e.g. auth) commits one table and then hits a transient
failure on the next, just re-run the same command — it resumes, rotating only
the tables still on the OLD key and reporting `rotated 0 rows` once everything
is done.

## 5. Verify

```bash
KEK_NEW_HEX=<new> <service> rotate-kek --verify
# "verify: 0 row(s) still on the old key"   → exit 0
# "verify: N row(s) still on the old key"   → exit 3
```

Verify trial-decrypts every row under the NEW key — the authoritative "done"
check. Exit 3 means rows remain; investigate before restarting.

## 6. Restart with the NEW key

Set the service's runtime KEK env var (see table) to the NEW key and start the
service. Confirm secrets decrypt (e.g. an SSO login for auth, an upstream pull
for proxy, a webhook delivery for webhook, an audit export for audit).

## Exit codes

| Code | Meaning |
|---|---|
| 0 | success (or verify: all rows on the new key) |
| 1 | infrastructure/internal error (DB unreachable, etc.) |
| 2 | operator input error (bad flags, missing/invalid `KEK_*_HEX`) |
| 3 | verify only: rows still on the old key |

## Ordering & rollback

- Rotate services in any order — each is independent.
- **Rollback:** if step 6 shows secrets failing to decrypt, restart with the OLD
  key still set and re-run `rotate-kek` with OLD and NEW swapped to reverse the
  sweep (the tool is symmetric).
- Zero-downtime dual-key rotation is a documented future upgrade path
  (`docs/superpowers/specs/2026-07-03-kek-rotation-design.md` §9); the
  `kek_version` column and `rekey` helper are forward-compatible with it.
