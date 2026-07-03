# RED-FU-015 — KEK Rotation Tool — Design Spec

> **Status:** design (approved decisions locked; pending spec review → plan)
> **Date:** 2026-07-03
> **Tracks:** RED-FU-015 (REDESIGN-001 Phase 6.4 follow-up, HIGH priority)
> **Posture:** single-tenant (`DEPLOYMENT_MODE=single`) is the supported deployment.

## 1. Problem

At-rest secrets are encrypted with per-service **key-encryption keys (KEKs)** — 32-byte AES-256 keys supplied via environment variables. Today there is **no supported way to rotate a KEK**: if a KEK is suspected compromised, an operator must hand-write SQL to decrypt-and-re-encrypt every affected row, or accept that the old key protects the data forever. Phase 6.4 (PR #203) added a versioned ciphertext prefix and deferred the rotation tooling itself — this is that tooling.

Rotation must: given an OLD key and a NEW key, re-encrypt every affected row so it decrypts under NEW, verify none remain on OLD, and let the operator cut the service over to NEW — safely, atomically, and observably.

## 2. Corrected scope (three backlog errors)

The `futures.md` RED-FU-015 entry contained three factual errors, confirmed against the code during scoping. This spec supersedes them:

1. **The ciphertext version byte is a *layout* marker, not a KEK identifier.** `libs/crypto/aes/aes.go` writes `0x01 || nonce || ct || tag`; the `0x01` encodes *how the bytes are framed*, not *which key* encrypted them. You cannot tell from a ciphertext which KEK produced it. Re-encrypted rows stay `0x01`. Therefore completion detection cannot rely on the version byte — it needs either a tracking column or trial-decryption (see §5).
2. **There is no single "master KEK."** There are **four independent KEK env vars across four services/databases** (§4). Rotation is inherently *per-service*, not one command.
3. **The cited `signatures.private_key_enc` does not exist.** The signer stores only `signature_digest`; private key material lives in **Vault Transit / cloud KMS**, never KEK-encrypted in Postgres. Signer key rotation is Vault/KMS's job and is **out of scope**.

Net: this is *a per-service re-encryption sweep across four databases*, not *rotate one master key from one CLI*.

## 3. Approved design decisions

| Decision | Choice | Notes |
|---|---|---|
| Downtime posture | **Approach A — brief maintenance window** | Tables are tiny (single-tenant → tens of rows); a seconds-long stop-sweep-restart is acceptable. Zero-downtime (Approach C) is the documented upgrade path, §9. |
| Tooling shape | **Per-service `rotate-kek` subcommand** | Mirrors the existing `bootstrap` subcommand. Table/column knowledge stays in the owning service. 4 operator invocations. |
| Legacy `auth_providers` | **Rotate if present** | Re-encrypt legacy rows too when the table exists and has rows; log a per-table count so nothing is silently stranded on the old key. |
| Completion tracking | **Add a `kek_version` column** | Cheap "how many rows remain on the old key" query + auditable rotation record. Trial-decryption remains the authoritative verify. |
| Key delivery | **Env vars `KEK_OLD_HEX` / `KEK_NEW_HEX`** | Avoids shell-history leakage; never logged. |
| Extra modes | **`--dry-run` and `--generate`** | Dry-run reports counts without mutating; generate mints + prints a fresh 32-byte hex key. |

## 4. Affected surfaces (authoritative)

| Service | Table.column | Ciphertext storage | KEK env var |
|---|---|---|---|
| auth | `global_sso_config.oauth_client_secret_enc` | BYTEA | `SSO_CREDENTIAL_KEY_HEX` |
| auth (legacy) | `auth_providers.oauth_client_secret_enc` | BYTEA | `SSO_CREDENTIAL_KEY_HEX` |
| proxy | `upstream_registries.password_enc` | BYTEA | `CREDENTIAL_KEY_HEX` |
| webhook | `webhook_endpoints.secret_enc` | **TEXT (hex-encoded)** | `CREDENTIAL_KEY_HEX` |
| audit | `audit_export_configs.hmac_secret` | BYTEA | `AUDIT_EXPORT_SECRETS_KEY_HEX` |
| audit | `audit_export_configs.bearer_token` | BYTEA | `AUDIT_EXPORT_SECRETS_KEY_HEX` |

> **Watch item:** the webhook column stores ciphertext as **hex TEXT**, not BYTEA. The sweep must hex-decode before `aes.Decrypt` and hex-encode after `aes.Encrypt` for that one column. Every other column is raw BYTEA.

Note: `services/auth`, `services/proxy`, and `services/webhook` each own their KEK independently — `CREDENTIAL_KEY_HEX` is the *same env var name* in proxy and webhook but a **separate value per deployment**. Rotating one does not touch the other.

## 5. Architecture

### 5.1 `libs/crypto/rekey` — shared re-encryption helper

A small, table-agnostic helper so the decrypt-old → encrypt-new loop lives once and every service reuses it. No change to `libs/crypto/aes` is required — the helper composes the two existing single-key calls (`aes.Decrypt(cell, old)` then `aes.Encrypt(plain, new)`).

Proposed surface (final shape settled in the plan):

```go
package rekey

// Rekey re-encrypts one ciphertext from oldKey to newKey. Returns the new
// ciphertext, or an error if the cell does not decrypt under oldKey
// (wrong key / corrupt / tampered — GCM auth failure).
func Rekey(oldKey, newKey, ciphertext []byte) ([]byte, error)

// OnNewKey reports whether a ciphertext already decrypts under newKey —
// the authoritative "is this row done?" check for --verify.
func OnNewKey(newKey, ciphertext []byte) bool
```

The per-service subcommand owns the SQL (which table, which column, hex-vs-BYTEA) and calls `Rekey` per row inside one transaction.

### 5.2 Per-service `rotate-kek` subcommand

Each affected service binary (`auth`, `proxy`, `webhook`, `audit`) gains a `rotate-kek` subcommand, dispatched in `cmd/server/main.go` **before** normal server config loads — exactly as the existing `bootstrap` subcommand is dispatched (`if os.Args[1] == "bootstrap" { ... }`). The subcommand:

1. Reads `KEK_OLD_HEX` + `KEK_NEW_HEX` from env; validates both decode to 32 bytes.
2. Connects to the service's own database (reuses the service's DSN config).
3. For each affected table (a service may own more than one — audit owns two columns on one table; auth owns current + legacy):
   - `BEGIN`
   - `SELECT id, <cipher_col> FROM <table> WHERE <cipher_col> IS NOT NULL` (`FOR UPDATE`).
   - For each row: `new = Rekey(old, new, cell)`; `UPDATE <table> SET <cipher_col> = $new, kek_version = $toVersion WHERE id = $id`.
   - `COMMIT` (or `ROLLBACK` on any error — all-or-nothing per table).
   - Log a per-table count: `rotated N rows in <table>`.
