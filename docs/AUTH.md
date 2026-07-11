# Authentication & Security — Implementation Detail

> **What this file is:** the mechanics behind the rules in `CLAUDE.md` §7.
> Extracted 2026-06-30 to keep the project rules file lean.
>
> **The rules themselves** (every gRPC server requires mTLS, fail-closed
> on auth unreachable, 90-day cert max, etc.) live in `CLAUDE.md` §7.
> This file explains *how* each of those rules is implemented + the
> reasoning that's load-bearing for future changes.

---

## mTLS hot reload

`libs/auth/mtls.ServerTLSConfig` / `ClientTLSConfig` (and their
`Reloading*` variants) wire `tls.Config.GetCertificate` /
`GetClientCertificate` to a per-config cache keyed on `(mtime, size)`.
The on-disk fingerprint is re-checked at each TLS handshake; a
successful change triggers a single re-read + parse, mutex-guarded so
concurrent handshakes coalesce to one disk read.

Cert-manager's atomic rename surfaces in the next handshake on every
connection without a service restart. The non-`Reloading*` constructors
also delegate to the reloading variant — universal opt-in.

Reload failures fall back to the cached cert (defence against
cert-manager mid-rename windows). The fallback emits `slog.Warn` so a
stuck rotation is visible; **it is not the right channel for emergency
revocation** — operators rotating to revoke must do so through the CA
pool / CRL / OCSP, not by deleting a leaf cert file (SEC-046).

**Builder API:**

```go
// ServerTLSConfig returns a tls.Config for gRPC servers requiring client certs.
// Both this and the reloading variant cache cert pairs by (mtime, size) so
// renewals pick up at the next handshake without a restart.
func ServerTLSConfig(caCertPath, certPath, keyPath string) (*tls.Config, error)
func ReloadingServerTLSConfig(caCertPath, certPath, keyPath string) (*tls.Config, error)

// ClientTLSConfig returns a tls.Config for gRPC clients presenting a cert.
// Same hot-reload semantics via GetClientCertificate.
func ClientTLSConfig(caCertPath, certPath, keyPath string, serverName string) (*tls.Config, error)
func ReloadingClientTLSConfig(caCertPath, certPath, keyPath string, serverName string) (*tls.Config, error)
```

