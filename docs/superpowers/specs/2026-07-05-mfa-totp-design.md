# TOTP MFA (step-up) — Design

> **Status:** approved design, ready for implementation planning.
> **Scope:** Tier-1 #1 (futures.md) — the TOTP half only. The active-session /
> device-list half is **deferred** to a separate follow-up (it is fully
> greenfield and shares almost no code with TOTP).
> **Posture:** single-tenant default (`DEPLOYMENT_MODE=single`); the design also
> holds in `multi`.

**Goal:** Add TOTP-based two-factor authentication for local password accounts:
self-service enrolment (QR + 8 backup codes), a two-step login step-up, and an
admin "require MFA for all password accounts" policy toggle.

**Architecture:** TOTP verification uses the `pquerna/otp` library (RFC 6238).
The shared secret is AES-256-GCM encrypted at rest under a dedicated KEK. Login
becomes two-step via a short-lived, type-scoped **stateless challenge JWT** —
no new server-side session store. Enrolment routes live directly on the auth
service (like the other `/users/me/*` routes), so no new gRPC RPC is needed for
self-service. The admin policy toggle rides the existing per-tenant
`token_policies` row.

**Tech stack:** Go (`services/auth`), `pquerna/otp`, `libs/crypto/aes` +
`libs/crypto/argon2`, PostgreSQL (goose migrations), Redis (existing JTI/lockout
stores), React + TanStack Query + `qrcode.react` (`frontend`).

---

## 1. Decisions (locked during brainstorming)

| # | Decision | Rationale |
|---|----------|-----------|
| D1 | **TOTP for local password (`kind='human'`) accounts only; SSO users exempt.** | SSO delegates the second factor to the identity provider (GitHub/GitLab-style). Password+TOTP does not map to passwordless SSO logins. |
| D2 | **Active-session/device list deferred.** | Fully greenfield (needs a new durable session table); shares no code with TOTP. Filed as a follow-up. |
| D3 | **`pquerna/otp` for TOTP** (not hand-rolled). | Don't hand-roll security primitives on an auth feature; covered by the now-blocking govulncheck gate. QR renders client-side from the `otpauth://` URI, so no backend QR/image dep. |
| D4 | **Stateless challenge JWT for the two-step login** (not a Redis challenge). | Reuses the existing key ring; no new store; 5-minute TTL bounds exposure; `typ`+audience scoping prevents the challenge ever being accepted as an access token. |
| D5 | **Dedicated `MFA_SECRET_KEY_HEX` KEK** (not reusing `SSO_CREDENTIAL_KEY_HEX`). | Independent rotation; consistent with the platform's per-secret KEK posture; avoids conflating SSO and MFA secret lifecycles. |
| D6 | **`require_mfa` lives on `token_policies`** (not tenant-service `deployment_metadata`). | Read locally in `Service.Login` — no cross-service gRPC on the login hot path. Single mode has one tenant row, so it is effectively deployment-wide (matches the futures RM-004 note). |
| D7 | **No forced session revocation on enable/disable.** | The 5-minute access-token TTL already bounds exposure; keeps the UX clean. |
| D8 | **Forced enrolment when policy is on and user is un-enrolled.** | `POST /login` returns a `setup_token` that authorizes *only* enroll/verify; the verify step then issues the full access token. Prevents an un-enrolled password user from receiving a real token while the policy requires MFA. |

---

## 2. Data model

### 2.1 Migration `services/auth/migrations/20260705HHMMSS_users_mfa.sql`

Additive columns on `users` (per §11 — never drop a column; every migration has
a down migration):

```sql
ALTER TABLE users
  ADD COLUMN mfa_enabled            BOOLEAN  NOT NULL DEFAULT false,
  ADD COLUMN mfa_secret_enc         BYTEA    NULL,
  ADD COLUMN mfa_secret_kek_version SMALLINT NULL,
  ADD COLUMN mfa_enrolled_at        TIMESTAMPTZ NULL,
  ADD COLUMN mfa_last_used_counter  BIGINT   NULL;
```

- `mfa_enabled` flips to `true` only after the first code is verified (§4.3), so
  a half-finished enrolment (secret stored, never verified) does **not** gate
  login.
