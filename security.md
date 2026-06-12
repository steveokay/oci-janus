# Security Issues

> Last updated: 2026-06-12 (SEC-005/007/009/010/011/012/013/016/017/018/019/020/021/022/023/024/026/027/028/029/030/031/032/034 resolved; SEC-006/015/025 deferred; SEC-033 open — sentinel error refactor)
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

## Open Issues (Deferred)

> These items require more invasive changes (new DB tables, multi-service topology refactors) and are tracked for the next sprint.

### SEC-006 — Connection pool exhaustion not mapped to correct gRPC status code
- **Severity:** LOW
- **Status:** OPEN
- **Service:** All services with PostgreSQL access (`registry-auth`, `registry-audit`, `registry-metadata`, `registry-tenant`)
- **Raised:** 2026-06-09
- **Description:** Default `pgxpool.Acquire` behaviour blocks until a connection is available or context times out. If the error is surfaced as `codes.Internal`, callers with a retry interceptor will retry on exhaustion, amplifying load.
- **Remediation:**
  1. Detect `context.DeadlineExceeded` from `pool.Acquire` and return `codes.ResourceExhausted`
  2. Set `ConnectTimeout` on pool config (default 5s)
  3. Add `MaxConnIdleTime` and `MaxConnLifetime` to prevent stale connections
  4. Retry interceptor must explicitly NOT retry on `codes.ResourceExhausted`
- **References:** `§13` in CLAUDE.md, `pgxpool` docs

---