Closed by REDESIGN-001 Phase 6.9 (PR #205).

## Peer-CN allowlist

`libs/middleware/grpc.PeerCNAllowlistFromEnv()` reads
`MTLS_PEER_CN_ALLOWLIST` (CSV, e.g. `registry-core,registry-management`)
and rejects gRPC requests whose client cert CN is not on the list.

- Empty/unset = no enforcement (**Option A** — per-server opt-in for
  backwards compat; flip to Option B once every service is wired).
- Case-sensitive comparison (matches `gen-dev-certs.sh` + cert-manager
  lowercase output).
- Rejections increment `registry_grpc_peer_cn_denied_total{method, reason}`.
- Disabled-in-production state is visible via the
  `registry_grpc_peer_cn_allowlist_enabled` gauge — alert on `== 0` when
  `OTEL_ENVIRONMENT=production` so a missed env var is noisy.

Closed by REDESIGN-001 Phase 6.10 (PR #204).

**FUT-019 Phase 3 — new audit → auth peer edge.** `registry-audit` now dials
`registry-auth.ResolveUserEmails` (to resolve email-notification recipients),
using `loader.BaseConfig.MTLSClientCreds("registry-auth")` with an eager
`conn.Connect()` at startup. `registry-auth`'s `MTLS_PEER_CN_ALLOWLIST` must
therefore include `registry-audit`'s client CN, otherwise the resolve call is
rejected at the peer-CN gate (and the email channel silently resolves no
recipients while the bell channel is unaffected).

## Client-side serverName pinning

`loader.BaseConfig.MTLSClientCreds(serverName)` is the canonical wrapper
for every outbound gRPC dial. Naming the expected peer (rather than
relying on CA verification alone) prevents a stolen CA-signed cert from
impersonating an arbitrary peer.

All 12 services with a gRPC client consume this wrapper (auth, metadata,
core, storage, signer, webhook, scanner, audit, gc, proxy, tenant,
management). Closed by SEC-038 (PR #181) + SEC-039 (PR #182) +
RED-FU-012 / RED-FU-014 (PR #186 + #189).

---

## JWT signing — multi-key ring + JWKS

Signing keys live in `services/auth/internal/service/keyring.go`, loaded
from `JWT_KEY_RING_PATH` at startup. The filename base of each key file
is the `kid`, stamped into `tok.Header["kid"]` on issuance.

- `JWT_SIGNING_KID` (optional) pins which key signs new tokens.
- Empty signing-kid defaults to the **most-recently-modified file**
  (not lex-greatest — operators using `prod-a/b/c` semantic names had
  the OLDEST file selected under lex order before this fix).
- Mixing the ring path with the legacy
  `JWT_PRIVATE_KEY_B64` / `JWT_PUBLIC_KEY_B64` / `JWT_KEY_ID` trio is
  rejected at startup with a clear error.
- Ring hard cap = 16 keys (SEC-048). Bounded DoS amplification on
  unknown-kid fallback.
- Fallback hits bump `registry_auth_jwt_kid_fallback_total{reason}`
  (`missing_kid` / `unknown_kid`).
- Boot-time `slog.Info "jwt key loaded"` with `(kid, pubkey_sha256, mtime)`
  per key (SEC-049).

**JWKS endpoint** at `/.well-known/jwks.json` enumerates every public
key in the ring so external validators rotate on the same schedule.

Closed by REDESIGN-001 Phase 6.5 (PR #206).

## JWT validation — fail-closed posture

Every gRPC server validates Bearer tokens via the
`registry-auth.ValidateToken` gRPC call. The auth service itself does
NOT need a cache because validation hits the in-process key ring.

**Aspiration (REM-002 follow-up):** the Redis-backed JWT validation
cache (`jwt:valid:<jti>`) on the management/BFF path remains
unimplemented. When implemented:

- The cached value must serialise the full `Access` list as JSON — the
  cache must not drop claim fields.
- On cache miss: call `registry-auth.ValidateToken` gRPC.
- If `registry-auth` is unreachable: **fail closed** (deny all), log
  error, increment metric.

The fail-closed posture also applies to the principal-revocation Redis
check (`revoke:user:<id>`) on the auth service — Redis unreachable
triggers a deny instead of a silent allow. Closed by REDESIGN-001
Phase 6.6 (PR #122).

## API-key Argon2 cache

Successful API-key Argon2 verifications cache in Redis at
`apikey:valid:<keyID>:<sha256-hex-secret>` with a 60s TTL so high-RPS
CI bots skip the ~50–100 ms Argon2id cost per request.

**Security invariants** (load-bearing — do not change without re-review):

- Cache key includes `sha256(secret)` so a stolen `keyID` alone cannot
  surface a HIT.
- **No negative cache.** Argon2 failure does NOT write an entry —
  preserves brute-force defence.
- HIT path **still hits the DB.** `s.apiKeys.GetByID` runs unconditionally
  and `applyKeyChecksFromCache` re-runs every row-state gate (expiry,
  disabled, SA-disabled, cross-tenant, scope-intersection) from the
  LIVE DB row so a stale cache cannot outlive a revocation.
- Cache invalidated on `DeleteAPIKey` / `SetUserDisabled` /
  `ServiceAccountService.SetDisabled` / `ServiceAccountService.Delete`
  via exported `InvalidateAPIKeyCache`.
- **Redis-down failure mode = fail-open.** Cache is an optimisation,
  not a security boundary — full Argon2 verify runs regardless. The
  only accepted race is the TTL-bounded `SetUserDisabled` window.

Closed by REDESIGN-001 Phase 6.7 (PR #207).

## HTTP Bearer dispatch — JWT and API-key forms

`registry-auth`'s `requireAuth` HTTP helper accepts two Bearer-token
shapes and dispatches internally on the literal `key.` prefix:

| Form | When | Routes |
|---|---|---|
| `Bearer <RS256 jwt>` (3-segment base64url, starts with `eyJ`) | Browsers / FE clients after `POST /api/v1/login` or `/auth/token` exchange | All authenticated routes |
| `Bearer key.<uuid>.<64-hex-secret>` (FUT-006, 2026-06-23) | CI bots / `curl` scripts wanting to introspect themselves directly | `/api/v1/users/me`, `/api/v1/access/activity`, anything that doesn't require a role claim |

API-key validation flows through `ValidateAPIKey` (argon2 verify +
expiry/disabled/SA-allowlist checks) and synthesises a `*Claims` with
`Subject = vk.UserID` (shadow user id for SA keys), `TenantID`,
`Access` (intersected scopes), and **empty `Roles`** — raw API keys
don't carry RBAC roles. Any handler that gates on `Roles` (e.g.
admin-only endpoints) must continue to require a JWT and will surface
a clean 403 rather than 401.

Full per-route contract + auth dispatch flow lives in
[`SERVICES.md` §2](SERVICES.md#2-registry-auth). Decision rationale
in `CLAUDE.md` §14 Decision #24 / `docs/adr/ADR-0024-*.md`.

---

## TOTP MFA (two-step login)

Tier-1 #1. Time-based one-time-password second factor for local
password accounts (`kind='human'`). **SSO users are exempt** — their
IdP owns the second factor, so MFA enrolment/challenge is never
required for an SSO-provisioned login. Implementation lives in
`services/auth/internal/mfa` (TOTP primitives, no DB/HTTP),
`internal/service/mfa.go` + `auth.go` (enrolment, login, typed tokens),
and `internal/repository/mfa.go`.

### Three token types

All three are RS256 tokens from the same key ring, discriminated by the
`typ` claim (and a dedicated `aud`):

| Token | `typ` | `aud` | TTL | Purpose |
|---|---|---|---|---|
| access | `""` (absent) | *(none)* | 5 min | normal authenticated calls |
| MFA challenge | `mfa_challenge` | `registry-auth-mfa` | 5 min | spent at `POST /login/mfa` to finish a two-step login |
| MFA setup | `mfa_setup` | `registry-auth-mfa-setup` | 15 min | authorises forced-enrolment `enroll`/`verify` |

`ValidateToken` **rejects any token with a non-empty `typ`** — a
challenge or setup token can never be replayed as an access token
(`services/auth/internal/service/auth.go` ValidateToken → the typed
tokens carry `typ` + a non-access audience and only
`ValidateMFAToken`/`ValidateMFASetupToken` accept them, each requiring
its exact `typ`).

### `amr` claim

The access token records the authentication methods that produced it
in the `amr` claim: `["pwd"]` (password only), `["pwd","otp"]`
(password + verified TOTP/backup code), or `["sso"]`. `RefreshToken`
forwards `amr` **verbatim** — a refresh never upgrades or downgrades
the recorded factors. `amr` is recorded for audit + future step-up
decisions.

### Enrolment flow

1. `POST /api/v1/users/me/mfa/enroll` — mints a fresh TOTP secret +
   `otpauth://` URI (FE renders the QR) and returns the base32 secret.
   The secret is stored **encrypted** (`users.mfa_secret_enc`).
2. `POST /api/v1/users/me/mfa/verify` — the user submits a code from
   their authenticator; on success MFA is enabled (`mfa_enabled=true`,
   `mfa_enrolled_at` set) and **8 single-use argon2id-hashed backup
   codes** are minted and returned once.
3. `GET /api/v1/users/me/mfa` reports status; `DELETE
   /api/v1/users/me/mfa` disables it; `POST
   /api/v1/users/me/mfa/backup-codes/regenerate` re-mints the 8 codes
   (invalidating the old set).

### Two-step login

1. `POST /api/v1/login` with password. If the user has MFA enabled the
   response is `mfa_required` + a short-lived `mfa_challenge` token
   (no access token is issued yet).
2. `POST /api/v1/login/mfa` with the challenge token + an OTP or backup
   code. On success the full access token is minted with
   `amr=["pwd","otp"]`.

### Replay + single-use guarantees

- **TOTP replay prevention:** each accepted code's time-step counter is
  stored in `users.mfa_last_used_counter`; a code whose counter is not
  strictly greater than the last accepted is rejected, so the same code
  cannot be spent twice within its ±1-step window.
- **Backup codes are single-use:** each is argon2id-hashed
  (`user_mfa_backup_codes.code_hash`) and marked `used_at` on
  redemption; a used code is never accepted again.

### Forced enrolment (`require_mfa`)

`token_policies.require_mfa` (per-tenant row; deployment-wide in single
mode) forces MFA on all password accounts. When it is on and a user
without a factor logs in, `POST /login/mfa` is not yet possible — the
login instead returns an `mfa_setup` token that authorises the
`enroll`/`verify` pair so the user can complete enrolment before
receiving an access token. SSO users remain exempt.

### Secrets at rest

TOTP secrets are AES-256-GCM encrypted (`libs/crypto/aes`) under a
**dedicated** KEK, `MFA_SECRET_KEY_HEX` — separate from the SSO
credential KEK (`SSO_CREDENTIAL_KEY_HEX`). It is required at startup
(32 bytes / 64 hex chars) and rotated independently via `registry-auth
rotate-kek --mfa` (`users.mfa_secret_enc` /
`users.mfa_secret_kek_version`); see
[`infra/runbooks/kek-rotation.md`](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/kek-rotation.md).
The generation stamped on newly-enrolled secrets is
`MFA_SECRET_KEK_VERSION` (default 1) — set it to the rotated generation
in lock-step with a `--mfa` sweep so fresh enrolments don't stamp a
stale version. The otpauth account label embedded in enrolment QR codes
is the user's email (then username), not the raw user id.

### Sessions (session list + revoke)

Every **interactive** login — password (`POST /login`), MFA completion
(`POST /login/mfa` and forced-enrol verify), and the SSO callback — mints
a stable session id (`sid`) and persists a `user_sessions` row (device
label parsed from the User-Agent, client IP via the SEC-009 trusted-proxy
`remoteIP`, `created_at`/`last_active_at`/`expires_at`). The `sid` is
embedded in the JWT as the `sid` claim and, unlike the JTI, is **preserved
verbatim across `RefreshToken`** — so a session stays listable/revocable
even though the JTI rotates every 300s. Machine identities carry no `sid`
and create no session: the OCI `/auth/token` Docker flow, workload-OIDC
tokens, and API-key (`Bearer key.*`) dispatch.

`ValidateToken` gains a **fail-closed `revoke:sid:<sid>` Redis gate**
alongside the existing `revoke:user` check (a Redis error denies, returning
`codes.Unavailable`) plus a **fail-open, Redis-debounced (`sid_active:<sid>`,
60s) `last_active_at` update** mirroring the FUT-003 API-key `last_used`
debouncer. Revoking a session sets `revoke:sid` with a TTL equal to the
session's remaining lifetime (SEC-005 TTL-coupling), so the current token is
denied on its next request and cannot be refreshed.

A session ends on: explicit revoke; **idle** (`last_active_at` older than
`token_policies.idle_revoke_days`, default 14d); or **absolute max** (30d
after `created_at`). An hourly sweep GCs dead rows. Self-service routes
(`requireAuth`, normal access token only — a setup token cannot manage
sessions): `GET /users/me/sessions`, `DELETE /users/me/sessions/{sid}`
(ownership-scoped → 404 otherwise), `POST /users/me/sessions/revoke-others`.

---

## SCIM provisioning (`/scim/v2/*`)

SCIM 2.0 (Users-only v1, Tier-1 #5) lets an enterprise IdP (Okta / Entra)
provision + deprovision users. It is a **superuser surface** and is deliberately
isolated from the user-auth path.

**Token model.** A single global SCIM bearer token gates every `/scim/v2/*`
route. The raw token is `scim.<64-hex>` (256 bits from `crypto/rand`); only its
Argon2id hash is persisted, in the `scim_config` singleton row (`id = 1`). The
token is **not** an env var — it is generated/rotated via the Phase 3 admin API
and stored in the DB. `requireSCIMAuth` extracts the Bearer token
(`libs/auth/bearer`), verifies it against the stored hash, and calls the wrapped
handler only on a match.

**Isolated principal.** The SCIM principal is **not** a user: it carries no RBAC
roles, mints no JWT, and is valid only under `/scim/v2/*`. It never touches the
`requireAuth` JWT/API-key dispatch path.

**Fail-closed.** Verification denies by default. A disabled config, an unset
config (SCIM never provisioned — `SetSCIMRepo` wires the repo at startup; when it
is nil `VerifySCIMToken` returns `(false, nil)`), a wrong token, or a missing/
malformed header all return `401` with an RFC 7644 error envelope. The error is
uniform — it never distinguishes "no token" from "wrong token" from "feature
disabled" (no oracle).

**Provisioning invariants (spec D3/D4/D5).**

- **D5 — baseline grant.** A newly provisioned user is passwordless
  (IdP-authenticated only), lands under the deployment bootstrap tenant
  (`s.scimTenantID`, threaded from `deployment_metadata` in single mode — not the
  dev default), and receives `reader@org:*`. A failed grant fails the whole
  provision so no role-less orphan is left behind.
- **D3 — takeover guard.** On an email collision, an existing **passwordless**
  account is linked (external_id backfilled); an existing **local-password**
  account is refused with `409 uniqueness`. The IdP can never silently adopt an
  account that still authenticates with a password.
- **D4 — disable, don't delete.** `active:false` (PATCH/PUT) and `DELETE` route
  to the existing `SetUserDisabled` primitive, which flips the status **and
  revokes the account's JTIs + API keys**. Nothing is hard-deleted, so the audit
  trail + per-tenant hash chain stay intact. `active:true` re-enables.

Cross-tenant reads are impossible: `GetSCIMUserByID`/`ListSCIMUsers` scope every
query to the bootstrap tenant and surface an out-of-tenant id as `404`.

---

## Dev fallback

When cert paths are unset, services log `slog.Warn` and use
`insecure.NewCredentials()`. **Never allow this in production** —
config validation in `main.go` must reject empty cert paths when
`OTEL_ENVIRONMENT=production`.

---

> **Cross-references:**
> - Rules + non-negotiable contracts: `CLAUDE.md` §7
> - Per-CVE audit log: [`security.md`](https://github.com/steveokay/oci-janus/blob/main/security.md)
> - Auth service implementation: `services/auth/`
> - mTLS library: `libs/auth/mtls/`
> - Decision rationale: [`adr/`](adr/README.md) (ADR-0003, ADR-0024 are the main ones)
