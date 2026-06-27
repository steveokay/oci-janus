# Security Issues

> Last updated: 2026-06-27 — SEC-037 logged (LOW, single-statement onboarding backfill could contend with login UPDATEs on large user tables) from Phase 4.3 §1 review of commit `ec43e05`.
>
> Last audited: 2026-06-21 — Round-3 PENTEST-029 / 031 / 032 verified RESOLVED in the codebase; PENTEST-033 verified PARTIAL (login password now `{{password}}` secret-typed env var, but `NewUser1234!` still inlined in createUser body and dev tenant UUID still defaulted in environment file). PENTEST-030 remains OPEN (no per-endpoint test-dispatch throttle yet).
>
> Last updated: 2026-06-19 (SEC-001..SEC-036 all resolved; PENTEST-001..026 all resolved. **Round 3 (2026-06-19):** post-merge review of FE-API-001/010/021..024 + the 00004 manifest backfill migration on branch `feat/frontend-rebuild` — 7 new findings (0 critical, 2 high, 3 medium, 2 low). **PENTEST-027 + PENTEST-028 (both HIGH) resolved same day** — webhook list/deliveries routes gated by `requireWebhookAdmin`; dispatcher errors sanitised so persisted `last_error` never carries URL-embedded tokens; manifest backfill split out of the migration into an idempotent `psql` runbook with a high-water-mark cursor + per-batch commits. PENTEST-029..033 (3 medium + 2 low) remain OPEN as follow-ups.)
> This file tracks all known security issues, findings, and open remediations across the platform.
> Sensitive details (CVEs, exploit paths) should not be committed here — link to a private issue tracker for those.

---

## Legend

| Severity | Meaning |
|---|---|
| `CRITICAL` | Exploitable now, immediate remediation required |
| `HIGH` | Significant risk, fix before next release |
| `MEDIUM` | Moderate risk, fix within current sprint |
| `LOW` | Minor risk, fix when convenient |
| `INFO` | Informational, no direct risk |

| Status | Meaning |
|---|---|
| `OPEN` | Not yet addressed |
| `IN PROGRESS` | Being remediated |
| `MITIGATED` | Workaround applied, full fix pending |
| `RESOLVED` | Fixed and verified |
| `ACCEPTED` | Risk accepted with documented rationale |
| `WONT FIX` | Out of scope, documented reason required |

---

## Open Issues

> No open security findings as of 2026-06-18 (excluding pentest findings below).
> Backend feature gaps (KMS signing backends, Notary v2, etc.) are tracked in
> `status.md` Sprint 6 — those are unimplemented features rather than
> security regressions, so they live in the project tracker, not here.

---

## Pentest Findings — 2026-06-18

> Findings from a thorough security review of the system. Each item is logged
> with a reproducible description and a concrete remediation path so they can
> be triaged into a fix sprint. ID prefix `PENTEST-` keeps these separate from
> the original SEC- items (which were author-flagged during development).
>
> **Triage tip:** CRITICAL and HIGH should be fixed before any non-local
> deployment. MEDIUM should be fixed before GA. LOW and INFO can be batched.

### CRITICAL

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-001 | CRITICAL | Audit HTTP API unauthenticated | `registry-audit` | RESOLVED ✅ (2026-06-18) |

**PENTEST-001 — Audit HTTP API has no authentication** — RESOLVED ✅
- **Original issue:** `services/audit/internal/server/server.go:100-101` registered `POST /audit/events` and `GET /audit/events` with no auth middleware. Any process that could reach the audit pod's HTTP port (8080 by default) could forge audit log entries for any tenant, read every tenant's audit trail, or DoS the audit pipeline.
- **Resolution (2026-06-18):** Applied remediation option (c) — **removed the HTTP write/query API entirely**. Verified via grep that no caller anywhere in the codebase consumed `POST/GET /audit/events`; the endpoints were dead code. All audit writes already flow through the RabbitMQ `eventconsumer` (durable + DLQ via `audit.events` queue with routing key `#`), and reads flow through the mTLS-gated `AuditService` gRPC API consumed by `registry-management.GetBuildHistory`. The fix:
  1. Removed route registrations from `services/audit/internal/server/server.go`
  2. Deleted `services/audit/internal/handler/http.go` (the unused `HTTPHandler`, `WriteEvent`, `QueryEvents`)
  3. Retained `/healthz` on the HTTP port for liveness probes
  4. Added a comment block in `server.go` documenting that re-introducing an HTTP write/query API requires mTLS + CN allowlist
- **Defense-in-depth result:** Audit log integrity now depends only on (1) mTLS on the gRPC port + (2) FORCE RLS + `registry_audit_app` low-privilege role (SEC-001) + (3) RabbitMQ DLQ for malformed events. No HTTP attack surface.

---

### HIGH

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-002 | HIGH | RBAC scope not enforced in management API | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-003 | HIGH | Public user creation with arbitrary tenant_id | `registry-auth` | RESOLVED ✅ (2026-06-18) |
| PENTEST-004 | HIGH | Username enumeration via login timing attack | `registry-auth` | RESOLVED ✅ (2026-06-18) |
| PENTEST-005 | HIGH | Username enumeration via lockout/disabled status codes | `registry-auth` | RESOLVED ✅ (2026-06-18) |

**PENTEST-002 — RBAC scope not enforced in management API** — RESOLVED ✅
- **Original issue:** `getUserRoles` returned a flat list of role names for the entire tenant. All RBAC enforcement sites used `hasRole(roles, "admin")` without scope, so admin-of-org-A could grant/revoke roles in org-B, delete repos in org-B, etc.
- **Resolution (2026-06-18):** Introduced a scope-aware authorization model end-to-end:
  1. **Proto:** added `repeated RoleAssignment role_assignments = 3` to `GetUserPermissionsResponse` so callers receive full per-scope assignment info (not just role names).
  2. **Auth backend:** `services/auth/internal/handler/grpc.go` `GetUserPermissions` now populates the new field with `{role, scope_type, scope_value, id}` for every assignment.
  3. **Management:** added `getUserAssignments(r)` and `hasScopedRole(assignments, scopeType, scopeValue, minRole)` in `services/management/internal/handler/rbac.go`. The helper implements the containment rule: org-scoped grants cover all repos in that org (`"myorg/anything"` matches an org grant on `"myorg"`); repo grants do NOT bubble up to the parent org or sibling repos.
  4. **Updated every enforcement site** to call `hasScopedRole` with the URL's actual scope:
     - `handleCreateRepository` → `("org", body.Org, "admin")`
     - `handleDeleteRepository` → `("repo", "org/repo", "admin")`
     - `handleDeleteTag` → `("repo", "org/repo", "writer")`
     - `handleTriggerScan` → `("repo", "org/repo", "writer")` (was previously unchecked — bonus fix)
     - `handleGrantOrgMember` / `handleRevokeOrgMember` → `("org", org, "admin")`
     - `handleGrantRepoMember` / `handleRevokeRepoMember` → `("repo", "org/repo", "admin")`
  5. **Tests:** `services/management/internal/handler/rbac_test.go` adds 6 dedicated tests including the specific attack scenarios — `orgGrantDoesNotCoverSiblingOrg`, `repoGrantDoesNotCoverSiblingRepo`, and `orgPrefixIsNotSubstring` (a "my" admin must not match "myorg/...").
- **Cross-check:** the auth-side `GrantRole`/`RevokeRole` gRPC handlers still don't authorize the caller (they only insert/delete). This is acceptable because gRPC is mTLS-restricted to internal services that perform authz before calling — but if any new service ever calls these handlers, it must enforce scope-aware authz on its own caller too. Future hardening: add caller authz inside the auth gRPC handlers as defence-in-depth.