- `mfa_secret_enc` holds the 20-byte (160-bit) base32 TOTP secret, AES-256-GCM
  encrypted via `libs/crypto/aes.Encrypt` under `MFA_SECRET_KEY_HEX`.
- `mfa_secret_kek_version` mirrors the rekey pattern (see §7.4) so the column is
  sweepable by the shipped `rotate-kek` tool.
- `mfa_last_used_counter` records the TOTP time-step counter of the last
  accepted code, so the **same 6-digit code cannot be replayed within its 30s
  window** (§5.4). A verify succeeds only if the code's counter is strictly
  greater than this value; on success the column is advanced.

### 2.2 New table `user_mfa_backup_codes`

```sql
CREATE TABLE user_mfa_backup_codes (
  id         UUID        NOT NULL DEFAULT gen_random_uuid() PRIMARY KEY,
  user_id    UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash  TEXT        NOT NULL,                 -- argon2id (libs/crypto/argon2)
  used_at    TIMESTAMPTZ NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_user_mfa_backup_codes_user ON user_mfa_backup_codes(user_id);
```

- 8 rows per enrolment. Single-use: `used_at` is stamped on consumption; a code
  with `used_at IS NOT NULL` is rejected.
- Regeneration = `DELETE` all rows for the user, then insert 8 fresh ones.
- A table (rather than a `TEXT[]` column) buys per-code argon2 hashing,
  single-use tracking, and clean regeneration.

### 2.3 Policy column — migration on `token_policies`

```sql
ALTER TABLE token_policies ADD COLUMN require_mfa BOOLEAN NOT NULL DEFAULT false;
```

Read locally in `Service.Login`. Single mode has exactly one `token_policies`
row, so this is the deployment-wide toggle. Set via the existing
`PutTokenPolicy` RPC/BFF (extended additively — see §5).

---

## 3. Token types & the `amr` claim

Three RS256 token types, all signed by the existing multi-key ring
(`services/auth/internal/service/keyring.go`), discriminated by a new `typ`
claim **and** a dedicated audience so a challenge/setup token can never be
accepted where an access token is expected:

| `typ` | Audience | TTL | Purpose |
|-------|----------|-----|---------|
| *(access — unchanged)* | `registry-core` | 5 min | Normal access token. |
| `mfa_challenge` | `registry-auth-mfa` | 5 min | Issued on correct password when MFA is enabled; spent at `POST /login/mfa`. |
| `mfa_setup` | `registry-auth-mfa-setup` | 15 min | Issued when policy requires MFA and the user is un-enrolled; authorizes **only** enroll/verify. |

**`Claims` change** (`services/auth/internal/service/auth.go:48`): add
`Amr []string \`json:"amr,omitempty"\``.

- Full password+OTP access token → `amr: ["pwd","otp"]`.
- Password-only access token (MFA off) → `amr: ["pwd"]`.
- SSO access token → `amr: ["sso"]`.
- **`RefreshToken` copies `amr` verbatim** — it must never upgrade a `["pwd"]`
  token into `["pwd","otp"]`. (Current `RefreshToken` already copies claims
  forward; the change is only to carry the new field through unchanged.)

Core services do **not** consult `amr` in v1 — it is recorded now as the
foundation for future step-up on sensitive operations (explicit YAGNI: no
step-up re-prompt this round).

Validation must reject a `typ=mfa_challenge`/`mfa_setup` token anywhere the
access path runs: `ValidateToken` returns an error if `typ` is a non-access
value, and the OCI/core audience check already rejects the MFA audiences.

---

## 4. Enrolment flow (self-service)

Routes are served **directly by the auth service** HTTP handler (mirroring the
existing `/api/v1/users/me/*` routes at `http_users_me.go`), each wrapped by
`requireAuth`, which binds the caller's identity from the Bearer token via
`ValidateToken` → `parseUserAndTenant`. No body-supplied IDs are trusted; no new
gRPC RPC is required.

### 4.1 `GET /api/v1/users/me/mfa` — status
Returns `{ "enabled": bool, "enrolled_at": timestamp|null }` for the settings
card.

### 4.2 `POST /api/v1/users/me/mfa/enroll` — begin
- If `mfa_enabled` already true → `409 Conflict` (`MFA_ALREADY_ENABLED`).
- Generate a fresh 20-byte secret via `pquerna/otp/totp.Generate` (issuer
  `oci-janus` / registry deployment name; account = user email or username).
