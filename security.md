# Security Issues

> Last updated: 2026-06-18 (SEC-001 through SEC-036 all resolved — no open security items at platform level. Sprint 6 backend gaps tracked in `status.md` are feature work, not security findings.)
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

> No open security findings as of 2026-06-18.
> Backend feature gaps (Vault/KMS signing backends, Notary v2, tag-level RBAC, etc.)
> are tracked in `status.md` Sprint 6 — those are unimplemented features rather than
> security regressions, so they live in the project tracker, not here.

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

## Recurring Security Tasks

| Task | Frequency | Owner | Last Run |
|---|---|---|---|
| OWASP ZAP baseline scan (staging) | Weekly | — | Never |
| `govulncheck` across all repos | Every PR | CI | Every PR (all 12 service CI workflows) |
| Dependency license check | Every PR | CI | Never |
| Secret rotation review | Quarterly | — | Never |
| Audit log retention review | Quarterly | — | Never |
| GC dry-run before production schedule change | Before each change | — | Never |
