# Service Catalogue — Detailed Reference

> Authoritative reference for every service: purpose, endpoints, gRPC interfaces, environment variables, and non-obvious implementation rules.
> CLAUDE.md §4 holds the one-paragraph summary table; this file holds the detail.
> When code disagrees with this file, prefer the proto files (`proto/<service>/v1/*.proto`) and migration files (`services/<service>/migrations/`) — they are the canonical contracts.

---

## Table of Contents

1. [registry-gateway](#1-registry-gateway)
2. [registry-auth](#2-registry-auth)
3. [registry-core](#3-registry-core)
4. [registry-storage](#4-registry-storage)
5. [registry-metadata](#5-registry-metadata)
6. [registry-proxy](#6-registry-proxy)
7. [registry-scanner](#7-registry-scanner)
8. [registry-signer](#8-registry-signer)
9. [registry-webhook](#9-registry-webhook)
10. [registry-audit](#10-registry-audit)
11. [registry-gc](#11-registry-gc)
12. [registry-tenant](#12-registry-tenant)
13. [registry-management](#13-registry-management)

---

## 1. registry-gateway

**Purpose:** Single ingress point. TLS termination. Routes by `Host` header to resolve tenant. Injects `X-Tenant-ID` header downstream. Rate limiting. DDoS protection.

**Tech:** Traefik v3 (preferred for dynamic config + Let's Encrypt) or Nginx with Lua.

**Responsibilities:**
- Terminate TLS (Let's Encrypt via ACME for custom domains, wildcard cert for platform domain)
- Resolve tenant from `Host` header via lookup in `registry-tenant` (cached in Redis, TTL 60s)
- Inject `X-Tenant-ID` and `X-Request-ID` headers on all downstream requests
- Rate limit by tenant + IP (Redis-backed sliding window)
- Block requests with missing or malformed `Host` headers
- Forward `/v2/` prefix to `registry-core`
- Forward `/auth/` prefix to `registry-auth`
- Forward `/api/v1/` to relevant internal services

**Security:**
- TLS 1.2 minimum, TLS 1.3 preferred
- HSTS header on all responses
- Reject HTTP (no redirect to HTTPS — hard fail)
- Strip all `X-Forwarded-*` headers from clients before re-setting them internally
- Log all requests with tenant ID, method, path, status, latency (no auth tokens in logs)

---

## 2. registry-auth

**Purpose:** Docker token auth service. Issues JWT access tokens. Manages API keys. Validates credentials.

**Endpoints (HTTP, called by Docker clients, gateway, and the BFF):**

```
POST /auth/token          # Docker token endpoint (RFC 7235 flow)
POST /api/v1/users        # Create user (admin-only — see PENTEST-003)
POST /api/v1/login        # Issue long-lived session token (may return mfa_required + challenge)
POST /api/v1/login/mfa    # Finish two-step login: spend mfa_challenge token + OTP/backup code
POST /api/v1/apikeys      # Create API key (robot accounts)
DELETE /api/v1/apikeys/:id
GET  /api/v1/apikeys      # List API keys for current user
POST /api/v1/logout
POST /api/v1/token/refresh   # Silent JWT refresh (60s before expiry — Sprint 5)
GET  /api/v1/users/me        # Current user metadata (FE-API-011)
PATCH /api/v1/users/me       # Update display name + email (FE-API-012)
POST /api/v1/users/me/password  # Change password (FE-API-013, argon2id + JTI revocation)
GET  /.well-known/jwks.json  # Public key set for JWT verification

# TOTP MFA — Tier-1 #1 (local password accounts; SSO users exempt)
GET  /api/v1/users/me/mfa                        # MFA status (enabled? enrolled_at?)
POST /api/v1/users/me/mfa/enroll                 # Begin enrolment → TOTP secret + otpauth QR URI
POST /api/v1/users/me/mfa/verify                 # Confirm code → enable MFA + 8 argon2 backup codes
DELETE /api/v1/users/me/mfa                       # Disable MFA
POST /api/v1/users/me/mfa/backup-codes/regenerate # Re-mint the 8 single-use backup codes
# Admin toggle: token_policies.require_mfa forces MFA on all password accounts
# (un-enrolled users get an mfa_setup token at login to complete forced enrolment).

# Active sessions — Tier-1 #1 (session management; self-service)
GET    /api/v1/users/me/sessions                 # list live sessions (current flagged via the sid claim)
DELETE /api/v1/users/me/sessions/{sid}           # revoke one owned session (404 if not owned)
POST   /api/v1/users/me/sessions/revoke-others   # revoke all sessions except the current one

# SSO — FE-API-034 (OAuth + SAML)
GET  /api/v1/auth/sso/providers          # List enabled SSO providers for tenant
GET  /api/v1/auth/oauth/start            # Begin OAuth (PKCE S256 + single-use state)
GET  /api/v1/auth/oauth/callback         # OAuth callback → 302 with sso_token
GET  /api/v1/auth/saml/start             # Begin SAML AuthnRequest
POST /api/v1/auth/saml/acs               # SAML AssertionConsumerService
GET  /api/v1/admin/sso/providers         # Per-tenant admin CRUD (list)
POST /api/v1/admin/sso/providers         # Create provider (client_secret AES-GCM-encrypted before persist)
PATCH /api/v1/admin/sso/providers/:id    # Update
DELETE /api/v1/admin/sso/providers/:id   # Delete
```

**JWT Structure:**
```json
{
  "iss": "registry-auth",
  "sub": "<user_id>",
  "aud": "registry-core",
  "exp": "<now + 300s>",
  "iat": "<now>",
  "jti": "<uuid>",
  "tenant_id": "<tenant_id>",
  "access": [
    {
      "type": "repository",
      "name": "myorg/myimage",
      "actions": ["push", "pull"]
    }
  ]
}
```

**Rules:**
- Sign with RS256. Private key loaded from environment (PEM, base64-encoded). Never hardcoded.
- Token TTL: 300 seconds (5 minutes). Non-configurable — Docker clients re-request automatically.
- API keys: stored as `argon2id` hash in PostgreSQL. Never stored in plaintext. Return raw key only once at creation.
- Enforce account lockout: 5 failed login attempts → lock for 15 minutes. Log lockout event to audit.
- `jti` (JWT ID) stored in Redis for token revocation. Check on every validation.
- Rotate signing key pair without downtime: support multiple public keys in JWKS, tag active key with `kid`.
- Password policy: minimum 12 characters, 1 uppercase, 1 lowercase, 1 number, 1 symbol. Enforce server-side — never rely on client.
- Rate limit: 10 failed auth attempts per IP per minute before returning 429.

**Authenticating to JSON HTTP routes — three Bearer flavours:**

The HTTP handler's `requireAuth` accepts three Bearer-token shapes; all
other JSON routes (`/users/me`, `/apikeys`, `/access/activity`, the SSO
admin surface, etc.) flow through it. Choose by use case:

| Mode | Bearer header value | Issued by | Typical caller | Roles claim | TTL |
|---|---|---|---|---|---|
| **JWT session** | `Bearer <RS256 jwt>` (3 base64url dot-segments, starts with `eyJ`) | `POST /api/v1/login` (password) or `POST /auth/token` (key→JWT exchange) | Browsers, FE clients | Yes — RBAC role names embedded at issuance time | 300s (silent refresh 60s before) |
| **API key (FUT-006, 2026-06-23)** | `Bearer key.<uuid>.<64-hex-secret>` | `POST /api/v1/apikeys` (human owner) or the SA-key issue path on the `/api-keys` admin hub (FE-API-048) | CI bots, machine accounts, `curl` from scripts | **No** — raw API keys carry no roles; handlers that gate on a specific role still require a JWT | Forever (until `DELETE /api/v1/apikeys/:id`) |
| **Docker OCI token** | `Bearer <RS256 jwt>` issued by `POST /auth/token` against Basic Auth | Same JWT signer; scoped to one OCI action | `docker push` / `docker pull` clients hitting `registry-core` | No (scope-only) | 300s |

**How `requireAuth` dispatches:**

```
Authorization: Bearer <token>
  ├─ token starts with "key." ─→ parseAPIKeyBearer → ValidateAPIKey
  │                                                ├─ argon2.Verify(secret, key.KeyHash)
  │                                                ├─ check expiry + is_active + SA disabled
  │                                                ├─ intersect key.Scopes ∩ SA.AllowedScopes (SA only)
  │                                                └─ synthesise *Claims:
  │                                                     Subject  = vk.UserID (shadow user id for SA-owned keys)
  │                                                     TenantID = vk.TenantID
  │                                                     Access   = vk.Access (intersected OCI scopes)
  │                                                     Roles    = []  ← intentionally empty
  └─ default ────────────────────→ ValidateToken (JWT verify + JTI revocation check)
```

The discriminator is the literal `key.` prefix. Anything without it (including the empty string) falls through to `ValidateToken`. The API-key parser is strict — see `parseAPIKeyBearer` for the rejection rules; structural mismatches return a generic 401 rather than leaking which half of the token shape was malformed.

**Why API-key auth ships with empty `Roles`:** RBAC role resolution happens at JWT issuance time (`POST /auth/token`'s Basic-auth path looks up the principal's role grants and embeds them in the JWT). A raw API key is just the credential — promoting it to a session token via `/auth/token` is what hydrates roles. Handlers that need roles (e.g. `requireSAAdmin`) should continue to require a JWT; they'll surface a clean 403 against the empty roles list rather than a confusing 401, telling the operator *what* is missing rather than just *that* auth failed.

**Per-route auth contract:**
- `/auth/token` — accepts Basic Auth (key-id-as-UUID:secret, OR username:password). Returns an OCI-scoped JWT.
- `/api/v1/login` — accepts JSON `{tenant_id, username, password}`. Returns a session JWT with roles claim.
- `/api/v1/users/me` — accepts ANY of the three Bearer flavours via `requireAuth`. Branches on `user.Kind == "service_account"` to return the SA principal envelope (FE-API-048 T16); human callers get `currentUserResponse`.
- `/api/v1/apikeys` create/list/delete — accepts JWT only today (gated by `requireSAAdmin`-style helpers that read `roles` from claims).
- `/api/v1/admin/sso/*` — JWT only (admin role required).
- `/api/v1/access/activity` — accepts ANY Bearer flavour; the API-key path lets a bot inspect its own activity log without an exchange step.

**gRPC (internal, mTLS):**

```protobuf
service AuthService {
  rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);
  rpc ValidateAPIKey(ValidateAPIKeyRequest) returns (ValidateAPIKeyResponse);
  rpc GetUserPermissions(GetUserPermissionsRequest) returns (GetUserPermissionsResponse);
  // RBAC management — called by registry-management and registry-core
  rpc GrantRole(GrantRoleRequest) returns (google.protobuf.Empty);
  rpc RevokeRole(RevokeRoleRequest) returns (google.protobuf.Empty);
  rpc ListMembers(ListMembersRequest) returns (ListMembersResponse);
  // FE-API-028 — used by the platform-admin tenant-detail view
  rpc CountTenantUsers(CountTenantUsersRequest) returns (CountTenantUsersResponse);
}
```

**Auth schema additions for FE-API-034 (SSO):**
- `auth_providers(id, tenant_id, type, display_name, enabled, oauth_client_id, oauth_client_secret_encrypted, oauth_issuer_url, oauth_scopes, saml_entity_id, saml_audience, saml_idp_metadata_xml, …)` — `type` enum: `google` | `github` | `microsoft` | `oidc` | `saml`; canonical-type providers (Google/GitHub/Microsoft) have a per-tenant unique constraint.
- `auth_login_sessions(state PK, tenant_id, provider_id, redirect_uri, pkce_verifier, created_at, expires_at)` — 10-minute TTL; single-use via `DELETE ... RETURNING`. SAML reuses `pkce_verifier` to store the `AuthnRequest.ID` so the callback can pass it to `ParseResponse` as the only permitted `InResponseTo`.
- `users.sso_provider_id` — points back to the provider when a user was auto-provisioned. Local-auth users have NULL.

Audit routing keys published by the SSO admin handler (not yet typed in `libs/rabbitmq/events`): `auth.provider_created`, `auth.provider_updated`, `auth.provider_deleted`, plus `auth.user_sso_provisioned` from the OAuth/SAML callback path.

**Schema additions for FE-API-048 (service accounts):**
- `users.kind TEXT NOT NULL DEFAULT 'human' CHECK (kind IN ('human','service_account'))` — every existing row defaulted to `'human'`; rows backing a service account are `'service_account'` and called *shadow users*. All single-row lookups on the human-auth path use the new `…Human…` repository helpers (`GetHumanByEmail`, `GetHumanByID`, `ListHumans`, `CountHumans`) so the kind guard is enforced at the repository layer rather than scattered across handlers. New `FROM users WHERE` reads must go through a kind-guarded helper or carry an `-- allow-any-kind` annotation. (The former `scripts/lint-user-queries.sh` CI check was retired under REM-015 — no diagnostic signal — leaving the repository-helper contract as the guard.)
- `service_accounts(id PK, tenant_id, shadow_user_id UUID UNIQUE FK users(id) ON DELETE CASCADE, name, description, allowed_scopes TEXT[], created_by FK users(id) ON DELETE SET NULL, created_at, disabled_at)` — UNIQUE `(tenant_id, name)`. `created_by` is `SET NULL` so admins offboarding never blocks; provenance lives in the `service_account.created` audit row (creator_email + creator_display_name snapshotted at creation time).
- `api_keys` — polymorphic owner. `user_id` is now nullable; `service_account_id UUID FK service_accounts(id) ON DELETE CASCADE` added. CHECK `(user_id IS NULL) <> (service_account_id IS NULL)` enforces exactly-one ownership. Old UNIQUE `(user_id, name)` replaced by two partial unique indexes (`api_keys_user_name_unique WHERE user_id IS NOT NULL` and `api_keys_sa_name_unique WHERE service_account_id IS NOT NULL`) so a human and an SA in the same tenant may each have a key named `ci-prod` without colliding. Down migration carries a `DO $$ RAISE EXCEPTION` guard that refuses rollback when any `service_account_id IS NOT NULL` rows exist (data-loss protection).

**Endpoints added by FE-API-048:**
```
GET    /api/v1/service-accounts                                  # list (admin)
POST   /api/v1/service-accounts                                  # create (admin)
GET    /api/v1/service-accounts/:id                              # get (admin)
PATCH  /api/v1/service-accounts/:id                              # update name/desc/allowed_scopes/disabled
DELETE /api/v1/service-accounts/:id                              # cascade delete
POST   /api/v1/service-accounts/:id/scopes/preflight             # {affected_keys: N} before scope-shrink save
GET    /api/v1/service-accounts/:id/api-keys                     # list SA's keys
POST   /api/v1/service-accounts/:id/api-keys                     # issue (scopes must ⊆ allowed_scopes)
DELETE /api/v1/service-accounts/:id/api-keys/:keyID              # revoke
GET    /api/v1/access/activity?principal_user_id=…&limit=…       # principal activity feed (404-not-403 on negative paths)
```
`POST /api/v1/apikeys` accepts an optional `service_account_id` field; when set, the call routes to SA-key issuance instead of the human-user path (admin gate + scope-subset enforcement on the SA). `GET /api/v1/users/me` returns a `type: "user"|"service_account"` envelope with nested `service_account: {…}` for SA principals (synthetic email `sa+<uuid>@internal.invalid` is never leaked — the response sets `email: null`).

**Hot-path security changes** (`ValidateAPIKey`, T9):
- Branches on polymorphic owner.
- SA branch: load SA → if `disabled_at IS NOT NULL` → `PermissionDenied`. If the request carries an `X-Tenant-ID` that disagrees with `service_accounts.tenant_id` → `Unauthenticated` + emit `pentest.cross_tenant_attempt` audit (security HIGH H1).
- Effective scopes = `key.scopes ∩ sa.allowed_scopes` (retroactive shrink); empty intersection → `PermissionDenied`.
- Order-of-checks: lookup → argon2 verify → expiry → is_active → owner branch. Verify runs FIRST so reject paths share the ~100 ms cost; closes the timing oracle that would distinguish "wrong secret" from "expired key" (same class as PENTEST-004 fix on `AuthenticateUser`).

**JWT revocation on SA disable** (security HIGH H2, T12): `ServiceAccountService.SetDisabled(true)` writes `revoke:user:<shadow_user_id>` to Redis with a 25-minute TTL. `ValidateToken` consults this key after the existing JTI check; presence returns `Unauthenticated`. The pattern is documented in CLAUDE.md §7 "JWT Validation."

**Audit vocabulary** — see [`docs/EVENTS.md`](EVENTS.md) for the SA lifecycle action codes.

**Bootstrap wiring:** `httpH.WithServiceAccountService(saSvc)` in `services/auth/internal/server/server.go` constructs the `ServiceAccountService` from the existing repos + a `redisCmdableAdapter` (the structural `RedisCmdable` interface doesn't accept `*redis.Client.Set/Del` directly because their return types are `*redis.StatusCmd`/`*redis.IntCmd` rather than the bare `interface{Err() error}` — the adapter is 6 lines). The `AuditEmitter` parameter is a `slogAuditEmitter` stand-in for now; durable audit emission via RabbitMQ is a follow-up (FUT-007).

**Not yet wired in production:**
- `ActivityService` (T11) — the audit gRPC client needs an `AUDIT_GRPC_ADDR` env var + mTLS dial. `/api/v1/access/activity` returns informative 501 until then. Tracked as FUT-005.
- `/users/me` SA-key authentication — the `requireAuth` middleware currently only accepts JWTs. SA principals reach `/users/me` only via JWT exchange through `/auth/token`, which works (the JWT's `sub` is the shadow user id). A CI bot wanting to introspect itself directly via raw API key is not yet supported. Tracked as FUT-006.

**RBAC role model (implemented in `services/auth/migrations/20260614000001_create_rbac.sql`):**

| Role | Actions granted | Scope types |
|---|---|---|
| `owner` | push, pull, delete + can grant/revoke roles | `org` or `repo` |
| `admin` | push, pull, delete + can grant/revoke roles | `org` or `repo` |
| `writer` | push, pull | `org` or `repo` |
| `reader` | pull only | `org` or `repo` |

Roles are stored in a seeded `roles` table (fixed UUIDs). Assignments live in `role_assignments(user_id, role_id, scope_type, scope_value, tenant_id)`. `scope_type` is `"org"` or `"repo"`; `scope_value` is the org name or `"org/repo"` string. An org-scoped assignment covers all repos within that org (resolved as `"org/*"` wildcard during permission checks).

`GetUserPermissions` maps assignments to `RepositoryAccess` entries using the hierarchy above. It is called by `registry-core` on every push/pull handler to enforce access at the OCI protocol layer, and by `registry-management` to gate destructive REST operations. Fails closed on error — a gRPC failure from auth denies the request.

On `GrantRole`/`RevokeRole` success the handler publishes `rbac.role_granted` / `rbac.role_revoked` to RabbitMQ so `registry-audit` can record membership changes without a direct gRPC coupling. A publish failure is logged but does not roll back the DB write.

---

## 3. registry-core

**Purpose:** OCI Distribution Spec v1.1 implementation. The primary interface for Docker/OCI clients.

**Supported clients:** anything that speaks OCI v1.1 — `docker push/pull`, `helm push/pull/install`, `oras`, `crane`, `skopeo`, etc. Helm charts use the same `/v2/<name>/manifests/<reference>` surface (just with `application/vnd.cncf.helm.config.v1+json` as the config media type), so tag immutability, signed-image admission, quotas, RBAC, audit, and quarantine all apply uniformly across artifact types. The dashboard's per-tag artifact-type pill (`image` / `helm` / `signature` / `sbom`) is a read-side discriminator only — write-path rules don't branch on it.

**Endpoints (all under `/v2/`):**

```
GET  /v2/                                           # Version check → 200 or 401
GET  /v2/<name>/tags/list                           # List tags
GET  /v2/<name>/manifests/<reference>               # Pull manifest (tag or digest)
PUT  /v2/<name>/manifests/<reference>               # Push manifest
DELETE /v2/<name>/manifests/<reference>             # Delete manifest
HEAD /v2/<name>/manifests/<reference>               # Manifest exists check
GET  /v2/<name>/blobs/<digest>                      # Pull blob
HEAD /v2/<name>/blobs/<digest>                      # Blob exists check
DELETE /v2/<name>/blobs/<digest>                    # Delete blob
POST /v2/<name>/blobs/uploads/                      # Initiate blob upload
GET  /v2/<name>/blobs/uploads/<uuid>                # Get upload status
PATCH /v2/<name>/blobs/uploads/<uuid>               # Chunked upload
PUT  /v2/<name>/blobs/uploads/<uuid>                # Complete upload
DELETE /v2/<name>/blobs/uploads/<uuid>              # Cancel upload
GET  /v2/<name>/referrers/<digest>                  # OCI referrers API (§4.5)
```

**Rules:**
- Every request must carry a valid Bearer token. Extract `tenant_id` from JWT. Validate via `registry-auth` gRPC.
- Enforce `X-Tenant-ID` from gateway matches `tenant_id` in JWT — reject mismatches with 403.
- Content-addressable blobs: SHA256 digest is the canonical key. Reject uploads where computed digest ≠ declared digest.
- Support both `Docker-Content-Digest` and OCI digest headers.
- Support manifest media types: `application/vnd.docker.distribution.manifest.v2+json`, `application/vnd.oci.image.manifest.v1+json`, `application/vnd.oci.image.index.v1+json` (multi-arch).
- Chunked uploads: store upload state (UUID, offset, tenant, repo) in Redis with 1-hour TTL.
- Never buffer a full blob in memory. Stream blobs directly to `registry-storage` via gRPC streaming.
- On successful manifest push: publish `push.completed` event to RabbitMQ (see `docs/EVENTS.md`).
- Enforce per-tenant storage quota (check before accepting upload, fail fast with 403 if exceeded).
- **Signed-image admission (futures.md Tier 1 #3).** `GetManifest` and `HeadManifest` check the parent repository's `require_signature` flag after the metadata resolve. When `TRUE`, the handler calls `registry-signer.ListSignatures(manifest_digest)`; if zero rows come back, the response is `403 DENIED` with body `repository requires a signed manifest; sign the image or turn require_signature off`. **Phase 2** additionally calls `metadata.ListRepositoryTrustedKeys(repo_id)` — when the returned allowlist is non-empty, the gate further requires that at least one recorded signature's `key_id` is in the allowlist, otherwise the same `403 DENIED` body fires. An empty allowlist is Phase 1 fallback ("any signature passes") so operators can flip `require_signature` on first and pin keys incrementally. Flipped via `PATCH /api/v1/repositories/{org}/{repo}` with `{"require_signature": true}`; allowlist managed via `POST/DELETE /api/v1/repositories/{org}/{repo}/trusted-keys[/{key_id}]`. Fails OPEN on metadata or signer reachability blips (warn + continue) so a transient outage doesn't break every pull; if `SIGNER_GRPC_ADDR` is unset at boot, the registry logs a startup warning and allows all pulls (dev-stack convenience). Push side is unaffected — operators sign with `cosign sign` after the push lands.
- **Tag immutability preflight (futures.md Tier 1 #2).** `PutManifest` checks two flags before writing whenever `reference` is a tag (digest pushes skip the check — content-addressable, can't move tags). Rejects with `400 MANIFEST_INVALID` + body `tag is immutable (repo immutable_tags=true or per-tag pin set); push to a new tag or unpin first` when EITHER:
  - `repositories.immutable_tags = TRUE` (repo-wide flag — flipped via `PATCH /api/v1/repositories/{org}/{repo}` with `{"immutable_tags": true}`); OR
  - `tags.immutable = TRUE` (per-tag pin — flipped via `POST/DELETE /api/v1/repositories/{org}/{repo}/tags/{tag}/pin`).
  Idempotent same-digest re-pushes always succeed (not a "move"). Per-tag pin takes precedence over the repo flag; the repo check is the second metadata RPC only when the same-digest fast path didn't fire. Fails OPEN on metadata reachability failures (warn + continue) so a transient DB blip doesn't reject every push. New-tag pushes (tag doesn't exist yet) are allowed regardless of either flag.
- Return `Link` header for paginated tag lists (`?n=` and `?last=` params per spec).
- `name` in all routes = `<org>/<repo>`. Reject single-component names.

**gRPC server — `CoreService` (internal read surface, mTLS-only):**

Beyond the OCI HTTP API, registry-core exposes a small internal gRPC surface (`proto/core/v1/core.proto`) consumed by `registry-management`:
- `ListReferrers(ListReferrersRequest) → ListReferrersResponse` — every OCI referrer artifact whose subject points at a manifest digest; backs the tag-detail Referrers tab (PR #282).
- `GetBlob(GetBlobRequest) → GetBlobResponse` — generic size-capped blob fetch (digest→bytes, `max_bytes` ceiling; the server refuses with `FailedPrecondition` once a blob exceeds the cap rather than truncating silently). Used by the BFF chart route to read a chart's config + content-layer blobs (FUT-022).

**gRPC calls made by this service:**
- `registry-auth`: ValidateToken, ValidateAPIKey, GetUserPermissions (RBAC enforcement on every push/pull)
- `registry-metadata`: CreateTag, GetManifest, ListTags, DeleteTag
- `registry-storage`: PutBlob, GetBlob, StatBlob, DeleteBlob, InitiateUpload, AppendChunk, CompleteUpload

**RBAC enforcement in registry-core:**
- `checkAccess(r, claims, repoName, action)` is called after JWT validation on every handler.
- Write handlers (InitiateUpload, PutManifest, DeleteManifest, DeleteBlob) require action `"push"`.
- Read handlers (GetManifest, HeadManifest, GetBlob, HeadBlob, ListTags) require action `"pull"`.
- Access is denied with HTTP 403 + OCI `{"errors":[{"code":"DENIED","message":"access denied"}]}` if the user lacks the required action.
- Wildcard names (`*`) and wildcard actions (`*`) in the permission list are accepted (covers org-level grants).
- `org/*` patterns match any repo whose name begins with `org/` (org-scoped role assignment expansion).
- `GET /v2/` version check is intentionally exempt — OCI spec requires it to be reachable unauthenticated.

**Environment variables (in addition to the common set):**

| Variable | Description | Default |
|---|---|---|
| `SIGNER_GRPC_ADDR` | Address of `registry-signer` for the signed-image admission gate. **Required in production** when any repo has `require_signature=true`; unset in dev causes a startup warning + "allow all pulls" fail-OPEN (see [`docs/SIGNING.md`](SIGNING.md) §8). | empty (dev only) |
| `PULL_EVENT_SAMPLE_RATE` | Probability (range `[0.0, 1.0]`) that a successful `GetManifest` publishes a `pull.image` event. Lower to reduce event volume on hot repos. FE-API-043's `max_idle_days` retention rule keeps working as long as the rate is > 0 because services/metadata debounces `last_pulled_at` updates to 24h. | `1.0` |
| `AUTH_REALM` | URL Docker clients use to fetch tokens — must be publicly reachable. PENTEST-010: must be HTTPS in production (validated at startup). | `http://localhost:8080/auth/token` |

---

## 4. registry-storage

**Purpose:** Storage abstraction. All blob I/O goes through this service. Clients never touch storage directly.

**Storage backends (configured per deployment, not per tenant):**

| Backend | Driver name | Notes |
|---|---|---|
| MinIO | `minio` | Self-hosted S3-compatible |
| AWS S3 | `s3` | Native SDK, supports IMDSv2 |
| GCP Cloud Storage | `gcs` | ADC or service account JSON |
| Azure Blob Storage | `azure` | Managed identity or connection string |
| Local filesystem | `filesystem` | Dev/testing only — never production |

**Backend selection:** `STORAGE_DRIVER` environment variable. Must be explicitly set — no default.

**Driver interface:** Defined in `libs/storage/driver/`. All drivers implement `PutBlob`, `GetBlob`, `StatBlob`, `DeleteBlob`, `BlobExists`, `ListBlobs`, multipart operations, and `Ping` for health.

**Storage key layout:**
```
blobs/<tenant_id>/sha256/<first2>/<digest>
manifests/<tenant_id>/<repo_encoded>/<reference>
uploads/<tenant_id>/<upload_uuid>/parts/<part_num>
```

**Security:**
- Credentials for cloud backends loaded from environment only. Never from config files committed to Git.
- For S3/GCS/Azure: use IAM roles / Workload Identity / Managed Identity where available. Avoid static credentials in production.
- Enable bucket versioning on S3/GCS (protects against accidental GC bugs).
- Server-side encryption: enforce SSE-S3 (S3), CMEK (GCS), SSE (Azure) — flag if backend does not support it.
- No presigned URLs exposed to end clients — all blob traffic proxied through `registry-core`.

**gRPC service:** `StorageService` in `proto/storage/v1/storage.proto`. Streaming PutBlob/GetBlob plus multipart.

**Configuration per driver:**

```
# MinIO
STORAGE_MINIO_ENDPOINT=          # e.g. minio:9000
STORAGE_MINIO_ACCESS_KEY=        # required
STORAGE_MINIO_SECRET_KEY=        # required
STORAGE_MINIO_BUCKET=            # required
STORAGE_MINIO_USE_SSL=true       # default true
STORAGE_MINIO_REGION=us-east-1   # optional

# AWS S3
STORAGE_S3_BUCKET=               # required
STORAGE_S3_REGION=               # required
STORAGE_S3_ROLE_ARN=             # optional, for cross-account assume-role
# Credentials: prefer IMDSv2 (no static keys). Static fallback:
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=

# GCP Cloud Storage
STORAGE_GCS_BUCKET=              # required
STORAGE_GCS_PROJECT=             # required
GOOGLE_APPLICATION_CREDENTIALS=  # path to service account JSON, or use Workload Identity

# Azure Blob
STORAGE_AZURE_CONTAINER=         # required
STORAGE_AZURE_ACCOUNT=           # required
# Auth: prefer managed identity. Static fallback:
STORAGE_AZURE_ACCOUNT_KEY=

# Filesystem (dev only)
STORAGE_FILESYSTEM_ROOT=/data    # required, absolute path
```

---

## 5. registry-metadata

**Purpose:** Source of truth for all registry metadata: repositories, tags, manifests, blob references, scan status, quota usage.

**This service owns the PostgreSQL database.** All other services that need metadata go through this service's gRPC API — they do not connect to PostgreSQL directly.

**gRPC service:** `MetadataService` in `proto/metadata/v1/metadata.proto`. Covers repositories, tags, manifests, blobs, quota, scan status, and the FE-API security-center surfaces.

Notable RPCs:
- `GetRepositoryByName(tenant_id, "org/repo")` — direct lookup, used by registry-management to avoid O(n) stream scans.
- `GetManifestByTag(tenant_id, repo_id, tag)` — used by the BFF's per-tag manifest detail route (FE-API-002).
- `ListRepositories`, `ListTags`, `ListOrphanedBlobs` — route to read replica when `DB_DSN_REPLICA` is set.
- `CountRepositories(tenant_id)` — replaces the previous O(n) `ListRepositories` stream drain in `/stats`.
- `UpdateRepository(tenant_id, repo_id, description, …)` — used by the BFF to surface description / visibility edits (FE-API-006).
- **Security center (FE-API-014/015/016/017/020):**
  - `GetTenantVulnerabilityCount` + extended `severity_counts` (critical/high/medium/low/negligible) for the `/stats` mini bar.
  - `GetSecurityOverview(tenant_id)` — single 3-CTE query: vuln rollup + scan coverage + recency.
  - `ListTenantVulnerabilities(tenant_id, severity, page_token, limit)` — DISTINCT ON CTE rolls findings per `cve_id` with deduped `affected[]`.
  - `ListScanHistory(tenant_id, since, page_token, limit)` — keyset cursor on `(completed_at, scan_id)`; uses the new `00006_scan_results_trigger.sql` `trigger` column (`push` / `manual` / `scheduled`).
  - `ListTenantRemediations(tenant_id, page_token, limit)` — groups by `(package, from_version, to_version)`; capped 10 per group with `affected_count` reporting true total.
- **Tenant admin (FE-API-028/031):**
  - `GetTenantUsage(tenant_id)` — storage/repository/organization counts.
  - `GetTenantStorageBreakdown(tenant_id)` — tenant total + top-50 repos with `percent_of_tenant` materialised server-side.
- **SBOM (FE-API-033):**
  - `UpsertScanSBOM(scan_id, format, sbom_json)` / `GetScanSBOM(scan_id)` — write path is the metadata RPC itself today (scanner integration deferred).
- **Admission gates (futures.md Tier 1 #2 + Tier 1 #3):**
  - `UpdateRepositoryImmutability(tenant_id, repo_id, immutable_tags)` — Tier 1 #2; busts the GetRepository cache slot via the new `bustRepositoryCache` helper so `services/core`'s `checkTagImmutable` reflects the flip on the next pull.
  - `UpdateTagImmutable(tenant_id, repo_id, name, immutable)` — Tier 1 #2 per-tag pin; CTE-then-join SELECT so the response carries the joined manifests row fields.
  - `UpdateRepositorySignaturePolicy(tenant_id, repo_id, require_signature)` — Tier 1 #3 Phase 1; same cache-bust posture.
  - `ListRepositoryTrustedKeys` / `AddRepositoryTrustedKey` / `RemoveRepositoryTrustedKey` — Tier 1 #3 Phase 2; List is cached (30s TTL); Add idempotent on (repo_id, key_id); Remove returns `ErrNotFound` when the pair doesn't exist. Add/Remove bust the trusted-keys cache via `bustTrustedKeysCache`.

**Database schema:** Canonical source is `services/metadata/migrations/`. Core tables:
- `tenants(id, name, created_at)`
- `organizations(id, tenant_id, name)`
- `repositories(id, org_id, tenant_id, name, is_public, storage_quota, storage_used, description, immutable_tags, require_signature)`
- `manifests(id, repo_id, tenant_id, digest, media_type, raw_json, size_bytes, image_size_bytes, config_media_type, artifact_type, last_pulled_at, retention_pending_delete_at, quarantined, quarantine_reason, quarantined_by, quarantined_at)`
- `tags(id, repo_id, tenant_id, name, manifest_digest, immutable, updated_at)`
- `blobs(digest PRIMARY KEY, size_bytes, storage_key)`
- `blob_links(repo_id, blob_digest)` — deduplication
- `scan_results(id, manifest_digest, repo_id, tenant_id, scanner_name, status, severity_counts, findings, trigger, sbom_format, sbom_json, completed_at)`
- `repository_trusted_keys(id, repo_id, tenant_id, key_id, display_name, added_by, added_at)` — futures.md Tier 1 #3 Phase 2

Migrations of note:
- `00004_manifest_image_size.sql` — column add only (per PENTEST-028); operator-run batched backfill in `infra/runbooks/manifest-image-size-backfill.md`.
- `00005_repo_description.sql` — FE-API-006.
- `00006_scan_results_trigger.sql` — FE-API-015 (`trigger` ∈ `{push, manual, scheduled}`).
- `00007_scan_results_sbom.sql` — FE-API-033 (`sbom_format` + `sbom_json BYTEA`).
- `00010_manifest_retention_pending.sql` — FE-API-040: `manifests.retention_pending_delete_at` for the soft-delete grace window.
- `00011_manifest_last_pulled.sql` — FE-API-042: `manifests.last_pulled_at` (24h-debounced updates) driving the FE-API-043 `max_idle_days` retention rule.
- `00012_manifest_quarantine.sql` — FE-API-050: quarantine state machine on `manifests`.
- `00013_manifest_artifact_type.sql` — S-MAINT-1 P6: `config_media_type` + derived `artifact_type` discriminator (`image` / `helm` / `signature` / `sbom` / `other`) for scanner-skip + dashboard pill.
- `00014_tag_immutability.sql` — futures.md Tier 1 #2: `repositories.immutable_tags` + `tags.immutable`.
- `00015_repository_require_signature.sql` — futures.md Tier 1 #3 Phase 1: `repositories.require_signature`.
- `00016_repository_trusted_keys.sql` — futures.md Tier 1 #3 Phase 2: per-repo trusted-key allowlist (UNIQUE on (repo_id, key_id), composite index on (tenant_id, repo_id)).

`PutManifest` enforces `maxManifestJSONBytes = 4 << 20` (PENTEST-029); `parseImageSize` truncates `Layers`/`Manifests` at `maxManifestEntries = 1000`.

All tables have `tenant_id UUID NOT NULL` and matching RLS policies (see CLAUDE.md §9).

---

## 6. registry-proxy

**Purpose:** Pull-through proxy cache. Routes `docker pull <registry>/cache/<upstream-prefix>/<image>:<tag>` through to upstream registries, caching locally.

**Upstream registry config (stored in DB, per tenant):**

```go
type UpstreamRegistry struct {
    Name        string        // e.g. "dockerhub", "quay", "gcr"
    URL         string        // e.g. "https://registry-1.docker.io"
    AuthType    string        // "none" | "basic" | "token"
    Username    string        // stored encrypted in DB
    Password    string        // stored encrypted in DB (AES-256-GCM, key from KMS/env)
    TTL         time.Duration // how long to cache manifests
    Enabled     bool
}
```

**Cache flow:**
1. Check `registry-metadata` for cached manifest by digest/tag
2. Cache hit and not expired → serve from `registry-storage`
3. Cache miss or expired → fetch from upstream, stream to client, store in background goroutine
4. Background store: save manifest to `registry-metadata`, blobs to `registry-storage`
5. Never block client response on background store completion
6. On background store failure: publish `store.queued` event to RabbitMQ; consumer retries (3 attempts, dead-letter after).

**Security:**
- Upstream credentials encrypted at rest (AES-256-GCM). Key from environment variable.
- Sanitise upstream responses: validate Content-Type, reject unexpected media types.
- Cap upstream response size (configurable, default 20GB per layer).
- Honour upstream `Content-Digest` — verify before caching.
- Do not expose upstream auth credentials in any log or error message.

---

## 7. registry-scanner

**Purpose:** Orchestrates vulnerability scanning. Hosts the scanner plugin interface. Does not implement scanning itself.

**Plugin interface:** Defined in `libs/scanner/plugin/`. Plugins implement `Scanner` with `Name()`, `Version()`, and `Scan(ctx, ScanRequest) (*ScanResult, error)`. Plugins are loaded as **external processes only** (Go `.so` plugin path was rejected — see CLAUDE.md Decision Log #1).

**Plugin loading:**
- `SCANNER_PLUGIN_PATH` env var points to the external plugin binary
- Validate plugin binary checksum (SHA256) against `SCANNER_PLUGIN_CHECKSUM` env var before loading
- If checksum mismatch: log critical, refuse to start
- Plugin binary path must be absolute; sanitised with `filepath.Clean` before use
- Communicate over stdin/stdout with newline-delimited JSON. Never shell-exec with user-supplied input.
- `io.LimitedReader` caps plugin stdout at 10MB.
- Subprocess receives a minimal env allowlist (PATH, HOME, TMPDIR, TRIVY_*, GRYPE_*).

**JSON-RPC protocol:**

Request (to stdin):
```json
{
  "id": "uuid",
  "method": "scan",
  "params": {
    "tenant_id": "...",
    "manifest_digest": "sha256:...",
    "layers": [{"digest": "sha256:...", "media_type": "..."}]
  }
}
```

Response (from stdout):
```json
{
  "id": "uuid",
  "result": {
    "scanner_name": "trivy",
    "scanner_version": "0.50.0",
    "findings": [...],
    "severity_counts": {"CRITICAL": 2, "HIGH": 5}
  },
  "error": null
}
```

- Process must exit 0 on success, non-zero on failure
- Do not use stderr for structured data — only for human-readable diagnostics

**Scan job flow:**
1. Consume `push.completed` from RabbitMQ
2. Create scan record in `registry-metadata` (status: `pending`)
3. Fetch manifest from `registry-metadata`, extract layer digests
4. Invoke scanner plugin with layer refs
5. Plugin fetches blobs via `registry-storage` gRPC (authenticated)
6. Update scan result in `registry-metadata`
7. Publish `scan.completed` event to RabbitMQ
8. If findings contain CRITICAL/HIGH and tenant policy requires blocking: update tag status to `blocked`

**Concurrency:**
- Worker pool, size configurable via `SCANNER_WORKER_COUNT` (default: 4)
- Each job has a timeout (`SCANNER_JOB_TIMEOUT_SECONDS`, default: 600)
- Dead-letter queue for failed jobs after 3 retries

**Scan policy & compliance reports (FE-API-018 / FE-API-019):**

> Bootstrap note: `services/scanner` previously had no DB. As of 2026-06-20 (`f40365f`) it owns its own Postgres database with goose migrations under `services/scanner/migrations/`. `DB_DSN` + `DB_MAX_CONNS` are required config; `goose.Up` runs at startup before serving traffic.

Tables (canonical source `services/scanner/migrations/20260620000001_scan_policies_and_reports.sql`):
- `scan_policies(tenant_id PK, scan_on_push, block_on_severity, scanner_plugin, allow_unscanned, exempt_repositories[], exempt_cves[])` — `GetScanPolicy` returns a default policy when no row exists (no 404). Validation: `block_on_severity ∈ {"", CRITICAL, HIGH, MEDIUM, LOW}`, `scanner_plugin ∈ {trivy, grype}`, `exempt_cves` each matches `^CVE-\d{4}-\d{4,7}$`.
- `compliance_reports(id, tenant_id, status, format, requested_by, requested_at, completed_at, output_path, error)` — `report_status` enum (`pending` | `running` | `succeeded` | `failed`), composite index `(tenant_id, status, requested_at)`. The report worker (`internal/reportworker`) polls every 5 s and claims via `FOR UPDATE SKIP LOCKED LIMIT 1` so the job is safe across multiple scanner replicas.

Renderers:
- **SPDX JSON 2.3** — minimal `SPDXVersion / dataLicense / SPDXID / documentName / packages` from findings.
- **PDF** — hand-crafted `%PDF-1.4` header + single page + Helvetica Type1; no 3rd-party PDF library.

Files land under `REPORT_OUTPUT_DIR/<tenant>/<id>.{pdf,spdx.json}` with `safeJoin` guarding path traversal. Real PDF layout / branding and object-storage with signed URLs are deferred (documented in code).

Scanner gRPC additions:
- `GetScanPolicy(tenant_id)` / `UpdateScanPolicy(tenant_id, …)`
- `GenerateComplianceReport(tenant_id, format)` → `{report_id, status:"pending"}` (async)
- `GetComplianceReport(tenant_id, report_id)` / `ListComplianceReports(tenant_id)`

---

## 8. registry-signer

**Purpose:** Image signing and verification using Cosign (Sigstore) and Notary v2.

> **Canonical reference:** end-to-end signing flow, key lifecycle, dashboard
> + CLI verification paths, threat model — see [`docs/SIGNING.md`](SIGNING.md).
> This section covers the proto / gRPC contract; the SIGNING doc covers
> where the keys live and how they're used in operations.

**Cosign integration:**
- Signatures stored as OCI artifacts in `registry-core` (standard Cosign behaviour)
- `registry-signer` exposes a signing API for CI/CD pipelines that don't have key material
- Key material stored in: env var (dev), HashiCorp Vault (production), or cloud KMS (AWS KMS / GCP KMS / Azure Key Vault)
- Key backend configured via `SIGNER_KEY_BACKEND` env var: `env` | `vault` | `awskms` | `gcpkms` | `azurekms`
- Never log, print, or include key material in error messages

**Notary v2 integration:**
- TUF (The Update Framework) metadata stored in `registry-storage`
- Delegation keys per tenant
- Root key ceremony documented in `infra/runbooks/notary-root-key-ceremony.md`

**Key backend config:**

| Backend | Config env vars |
|---|---|
| `env` | `SIGNER_COSIGN_PRIVATE_KEY` (PEM, base64), `SIGNER_COSIGN_PUBLIC_KEY` |
| `vault` | `VAULT_ADDR`, `VAULT_TOKEN` (or K8s SA auth), `VAULT_COSIGN_PATH` |
| `awskms` | `SIGNER_KMS_ARN`, standard AWS credential chain |
| `gcpkms` | `SIGNER_KMS_RESOURCE_ID`, standard GCP credential chain |
| `azurekms` | `SIGNER_KMS_VAULT_URL`, `SIGNER_KMS_KEY_NAME`, standard Azure credential chain |

**Rules:**
- Key material never leaves the signing service
- Signing operations are audit-logged
- Public keys are discoverable via `GET /api/v1/signers/cosign/public-key` (per tenant)
- Verification does not require the signing service — clients can verify with the public key directly

**gRPC service:** `SignerService` in `proto/signer/v1/signer.proto`. Provides `SignManifest`, `VerifyManifest`, `ListSignatures`.

Signatures are persisted in PostgreSQL (`signatures` table) with a write-through cache. `SigB64` is computed at retrieval, not stored — see SEC-015.

---

## 9. registry-webhook

**Purpose:** Reliable webhook delivery with retries, dead-lettering, and HMAC signing.

**Events delivered:**
- `image.pushed` — new tag/manifest pushed
- `image.deleted` — tag or manifest deleted
- `scan.completed` — vulnerability scan finished
- `scan.policy_blocked` — image blocked by policy
- `image.signed` — signature added

**Delivery guarantees:**
- At-least-once delivery
- Retry with exponential backoff: 5s, 30s, 5m, 30m, 2h (5 attempts total)
- After 5 failures: move to dead-letter queue, notify tenant admin
- Timeout per delivery attempt: 30 seconds

**Security:**
- HMAC-SHA256 signature on payload, key set per webhook endpoint
- Signature in `X-Registry-Signature: sha256=<hex>` header
- Validate destination URL is not a private IP range (SSRF protection): block 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, ::1, metadata endpoints (169.254.169.254)
- Enforce HTTPS-only webhook endpoints (reject HTTP)
- Dispatcher errors are sanitised via `sanitizeURLForError` so persisted `last_error` strings never carry URL-embedded tokens (PENTEST-027)
- `UpdateEndpoint` re-runs `ValidateURL` against the stored URL when the caller omits `req.Url` (PENTEST-032)
- Never include auth tokens or credentials in webhook payload

**gRPC service:** `WebhookService` in `proto/webhook/v1/webhook.proto`. RPCs:
- `CreateEndpoint`, `UpdateEndpoint` (with `optional` URL/events/active), `DeleteEndpoint`, `ListEndpoints`
- `RotateEndpointSecret` — generates a new HMAC secret; returns plaintext once
- `ListDeliveries(endpoint_id, tenant_id, since, limit)` — newest-first, cap 200
- `GetDelivery(endpoint_id, delivery_id, tenant_id)` — `DeliveryDetail` summary + `payload_json` + `signature_header` + `response_body` (FE-API-035; `signature_header` and `response_body` reserved on the wire, populated by a follow-up migration + dispatcher patch)
- `TestDispatch(endpoint_id, tenant_id)` — synchronous; reuses the same `delivery.Dispatcher` (SSRF guard + timeouts); not recorded in `webhook_deliveries`

---

## 10. registry-audit

**Purpose:** Immutable audit log for all significant actions.

**Events logged (minimum):**
- User login / logout / lockout
- Token issued / revoked
- API key created / deleted
- Image pushed / pulled / deleted
- Repository created / deleted
- Webhook created / triggered
- Scan started / completed
- Policy violation
- Tenant config changed
- RBAC changes (consumed from `rbac.role_granted` / `rbac.role_revoked` events)

**Audit record structure:**
```go
type AuditEvent struct {
    ID         uuid.UUID  `json:"id"`
    TenantID   uuid.UUID  `json:"tenant_id"`
    ActorID    string     `json:"actor_id"`    // user ID or "system"
    ActorType  string     `json:"actor_type"`  // "user" | "robot" | "system"
    ActorIP    string     `json:"actor_ip"`    // IPv4/IPv6, never logged raw from header (use trusted proxy IP)
    Action     string     `json:"action"`      // verb.resource: "push.image"
    Resource   string     `json:"resource"`    // e.g. "myorg/myimage:v1.2.3"
    Outcome    string     `json:"outcome"`     // "success" | "failure"
    Metadata   JSONB      `json:"metadata"`    // additional context, no secrets
    OccurredAt time.Time  `json:"occurred_at"`
}
```

**Rules:**
- Audit records are append-only. No UPDATE or DELETE on audit table — enforced via PostgreSQL `FORCE ROW LEVEL SECURITY` and a separate low-privilege `registry_audit_app` role (INSERT + SELECT only; DELETE permitted on `audit_events_default` partition for retention rollovers).
- Pool `AfterConnect` does `SET ROLE registry_audit_app` on every connection.
- `checkRole()` startup check refuses to start if effective role ≠ `registry_audit_app`.
- Actor IP extracted from `X-Forwarded-For` only if request came through trusted gateway IP. Otherwise use direct TCP peer.
- Never log passwords, tokens, API keys, or secret values in `metadata`.
- Retain audit logs for minimum 90 days (configurable, default 365 days).

**gRPC service:** `AuditService` in `proto/audit/v1/audit.proto`. RPCs:
- `GetBuildHistory(tenant_id, repo_id, tag, …)` — used by the BFF `builds` route
- `GetDailyPullCount(tenant_id, repo_id)`
- `GetRepoActivity(tenant_id, org, repo, since, limit, page_token, event_types)` — FE-API-004; 8-action allowlist (`push.completed/failed`, `manifest.deleted`, `tag.deleted`, `scan.completed`, `scan.policy_blocked`, `image.signed`); keyset cursor over `(tenant_id, occurred_at DESC)`
- `GetNotifications(tenant_id, since, limit, page_token, event_types, unread_only)` — FE-API-008; renderer synthesises `title` + `summary` + `link` per event type
- `GetAnalytics(tenant_id, scope_type, repo_id, range_secs, bucket_secs)` — FE-API-030; PG14 `date_bin` time-series; BFF pre-allocates empty buckets so quiet periods return `count=0`
- `GetLastTenantPush(tenant_id)` — FE-API-028; serialised as JSON `null` when no push activity yet

---

## 11. registry-gc

**Purpose:** Garbage collection worker. Identifies and deletes orphaned blobs and untagged manifests.

**GC modes:**
- `dry-run` — report what would be deleted, no deletions
- `manifests` — delete untagged manifests only
- `blobs` — delete orphaned blobs only (no manifest references)
- `full` — manifests then blobs

**GC algorithm:**
```
Phase 1 — Mark (read-only):
  1. Lock repository in registry-metadata (advisory lock, not table lock)
  2. Walk all tags → collect all referenced manifest digests
  3. Walk all manifests → collect all referenced blob digests
  4. Set of "live blobs" = union of all referenced digests

Phase 2 — Sweep:
  1. List all blobs in registry-storage for tenant
  2. For each blob not in live set AND older than GC_BLOB_MIN_AGE (default 1h):
     - Delete from registry-storage
     - Delete from blobs table in registry-metadata
     - Emit gc.blob_deleted event
  3. For each untagged manifest older than GC_MANIFEST_MIN_AGE (default 24h):
     - Delete manifest
     - Emit gc.manifest_deleted event

Phase 3 — Update quota:
  1. Recompute storage_used per repository
  2. Update registry-metadata
```

**Advisory locking:**
- `pg_try_advisory_lock` (non-blocking) keyed by FNV-64a hash of tenant UUID
- Single pinned connection via `pgxpool.Acquire()`; explicit `pg_advisory_unlock` + `Release()` in deferred unlock func
- `GC_ADVISORY_LOCK_DB_DSN` env var; graceful no-op when unset (single-worker mode)
- Tenants where lock cannot be acquired are skipped (counted in `Result.TenantsSkipped`)

**Safety rules:**
- Always run dry-run first in CI before scheduling
- Never delete blobs younger than `GC_BLOB_MIN_AGE` — in-flight pushes write blobs before manifests
- Emit audit event for every deletion
- GC is scheduled, not triggered by push events — run nightly by default (configurable cron)
- GC must be idempotent: safe to run multiple times

**GC status visibility (FE-API-032):**

> Bootstrap note: `services/gc` previously had neither a gRPC server nor a DB. As of 2026-06-21 (`92e6028`) it owns its own Postgres database with goose migrations under `services/gc/migrations/`. When `DB_DSN` is unset, the legacy in-process `runLoop` still works — the new persistence path is opt-in.

Table (canonical source `services/gc/migrations/20260621000001_gc_runs.sql`):
- `gc_runs(id, tenant_id NULL, mode, status, triggered_by, queued_at, started_at, completed_at, …)` — `status` ∈ `{queued, running, succeeded, failed}`; `mode` ∈ `{dry-run, manifests, blobs, full}`; `tenant_id` is NULL for cross-tenant cron sweeps; `triggered_by` is `cron` or a user_id.

gRPC service: `GCService` in `proto/gc/v1/gc.proto`:
- `GetStatus()` → `last_completed_at`, `last_status`, `next_scheduled_at` (best-effort: `last_completed_at + GC_RUN_INTERVAL_HOURS`)
- `RunNow(mode, tenant_id?)` — async: INSERTs a `queued` row + non-blocking send on a buffered channel, returns immediately with `{run_id, status: "queued"}`
- `ListRuns(page_token, limit)` — newest-first

Cron loop drains queued rows between ticks via `PersistedRunner.ClaimNextQueued` (`FOR UPDATE SKIP LOCKED`). Existing REM-009 per-tenant advisory locks still arbitrate concurrent sweeps.

---

## 12. registry-tenant

**Purpose:** Tenant lifecycle management, custom domain provisioning, per-tenant configuration.

**Responsibilities:**
- CRUD for tenants (super-admin API, not exposed to end users)
- Custom domain registration and verification (DNS TXT record or HTTP challenge)
- Per-tenant quota configuration
- Per-tenant feature flags (proxy cache enabled, signing required, scan policy)
- Provision tenant isolation: create org in `registry-metadata`, create S3 prefix/bucket policy

**Custom domain flow:**
1. Tenant submits domain `registry.acme.com`
2. System generates DNS TXT verification record
3. Tenant adds `_registry-verify.<domain>` TXT record
4. Background worker polls DNS until verified (max 48h)
5. On verification: trigger Let's Encrypt certificate issuance via gateway ACME
6. Store cert in Redis (Traefik reads it) or notify Nginx via API
7. Update `registry-gateway` routing table (Redis-backed, TTL-less)

**Notifications + backoff (REM-004):**
- `Notified24h`, `Notified48h` flags on `DomainRecord` for idempotent notifications
- 24h notification logged when age ≥ 24h; 48h failure notification at age ≥ 47h
- Exponential poll backoff: <1h → 5min, 1h–12h → 10min, >12h → 20min
- `next_poll_after` column + index; `ListUnverifiedDomains` filters `next_poll_after <= now()`

**FE-API-007 / 009 / 027 / 028 / 029 additions:**

Migrations:
- `20260620000001_add_tenant_slug.sql` — `tenants.slug TEXT NOT NULL` (backfilled via `regexp_replace + trim`; unique index).
- `20260620000002_add_domain_is_primary.sql` — `tenant_domains.is_primary BOOLEAN`; partial unique index `WHERE is_primary`; backfill picks the oldest verified per tenant.

`Tenant` proto extended: `slug` (5), `host` (6), `host_is_custom` (7), `domains[]` (8) — append-only field numbers preserved. Host algorithm in `GRPCHandler.buildTenantProto`: primary verified domain wins → `host = domain`, `host_is_custom = true`; otherwise `host = <slug>.<PLATFORM_BASE_DOMAIN>`. `MarkDomainVerified` auto-promotes the first verified domain in the same tx.

New gRPC RPCs:
- `ListTenants(page_size, page_token)` — base64url(`created_at|id`) cursor for stable ordering.
- `UpdateTenant(id, name?, plan?)` — FE-API-029; rename cascade recomputes slug **atomically inside the same tx** so no observable state has new-name with old-slug. Validation: name regex `^[a-z0-9][a-z0-9-]{1,63}$`, plan ∈ `{free, pro, enterprise}`. Per-field events `tenant.renamed` + `tenant.plan_changed` (patching both fires two events).
- `ListTenantDomains(tenant_id)` — `verification_token` + derived `txt_record_name` surfaced on the admin-gated FE response so the dashboard can re-display the TXT challenge after the register dialog closes (DSGN-021). Same gate as `RegisterDomain`, so disclosure adds no new privilege.
- `VerifyDomainNow(tenant_id, domain)` — synchronous re-check via swappable `txtLookup` package var.
- `SetPrimaryDomain(tenant_id, domain)` — atomic SELECT verified → demote-all → promote-target RETURNING.
- `DeleteDomain(tenant_id, domain)` — returns `X-Janus-Warning: primary-domain-removed` when removing the primary (warning lives on the management response).

PATCH `is_primary:false` → 400 with `"is_primary must be true; delete the domain to clear primary"` — silently demoting would orphan the host onto the wildcard fallback.

`PLATFORM_BASE_DOMAIN` env var (default `registry.localhost`) — wildcard-platform guard rejects any domain ending in `.<PLATFORM_BASE_DOMAIN>`.

---

## 13. registry-management

**Purpose:** Management REST API — BFF (Backend For Frontend) serving the React dashboard and any CI/CD tooling, CLIs, or Terraform providers that need programmatic access to registry metadata. Translates HTTP REST calls into gRPC calls against `registry-auth` and `registry-metadata`. No gRPC server of its own.

**Endpoints (HTTP, default port `:8085`):**

```
GET  /healthz                             # Health check (no auth required)

# Stats & overview
GET  /api/v1/stats                        # Tenant-scoped aggregated stats

# Repository management
GET  /api/v1/repositories                 # List repositories for tenant
POST /api/v1/repositories                 # Create repository
GET  /api/v1/repositories/:org/:repo      # Get single repository
PATCH /api/v1/repositories/:org/:repo     # Update repository (description + immutable_tags + require_signature)
DELETE /api/v1/repositories/:org/:repo    # Delete repository

GET    /api/v1/repositories/:org/:repo/trusted-keys             # List the trusted-key allowlist (Phase 2)
POST   /api/v1/repositories/:org/:repo/trusted-keys             # Approve a signing key (body: {key_id, display_name?})
DELETE /api/v1/repositories/:org/:repo/trusted-keys/:key_id     # Revoke an approval — removing the last entry widens the gate back to Phase 1

# Tag management
GET  /api/v1/repositories/:org/:repo/tags          # List tags
DELETE /api/v1/repositories/:org/:repo/tags/:tag   # Delete tag
POST   /api/v1/repositories/:org/:repo/tags/:tag/pin   # Pin tag (futures.md Tier 1 #2; repo admin)
DELETE /api/v1/repositories/:org/:repo/tags/:tag/pin   # Unpin tag

# Vulnerability scanning
GET  /api/v1/repositories/:org/:repo/tags/:tag/scan  # Get scan result for a tag
POST /api/v1/repositories/:org/:repo/tags/:tag/scan  # Trigger a scan
POST /api/v1/repositories/:org/:repo/scan            # Bulk scan every image tag in repo (S-MAINT-1 F1)
POST /api/v1/orgs/:org/scan                          # Bulk scan every image tag in every repo of org (org admin; S-MAINT-1 F1)

# Build / audit history
GET  /api/v1/repositories/:org/:repo/tags/:tag/builds  # List build history

# RBAC member management
GET    /api/v1/orgs/:org/members                               # List org members (all roles)
POST   /api/v1/orgs/:org/members                               # Grant role to user in org (admin+ only)
DELETE /api/v1/orgs/:org/members/:assignmentID                 # Revoke org role assignment (admin+ only)
GET    /api/v1/repositories/:org/:repo/members                 # List repo members (all roles)
POST   /api/v1/repositories/:org/:repo/members                 # Grant role to user in repo (admin+ only)
DELETE /api/v1/repositories/:org/:repo/members/:assignmentID   # Revoke repo role assignment (admin+ only)

# Webhooks (FE-API-021..024, 035)
GET    /api/v1/webhooks                                # List endpoints (admin-only — PENTEST-027)
POST   /api/v1/webhooks                                # Create — secret returned once
PATCH  /api/v1/webhooks/:id                            # Edit URL / events / active
DELETE /api/v1/webhooks/:id
GET    /api/v1/webhooks/:id/deliveries                 # Delivery log (admin-only)
GET    /api/v1/webhooks/:id/deliveries/:delivery_id    # Single delivery detail (payload, signature, response_body)
POST   /api/v1/webhooks/:id/test                       # Synchronous test dispatch (15s deadline)
POST   /api/v1/webhooks/:id/rotate-secret              # New HMAC secret — returned once

# Workspace (FE-API-009, 027)
GET    /api/v1/workspace/me                            # Tenant identity (slug, host, host_is_custom, domains[])
GET    /api/v1/workspace/me/domains                    # Custom domain list (admin-only; includes verification_token + txt_record_name per DSGN-021)
POST   /api/v1/workspace/me/domains                    # Register — returns DNS TXT challenge
POST   /api/v1/workspace/me/domains/:domain/verify     # Re-run DNS TXT check synchronously
PATCH  /api/v1/workspace/me/domains/:domain            # Set is_primary=true (false → 400)
DELETE /api/v1/workspace/me/domains/:domain            # X-Janus-Warning header if removing primary

# Notifications / analytics (FE-API-008, 030)
GET    /api/v1/notifications                           # Poll-based notifications (since, event_types, unread_only)
GET    /api/v1/repositories/:org/:repo/analytics       # Repo-scoped time-series
GET    /api/v1/stats/analytics                         # Tenant-wide analytics
GET    /api/v1/stats/storage                           # Per-repo storage breakdown (FE-API-031)

# Security center (FE-API-014..020)
GET    /api/v1/security/overview                       # Open vulns + scan coverage + recency
GET    /api/v1/security/vulnerabilities                # CVE rollup with affected[]
GET    /api/v1/security/scans                          # Scan history (trigger field)
GET    /api/v1/security/remediation                    # Upgrade groups
PUT    /api/v1/security/policy                         # Update scan policy (admin)
GET    /api/v1/security/policy
POST   /api/v1/security/reports/generate               # Compliance report job
GET    /api/v1/security/reports
GET    /api/v1/security/reports/:id
GET    /api/v1/security/reports/:id/download/:format   # pdf | sbom

# Per-tag manifest / signing / SBOM (FE-API-002 / 003 / 025 / 026 / 033)
GET    /api/v1/repositories/:org/:repo/tags/:tag/manifest
GET    /api/v1/repositories/:org/:repo/tags/:tag/signature[?verify=true]
POST   /api/v1/repositories/:org/:repo/tags/:tag/sign
GET    /api/v1/repositories/:org/:repo/tags/:tag/sbom?format=spdx-json
GET    /api/v1/repositories/:org/:repo/tags/:tag/chart # Helm chart detail (FUT-022): Chart.yaml metadata + values.yaml; 404 when core gRPC unset; 400 when the tag isn't a Helm chart
GET    /api/v1/repositories/:org/:repo/activity        # FE-API-004
DELETE /api/v1/repositories/:org/:repo/tags            # Bulk tag delete (FE-API-036; body {tag_names[]})

# Platform admin (FE-API-028 / 029 / 032)
GET    /api/v1/admin/tenants                           # platform-admin marker (org=*, admin)
POST   /api/v1/admin/tenants
GET    /api/v1/admin/tenants/:id                       # Composition: tenant + usage + user count + last push
PATCH  /api/v1/admin/tenants/:id                       # Rename + plan change
DELETE /api/v1/admin/tenants/:id
PUT    /api/v1/admin/tenants/:id/quota
GET    /api/v1/admin/gc/status                         # FE-API-032
GET    /api/v1/admin/gc/runs
POST   /api/v1/admin/gc/run
```

**Rules:**
- Every route except `/healthz` is wrapped with `RequireAuth` middleware (validates JWT against `registry-auth` gRPC)
- `TenantIDFromContext` used for every metadata gRPC call — never a user-supplied header or body value
- Error responses: `{"error":"<generic message>"}` only — no gRPC status codes, stack traces, or internal service names
- `CORS_ALLOWED_ORIGIN` env var controls allowed origin; dev default `http://localhost:5173`
- mTLS configured for gRPC connections in production (`MTLS_CA_CERT_PATH`, `MTLS_CERT_PATH`, `MTLS_KEY_PATH`)
- HTTP server timeouts: `ReadTimeout: 10s`, `WriteTimeout: 30s`, `IdleTimeout: 120s`
- RBAC DELETE routes (delete repo, delete tag) require caller to hold at least `writer` role. Member grant/revoke endpoints require `admin` or `owner`. Checked via `GetUserPermissions` before the operation proceeds.

**gRPC calls made by this service:**
- `registry-auth`: `ValidateToken`, `GetUserPermissions`, `GrantRole`, `RevokeRole`, `ListMembers`, `CountTenantUsers`
- `registry-metadata`: all of the repository / tag / scan / security-center / tenant-usage / SBOM RPCs listed in §5
- `registry-audit`: `WriteEvent`, `GetBuildHistory`, `GetDailyPullCount`, `GetRepoActivity`, `GetNotifications`, `GetAnalytics`, `GetLastTenantPush`
- `registry-tenant`: `GetTenant`, `ListTenants`, `UpdateTenant`, `ListTenantDomains`, `VerifyDomainNow`, `SetPrimaryDomain`, `DeleteDomain`
- `registry-signer` (opt-in via `SIGNER_GRPC_ADDR`): `ListSignatures`, `SignManifest`, `VerifyManifest`
- `registry-scanner` (opt-in via `SCANNER_GRPC_ADDR`): scan-policy + compliance-report RPCs
- `registry-webhook` (opt-in via `WEBHOOK_GRPC_ADDR`): endpoint CRUD + deliveries + test/rotate
- `registry-gc` (opt-in via `GC_GRPC_ADDR`): `GetStatus`, `RunNow`, `ListRuns`
- `registry-core` (opt-in via `CORE_GRPC_ADDR`): `GetBlob` — backs the Helm chart-detail route (FUT-022); route returns 404 when unset

When a downstream service's gRPC address is unset, the BFF returns `404 "route disabled"` on the corresponding routes rather than failing the whole service.

**Environment variables:**
```
HTTP_ADDR=:8085
AUTH_GRPC_ADDR=                  # required
METADATA_GRPC_ADDR=              # required
AUDIT_GRPC_ADDR=                 # required
TENANT_GRPC_ADDR=                # required for admin tenant routes + /workspace/me
SIGNER_GRPC_ADDR=                # optional — FE-API-003/025/026
SCANNER_GRPC_ADDR=               # optional — FE-API-018/019
WEBHOOK_GRPC_ADDR=               # optional — FE-API-021..024/035
GC_GRPC_ADDR=                    # optional — FE-API-032
RABBITMQ_URL=                    # required (scan.queued + image.signed publishes)
CORS_ALLOWED_ORIGIN=             # required in production; default http://localhost:5173 in dev
MTLS_CA_CERT_PATH=               # required in production
MTLS_CERT_PATH=                  # required in production
MTLS_KEY_PATH=                   # required in production
```

**Scan trigger flow (end to end):**
- `POST /api/v1/repositories/:org/:repo/tags/:tag/scan` validates the repo + tag, then publishes a `scan.queued` event to RabbitMQ.
- `registry-scanner` binds the `scanner.scan.queued` queue (in addition to its `push.completed` queue) and routes through `worker.Pool.HandleScanQueued`, which allocates a scan_id and enqueues the job.
- See `services/management/internal/handler/handler.go` `handleTriggerScan` and `services/scanner/internal/worker/worker.go` `ScanQueuedConsumerConfig`.