4. Legacy `auth_providers`: rotate only if the table exists **and** has ≥1 row; log the count (0 → "skipped, no rows").

### 5.3 `kek_version` tracking column

A nullable `kek_version SMALLINT` on each affected table (one goose migration per service, with a down migration). The sweep stamps every re-encrypted row with `--to-version` (default = current max in the table + 1). Purpose:

- **Completion query:** `SELECT count(*) FROM <table> WHERE kek_version IS DISTINCT FROM $toVersion` → rows still on the old key.
- **Audit:** an operator can see which rotation generation last touched each row.

It is deliberately **not** the decrypt-dispatch mechanism (the tool always has both keys in hand); trial-decryption via `rekey.OnNewKey` is the authoritative verify, with the column as the cheap first-pass check.

## 6. Operator procedure (per service)

```
# 1. Pre-flight (no mutation): how many rows will rotate?
KEK_OLD_HEX=<old> KEK_NEW_HEX=<new> <service> rotate-kek --dry-run

# 2. Stop the service so no writer races the sweep.
#    (Window is seconds — tables hold tens of rows.)

# 3. Rotate.
KEK_OLD_HEX=<old> KEK_NEW_HEX=<new> <service> rotate-kek

# 4. Verify no row remains on the old key (trial-decrypt under NEW).
KEK_NEW_HEX=<new> <service> rotate-kek --verify

# 5. Restart the service with its KEK env now set to the NEW key.
```