### SEC-015 — `registry-signer` in-memory sigstore is volatile
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-signer`
- **Raised:** 2026-06-10
- **Description:** `services/signer/internal/sigstore/store.go` holds all signature records in a `sync.RWMutex`-protected map. On process restart (pod crash, rolling deploy, OOM kill), all records are lost. `VerifyManifest` will return `Verified: false` for all previously signed images, breaking any policy that requires signature verification. Two signer replicas also have independent stores.
- **Remediation:**
  1. Persist signature records to PostgreSQL (add a `signatures` table to the signer's own DB, or reuse `registry-metadata`'s gRPC API)
  2. Alternatively, follow Cosign's intended model: push the signature as an OCI artifact to `registry-core` and query it back
  3. Do not persist `SigB64` in cleartext — store only the signature digest
- **References:** `services/signer/internal/sigstore/store.go`, CLAUDE.md §4.8

---

### SEC-025 — `/metrics` endpoints unauthenticated and exposed on public HTTP port
- **Severity:** LOW
- **Status:** OPEN
- **Service:** All services
- **Raised:** 2026-06-10
- **Description:** Every service serves `/metrics` on the same port as business endpoints. Once Prometheus metrics are fully wired, they will expose per-tenant request rates and storage utilisation. In Kubernetes, metrics should be on a separate internal-only port.
- **Remediation:**
  1. Serve `/metrics` on a dedicated second HTTP port via `METRICS_ADDR` env var (default `:9090`)
  2. Exclude the metrics port from `NetworkPolicy` egress rules — allow only Prometheus pods to reach it

---


### SEC-033 — `IsPasswordPolicyError` uses fragile string-prefix heuristic; classification can break silently
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `registry-auth`
- **Raised:** 2026-06-12
- **Description:** `services/auth/internal/service/errors.go:IsPasswordPolicyError` distinguishes safe policy errors (safe to forward to callers) from internal errors (must not be forwarded) by checking whether the error message starts with the string `"hash password:"`. This is a fragile coupling to `fmt.Errorf` message text in `service.CreateUser`. Any future change to the wrapping prefix in `CreateUser` (e.g. renaming, refactoring) would silently cause internal argon2 errors to be classified as policy errors and leaked to callers, violating SEC-011. The function also does not use `errors.Is` or `errors.As`, so it cannot handle wrapped error chains correctly. The safer pattern is a sentinel error type for policy violations, or wrapping with a dedicated exported error variable.
- **Remediation:**
  1. Define a sentinel type `type PasswordPolicyError struct { msg string }` (implementing `error`) in `service` package
  2. Have `ValidatePassword` return `&PasswordPolicyError{msg: "..."}` instead of bare `errors.New`
  3. Replace `IsPasswordPolicyError` with `errors.As(err, &PasswordPolicyError{})` or `errors.As(err, new(*PasswordPolicyError))`
  4. This removes the dependency on message-string matching and makes the classification robust to future refactoring
- **References:** `services/auth/internal/service/errors.go:19`, `services/auth/internal/service/auth.go:193`, `services/auth/internal/handler/http.go:201`

---


## Resolved Issues

| ID | Title | Service | Resolved | How |
|---|---|---|---|---|
| SEC-001 | Audit table RLS bypassed by schema owner role | `registry-audit` | 2026-06-10 | Migration `20240101000002_audit_rls_role.sql` creates `registry_audit_app` NOLOGIN role, grants INSERT+SELECT on `audit_events` and DELETE on `audit_events_default` (retention path). `ALTER TABLE audit_events FORCE ROW LEVEL SECURITY` applies RLS even to the table owner. INSERT and SELECT policies defined; no UPDATE/DELETE policy → default-deny. Pool `AfterConnect` does `SET ROLE registry_audit_app` on every connection. `checkRole()` in `server.go` fails startup if effective role is not `registry_audit_app`. |
| SEC-002 | GC advisory locks: undefined locking behaviour under concurrent workers | `registry-gc` | 2026-06-11 | `services/gc/internal/advisory/lock.go` — `pg_try_advisory_lock(int8)` with FNV-64a key from tenant UUID. Connection pinned via `pgxpool.Acquire()`; explicit `pg_advisory_unlock` + `Release()` in deferred unlock. `runForTenant()` helper scopes the lock to one tenant at a time. `GC_ADVISORY_LOCK_DB_DSN` env var; no-op when unset (single-worker safe). |
| SEC-003 | Go plugin scanner path: supply chain and ABI risk | `registry-scanner` | 2026-06-11 | `.so` path was never implemented. `process.go` now uses pipe + `io.LimitReader(stdoutPipe, 10<<20)` instead of `cmd.Output()`. `pluginEnv()` passes an explicit allowlist (PATH, HOME, TMPDIR, TRIVY_*/GRYPE_* prefixes only) — all other env vars including DB/JWT credentials are stripped. |
| SEC-004 | Proxy background store: fire-and-forget failure creates silent inconsistency | `registry-proxy` | 2026-06-11 | Background goroutine calls `publishStoreQueued()` on failure, which publishes a `store.queued` RabbitMQ event. `HandleStoreQueued` consumer re-fetches blob from upstream and retries the store. Dead-letters after 3 retries via `consumer.Config{MaxRetries: 3}`. No-op when `RABBITMQ_URL` is unset. |
| SEC-008 | gRPC clients use plaintext transport | `registry-core`, `registry-proxy` | 2026-06-10 / 2026-06-11 | Added `clientCreds()` helper in both `services/core/internal/server/server.go` and `services/proxy/internal/server/server.go`. Calls `libs/auth/mtls.ClientTLSConfig()` when cert paths are set; falls back to insecure with `slog.Warn` in dev. Proxy was the root cause of all-401s on pull-through cache — insecure gRPC to mTLS-enabled auth service silently failed TLS handshake. |
| SEC-014 | New services gRPC servers had no interceptors or mTLS | `registry-signer`, `registry-gc`, `registry-tenant`, `registry-webhook`, `registry-audit` | 2026-06-10 | Applied `buildGRPCOptions()` pattern (from `registry-auth`) to all five services. Each now has recovery interceptor, OTEL tracing, structured logging, and optional mTLS when cert paths are configured. Commit `c4e08d7`. |
| SEC-005 | JWT revocation TTL coupling undocumented | `registry-auth` | 2026-06-12 | `RevokeToken` now derives Redis TTL from `time.Until(claims.ExpiresAt.Time)` with a comment explaining the self-cleaning coupling. `ValidateToken` comment cross-references the contract. |
| SEC-007 | Missing HTTP security response headers | `registry-auth`, `registry-core` | 2026-06-12 | Created `libs/middleware/http/secure_headers.go` with `SecureHeaders` middleware setting `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, `X-XSS-Protection: 0`. Applied to auth and core HTTP servers. |
| SEC-009 | IP rate limiting targets gateway IP, not client IP | `registry-auth` | 2026-06-12 | `remoteIP()` now checks `X-Forwarded-For` only when TCP peer is in `TRUSTED_PROXY_CIDRS` (comma-separated env var). Falls back to `RemoteAddr` for direct connections. Startup warning when CIDR list is empty. |
| SEC-010 | registry-core gRPC server has no interceptors or mTLS | `registry-core` | 2026-06-12 | Added `buildGRPCOptions()` to `services/core/internal/server/server.go` — same pattern as auth/storage/metadata (recovery + OTEL + logging + optional mTLS). |
| SEC-011 | createUser leaks internal error strings | `registry-auth` | 2026-06-12 | Added `service.IsPasswordPolicyError(err)` helper. Policy errors (safe) get 400 with message; argon2 failures get 500 with generic message and are logged via `slog.ErrorContext`. |
| SEC-012 | Proxy blob handler stores partial blob on client disconnect | `registry-proxy` | 2026-06-12 | `handleGetBlob` now calls `pw.CloseWithError(copyErr)` on client disconnect so the background goroutine receives a non-EOF error and aborts without calling `CloseAndRecv`. |
| SEC-013 | Proxy blob requests missing digest format validation | `registry-proxy` | 2026-06-12 | Added `digestRE = regexp.MustCompile("^sha256:[a-f0-9]{64}$")` to proxy handler. Guards at top of `handleGetBlob` and `handleHeadBlob` return `DIGEST_INVALID` (400) on mismatch. |
| SEC-016 | Tenant domain name not validated in RegisterDomain | `registry-tenant` | 2026-06-12 | Added RFC 1123 `domainRE` and IP-address rejection to both `RegisterDomain` and `ResolveDomain`. Returns `codes.InvalidArgument` for non-conforming domains. |
| SEC-017 | Tenant name not validated against allowlist | `registry-tenant` | 2026-06-12 | Added `tenantNameRE` (`^[a-z0-9][a-z0-9-]{1,63}$`) to `CreateTenant`. pgx `23505` unique violation mapped to `codes.AlreadyExists` via `isDuplicateKeyError` helper. |
| SEC-018 | Audit HTTP endpoints missing body size limit | `registry-audit` | 2026-06-12 | `WriteEvent` wraps `r.Body` with `http.MaxBytesReader(w, r.Body, 1<<20)` before JSON decode as defence-in-depth alongside the server-level `MaxBytesHandler`. |
| SEC-019 | HTTP servers missing ReadHeaderTimeout | All services | 2026-06-12 | Added `ReadHeaderTimeout: 10 * time.Second` to all 12 service HTTP servers that were missing it. |
| SEC-020 | HTTP servers missing ReadTimeout and WriteTimeout | All services | 2026-06-12 | Added `ReadTimeout: 30 * time.Second` and `WriteTimeout: 30 * time.Second` (60s for blob-streaming services) to all 12 service HTTP servers. |
| SEC-021 | Healthcheck binary uses http.DefaultClient without timeout | `libs/cmd/healthcheck` | 2026-06-12 | Replaced `http.Get(addr)` with `&http.Client{Timeout: 5*time.Second}`. Removed `//nolint:gosec` suppression. |
| SEC-022 | sslmode=prefer in docker-compose contradicts sslmode=require | All DB services | 2026-06-12 | `libs/config/loader/loader.go` now emits `slog.Warn` when DSN `sslmode` is not `"require"`. Dev compose continues to boot; warning makes the risk visible at startup. |
| SEC-023 | Vault dev root token hardcoded in docker-compose | `vault` (dev) | 2026-06-12 | Vault service and vault-init now use `${VAULT_DEV_ROOT_TOKEN:-dev-root-token}`. Warning comment added above the vault block. `VAULT_DEV_ROOT_TOKEN=` added to `.env.example`. |
| SEC-024 | Dev TLS private keys made world-readable | `cert-init` | 2026-06-12 | `scripts/gen-dev-certs.sh` now uses `chmod 644 *.crt` + `chown 65532:65532 *.key; chmod 600 *.key` instead of `chmod a+r *.key`. |
| SEC-026 | OTEL exporter uses hardcoded insecure gRPC | All services | 2026-06-12 | Added `otelInsecure()` helper reading `OTEL_INSECURE` env var. `WithInsecure()` now only applied when `OTEL_INSECURE=true`. `docker-compose.yml` sets `OTEL_INSECURE: "true"` for local dev. |
| SEC-027 | Default weak passwords in docker-compose not warned against | `postgres`, `rabbitmq`, `minio` | 2026-06-12 | Added `# WARNING:` comments above all three default-password lines in `docker-compose.yml`. |
| SEC-028 | context.Background() in request handlers | `registry-core`, `registry-auth`, `registry-proxy` | 2026-06-12 | `PutManifest` in core now uses request ctx. Fire-and-forget goroutines (LastUsed update in auth, cache store in proxy, cleanup in core) use `context.Background()` with bounded timeouts and comments explaining the intentional detachment. |
| SEC-029 | Scanner plugin path not sanitised with filepath.Clean | `registry-scanner` | 2026-06-12 | `New()` in `process.go` applies `filepath.Clean` then `filepath.IsAbs` check; fails fast with clear error if path is relative or contains `..` segments. |
| SEC-030 | SecureHeaders middleware never wired into any HTTP server | All services | 2026-06-12 | Added `httpmiddleware "github.com/steveokay/oci-janus/libs/middleware/http"` import and wrapped `http.Server.Handler` with `httpmiddleware.SecureHeaders(...)` as outermost layer in all 12 service `server.go` files. X-Content-Type-Options, X-Frame-Options, X-XSS-Protection now sent on every HTTP response including error responses from MaxBytesHandler. |
| SEC-031 | tenant/webhook/audit bypass sslmode validation on DB pool | `registry-tenant`, `registry-webhook`, `registry-audit` | 2026-06-12 | Replaced direct `pgxpool.ParseConfig(cfg.DBDSN)` calls with `loader.DBConfig{DBDSN: cfg.DBDSN, DBMaxConns: cfg.DBMaxConns}.PoolConfig()` in all three service Run() functions. sslmode=disable now rejected at startup; weaker modes logged as warning. audit AfterConnect (SET ROLE) preserved after the new PoolConfig call. |
| SEC-032 | fmt.Printf for warnings in core service loses structured context | `registry-core` | 2026-06-12 | Replaced two `fmt.Printf` calls in `registry.go` with `slog.WarnContext` — referrer store failure uses `ctx5`, push.completed publish failure uses `ctx`. Added `"log/slog"` to imports. Warnings now carry trace_id/span_id and appear in the structured log pipeline. |
| SEC-034 | TRUSTED_PROXY_CIDRS parse errors silently discarded | `registry-auth` | 2026-06-12 | `init()` in `http.go` now calls `slog.Warn` with the offending CIDR entry and parse error when `net.ParseCIDR` fails, so operators see misconfigured entries at startup rather than silently operating with reduced proxy trust coverage. |

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
| Metrics on separate port | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) | ✗ (SEC-025) |
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