- Store `mfa_secret_enc` (encrypted) + `mfa_secret_kek_version` with
  `mfa_enabled` still `false` (pending). Re-enrolling before verify overwrites
  the pending secret.
- Return `{ "secret_base32": "...", "otpauth_uri": "otpauth://totp/..." }`.
  **No backup codes yet.**

### 4.3 `POST /api/v1/users/me/mfa/verify` — confirm
- Body `{ "code": "123456" }`. Decrypt the pending secret, verify via
  `totp.Validate` (±1 period skew).
- On success: set `mfa_enabled=true`, `mfa_enrolled_at=now()`; generate 8 backup
  codes, argon2-hash and insert them; return `{ "backup_codes": [...8...] }`
  **once**. On failure: `400` (`MFA_CODE_INVALID`), feed the account lockout
  counter.
- Also accepts a `mfa_setup` token as its auth (the forced-enrolment path, §6);
  in that case a successful verify **additionally** returns a full access token.

### 4.4 `DELETE /api/v1/users/me/mfa` — disable
- Body `{ "password": "..." }` **or** `{ "code": "..." }` — requires re-auth
  (current password, a valid OTP, or an unused backup code).
- Clears `mfa_secret_enc`/`mfa_secret_kek_version`, sets `mfa_enabled=false`,
  clears `mfa_enrolled_at`, and deletes all `user_mfa_backup_codes` rows.

### 4.5 `POST /api/v1/users/me/mfa/backup-codes/regenerate`
- Same re-auth as disable. Deletes existing codes, inserts 8 fresh ones, returns
  them once.

---

## 5. Login step-up flow

### 5.1 `POST /api/v1/login` (`services/auth/internal/service/auth.go` `Login`)
`AuthenticateUser` runs unchanged (password verify, active/lockout checks). The
MFA branch slots between `AuthenticateUser` and `IssueToken`:

```
user := AuthenticateUser(...)                 // returns on success today
switch {
case user.MFAEnabled:
    return { mfa_required: true, challenge_token: <mfa_challenge, 5m> }   // no access token
case policy.RequireMFA && user.kind=="human" && !user.MFAEnabled:
    return { mfa_setup_required: true, setup_token: <mfa_setup, 15m> }    // forced enrolment
default:
    return { token: <access, amr=["pwd"]> }                              // unchanged behaviour
}
```

The response is still `200`; the FE branches on the flags.

### 5.2 `POST /api/v1/login/mfa`
- Body `{ "challenge_token": "...", "code": "..." }`.
- Validate the token is `typ=mfa_challenge` (audience `registry-auth-mfa`), not
  expired; resolve `sub` → user.
- Verify `code` as a TOTP (`totp.Validate`) **or** as an unused backup code
  (argon2 compare + stamp `used_at`).
- On success mint the full access token with `amr=["pwd","otp"]`. On failure
  feed the existing per-account lockout (5 attempts → 15 min).

### 5.3 Rate limiting / lockout
OTP verification reuses the existing failed-login lockout machinery
(`failed_logins`/`locked_until` on `users`, plus the per-IP rate limiter), so a
brute-force of the 6-digit code is bounded exactly like password guessing.

### 5.4 Code-reuse prevention
A verify (login step-up *and* enrolment confirm) computes the TOTP time-step
counter for the accepted code and rejects it if that counter is not strictly
greater than `mfa_last_used_counter`; on success it advances the column. This
stops the same code (or a stolen challenge token) being replayed a second time
within the code's 30-second validity window. Backup codes are single-use via
their own `used_at` stamp (§2.2) and are exempt from the counter check.

---

## 6. Forced enrolment (policy on + un-enrolled)

When `require_mfa` is on and a password user has no MFA:
1. `POST /login` returns `mfa_setup_required:true` + a `mfa_setup` token (15 min).
2. The FE routes to a blocking enrolment screen (the same enrol/verify dialog),
   passing the `mfa_setup` token as its auth.
3. `POST /users/me/mfa/enroll` and `/verify` accept the `mfa_setup` token.
4. On successful `/verify` driven by a setup token, the response **additionally**
   includes a full access token (`amr=["pwd","otp"]`), completing login.

The `mfa_setup` token's dedicated audience means it unlocks only the enroll and
verify endpoints — it cannot be used against the rest of the API.