Repeat for each of the four services. A new **`infra/runbooks/kek-rotation.md`** captures this end-to-end, including how to mint a key (`rotate-kek --generate`) and the ordering/rollback guidance.

## 7. Atomicity, rollback, safety

- **Per-table single transaction** — all rows in a table rotate or none do. Safe because the tables are tiny; no batching needed.
- **Fail-closed on decrypt failure** — if any cell fails to decrypt under OLD (wrong old key, or a tampered/corrupt row), the transaction rolls back and the tool exits non-zero with the offending row id. Nothing is half-rotated.
- **Idempotent verify** — `--verify` and `--dry-run` never mutate; safe to run repeatedly, before and after.
- **Secrets never logged** — only counts and row ids are logged, never plaintext or key material (CLAUDE.md §10).
- **Maintenance-window assumption** — the service is stopped during the sweep, so no concurrent writer can encrypt a new row under OLD after the sweep passes it. (Zero-downtime removes this assumption — §9.)

## 8. Testing strategy

- **`libs/crypto/rekey` unit tests:** round-trip (encrypt under OLD → `Rekey` → decrypts under NEW, not under OLD); `Rekey` errors on a cell encrypted under a third/wrong key; `OnNewKey` true/false; empty/nil handling.
- **Per-service subcommand integration tests** (testcontainers Postgres): seed rows encrypted under OLD across every affected column (including the webhook hex-TEXT column and both audit columns); run `rotate-kek`; assert every row decrypts under NEW and `kek_version` stamped; assert `--verify` reports 0-on-old; assert `--dry-run` reports the right count and mutates nothing.
- **Legacy handling:** table-absent and table-present-but-empty both skip cleanly; table-with-rows rotates and logs the count.
- **Failure path:** a deliberately-corrupt cell rolls the whole table back (no partial rotation).
- **Key validation:** non-hex / wrong-length `KEK_*_HEX` fails fast with a clear error.

## 9. Out of scope / future

- **Signer keys** — Vault Transit / KMS, rotated by that system, never KEK-encrypted in Postgres.
- **JWT signing keys** — already rotated by the shipped JWKS multi-key ring (Phase 6.5); unrelated to KEKs.
- **Approach C — zero-downtime dual-key rotation** (documented upgrade path): add a two-key decrypt to `libs/crypto/aes` (the never-shipped `DecryptWithVersion`) + an optional `*_KEK_PREVIOUS_HEX` env per service, so services decrypt under {NEW, OLD} while the §6 sweep runs live, then a follow-up deploy drops the previous key. Mirrors the JWKS ring pattern. Not needed for the MVP; the `kek_version` column and `rekey` helper are forward-compatible with it.

## 10. Deliverables

1. `libs/crypto/rekey` package + unit tests.
2. `rotate-kek` subcommand in each of `services/{auth,proxy,webhook,audit}/cmd/server/main.go` + integration tests.
3. One goose migration per service adding the nullable `kek_version SMALLINT` (with down migrations).
4. `infra/runbooks/kek-rotation.md` operator runbook.
5. Tracker updates: `futures.md` RED-FU-015 → done with the corrected-scope note; `status.md` row.

## 11. Open questions deferred to implementation

- Exact `--to-version` defaulting (auto max+1 vs operator-supplied) — settle in the plan; both are trivial.
- Whether `audit`'s two columns rotate in one transaction (they share a table — yes) vs the service iterating tables generically.
- Whether to expose `rotate-kek --verify` exit codes distinctly (0 = all-on-new, non-zero = rows remain) for scripting.