**PENTEST-003 — Public user creation with arbitrary tenant_id** — RESOLVED ✅
- **Original issue:** `POST /api/v1/users` was unauthenticated and accepted any `tenant_id` from the request body. Allowed account squatting, username enumeration via 409 responses, user-table DoS via Argon2 spam, and cross-tenant user injection (attacker logs in as the injected user and gets a JWT carrying the target tenant's UUID).
- **Resolution (2026-06-18):** Applied remediation option (a) — admin-only endpoint:
  1. `createUser` now calls `requireAuth` first; anonymous requests get `401 UNAUTHORIZED`.
  2. The target tenant is taken from the caller's JWT `tenant_id` claim. If `body.tenant_id` is supplied it must match — otherwise `403 DENIED "cannot create users in a different tenant"`.
  3. The caller must hold an `admin` or `owner` role somewhere in that tenant, verified via a new `callerIsTenantAdmin` helper that calls `svc.GetUserRoles` and fails closed on lookup error. Non-admins get `403 DENIED "admin role required to create users"`.
  4. Bootstrap (first user in a tenant) deliberately CANNOT happen through this endpoint — it must come from a seed migration (`services/auth/migrations/20260610000001_seed_dev_tenant.sql` does this for dev) or out-of-band tooling. This is by design: an unauthenticated bootstrap path would re-introduce the original vulnerability.
- **Tests:** `services/auth/internal/handler/http_test.go` adds three dedicated security tests — `TestCreateUser_noAuth_returns401`, `TestCreateUser_callerNotAdmin_returns403`, `TestCreateUser_crossTenant_returns403` — plus updates the existing happy-path tests to thread an admin token through the new `newAdminAuthedRequest` helper. All 7 createUser tests pass.
- **Follow-up considerations:** A future "platform admin" endpoint for super-admins who manage multiple tenants would need a separate route (e.g. `POST /api/v1/admin/tenants/{id}/users`) gated by a new platform-admin role marker — out of scope for this fix.

**PENTEST-004 — Username enumeration via login timing attack** — RESOLVED ✅
- **Original issue:** Unknown user → fast path (~5 ms, DB lookup only). Known user, wrong password → slow path (~100 ms, Argon2id verify). The reliable measurable gap let an attacker enumerate valid usernames over the network.
- **Resolution (2026-06-18):** Added `dummyArgonHash()` in `services/auth/internal/service/auth.go` — a lazily-generated (`sync.Once`) Argon2id hash of a throwaway password. In `AuthenticateUser`, when `GetByUsername` returns `ErrNotFound`, we still call `argon2pkg.Verify(password, dummyArgonHash())` and discard the result, so the wall-clock time matches the known-user-wrong-password path.
- **Tests:** `TestAuthenticateUser_unknownUsername_runsDummyVerify` directly measures both paths and fails if the ratio diverges by more than 4× — a deliberately loose threshold (CI flakiness) but tight enough to catch a regression that bypasses the dummy verify (would yield a >10× gap).

**PENTEST-005 — Username enumeration via lockout/disabled status codes** — RESOLVED ✅
- **Original issue:** `403 "account locked"` and `403 "account disabled"` leaked whether a username existed in the tenant.
- **Resolution (2026-06-18):** Both HTTP handlers (`/auth/token` and `/api/v1/login`) now collapse ALL auth-failure variants — unknown user, wrong password, locked, disabled — to one identical `401 UNAUTHORIZED "invalid credentials"` response. A new `logAuthFailure` helper classifies the underlying cause at `slog.Info`/`slog.Warn` server-side so ops still see lockout events. The typed errors (`ErrAccountLocked`, `ErrAccountDisabled`) remain in the service layer for internal flow control but never propagate to the wire.
- **Tests:** `TestLogin_unknownVsKnown_returnsSameStatusAndBody` asserts that probing an unknown username and a known username with the wrong password produces identical HTTP responses (same status, byte-identical body) — the explicit no-oracle guarantee. The three legacy tests that asserted the old `403 "account locked/disabled"` behavior were inverted to assert `401` (renamed `_returns401_noLeakage`).

---

### MEDIUM

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-006 | MEDIUM | Member list endpoints leak roles to non-admin tenant users | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-007 | MEDIUM | Webhook response body not size-limited | `registry-webhook` | RESOLVED ✅ (2026-06-18) |
| PENTEST-008 | MEDIUM | CORS middleware always returns allowed origin | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-009 | MEDIUM | WWW-Authenticate parser splits on comma naively | `registry-proxy` | RESOLVED ✅ (2026-06-18) |
| PENTEST-010 | MEDIUM | AUTH_REALM default uses HTTP, not HTTPS | `registry-core`, `registry-proxy` | RESOLVED ✅ (2026-06-18) |
| PENTEST-011 | MEDIUM | RBAC revoke does not verify assignment belongs to scope | `registry-management` | RESOLVED ✅ (2026-06-18) |

**PENTEST-006 — Member list leaks roles** — RESOLVED ✅
- **Original issue:** `handleListOrgMembers` and `handleListRepoMembers` had no role check; any authenticated tenant user could enumerate org/repo members.
- **Resolution (2026-06-18):** Both handlers now require at least `reader` on the target scope via `hasScopedRole`. Non-members receive `404 not found` (not `403 forbidden`) so the existence of the org/repo isn't confirmed. Bundled with the PENTEST-002 fix.

**PENTEST-007 — Webhook response body not size-limited** — RESOLVED ✅
- **Original issue:** `services/webhook/internal/delivery/dispatcher.go` drained the full response body with `io.Copy(io.Discard, resp.Body)` — no upper bound. A hostile webhook endpoint could stream unbounded bytes back, tying up worker goroutines for the full request timeout.
- **Resolution (2026-06-18):** Added `maxResponseBytes = 8 * 1024` constant and wrapped the discard copy with `io.LimitReader(resp.Body, maxResponseBytes)`. Webhook ACKs are typically empty or a few hundred bytes; 8 KB is generous. Same hardening applied opportunistically to the signer Vault key-fetch and sign paths (both now capped at 64 KB) — they previously read unbounded `io.ReadAll` on the error path.

**PENTEST-008 — CORS middleware always returns configured origin** — RESOLVED ✅
- **Original issue:** The middleware unconditionally echoed a fixed origin and never set `Vary: Origin`, weakening defense-in-depth and blocking any future multi-origin support.
- **Resolution (2026-06-18):** Rewrote `services/management/internal/middleware/cors.go` to:
  - Accept a comma-separated allowlist (`CORS_ALLOWED_ORIGIN=https://a.example,https://b.example`) — single-origin configurations still work since they're a one-element list.
  - Always emit `Vary: Origin` so caching proxies key on origin even for blocked responses.
  - Echo `Access-Control-Allow-Origin` only when the request's `Origin` is in the allowlist (exact RFC 6454 match, case-sensitive). Disallowed origins receive no CORS headers and the browser blocks via SOP.
  - Skip CORS headers entirely on non-CORS requests (no `Origin` header) so same-origin responses stay clean.
  - Always return 204 for OPTIONS, regardless of allowlist outcome, so an attacker can't probe the allowlist via preflight differences.
- **Tests:** 5 new tests in `cors_test.go`: allowed-origin echo, disallowed-origin omission (the defining PENTEST-008 test), no-Origin clean response, preflight-always-204, and case-sensitive matching.

**PENTEST-009 — `parseBearerChallenge` splits on `,` naively** — RESOLVED ✅
- **Original issue:** The parser used `strings.Split(header, ",")` which broke for quoted values containing commas (e.g. `scope="repository:foo,bar:pull"`), causing pull failures against any upstream registry that uses comma-bearing scopes.
- **Resolution (2026-06-18):** Rewrote `parseBearerChallenge` with a quote-aware tokenizer (`splitCommaRespectingQuotes`) that walks the header tracking quote state, plus an `unescapeQuoted` helper that resolves the RFC 7230 backslash escapes (`\"` → `"`, `\\` → `\`) inside quoted strings. Comma-bearing scopes are now preserved as a single value.
- **Tests:** 4 new tests in `parse_challenge_test.go`: simple Docker Hub-style header, the defining quoted-comma case, escaped quotes inside a value, and tolerance of malformed segments (extra whitespace, missing `=`).

**PENTEST-010 — AUTH_REALM defaults to HTTP** — RESOLVED ✅
- **Original issue:** Both `registry-core` and `registry-proxy` defaulted `AUTH_REALM` to `http://localhost:8080/auth/token`. An operator deploying without overriding it would direct Docker clients to send Basic-auth credentials over plaintext.
- **Resolution (2026-06-18):** Added `validateAuthRealm(realm, environment)` in both `services/core/internal/config/config.go` and `services/proxy/internal/config/config.go`. The validator:
  - **Refuses** `http://` when `OTEL_ENVIRONMENT` is `production` or `staging` — startup fails fast with a clear error.
  - **Warns** at `slog.Warn` when `http://` is used in any other environment (development, empty, etc.) so misconfiguration is visible in logs.
  - **Accepts** `https://` everywhere; rejects other schemes (`ftp://` etc.) outright.
  - Scheme matching is case-insensitive (`HTTPS://` accepted) but the rest of the URL is preserved verbatim.
- **Tests:** 10 table-driven subtests in `auth_realm_test.go` covering every combination of scheme × environment plus malformed-URL / case-folding paths. Both core and proxy share the same validator semantics so the test coverage applies to both.

**PENTEST-011 — Revoke does not verify assignment belongs to scope** — RESOLVED ✅
- **Original issue:** Both revoke handlers passed the assignment ID to the auth gRPC `RevokeRole` without verifying the assignment's scope matched the URL path. Admin-of-org-A could delete assignments in org-B by URL-guessing or by visibility through `ListMembers`.
- **Resolution (2026-06-18):** Added two new fields to `RevokeRoleRequest` proto — `expected_scope_type` and `expected_scope_value`. Management's revoke handlers populate them from the URL path. Auth's `RevokeRoleScoped` repository method extends the DELETE SQL with `($3 = '' OR scope_type = $3) AND ($4 = '' OR scope_value = $4)` so a mismatched assignment matches zero rows and returns `ErrNotFound`. Auth's gRPC handler maps that to `codes.NotFound` indistinguishable from "row doesn't exist" — preventing scope enumeration via error differences.

---

### LOW

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-012 | LOW | TLS minimum version is 1.2 | `libs/auth/mtls` | RESOLVED ✅ (2026-06-18) |
| PENTEST-013 | LOW | Authorization header parsing is case-sensitive | `registry-management`, `registry-core` | RESOLVED ✅ (2026-06-18) |
| PENTEST-014 | LOW | No per-tenant read rate limit on management API | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-015 | LOW | Dashboard `useUserIsAdmin` reads from non-existent localStorage entry | `frontend/` | RESOLVED ✅ (2026-06-18) |
| PENTEST-016 | LOW | Audit HTTP `QueryEvents` allows unbounded `limit` param | `registry-audit` | RESOLVED ✅ (2026-06-18) |

**PENTEST-012 — TLS 1.2 minimum** — RESOLVED ✅
- **Original issue:** `libs/auth/mtls/mtls.go` set `MinVersion: tls.VersionTLS12` for both server and client mTLS configs. Internal service-to-service mTLS has no legacy-client constraint and should mandate the modern baseline.
- **Resolution (2026-06-18):** Set `MinVersion: tls.VersionTLS13` in both `ServerTLSConfig` and `ClientTLSConfig`. TLS 1.3 mandates forward secrecy and AEAD-only cipher suites, removing legacy renegotiation. No external clients touch these gRPC ports — all calls are service-to-service inside the cluster.

**PENTEST-013 — Authorization header case-sensitive parse** — RESOLVED ✅
- **Original issue:** Hand-rolled `strings.HasPrefix(authHeader, "Bearer ")` checks rejected `bearer xyz` (lowercase) and other case variants even though RFC 7235 §2.1 makes auth scheme names case-insensitive.
- **Resolution (2026-06-18):** Created `libs/auth/bearer/bearer.go` with `Extract(authHeader)` that case-insensitively matches the `Bearer` scheme and returns the token plus a found-flag. Updated every parsing site (`registry-auth` `requireAuth` + `refreshToken`, `registry-core` `authenticate`, `registry-proxy` `authenticate`, `registry-management` `RequireAuth`) to use the helper. Basic-auth parsing in core/proxy also switched to `strings.EqualFold` for symmetry.
- **Tests:** 12 table-driven cases in `bearer_test.go` covering all-uppercase, all-lowercase, mixed-case, tab separator, scheme-only, empty, Basic-scheme rejection, and the `BearerExt`-confusable rejection.

**PENTEST-014 — No per-tenant read rate limit** — RESOLVED ✅
- **Original issue:** No per-user cap on `/api/v1/*` reads. An authenticated tenant user could hammer stats/repositories endpoints to drive load on metadata + audit.
- **Resolution (2026-06-18):** Added `PerUserRateLimiter` in `services/management/internal/middleware/ratelimit.go`:
  - In-process token bucket via `golang.org/x/time/rate`, keyed by user_id from `UserIDFromContext`.
  - Default 20 rps with burst 40 — generous for an interactive dashboard, blocks a runaway script.
  - Background GC sweeps stale buckets every 5 minutes (10-minute idle TTL), keeping memory bounded.
  - Returns `429 Too Many Requests` with `Retry-After: 1` when exceeded.
  - Passes through requests without an authenticated user_id (e.g. `/healthz`) so unauthenticated probes don't poison everyone's bucket.
  - Wired into `Handler.Register` after `RequireAuth` populates context, so the limiter sees the user_id. Optional via `WithRateLimiter` for tests that need deterministic timing.
- **Multi-replica note:** in-process by design; with N replicas the effective cluster cap is N×20 rps. A Redis-backed limiter can drop in transparently if a global cap is needed, by satisfying the same `Middleware` signature.

**PENTEST-015 — `useUserIsAdmin` reads non-existent localStorage entry** — RESOLVED ✅
- **Original issue:** `dashboard/index.tsx:22` read `localStorage.getItem('auth_token')` — a key that's never written anywhere (the token lives only in Zustand memory per FE-SEC-001). The hook always returned `false`, so admin UI was permanently hidden.
- **Resolution (2026-06-18):**
  1. Added `roles?: string[]` to `AuthUser` in `frontend/src/store/authStore.ts` so the existing `JSON.parse(atob(...)) as AuthUser` decode path picks up the JWT `roles` claim end-to-end (backend already emits it per the PENTEST-002 / roles-claim work).
  2. Rewrote `useUserIsAdmin` in `frontend/src/routes/_authenticated/dashboard/index.tsx` to read `roles` from `useAuthStore` and check `includes('admin') || includes('owner')`.
- **Verified:** frontend `tsc --noEmit` clean. End-to-end chain: backend `Login` → JWT roles claim → Zustand store → admin UI gate.

**PENTEST-016 — Audit `limit` param** — RESOLVED ✅ (by removal)
- **Resolution (2026-06-18):** The entire audit HTTP API (`POST/GET /audit/events`) was removed in the PENTEST-001 fix. The `limit` query parameter no longer exists. The audit query path is now gRPC-only (`AuditService.GetBuildHistory`), which enforces its own server-side cap.

---

### INFORMATIONAL

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-017 | INFO | Default dev credentials in docker-compose | `infra/` | RESOLVED ✅ (2026-06-18) |
| PENTEST-018 | INFO | `sslmode=prefer` in dev compose | `infra/` | RESOLVED ✅ (2026-06-18) |
| PENTEST-019 | INFO | Scanner plugin cache directory writable by non-root | `registry-scanner` | RESOLVED ✅ (2026-06-18) |
| PENTEST-020 | INFO | No CSRF protection on state-changing management endpoints | `registry-management` | RESOLVED ✅ (2026-06-18) (accepted-with-conditions, code-level guard added) |

**PENTEST-017 — Default dev credentials** — RESOLVED ✅
- **Original risk:** A docker-compose deployment promoted to a non-local environment without overriding `POSTGRES_PASSWORD`, `RABBITMQ_DEFAULT_PASS`, `MINIO_ROOT_PASSWORD`, or `VAULT_DEV_ROOT_TOKEN` would silently ship with publicly-known credentials.
- **Resolution (2026-06-18):** Added `CheckDevDefaults` and `CheckDevDefaultsFromDSN` to `libs/config/loader/dev_defaults.go`. A central `wellKnownDevDefaults` map enumerates every default credential shipped in compose. Behaviour:
  - **`OTEL_ENVIRONMENT=production` or `staging`:** any match returns a startup error that names the offending env var. The process refuses to start.
  - **Any other environment (development, empty):** matches log at `slog.Warn` so the operator sees them at boot.
- **Wiring:** `DBConfig.PoolConfig()` now calls `CheckDevDefaultsFromDSN` automatically, so every service that uses the shared pool helper (auth, metadata, audit, tenant, webhook, proxy) gets the check for free. Storage (`STORAGE_MINIO_SECRET_KEY`) and signer (`VAULT_TOKEN`) call `CheckDevDefaults` explicitly in their `validate` functions. The three services that build temp `DBConfig` structs (audit, webhook, tenant) now pass `Environment: cfg.OTELEnvironment` so the check engages there too.
- **Tests:** 14 cases in `dev_defaults_test.go` cover production-rejection, staging-rejection, dev-warning, strong-credential acceptance, unknown-env tolerance, unknown-credential-name passthrough, and DSN password extraction for both postgres-URL and amqp-URL formats.

**PENTEST-018 — `sslmode=prefer` in dev compose** — RESOLVED ✅ (already mitigated)
- **Original risk:** Dev compose uses `sslmode=prefer` which silently falls back to plaintext if the server lacks a cert.
- **Resolution:** Three layered mitigations cover this:
  1. **SEC-022:** `loader.PoolConfig()` rejects `sslmode=disable` outright at startup.
  2. **SEC-022 continued:** Any sslmode weaker than `require` emits a `slog.Warn` at boot listing the offending DSN parameter.
  3. **PENTEST-017 (above):** in `OTEL_ENVIRONMENT=production`, the dev-default password (which is what gets transmitted in cleartext under `prefer`) also refuses to start. So even if `prefer` survives into production, the password check blocks first.
- The `prefer` mode remains supported in dev because the embedded postgres compose service runs without TLS — switching it to `require` would break local-dev startup.

**PENTEST-019 — Scanner plugin cache writable** — RESOLVED ✅ (documented + alternative hardening path)
- **Original risk:** `/trivy-cache` is writable by the scanner process. A subverted Trivy binary could write malicious DB files into the cache.
- **Resolution (2026-06-18):** Codified the trust model in `services/scanner/Dockerfile` with an inline comment that lists all three in-place mitigations (binary SHA256 verification via `SCANNER_PLUGIN_CHECKSUM`, non-root execution as UID 65532, read-only container FS outside cache + tmp) plus the recommended hardening path for operators who need stricter cache integrity (tmpfs-backed overlay, or pre-baked read-only DB layers with `TRIVY_NO_PROGRESS`). The `infra/runbooks/scanner-cache-hardening.md` runbook reference is the deployment-time follow-up.
- The risk is bounded: an attacker who can swap the Trivy binary already controls the scanner process, so cache-tampering doesn't expand impact beyond what plugin-binary tampering already provides — and that path is checksum-blocked.

**PENTEST-020 — No CSRF protection on management API** — RESOLVED ✅ (accepted-with-conditions, code-level guard)
- **Original posture:** No CSRF tokens, but JWT in `Authorization` header (not cookies) + strict CORS allowlist makes CSRF impossible by construction.
- **Resolution (2026-06-18):**
  1. Documented the load-bearing assumption in `services/management/internal/middleware/auth.go` with a multi-line comment on `RequireAuth` explaining why the current architecture is CSRF-immune and exactly what would need to change if cookie-based auth is ever added.
  2. Added an `assertNoCookieAuth` package-level marker string that doubles as a search target for future code reviewers: anyone searching for `r.Cookie(` in this file should get zero hits. Any future patch that adds cookie auth would have to also delete this marker, which a reviewer would notice.
- **Re-open trigger:** when FE-SEC-009 (refresh tokens via `HttpOnly` cookie) is implemented, this finding must reopen with CSRF tokens (double-submit cookie pattern or per-session token in header) added alongside.

---

## Pentest Findings Summary

| Severity | Total | Open | Resolved |
|---|---|---|---|
| CRITICAL | 1 | 0 | 1 (PENTEST-001 ✅) |
| HIGH | 4 | 0 | 4 (PENTEST-002 ✅, 003 ✅, 004 ✅, 005 ✅) |
| MEDIUM | 7 | 0 | 7 (PENTEST-006..011 ✅, 021 ✅) |
| LOW | 9 | 0 | 9 (PENTEST-012..016 ✅, 022..025 ✅) |
| INFO | 5 | 0 | 5 (PENTEST-017..020 ✅, 026 ✅) |
| **TOTAL** | **26** | **0** | **🎯 26/26 ✅** |

**🎉 Pentest fully closed across both rounds — every finding (CRITICAL, HIGH, MEDIUM, LOW, INFO) is resolved.** The codebase has no known open security findings as of 2026-06-18.

**🎯 Pentest review is fully closed. 20/20 findings resolved across all severities.**

Fix order completed:
1. ✅ PENTEST-001 — Audit HTTP API removed
2. ✅ PENTEST-002 + 011 + 006 — RBAC scope enforcement
3. ✅ PENTEST-003 — Admin-only user creation
4. ✅ PENTEST-004 + 005 — Username-enumeration mitigations
5. ✅ PENTEST-007–010 — Defense-in-depth (webhook body cap, CORS allowlist, RFC 7235 parser, HTTPS AUTH_REALM)
6. ✅ PENTEST-012–016 — LOW hardening (TLS 1.3, case-insensitive Bearer, rate limit, frontend admin gate, audit limit moot)
7. ✅ PENTEST-017–020 — INFO operator gates (dev-default credentials check, sslmode triple-mitigation, scanner cache documented, CSRF posture asserted)

Re-open triggers to monitor:
- **PENTEST-020** must reopen alongside any cookie-based refresh-token work (FE-SEC-009).
- **PENTEST-019** should be revisited if Trivy ever ships a CVE in its DB-load path; the runbook lists the tmpfs-overlay mitigation.

---

## Pentest Round 2 — 2026-06-18 (post-fix broader scan)

> Round-2 review after the original 20-finding fix landed. Goal: catch any
> regression introduced by my own fixes, plus scan services not deep-dived
> the first time (storage, gateway, gc, RabbitMQ paths). 6 new findings;
> none CRITICAL or HIGH.

### MEDIUM

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-021 | MEDIUM | Storage gRPC handler leaks raw error messages | `registry-storage` | RESOLVED ✅ (2026-06-18) |

**PENTEST-021 — Storage handler leaks internal error detail** — RESOLVED ✅
- **Original issue:** `mapErr` in `services/storage/internal/handler/grpc.go` returned `status.Error(codes.Internal, err.Error())`, exposing driver text (MinIO/S3/GCS/Azure paths, IAM principals, signed-URL fragments) on the wire.
- **Resolution (2026-06-18):** Replaced `mapErr` with `mapErrCtx(ctx, op, err)` that logs the full error via `slog.ErrorContext` (preserving trace_id + tenant_id through the slog handler) and returns a generic `status.Error(codes.Internal, "internal error")` to callers. Updated every call site (12 in `grpc.go`) to pass its request context plus an op name. Test `TestMapErrCtx_unknownError_returnsGenericInternalMessage` uses a deliberately-leaky driver error (`AccessDenied: arn:aws:s3:::secret-bucket/...`) and asserts the wire message is exactly `"internal error"` — fails if a future change re-introduces the leak.

### LOW

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-022 | LOW | sigstore DB calls use `context.Background()` | `registry-signer` | RESOLVED ✅ (2026-06-18) |
| PENTEST-023 | LOW | Scanner Enqueue spawns unbounded goroutines on queue full | `registry-scanner` | RESOLVED ✅ (2026-06-18) |
| PENTEST-024 | LOW | `handleSetTenantQuota` uses unscoped `hasRole()` | `registry-management` | RESOLVED ✅ (2026-06-18) |
| PENTEST-025 | LOW | `PerUserRateLimiter.gcLoop` has no stop signal | `registry-management` | RESOLVED ✅ (2026-06-18) |

**PENTEST-022 — `sigstore.Store` uses Background context** — RESOLVED ✅
- **Original issue:** `Add`, `List`, `FindRec` all swapped the caller's request context for `context.Background()`, so cancelled gRPC requests left DB connections pinned.
- **Resolution (2026-06-18):** Added `ctx context.Context` as the first parameter on `Store.List(ctx, ...)` and `Store.FindRec(ctx, ...)` so the caller's request context propagates and cancellation reaches the DB. `Store.Add` keeps its decoupled-context pattern but now wraps with `context.WithTimeout(context.Background(), 5*time.Second)` to hard-cap pool use. Handler callers updated; `slog.Error` upgraded to `slog.ErrorContext` so trace_id flows into logs.

**PENTEST-023 — Scanner Enqueue unbounded goroutine spawn** — RESOLVED ✅
- **Original issue:** Queue-full fallback at `worker.go:103` did `go p.runJob(context.Background(), job)`, so a flood of `push.completed` events could spawn unbounded goroutines.
- **Resolution (2026-06-18):** Rewrote `Enqueue` to return an `error`. A short blocking attempt (50 ms) absorbs micro-bursts; if the queue is still full, `ErrQueueFull` is returned. `HandlePushCompleted` and `HandleScanQueued` propagate that as an error to the RabbitMQ consumer, which NACKs — the broker re-delivers after backoff (the correct backpressure signal). Total goroutine concurrency is now bounded by the configured worker count, not by event arrival rate.

**PENTEST-024 — `handleSetTenantQuota` uses unscoped `hasRole`** — RESOLVED ✅
- **Original issue:** Inside the platform-admin tenant, any user with `admin` role at any scope was treated as a platform admin.
- **Resolution (2026-06-18):** Replaced with `hasScopedRole(assignments, "org", "*", "admin")` — the literal `"*"` is a reserved marker scope that `validateOrgName` rejects, so it can never collide with a real org name. Operators must explicitly grant `("admin", "org", "*")` to platform admins. Bonus cleanup: deleted the now-unused `hasRole` helper and `Handler.getUserRoles` method so a future change can't accidentally re-introduce the unscoped pattern. Test `TestHasScopedRole_platformAdminMarker` asserts both directions: a regular org admin fails the platform gate, and the `"*"` marker doesn't bleed into specific-org checks.

**PENTEST-025 — Rate-limiter GC goroutine has no stop signal** — RESOLVED ✅
- **Original issue:** `NewPerUserRateLimiter` spawned `gcLoop` with no way to stop it; goroutine leaked one per limiter for the test scenarios that re-create the limiter.
- **Resolution (2026-06-18):** Added a `stop chan struct{}` field initialized in `NewPerUserRateLimiter`, plus a public `Stop()` method that closes it (idempotent — safe to double-call). `gcLoop` now selects between `<-l.stop` and `<-ticker.C`, returning cleanly on stop. Production callers can ignore `Stop()` (limiter lives for process lifetime); tests `defer limiter.Stop()` to keep goroutine counts flat.

### INFO

| ID | Severity | Title | Service | Status |
|---|---|---|---|---|
| PENTEST-026 | INFO | Storage handler trusts caller-supplied `req.Key` without tenant validation | `registry-storage` | RESOLVED ✅ (2026-06-18) |

**PENTEST-026 — Storage handler doesn't validate key tenant prefix** — RESOLVED ✅
- **Original issue:** Every storage RPC accepted `req.Key` / `req.Prefix` as opaque strings and passed them to the driver. Defense-in-depth gap — a buggy internal caller could read or write any tenant's blobs.
- **Resolution (2026-06-18):** No proto change needed — every storage RPC already had a `tenant_id` field (PutBlobMeta, GetBlobRequest, etc.) and every caller (core, proxy, scanner, gc) was already populating it; the handler just wasn't validating. Added two helpers in `services/storage/internal/handler/grpc.go`:
  - `validateTenantKey(ctx, op, tenantID, key)` — requires non-empty tenant_id, then requires key to start with `blobs/<tenantID>/`, `manifests/<tenantID>/`, or `uploads/<tenantID>/` (the three roots documented in CLAUDE.md §8). Returns `codes.PermissionDenied` on mismatch (logged at WARN with op + tenant + key for triage).
  - `validateTenantPrefix(ctx, op, tenantID, prefix)` — same idea for `ListBlobs`, additionally requiring a non-empty prefix (the previous "default to blobs/" behaviour would have leaked every tenant's keys).
  - Applied to all 9 storage handler methods.
- **Tests:** new `TestStorageHandler_crossTenantAccessBlocked` runs every method with a caller in `t1` against a key in `t2` and asserts each one returns `PermissionDenied` before the driver is touched. `TestStorageHandler_emptyTenantIDRejected` asserts empty tenant_id can't bypass the gate.

---

## Round 2 Verification

- **No regressions** introduced by the round-1 fixes: all 30+ backend test suites still pass uncached.
- **No new CRITICAL or HIGH** findings in the post-fix codebase.
- 6 new findings (1 MEDIUM, 4 LOW, 1 INFO) — all in pre-existing code I hadn't deep-dived; **none introduced by recent changes**.
- 5 of the 6 round-2 findings fixed the same day (PENTEST-021 MEDIUM + PENTEST-022..025 LOW). Only PENTEST-026 INFO remains, deferred because it requires a proto change + caller migration.
- Round-2 fix verification:
  - **PENTEST-021:** new `TestMapErrCtx_unknownError_returnsGenericInternalMessage` asserts the wire message is the generic `"internal error"` even when the driver throws a leaky `AccessDenied: arn:aws:s3:::secret-bucket/...`.
  - **PENTEST-022:** caller-context propagation verified by existing signer handler tests passing uncached.
  - **PENTEST-023:** backpressure path covered by existing worker tests; manual review confirms `ErrQueueFull` propagates as a NACK to the broker via `consumer.Handler` error semantics.
  - **PENTEST-024:** new `TestHasScopedRole_platformAdminMarker` asserts the `"*"` marker scope behaves as expected in both directions (regular admin can't impersonate platform admin, marker doesn't bleed into specific-org checks). Dead-code removal of `hasRole`/`getUserRoles` confirmed by clean build with no callers.
  - **PENTEST-025:** new `Stop()` method exits the GC loop cleanly; safe to call multiple times. Existing rate-limit tests still pass.

---

## Resolved Issues

| ID | Title | Service | Resolved | How |
|---|---|---|---|---|
| SEC-001 | Audit table RLS bypassed by schema owner role | `registry-audit` | 2026-06-10 | Migration `20240101000002_audit_rls_role.sql` creates `registry_audit_app` NOLOGIN role, grants INSERT+SELECT on `audit_events` and DELETE on `audit_events_default` (retention path). `ALTER TABLE audit_events FORCE ROW LEVEL SECURITY` applies RLS even to the table owner. INSERT and SELECT policies defined; no UPDATE/DELETE policy → default-deny. Pool `AfterConnect` does `SET ROLE registry_audit_app` on every connection. `checkRole()` in `server.go` fails startup if effective role is not `registry_audit_app`. |
| SEC-002 | GC advisory locks: undefined locking behaviour under concurrent workers | `registry-gc` | 2026-06-11 | `services/gc/internal/advisory/lock.go` — `pg_try_advisory_lock(int8)` with FNV-64a key from tenant UUID. Connection pinned via `pgxpool.Acquire()`; explicit `pg_advisory_unlock` + `Release()` in deferred unlock. `runForTenant()` helper scopes the lock to one tenant at a time. `GC_ADVISORY_LOCK_DB_DSN` env var; no-op when unset (single-worker safe). |
| SEC-003 | Go plugin scanner path: supply chain and ABI risk | `registry-scanner` | 2026-06-11 | `.so` path was never implemented. `process.go` now uses pipe + `io.LimitReader(stdoutPipe, 10<<20)` instead of `cmd.Output()`. `pluginEnv()` passes an explicit allowlist (PATH, HOME, TMPDIR, TRIVY_*/GRYPE_* prefixes only) — all other env vars including DB/JWT credentials are stripped. |
| SEC-033 | `IsPasswordPolicyError` uses fragile string-prefix heuristic | `registry-auth` | 2026-06-12 | Defined `PasswordPolicyError` sentinel struct in `service/password.go`; `ValidatePassword` now returns `&PasswordPolicyError{...}`. `IsPasswordPolicyError` rewritten to use `errors.As(err, new(*PasswordPolicyError))` — type-safe, handles wrapped chains, no string matching. |
| SEC-004 | Proxy background store: fire-and-forget failure creates silent inconsistency | `registry-proxy` | 2026-06-11 | Background goroutine calls `publishStoreQueued()` on failure, which publishes a `store.queued` RabbitMQ event. `HandleStoreQueued` consumer re-fetches blob from upstream and retries the store. Dead-letters after 3 retries via `consumer.Config{MaxRetries: 3}`. No-op when `RABBITMQ_URL` is unset. |
| SEC-008 | gRPC clients use plaintext transport | `registry-core`, `registry-proxy` | 2026-06-10 / 2026-06-11 | Added `clientCreds()` helper in both `services/core/internal/server/server.go` and `services/proxy/internal/server/server.go`. Calls `libs/auth/mtls.ClientTLSConfig()` when cert paths are set; falls back to insecure with `slog.Warn` in dev. Proxy was the root cause of all-401s on pull-through cache — insecure gRPC to mTLS-enabled auth service silently failed TLS handshake. |
| SEC-014 | New services gRPC servers had no interceptors or mTLS | `registry-signer`, `registry-gc`, `registry-tenant`, `registry-webhook`, `registry-audit` | 2026-06-10 | Applied `buildGRPCOptions()` pattern (from `registry-auth`) to all five services. Each now has recovery interceptor, OTEL tracing, structured logging, and optional mTLS when cert paths are configured. Commit `c4e08d7`. |
| SEC-005 | JWT revocation TTL coupling undocumented | `registry-auth` | 2026-06-12 | `RevokeToken` now derives Redis TTL from `time.Until(claims.ExpiresAt.Time)` with a comment explaining the self-cleaning coupling. `ValidateToken` comment cross-references the contract. |
| SEC-006 | Connection pool exhaustion not mapped to ResourceExhausted | All PostgreSQL-using services | 2026-06-17 | `libs/errors/codes.MapDBError` now detects `context.DeadlineExceeded` and `pgxpool` exhaustion paths and maps to `codes.ResourceExhausted`. `libs/config/loader.DBConfig.PoolConfig()` sets `ConnectTimeout: 5s`, `MaxConnLifetime: 30m`, `MaxConnIdleTime: 5m` so stale connections cannot accumulate. gRPC client retry interceptor was updated to skip `ResourceExhausted`. Commit `0f95144`. |
| SEC-007 | Missing HTTP security response headers | `registry-auth`, `registry-core` | 2026-06-12 | Created `libs/middleware/http/secure_headers.go` with `SecureHeaders` middleware setting `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `X-XSS-Protection: 0`. Applied to auth and core HTTP servers. |
| SEC-009 | IP rate limiting targets gateway IP, not client IP | `registry-auth` | 2026-06-12 | `remoteIP()` now checks `X-Forwarded-For` only when TCP peer is in `TRUSTED_PROXY_CIDRS` (comma-separated env var). Falls back to `RemoteAddr` for direct connections. Startup warning when CIDR list is empty. |
| SEC-010 | registry-core gRPC server has no interceptors or mTLS | `registry-core` | 2026-06-12 | Added `buildGRPCOptions()` to `services/core/internal/server/server.go` — same pattern as auth/storage/metadata (recovery + OTEL + logging + optional mTLS). |
| SEC-011 | createUser leaks internal error strings | `registry-auth` | 2026-06-12 | Added `service.IsPasswordPolicyError(err)` helper. Policy errors (safe) get 400 with message; argon2 failures get 500 with generic message and are logged via `slog.ErrorContext`. |
| SEC-012 | Proxy blob handler stores partial blob on client disconnect | `registry-proxy` | 2026-06-12 | `handleGetBlob` now calls `pw.CloseWithError(copyErr)` on client disconnect so the background goroutine receives a non-EOF error and aborts without calling `CloseAndRecv`. |
| SEC-013 | Proxy blob requests missing digest format validation | `registry-proxy` | 2026-06-12 | Added `digestRE = regexp.MustCompile("^sha256:[a-f0-9]{64}$")` to proxy handler. Guards at top of `handleGetBlob` and `handleHeadBlob` return `DIGEST_INVALID` (400) on mismatch. |
| SEC-015 | `registry-signer` in-memory sigstore was volatile | `registry-signer` | 2026-06-17 | Replaced the `sync.RWMutex`-protected map with PostgreSQL persistence. `services/signer/migrations/` adds a `signatures` table; `internal/sigstore/store.go` writes through to the DB and keeps an in-process LRU cache. `SigB64` is not persisted in cleartext — only the signature digest plus the verifiable Cosign payload reference. `VerifyManifest` now returns the correct result across restarts and across multiple signer replicas. Commit `0f95144`. |
| SEC-016 | Tenant domain name not validated in RegisterDomain | `registry-tenant` | 2026-06-12 | Added RFC 1123 `domainRE` and IP-address rejection to both `RegisterDomain` and `ResolveDomain`. Returns `codes.InvalidArgument` for non-conforming domains. |
| SEC-017 | Tenant name not validated against allowlist | `registry-tenant` | 2026-06-12 | Added `tenantNameRE` (`^[a-z0-9][a-z0-9-]{1,63}$`) to `CreateTenant`. pgx `23505` unique violation mapped to `codes.AlreadyExists` via `isDuplicateKeyError` helper. |
| SEC-018 | Audit HTTP endpoints missing body size limit | `registry-audit` | 2026-06-12 | `WriteEvent` wraps `r.Body` with `http.MaxBytesReader(w, r.Body, 1<<20)` before JSON decode as defence-in-depth alongside the server-level `MaxBytesHandler`. |
| SEC-019 | HTTP servers missing ReadHeaderTimeout | All services | 2026-06-12 | Added `ReadHeaderTimeout: 10 * time.Second` to all 12 service HTTP servers that were missing it. |
| SEC-020 | HTTP servers missing ReadTimeout and WriteTimeout | All services | 2026-06-12 | Added `ReadTimeout: 30 * time.Second` and `WriteTimeout: 30 * time.Second` (60s for blob-streaming services) to all 12 service HTTP servers. |
| SEC-021 | Healthcheck binary uses http.DefaultClient without timeout | `libs/cmd/healthcheck` | 2026-06-12 | Replaced `http.Get(addr)` with `&http.Client{Timeout: 5*time.Second}`. Removed `//nolint:gosec` suppression. |
| SEC-022 | sslmode=prefer in docker-compose contradicts sslmode=require | All DB services | 2026-06-12 | `libs/config/loader/loader.go` now emits `slog.Warn` when DSN `sslmode` is not `"require"`. Dev compose continues to boot; warning makes the risk visible at startup. |
| SEC-023 | Vault dev root token hardcoded in docker-compose | `vault` (dev) | 2026-06-12 | Vault service and vault-init now use `${VAULT_DEV_ROOT_TOKEN:-dev-root-token}`. Warning comment added above the vault block. `VAULT_DEV_ROOT_TOKEN=` added to `.env.example`. |
| SEC-024 | Dev TLS private keys made world-readable | `cert-init` | 2026-06-12 | `scripts/gen-dev-certs.sh` now uses `chmod 644 *.crt` + `chown 65532:65532 *.key; chmod 600 *.key` instead of `chmod a+r *.key`. |
| SEC-025 | `/metrics` endpoints exposed on the public HTTP port | All services | 2026-06-17 | Every service now spins up a dedicated metrics HTTP server on `cfg.MetricsAddr` (default `:9090`) separate from the business port. NetworkPolicy stencils in `infra/helm/` allow only the Prometheus pod to reach the metrics port. Verified in `services/auth/internal/server/server.go`, `services/audit/.../server.go`, `services/core/.../server.go` plus all other services. Commit `0f95144`. |
| SEC-026 | OTEL exporter uses hardcoded insecure gRPC | All services | 2026-06-12 | Added `otelInsecure()` helper reading `OTEL_INSECURE` env var. `WithInsecure()` now only applied when `OTEL_INSECURE=true`. `docker-compose.yml` sets `OTEL_INSECURE: "true"` for local dev. |
| SEC-027 | Default weak passwords in docker-compose not warned against | `postgres`, `rabbitmq`, `minio` | 2026-06-12 | Added `# WARNING:` comments above all three default-password lines in `docker-compose.yml`. |
| SEC-028 | context.Background() in request handlers | `registry-core`, `registry-auth`, `registry-proxy` | 2026-06-12 | `PutManifest` in core now uses request ctx. Fire-and-forget goroutines (LastUsed update in auth, cache store in proxy, cleanup in core) use `context.Background()` with bounded timeouts and comments explaining the intentional detachment. |
| SEC-029 | Scanner plugin path not sanitised with filepath.Clean | `registry-scanner` | 2026-06-12 | `New()` in `process.go` applies `filepath.Clean` then `filepath.IsAbs` check; fails fast with clear error if path is relative or contains `..` segments. |
| SEC-030 | SecureHeaders middleware never wired into any HTTP server | All services | 2026-06-12 | Added `httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"` import and wrapped `http.Server.Handler` with `httpmiddleware.SecureHeaders(...)` as outermost layer in all 12 service `server.go` files. X-Content-Type-Options, X-Frame-Options, X-XSS-Protection now sent on every HTTP response including error responses from MaxBytesHandler. |
| SEC-031 | tenant/webhook/audit bypass sslmode validation on DB pool | `registry-tenant`, `registry-webhook`, `registry-audit` | 2026-06-12 | Replaced direct `pgxpool.ParseConfig(cfg.DBDSN)` calls with `loader.DBConfig{DBDSN: cfg.DBDSN, DBMaxConns: cfg.DBMaxConns}.PoolConfig()` in all three service Run() functions. sslmode=disable now rejected at startup; weaker modes logged as warning. audit AfterConnect (SET ROLE) preserved after the new PoolConfig call. |
| SEC-032 | fmt.Printf for warnings in core service loses structured context | `registry-core` | 2026-06-12 | Replaced two `fmt.Printf` calls in `registry.go` with `slog.WarnContext` — referrer store failure uses `ctx5`, push.completed publish failure uses `ctx`. Added `"log/slog"` to imports. Warnings now carry trace_id/span_id and appear in the structured log pipeline. |
| SEC-034 | TRUSTED_PROXY_CIDRS parse errors silently discarded | `registry-auth` | 2026-06-12 | `init()` in `http.go` now calls `slog.Warn` with the offending CIDR entry and parse error when `net.ParseCIDR` fails, so operators see misconfigured entries at startup rather than silently operating with reduced proxy trust coverage. |
| SEC-035 | No server-side RBAC enforcement on OCI push/pull | `registry-core` | 2026-06-14 | `checkAccess()` added to `services/core/internal/handler/http.go`. Calls `GetUserPermissions` on `registry-auth` (5s deadline, fails closed). Enforced on every write handler (`"push"` action: InitiateUpload, PutManifest, DeleteManifest, DeleteBlob) and every read handler (`"pull"` action: GetManifest, HeadManifest, GetBlob, HeadBlob, ListTags). Returns HTTP 403 OCI DENIED on miss or RPC error. Wildcard `*` entries in permission list supported for org-level grants. |
| SEC-036 | RBAC membership changes not audit-logged | `registry-auth` | 2026-06-14 | `GrantRole` and `RevokeRole` gRPC handlers publish `rbac.role_granted` / `rbac.role_revoked` RabbitMQ events after successful DB writes. `registry-audit` consumers record these as audit events. Publish failure is logged but does not roll back the grant/revoke — audit gap is acceptable vs. transaction complexity. `RABBITMQ_URL` is optional in auth config; events are silently skipped when unset (dev environments without a broker). |

---

## Security Hardening Checklist Status

Tracked per service. `?` = not yet assessed.

| Rule | gateway | auth | core | storage | metadata | proxy | scanner | signer | webhook | audit | gc | tenant |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| No `unsafe` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| No `exec.Command` with user input | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| No `os.Getenv` in handlers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| File paths sanitised | N/A | N/A | N/A | ✓ | N/A | N/A | ✓ | N/A | N/A | N/A | N/A | N/A |
| HTTP client timeouts set | N/A | N/A | N/A | N/A | N/A | ✓ | N/A | N/A | ✓ | N/A | N/A | N/A |
| No `http.DefaultClient` | ✓ | N/A | ✓ | ✓ | ✓ | ✓ | ✓ | N/A | ✓ | N/A | N/A | ✓ |
| `context.Background()` not in handlers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `crypto/rand` used (not `math/rand`) | N/A | ✓ | ✓ | N/A | N/A | ✓ | N/A | ✓ | N/A | ✓ | N/A | ✓ |
| `ReadHeaderTimeout` set on HTTP server | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `ReadTimeout`/`WriteTimeout` set | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| CSP header on HTML responses | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| `X-Content-Type-Options: nosniff` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| CORS explicitly configured | N/A | ✗ (unassessed) | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| Request body size limits | ✗ (SEC-019) | ✓ | ✓ | ✓ | ✓ | ✓ | N/A | N/A | N/A | ✓ | N/A | N/A |
| Metrics on separate port | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `govulncheck` in CI | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `gosec` in CI | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `gitleaks` in CI | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| No secrets in Docker layers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |

---

## Pentest Findings — Round 3 (2026-06-19)

> Defensive review of branch `feat/frontend-rebuild` covering FE-API-001
> (`Repository.org` + `Tag.size_bytes`), FE-API-010 (`Org` surfaced on
> management REST), FE-API-021..024 (webhook CRUD / deliveries / test /
> rotate-secret), and the 00004 manifest backfill migration. 7 findings
> (0 CRITICAL, 2 HIGH, 3 MEDIUM, 2 LOW). All review items 1–10 from the
> request are covered; categories where nothing was found are stated
> explicitly.

### PENTEST-027 — Webhook URL list discloses URL-embedded credentials to any tenant reader
- **Severity:** HIGH
- **Status:** RESOLVED 2026-06-19
- **Service:** `services/management`
- **Raised:** 2026-06-19
- **Description:** `GET /api/v1/webhooks` and `GET /api/v1/webhooks/{id}/deliveries` were gated only by `RequireAuth` — every authenticated user in the tenant could read the full list of webhook endpoints. The `EndpointResponse.URL` field and the `Delivery.LastError` field (which the dispatcher wrote with the target URL embedded) both surfaced the raw webhook URL. A common operator anti-pattern is to embed an auth token in the webhook URL itself (`https://hooks.example.com/registry?token=...`). Combined with `last_error` containing the failing URL, a low-privilege reader could exfiltrate another team's webhook secret in the same tenant. The dispatcher message wrapping the URL in failures meant even a write-once URL leak via a deleted endpoint could persist in `webhook_deliveries.last_error`.
- **Resolution (2026-06-19):**
  1. `handleListWebhooks` and `handleListWebhookDeliveries` now call `h.requireWebhookAdmin(r)` before any data is returned — matches the mutation-side gate. (`services/management/internal/handler/webhooks.go:111-127`, `:271-289`.)
  2. Dispatcher errors now sanitise the URL: `sanitizeURLForError` strips query, fragment, and userinfo via `url.Parse`/rebuild; `stripURLFromError` unwraps `*url.Error` so the stdlib doesn't reattach the raw URL via `%w`. Result: `webhook_deliveries.last_error` only ever sees `scheme://host[:port]/path`. (`services/webhook/internal/delivery/dispatcher.go:18-83`, `:127-137`.)
  3. Tests added in `dispatcher_test.go`: `TestSanitizeURLForError` (7 cases, including userinfo / query / fragment / unparseable / hostless) and `TestDispatcher_errorMessageRedactsURL` (end-to-end check that an SSRF-blocked send never echoes the URL token).
- **Choices considered but not taken:** Userinfo redaction on `EndpointResponse.URL` itself — held off because list is now admin-gated, admins should be able to see what they configured, and a PATCH that omits `url` cleanly preserves the stored userinfo (the gRPC `optional` field is untouched). Revisit if FE-API-024 ever ships an inline-edit UI that re-sends `url` on every save.
- **References:** CLAUDE.md §10 (no sensitive data in logs/responses), CWE-200, OWASP A01:2021.

### PENTEST-028 — Manifest backfill migration is an unbounded full-table scan that can stall startup
- **Severity:** HIGH
- **Status:** RESOLVED 2026-06-19
- **Service:** `services/metadata`
- **Raised:** 2026-06-19
- **Description:** `services/metadata/migrations/00004_manifest_image_size.sql` ran a `DO $$ ... FOR r IN SELECT id, raw_json FROM manifests WHERE image_size_bytes = 0 LOOP ... END LOOP $$` inside a single goose migration step. Goose runs the entire `StatementBegin/StatementEnd` block in one transaction — on a tenant with 100k–1M manifests this would (a) hold a long-running transaction blocking autovacuum / DDL on `manifests`, (b) load every `raw_json` into the backend session memory one row at a time, (c) keep one DB connection occupied for the duration and prevent the metadata service from accepting traffic since `goose up` blocks before `Serve`.
- **Resolution (2026-06-19):** Recommendation #1 chosen.
  1. `00004_manifest_image_size.sql` reduced to the `ALTER TABLE ... ADD COLUMN image_size_bytes BIGINT NOT NULL DEFAULT 0` only — no backfill in the migration. With a constant default this is a metadata-only catalog change in PG 11+ (no row rewrite), so it returns instantly regardless of row count.
  2. New rows are populated by `parseImageSize(rawJSON)` inside `PutManifest` (Go) — already shipped with FE-API-001.
  3. The batched, COMMIT-per-batch backfill lives in `infra/runbooks/manifest-image-size-backfill.md` as a `psql -f backfill.sql` script for operators to run during a maintenance window. The procedure (a) uses a high-water-mark cursor on `id` so a row whose JSON fails to parse is permanently skipped (otherwise an all-malformed batch would loop forever on `WHERE image_size_bytes = 0`), (b) commits every 1000 rows so vacuum and replication can keep up, (c) wraps each row in `BEGIN ... EXCEPTION WHEN OTHERS THEN NULL ... END` so one bad row never derails the batch, (d) is idempotent — re-running it skips already-backfilled rows via the same predicate. Postgres 11+ procedure semantics (`COMMIT` inside a `CALL`'d procedure, outside any FOR-cursor body) make this safe.
- **References:** CLAUDE.md §11, CWE-400.

### PENTEST-029 — `parseImageSize` has no input bound, opening a memory DoS via crafted manifest JSON
- **Severity:** MEDIUM
- **Status:** RESOLVED 2026-06-21 (audit)
- **Service:** `services/metadata`
- **Raised:** 2026-06-19
- **Description:** `services/metadata/internal/repository/repository.go:368-391` (`parseImageSize`) calls `json.Unmarshal(rawJSON, &doc)` with an anonymous struct that has `Layers []struct{...}` and `Manifests []struct{...}`. Per the request, OCI core's `services/core/internal/handler/http.go:34` does cap manifest body to 4 MiB before forwarding — that bound holds for the OCI push path. **However the metadata gRPC `PutManifest` RPC (`services/metadata/internal/handler/grpc.go:181`) accepts `raw_json` from any internal caller without enforcing the same cap**, and the default grpc-go MaxRecvMsgSize is 4 MiB which is a soft ceiling, not a parser-side guard. A 4 MiB JSON document with ~1M empty array entries unmarshals into ~16-24 MiB of Go slice memory per call (16-byte struct × 1M). Concurrent crafted pushes from a misbehaving internal client (or any future direct-call path) would multiply this. There is also no recursion-depth limit on `json.Unmarshal`; a deeply nested document (`{"layers":[{"layers":[...]}...]}`) does not match this schema, so depth attack is not a concern in the actual struct — but the resource cost stands for wide arrays.
- **Resolution (verified 2026-06-21):** Both recommendations implemented:
  1. `services/metadata/internal/handler/grpc.go:217-225` defines `maxManifestJSONBytes = 4 << 20` and `PutManifest` returns `codes.InvalidArgument` when `len(req.RawJson) > maxManifestJSONBytes` — explicit byte-count check before the parser is touched.
  2. `services/metadata/internal/repository/repository.go:393-418` defines `maxManifestEntries = 1000` and `parseImageSize` truncates `doc.Layers` and `doc.Manifests` to that cap before summing. Real-world OCI images stay well under 200 layers / 50 platforms.
- **References:** CLAUDE.md §13 (request body size limits on all servers), CWE-400, CWE-770.

### PENTEST-030 — Test-dispatch endpoint enables low-cost outbound amplification within the per-user limit
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `services/management` + `services/webhook`
- **Raised:** 2026-06-19
- **Description:** `POST /api/v1/webhooks/{id}/test` (`services/management/internal/handler/webhooks.go:330-360`) and `services/webhook/internal/handler/grpc.go:295-344` (`TestDispatch`) let an authenticated admin trigger a synchronous HTTPS POST to a previously-validated URL. The SSRF guard, response-body cap (8 KiB), and 15s timeout are all in place — good. The amplification concern is volume: a single admin under the per-user limit (20 rps, burst 40) can sustain ~1200 requests/min to one URL from this service alone; horizontally scaled (`replicas × rps` per `services/management/internal/middleware/ratelimit.go:21-23`) the cluster-wide cap is N×. The synthetic payload is ~256 bytes, the response cap is 8 KiB, so amplification factor is small (≤32×), but the source IPs are the registry's egress IPs — useful for an attacker who has already compromised one tenant-admin credential and wants to obscure attribution. The URL was validated at create time, but DNS-rebinding between create and test can shift the resolved IP without re-running `ValidateURL` (the runtime dialer in `dispatcher.go:50-66` re-resolves and re-checks, so this is actually OK — call out as INFO not a finding).
- **Remediation:**
  1. Add a dedicated per-endpoint test-dispatch rate limit (e.g. max 1 test per endpoint per 10s) keyed on `(tenant_id, endpoint_id)` in Redis so a runaway script cannot drive 1k/min at one victim URL.
  2. Add a `TEST_DISPATCH_DAILY_LIMIT` counter per tenant (Redis INCR with EXPIRE 86400) and 429 when the daily budget is exhausted.
- **References:** CLAUDE.md §13 (request body size limits, rate limits), CWE-406 (Insufficient Control of Network Message Volume).

### PENTEST-031 — Webhook gRPC `mapWebhookGRPCError` leaks SSRF guard internals via `InvalidArgument` message passthrough
- **Severity:** MEDIUM
- **Status:** RESOLVED 2026-06-21 (audit)
- **Service:** `services/management`
- **Raised:** 2026-06-19
- **Description:** `services/management/internal/handler/webhooks.go:459-472` maps gRPC errors to HTTP. For `codes.InvalidArgument` the response body is `{"error": st.Message()}` — the verbatim gRPC `status.Message`. Upstream messages include strings like `invalid webhook URL: webhook destination "10.20.30.40.nip.io" resolves to private IP 10.20.30.40 — blocked (SSRF protection)` (from `services/webhook/internal/delivery/ssrf.go:65` via `services/webhook/internal/handler/grpc.go:81`). The `tenant_id and url are required` / `invalid tenant_id` strings also reach the client. CLAUDE.md §4.13 (and the file-top doc of `webhooks.go:13`) say internal gRPC detail must NOT be leaked to the API client. Bad enough on its own; the SSRF message also confirms to an attacker that the SSRF filter is enabled and what IP they hit, which is useful reconnaissance.
- **Resolution (verified 2026-06-21):** `services/management/internal/handler/webhooks.go:477` `mapWebhookGRPCError` now logs `st.Message()` server-side at `slog.Warn` (with `opLabel` + `detail` fields for triage) and returns the fixed string `{"error":"invalid request"}` to callers. Regression coverage: `services/management/internal/handler/webhooks_test.go:145` asserts the upstream SSRF detail never appears on the wire.
- **References:** CLAUDE.md §4.13 (generic error responses), CWE-209 (Information Exposure Through an Error Message).

### PENTEST-032 — `UpdateEndpoint` proto leaves URL revalidation optional when caller omits the field but events change
- **Severity:** LOW
- **Status:** RESOLVED 2026-06-21 (audit)
- **Service:** `services/webhook`
- **Raised:** 2026-06-19
- **Description:** `services/webhook/internal/handler/grpc.go:165-200` (`UpdateEndpoint`) only revalidates the destination URL when `req.Url != nil` (line 179). This is correct for partial updates, but if an operator originally created an endpoint pointing at a public IP that has since been moved to RFC1918 (e.g. a DNS A-record flip), every subsequent PATCH that touches `events`/`active` will silently leave the now-private URL in place. The runtime dialer (`dispatcher.go:50-66`) still re-resolves on each delivery so SSRF is still blocked at send-time — but an operator who's edited the row recently might assume "the URL was validated when I last touched the row." Suggestion: opportunistically re-run `ValidateURL` on the current stored URL whenever any update is performed; if validation now fails, refuse the update with a clear error (`webhook endpoint URL no longer resolvable to a public address — please update or delete the endpoint`).
- **Resolution (verified 2026-06-21):** `services/webhook/internal/handler/grpc.go:186-202` — when `req.Url == nil`, `UpdateEndpoint` fetches the stored endpoint via `GetEndpointForTenant` and runs `delivery.ValidateURL(existing.URL)`. On regression (URL now resolves to RFC1918, scheme degraded, etc.) the handler returns `codes.InvalidArgument "stored webhook URL is no longer valid: <reason>"` and refuses to persist the update — the operator must either supply a fresh URL or delete the endpoint.
- **References:** CLAUDE.md §13 (SSRF posture), CWE-918 (defence in depth).

### PENTEST-033 — Postman collection ships dev credentials inline and tenant UUID as a default
- **Severity:** LOW
- **Status:** PARTIAL — login uses `{{password}}` (now `type: secret`); createUser body and tenant UUID default still open
- **Service:** `docs/postman`
- **Raised:** 2026-06-19
- **Description:** `docs/postman/registry-management.postman_collection.json:74` has `"password": "Admin1234!dev"` and `:114` has `"password": "NewUser1234!"` baked into the request body raw text (not as environment variables). The environment file (`docs/postman/registry-management.postman_environment.json:6`) defaults `tenantId` to `98dbe36b-ef28-4903-b25c-bff1b2921c9e`, which matches the dev seed. None of these are real production secrets, but: (a) operators commonly copy a working Postman collection into Slack / a wiki; baked-in creds increase the chance someone runs the dev login attempt against a production gateway, (b) seeing `Admin1234!dev` on a screen during a demo trains operators that simple passwords are acceptable, (c) the seed tenant UUID being in version control makes targeted enumeration trivial if the gateway is reachable.
- **Status (2026-06-21 audit):**
  - ✅ Login request body now uses `{{password}}` (verified `registry-management.postman_collection.json:74`) and the env var is `type: "secret"` (verified `registry-management.postman_environment.json:8`). First mitigation landed.
  - ❌ createUser body at `registry-management.postman_collection.json:114` still inlines `"password": "NewUser1234!"`. Move to `{{newUserPassword}}` env var.
  - ❌ `tenantId` defaulted to the dev seed UUID at `registry-management.postman_environment.json:6`. Switch to empty default with description (Postman supports `value: ""`).
- **Remaining remediation:**
  1. Replace `"NewUser1234!"` in `:114` with `{{newUserPassword}}` and add the variable to the environment file with empty default + `"type": "secret"`.
  2. Make the tenant UUID a required prompt rather than a default — set `"value": ""` with a description pointing at the dev seed migration.
  3. Add a `// dev seed — not for any non-local environment` comment string into the login request body's pre-request script.
- **References:** CLAUDE.md §13 ("No secrets in Git history"), CWE-798 (Use of Hard-coded Credentials — informational level, since these are documented dev seeds).

### Items reviewed with no findings

- **IDOR / cross-tenant access on webhook routes:** every handler in `webhooks.go` calls `middleware.TenantIDFromContext(r.Context())` (lines 116, 154, 206, 249, 275, 345, 387) and passes it to the gRPC request. The repository layer (`services/webhook/internal/repository/repository.go:69-81, 229-240, 245-260, 265-278, 283-315`) gates every query by `tenant_id`. The gRPC handler enforces UUID parsing on both `endpoint_id` and `tenant_id` and always passes both to the repo. `GetEndpointForTenant` (line 229) returns `pgx.ErrNoRows` for a real endpoint in another tenant — confirmed correct.
- **SSRF coverage:** `services/webhook/internal/delivery/ssrf.go:14-35` covers 0.0.0.0/8, 10/8, 127/8, 169.254/16 (incl. cloud metadata), 172.16/12, 192.168/16, 100.64/10 (CGNAT), ::1/128, fc00::/7 (ULA), fe80::/10 (link-local). HTTPS-only enforced (`ssrf.go:45-47`). Runtime dialer re-resolves DNS on every connect (`dispatcher.go:50-66`) so DNS-rebinding between create and use is blocked. The shared dispatcher is reused by `TestDispatch` (confirmed via `services/webhook/internal/handler/grpc.go:335`). One minor gap noted: the IPv4-mapped IPv6 form `::ffff:10.0.0.1` is not in the table — Go's `net.IP.To4()` would still flag the IPv4 portion as private when re-evaluated, but it's worth a defensive `ip.To4()` normalisation pass.
- **Secret handling on creation/rotation:** secret is generated by `crypto/rand` in management (`webhooks.go:413-419`), passed once over the mTLS-encrypted gRPC channel, encrypted with AES-256-GCM in the webhook service before persistence (`services/webhook/internal/handler/grpc.go:90-94`, `:217-220`), and `EndpointResponse.Secret` uses `omitempty` so list/update/delete never include it. The `RotateEndpointSecret` error path does not echo the new secret (`webhooks.go:399-403`). The shared gRPC logging interceptor (`libs/middleware/grpc/server.go:140-160`) only logs `method`/`code`/`duration_ms`/`peer`/`request_id` — request bodies are never serialised. No leak of the plaintext secret found.
- **Auth gate strength for webhook mutations:** `requireWebhookAdmin` (`webhooks.go:83-93`) requires `role >= admin AND scope_type == "org"`. The platform-admin marker (`admin`, `org`, `*`) satisfies this. A repo-scoped admin does NOT satisfy this. List/list-deliveries deliberately open — see PENTEST-027 for the recommendation to tighten this.
- **SQL injection in new JOINs:** `o.name || '/' || r.name = $2` in `services/metadata/internal/repository/repository.go:113` is parameterised; the bound parameter is the user's `org/repo` string. Org names are constrained by `services/core/internal/service/registry.go:26` (`^[a-z0-9]+([._-][a-z0-9]+)*/[a-z0-9]+([._-][a-z0-9]+)*$`) on push and by `validateOrgName` (`services/management/internal/handler/validate.go:37`) on REST create, neither of which permits `/` — so the `o.name || '/' || r.name` predicate cannot have ambiguous matches. The metadata gRPC `GetOrCreateOrganization` (`services/metadata/internal/repository/repository.go:206`) does not itself revalidate the org name, but every caller path validates first. Worth a defence-in-depth assertion (`reOrgName`-equivalent guard inside `GetOrCreateOrganization`) but not exploitable today.
- **Info disclosure via `Org` on Repository:** the metadata cache (`services/metadata/internal/server/server.go:184-217`) keys every cacheable method on `tenant_id + ...`; `GetRepositoryByName` / `GetRepositoryByFullName` are NOT in the cache map, so the new `Repository.org` field cannot be served from a stale cross-tenant entry. The repo's `repoSelectCols` (`services/metadata/internal/repository/repository.go:53-54`) always JOINs and the tenant predicate is in every `WHERE` clause. No leak path found.
- **JSON parsing DoS on PutManifest:** covered as PENTEST-029.
- **Migration backfill DoS:** covered as PENTEST-028.
- **Postman hygiene:** covered as PENTEST-033. No real production hostnames or live bearer tokens present.

---

### PENTEST-AUTH-001 — Polymorphic api_keys cross-tenant guard (resolved pre-merge)
Closed by FE-API-048 implementation (commit `da86cdd`). `ValidateAPIKey` for service-account
keys verifies the request's claimed tenant matches `service_accounts.tenant_id`;
mismatch returns Unauthenticated + writes a `pentest.cross_tenant_attempt`
audit row. Test: T5 in spec §8.1.

### PENTEST-AUTH-002 — JWT revocation pattern extended to per-user (resolved pre-merge)
Closed by FE-API-048 implementation (commit `66aab14`). `ValidateToken` consults
`revoke:user:<user_id>` Redis key set by `SetDisabled` on a service account.
Closes the 300s JTI window for the SA disable path. Pattern is documented
under CLAUDE.md §7 "JWT Validation."

---

## Recurring Security Tasks

| Task | Frequency | Owner | Last Run |
|---|---|---|---|
| OWASP ZAP baseline scan (staging) | Weekly | — | Never |
| `govulncheck` across all repos | Every PR | CI | Every PR (all 12 service CI workflows) |
| Dependency license check | Every PR | CI | Never |
| Secret rotation review | Quarterly | — | Never |
| Audit log retention review | Quarterly | — | Never |
| GC dry-run before production schedule change | Before each change | — | Never |

---

### SEC-037 — Onboarding-flag backfill UPDATE locks every users row in one statement
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `services/auth`
- **Raised:** 2026-06-27
- **Description:** `services/auth/migrations/20260629000002_users_onboarding_complete.sql:32-34` performs `UPDATE users SET onboarding_complete = true WHERE created_at < NOW()` — an unbounded single-statement UPDATE that targets every row in the table. Goose runs each migration in a single transaction so this holds a row-level lock on every users row simultaneously and a transaction-scoped UPDATE conflict with any concurrent writer. The same transaction also contains the `ALTER TABLE … ADD COLUMN`, which (although constant-default and non-rewriting on PG ≥ 11) takes an `AccessExclusiveLock` for its duration. Concurrent login traffic that touches the users table (`ResetFailedLogins`, `RecordFailedLogin`, `TouchLastLogin`, `UpdatePasswordHash`) will block until migration commit. For installs with ≤ tens of thousands of users this is sub-second and invisible; for a SaaS install with a large user table it can produce a noticeable login stall and (under saturation) lock-wait timeouts during the migration window. No data correctness issue — purely an availability concern during the deployment window.
- **Remediation:**
  1. For deployments with > ~100k humans, batch the backfill into chunks (e.g. `UPDATE users SET onboarding_complete = true WHERE id IN (SELECT id FROM users WHERE NOT onboarding_complete LIMIT 10000)` looped) and commit between batches via an out-of-band runbook, leaving the migration to only run the `ALTER TABLE … ADD COLUMN NOT NULL DEFAULT false` (which is metadata-only on PG ≥ 11).
  2. Alternatively, drop the `WHERE created_at < NOW()` backfill entirely and rely on `NOT NULL DEFAULT false` for existing rows (this would re-show the wizard to pre-existing humans — a product choice, not a security one).
  3. Document the deployment-window cost in `infra/runbooks/` so operators of large installs know to run the backfill out-of-band before the schema migration.
- **References:** CLAUDE.md §11 (migration rules — "run migrations at startup in a separate step before serving traffic"); PENTEST-028 manifest-backfill precedent (split bulk UPDATE out of migration into idempotent runbook).
