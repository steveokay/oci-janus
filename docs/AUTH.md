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

## Dev fallback

When cert paths are unset, services log `slog.Warn` and use
`insecure.NewCredentials()`. **Never allow this in production** —
config validation in `main.go` must reject empty cert paths when
`OTEL_ENVIRONMENT=production`.

---

> **Cross-references:**
> - Rules + non-negotiable contracts: `CLAUDE.md` §7
> - Per-CVE audit log: [`../security.md`](../security.md)
> - Auth service implementation: `services/auth/`
> - mTLS library: `libs/auth/mtls/`
> - Decision rationale: [`adr/`](adr/) (ADR-0003, ADR-0024 are the main ones)