---

## 7. Cross-cutting

### 7.1 Frontend (`/settings/account` `SecuritySection` — placeholder already present)
- **MFA card:** enabled/disabled status + enrolled date; opens the enrolment or
  disable dialog.
- **Enrolment dialog** (mirrors `components/profile/change-password-dialog.tsx`):
  step 1 renders the QR from `otpauth_uri` via a new `qrcode.react` dependency +
  shows the manual base32 secret; step 2 is 6-digit code entry; step 3 displays
  the 8 backup codes with copy/download and an "I've saved these" confirm before
  the dialog can close.
- **Disable dialog** + **Regenerate backup codes** action, both gated on
  password-or-code re-auth.
- **Login flow:** the login mutation handles the three-way response —
  `mfa_required` → OTP entry screen (`POST /login/mfa`); `mfa_setup_required` →
  forced-enrolment screen (same dialog, driven by the `setup_token`, ending in a
  full token); otherwise straight in.
- **Hooks:** new `frontend/src/lib/api/mfa.ts`
  (`useMfaStatus/useMfaEnroll/useMfaVerify/useMfaDisable/useRegenerateBackupCodes`)
  mirroring `me.ts`.
- **Admin toggle:** "Require MFA for all password accounts" on the token-policy
  settings surface, wired through the extended `PutTokenPolicy`.

### 7.2 gRPC / proto (`proto/auth/v1/auth.proto`)
Only the policy path touches gRPC: add `bool require_mfa` (new field number) to
the `TokenPolicy` message and its `PutTokenPolicy` request, additively (never
renumber). Enrolment/login stay HTTP-only on the auth service. Regenerate the
committed stubs with `buf generate`.

### 7.3 Security checklist
- TOTP secret encrypted at rest (dedicated KEK); backup codes argon2-hashed +
  single-use; both wiped on disable.
- OTP verify rate-limited into the existing account lockout; backup-code and
  OTP comparisons are constant-time (`argon2.Verify` / `pquerna`).
- Same-code replay prevented via `mfa_last_used_counter` (§5.4); backup codes
  single-use via `used_at`.
- Challenge/setup tokens short-lived, `typ`+audience-scoped, never accepted as
  access tokens; `RefreshToken` never upgrades `amr`.
- Disable / regenerate require re-auth.
- **Never log** the secret, the `otpauth_uri`, backup codes, or OTP inputs.

### 7.4 Rekey coupling
Register `users.mfa_secret_enc` (+ `mfa_secret_kek_version`, KEK env
`MFA_SECRET_KEY_HEX`) as a `TableSpec` entry in
`services/auth/internal/rotatekek/` so the secret rotates with the shipped
`rotate-kek` tool. Add the KEK to the runbook's per-service table.

### 7.5 Docs
`docs/AUTH.md` MFA section (flows + token types + `amr`); `.env.example` for
`MFA_SECRET_KEY_HEX`; `infra/runbooks/kek-rotation.md` per-service KEK table;
`docs/SERVICES.md` §2 route table.

---

## 8. Testing

- **Backend unit:** TOTP wrapper vs RFC 6238 vectors; `typ`/audience
  discrimination (a `mfa_challenge`/`mfa_setup` token is rejected as an access
  token; the setup token unlocks *only* enroll/verify); backup-code single-use;
  same-code replay rejected via the `mfa_last_used_counter` advance (§5.4);
  `amr` set correctly and preserved verbatim across `RefreshToken`; the three
  `Login` branches; disable-requires-re-auth.
- **Integration (testcontainers PG + Redis):** migration up/down; repository
  round-trips (encrypted secret, backup-code lifecycle); the full
  password→challenge→OTP→access-token flow and the forced-enrolment flow.
- **Frontend (vitest):** enrolment dialog steps, the backup-code confirm gate,
  the login OTP + forced-enrolment screens.

---

## 9. Out of scope (explicit)

- Active-session / device list with per-row revoke (D2 — separate follow-up).
- WebAuthn / hardware keys (deferrable per futures.md).
- MFA for SSO users (D1 — IdP owns their second factor).
- Step-up re-prompt on sensitive operations (only the `amr` foundation ships;
  no consumer checks it yet).
- SMS/email OTP (TOTP only).
