# Security Issues

> Last updated: 2026-06-11 (SEC-002/003/004/014 resolved; proxy mTLS fix added to SEC-008 resolved entry; all docs updated)
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

### SEC-005 — JWT revocation TTL coupling undocumented
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `registry-auth`
- **Raised:** 2026-06-09
- **Description:** The Redis `jti` revocation key TTL must equal the JWT remaining lifetime. This coupling is implicit and undocumented — a future developer could "optimise" the Redis TTL independently, inadvertently extending the window for revoked tokens to be accepted.
- **Remediation:**
  1. In code: derive Redis TTL dynamically from `time.Until(claims.ExpiresAt.Time)`, never a hardcoded constant
  2. Add a code comment explaining the coupling
  3. Add a test that verifies a revoked token is rejected after Redis TTL is set to remaining lifetime (not a fixed value)
- **References:** `§4.2` in CLAUDE.md

---

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

### SEC-007 — Missing HTTP security response headers on `registry-auth` and `registry-core`
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-auth`, `registry-core`
- **Raised:** 2026-06-09
- **Description:** Neither service sets `X-Content-Type-Options: nosniff` or `X-Frame-Options: DENY` on HTTP responses. `registry-auth`'s `writeJSON`/`writeError` helpers only set `Content-Type`. `registry-core`'s `ociError` helper has the same gap. CLAUDE.md §17 requires both headers on all responses.
- **Remediation:**
  1. Add a thin `secureHeaders` HTTP middleware to `libs/middleware/http` that injects `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY`, and `X-XSS-Protection: 0` on every response
  2. Wrap both HTTP servers' mux with this middleware (one line change in each `server.go`)
  3. Add a test asserting these headers are present on all response codes
- **References:** CLAUDE.md §17, OWASP Secure Headers Project

---

### SEC-009 — IP rate limiting in `registry-auth` targets gateway IP, not client IP
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-auth`
- **Raised:** 2026-06-09
- **Description:** `remoteIP()` in `services/auth/internal/handler/http.go` reads `r.RemoteAddr` (the TCP peer). When `registry-auth` is deployed behind `registry-gateway`, the TCP peer is always the gateway's IP — all clients share a single rate-limit bucket, making per-client rate limiting ineffective. An attacker can brute-force credentials against multiple accounts without hitting the per-IP limit.
- **Remediation:**
  1. Check `X-Forwarded-For` only when the request's TCP peer IP is in a configured trusted proxy CIDR list (`TRUSTED_PROXY_CIDRS` env var)
  2. If trusted: parse the leftmost non-private IP from `X-Forwarded-For`
  3. If not trusted: fall back to `r.RemoteAddr` (current behaviour — correct for direct connections)
  4. Validate the parsed IP is a valid, non-reserved address before using as rate-limit key
  5. Add a startup warning if `TRUSTED_PROXY_CIDRS` is not configured (rate limiting is degraded)
- **References:** CLAUDE.md §4.10 (audit IP note), §4.2 (rate limit requirement), `remoteIP()` in http.go

---

### SEC-010 — `registry-core` health-check gRPC server has no interceptors or mTLS
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `registry-core`
- **Raised:** 2026-06-09
- **Description:** The gRPC server in `services/core/internal/server/server.go` is created with `grpc.NewServer()` — no interceptors, no mTLS, no recovery handler. Other services (auth, storage, metadata) all use `buildGRPCOptions()` which chains OTEL, logging, recovery, and optionally mTLS. An unhandled panic in a future gRPC handler would crash the process instead of returning `codes.Internal`.
- **Remediation:**
  1. Apply the same `buildGRPCOptions()` pattern from `registry-auth` to `registry-core`'s gRPC server
  2. SEC-008 (outbound client mTLS) is already resolved — this covers the inbound server side
- **References:** `services/auth/internal/server/server.go` (reference), `libs/middleware/grpc`

---

### SEC-011 — `createUser` endpoint leaks internal error strings
- **Severity:** INFO
- **Status:** OPEN
- **Service:** `registry-auth`
- **Raised:** 2026-06-09
- **Description:** `services/auth/internal/handler/http.go` line 152: `writeError(w, http.StatusBadRequest, "BADREQUEST", err.Error())`. The comment acknowledges this catches both intentional password-policy errors and "extremely rare" hash failures. An argon2id hash failure would expose the raw internal error string in the HTTP response body.
- **Remediation:**
  1. Enumerate the expected password-policy errors explicitly and map them to user-facing messages
  2. For all other errors from `CreateUser`, return a generic "unable to create user" message and log the real error with `slog.ErrorContext`
- **References:** `services/auth/internal/handler/http.go:152`

---

### SEC-012 — `registry-proxy` blob handler stores partial blob on client disconnect
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-proxy`
- **Raised:** 2026-06-09
- **Description:** `services/proxy/internal/handler/http.go` — `handleGetBlob` uses `io.TeeReader` to simultaneously stream a blob to the client and a background goroutine that stores it in `registry-storage`. If the client disconnects mid-stream, `io.Copy` returns an error but `pw.Close()` (pipe writer close) is called unconditionally immediately after. The background goroutine sees EOF on the pipe and stores a truncated blob under the correct digest key, corrupting the cache entry for all future clients.
- **Remediation:**
  1. Track `io.Copy` error and call `pw.CloseWithError(err)` on failure so the background goroutine receives a non-EOF error
  2. In the background goroutine, abort the `PutBlob` stream on pipe error — do not call `CloseAndRecv()`
  3. On abort: issue a best-effort `DeleteBlob` to remove any partial write
  4. Alternatively, buffer the full blob before streaming — acceptable for manifest-sized objects but not large blobs
- **References:** `services/proxy/internal/handler/http.go:handleGetBlob`, SEC-004

---

### SEC-015 — `registry-signer` in-memory sigstore is volatile
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-signer`
- **Raised:** 2026-06-10
- **Description:** `services/signer/internal/sigstore/store.go` holds all signature records in a `sync.RWMutex`-protected map. On process restart (pod crash, rolling deploy, OOM kill), all records are lost. `VerifyManifest` will return `Verified: false` for all previously signed images, breaking any policy that requires signature verification. This is also a correctness issue: two signer replicas have independent stores, so the instance that didn't sign the manifest can't verify it.
- **Remediation:**
  1. Persist signature records to PostgreSQL (add a `signatures` table to the signer's own DB, or reuse `registry-metadata`'s gRPC API)
  2. Alternatively, follow Cosign's intended model: push the signature as an OCI artifact to `registry-core` and query it back via `registry-core`'s OCI API — the in-memory store is only a hot cache for the local instance
  3. The `SigB64` field in the Record (raw private key signature bytes) should not be persisted in cleartext — store only the signature digest and re-sign on demand, or store encrypted
- **References:** `services/signer/internal/sigstore/store.go`, CLAUDE.md §4.8

---

### SEC-016 — `registry-tenant` domain name not validated in `RegisterDomain`
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-tenant`
- **Raised:** 2026-06-10
- **Description:** `RegisterDomain` in `handler/grpc.go` accepts `req.Domain` and passes it to the repository without format validation. The domain is stored in PostgreSQL (parameterised — no SQL injection risk) and later used in two unsafe ways: (1) string-concatenated into the DNS lookup target `"_registry-verify." + d.Domain` in the domain worker; (2) string-concatenated into the Redis key `"domain:" + d.Domain`. A domain containing newlines, null bytes, or Redis special characters could cause unexpected behaviour. Additionally, accepting non-RFC-1123 hostnames means the DNS TXT lookup will silently fail instead of returning an early validation error.
- **Remediation:**
  1. Add `domainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)` to the handler
  2. Reject domains that don't match at the gRPC layer (return `codes.InvalidArgument`)
  3. Also validate that the domain is not an IP address (`net.ParseIP(req.Domain) == nil`)
  4. Apply the same regex in `ResolveDomain` before doing the DB lookup
- **References:** RFC 1123 hostname syntax, `services/tenant/internal/handler/grpc.go:RegisterDomain`

---

### SEC-017 — `registry-tenant` tenant name not validated against allowlist
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `registry-tenant`
- **Raised:** 2026-06-10
- **Description:** `CreateTenant` checks only that `req.Name != ""`. CLAUDE.md §7 specifies org name: `^[a-z0-9-]{2,64}$`. Tenant names are used as subdomains in the platform domain (`<tenant>.registry.example.com`) and as display names. Accepting names with uppercase, special characters, or names shorter than 2 characters can cause subtle bugs in subdomain routing, URL construction, and display.
- **Remediation:**
  1. Add `tenantNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)` (same as CLAUDE.md org name rule)
  2. Return `codes.InvalidArgument` for non-matching names
  3. Add a uniqueness error mapping: translate pgx duplicate-key errors to `codes.AlreadyExists` instead of surfacing the raw DB error
- **References:** CLAUDE.md §7 (input validation), `services/tenant/internal/handler/grpc.go:CreateTenant`

---

### SEC-018 — `registry-audit` HTTP endpoints missing security response headers and body size limit
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-audit`
- **Raised:** 2026-06-10
- **Description:** `POST /audit/events` and `GET /audit/events` in `handler/http.go` set no `X-Content-Type-Options: nosniff` header. `WriteEvent` decodes the request body via `json.NewDecoder(r.Body).Decode(&req)` with no `http.MaxBytesReader` size cap. A caller (internal or otherwise) can send an arbitrarily large request body, causing unbounded memory allocation. `metadata` field is `json.RawMessage` — a 100 MB metadata blob would be fully read into memory before rejection.
- **Remediation:**
  1. Wrap `r.Body` with `http.MaxBytesReader(w, r.Body, 1<<20)` (1 MB) before decoding — consistent with CLAUDE.md §17 request body size limits
  2. Add a `secureHeaders` middleware (per SEC-007 remediation) to the audit HTTP mux — covers both endpoints in one place
  3. Consider bounding the `metadata` field to a known-safe size in the request struct
- **References:** SEC-007, `services/audit/internal/handler/http.go:WriteEvent`, CLAUDE.md §17

---

### SEC-013 — `registry-proxy` blob requests missing digest format validation
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `registry-proxy`
- **Raised:** 2026-06-09
- **Description:** `handleGetBlob` in `http.go` passes the `digest` path parameter directly to `blobKey(tenantID, digest)`. `blobKey` calls `strings.TrimPrefix(digest, "sha256:")` and then `hex[:2]` — if digest is malformed (e.g. empty string, or a value without the sha256 prefix that leaves `hex` less than 2 chars), this panics with an index out of range. No regex validation is applied to the digest before use.
- **Remediation:**
  1. Add `digestRE = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)` (identical to `registry-core`)
  2. Validate `digest` at the top of `handleGetBlob` and `handleHeadBlob`, return `DIGEST_INVALID` (400) on mismatch
  3. Apply the same validation to the `reference` parameter in manifest handlers when it starts with `sha256:`
- **References:** `services/proxy/internal/handler/http.go:blobKey`, `services/core/internal/handler/http.go:digestRE`

---

### SEC-019 — HTTP servers missing `ReadHeaderTimeout` (slowloris attack vector)
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-auth`, `registry-core`, `registry-proxy`, `registry-metadata`, `registry-gateway`, `registry-storage`
- **Raised:** 2026-06-10
- **Description:** Six HTTP server instances lack `ReadHeaderTimeout`. This permits slowloris attacks where a client sends HTTP headers slowly to exhaust server goroutines and connections. Some services (`registry-signer`, `registry-webhook`, `registry-audit`, `registry-gc`, `registry-scanner`, `registry-tenant`) correctly set `ReadHeaderTimeout: 10 * time.Second`; the six affected services do not.
- **Remediation:**
  1. Add `ReadHeaderTimeout: 10 * time.Second` to every `http.Server{}` literal across all six services
  2. While at it, add `ReadTimeout: 30 * time.Second` and `WriteTimeout: 30 * time.Second` for full slowloris protection (see SEC-020)
- **References:** `net/http` package docs — Server timeouts, OWASP Slowloris attack description

---

### SEC-020 — HTTP servers missing `ReadTimeout` and `WriteTimeout`
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** All services with HTTP servers
- **Raised:** 2026-06-10
- **Description:** No service configures `ReadTimeout` or `WriteTimeout` on its `http.Server`. `ReadHeaderTimeout` (partially set) protects only the header phase. An attacker sending a slow POST body or a slow-reading client holding a response open can exhaust goroutines over time. All services are affected, including those that correctly set `ReadHeaderTimeout`.
- **Remediation:**
  1. Add `ReadTimeout: 30 * time.Second` and `WriteTimeout: 30 * time.Second` to all `http.Server{}` instances
  2. For streaming endpoints (blob upload/download in `registry-core`, `registry-storage`), use `http.ResponseController.SetWriteDeadline` per-request to extend the deadline only where needed, rather than a global high timeout

---

### SEC-021 — Healthcheck binary uses `http.DefaultClient` without timeout
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `libs/cmd/healthcheck` (embedded in all service images)
- **Raised:** 2026-06-10
- **Description:** `libs/cmd/healthcheck/main.go` calls `http.Get(addr)` which uses `http.DefaultClient` with no timeout. If the target service hangs on a slow response, the healthcheck probe will block indefinitely, preventing Kubernetes from detecting the stalled container and triggering a pod restart. The `//nolint:noctx,gosec` comment suppresses linting but does not address the underlying issue.
- **Remediation:**
  ```go
  client := &http.Client{Timeout: 5 * time.Second}
  resp, err := client.Get(addr)
  ```
  Remove the `//nolint:noctx,gosec` suppression after fixing.
- **References:** CLAUDE.md §17 — "HTTP clients: always set timeouts; No default HTTP client"

---

### SEC-022 — `sslmode=prefer` in docker-compose contradicts enforced `sslmode=require`
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** All DB-owning services via `infra/docker-compose/docker-compose.yml`
- **Raised:** 2026-06-10
- **Description:** All PostgreSQL DSNs in `docker-compose.yml` use `sslmode=prefer`. The config loader in `libs/config/loader/loader.go` blocks `sslmode=disable` but accepts `sslmode=prefer`. With `sslmode=prefer`, the driver attempts TLS but silently falls back to a plaintext connection if TLS negotiation fails. In the dev Postgres container (no server cert), the actual connection is unencrypted, which contradicts the security model documented in CLAUDE.md §13 (`sslmode=require` is mandatory). Developer habits formed with `sslmode=prefer` can leak into production configurations.
- **Remediation:**
  1. Short-term: keep `sslmode=prefer` in dev compose but add a large warning comment and a `Makefile` target that asserts production DSNs use `sslmode=require`
  2. Better: configure the dev Postgres container with a self-signed TLS cert so `sslmode=require` works in dev too (see `POSTGRES_SSL_*` env vars in Postgres Docker image)
  3. Update the loader validation to emit a `slog.Warn` when `sslmode != "require"` instead of silently accepting it

---

### SEC-023 — Vault dev root token hardcoded in docker-compose
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `vault` (dev)
- **Raised:** 2026-06-10
- **Description:** `docker-compose.yml` starts Vault with `-dev-root-token-id=dev-root-token` and the vault-init container references `VAULT_TOKEN: dev-root-token` as a literal string (not an env var). This "magic string" is invisible in `.env.example` and has no protection against accidental promotion to non-dev environments. If the compose file is reused in CI or staging without overriding the token, Vault will be exposed with a well-known root credential.
- **Remediation:**
  1. Move `VAULT_DEV_ROOT_TOKEN=dev-root-token` into `.env.example` as a documented variable
  2. Replace the hardcoded string in `docker-compose.yml` with `${VAULT_DEV_ROOT_TOKEN:-dev-root-token}`
  3. Add a pre-flight check in `Makefile` or `scripts/check-env.sh` that warns if default dev tokens are detected in non-dev environments

---

### SEC-024 — Dev TLS private keys made world-readable (`chmod a+r *.key`)
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `cert-init` (affects all services via shared certs volume)
- **Raised:** 2026-06-10
- **Description:** `scripts/gen-dev-certs.sh` runs `chmod a+r "$CERTS_DIR"/*.key` to allow the non-root service container (uid 65532) to read the certs from the shared Docker volume. This makes all private key files (including the CA key) world-readable (mode 644) on the developer's host filesystem. Any process running on the dev machine can read the keys from the volume mount path. While these are dev-only certs, the pattern normalises insecure key file permissions.
- **Remediation:**
  1. Remove the `chmod a+r *.key` line; keep only `chmod 644 *.crt`
  2. Change the ownership of `.key` files to uid 65532 instead: `chown 65532:65532 "$CERTS_DIR"/*.key && chmod 600 "$CERTS_DIR"/*.key`
  3. In the cert-init `Dockerfile`, run as the same uid so generated files are already owned correctly

---

### SEC-025 — `/metrics` endpoints unauthenticated and exposed on public HTTP port
- **Severity:** LOW
- **Status:** OPEN
- **Service:** All services
- **Raised:** 2026-06-10
- **Description:** Every service serves `/metrics` on the same port as `/healthz` and business endpoints. In the current Prometheus-`TODO` state, these return 200 OK with no data, but once wired, they will expose per-tenant request rates, error counts, and storage utilisation. An authenticated user who knows the internal port can infer activity patterns for other tenants. In Kubernetes, the metrics port should be a separate, unadvertised port only reachable from within the cluster (Prometheus `serviceMonitor` targets it directly).
- **Remediation:**
  1. Serve `/metrics` on a dedicated second HTTP port (e.g. `:9090`) that is not included in the service's main `HTTP_ADDR`
  2. Add a `METRICS_ADDR` env var (default `:9090`) to each service config
  3. Exclude the metrics port from `NetworkPolicy` egress rules — allow only Prometheus pods to reach it

---

### SEC-026 — OTEL exporter uses insecure (plaintext) gRPC to Jaeger
- **Severity:** LOW
- **Status:** OPEN
- **Service:** All services (via `libs/observability/otel/otel.go`)
- **Raised:** 2026-06-10
- **Description:** `libs/observability/otel/otel.go` uses `otlptracegrpc.WithInsecure()` and `otlpmetricgrpc.WithInsecure()` for both trace and metric exporters. Span data may contain resource identifiers, user IDs, error messages, and request metadata. Transmitting this over plaintext allows any on-path observer to read or modify telemetry. A comment states "TLS terminated at the collector sidecar" — this assumption holds only in a service-mesh environment and is not enforced.
- **Remediation:**
  1. Add a `OTEL_INSECURE` boolean env var (default `false`); only apply `WithInsecure()` when explicitly set
  2. In production K8s: configure a TLS-terminating sidecar or use OTEL Collector with TLS and remove `WithInsecure()` entirely
  3. Update `local-setup.md` to document that `OTEL_INSECURE=true` is required in docker-compose dev mode

---

### SEC-027 — Default weak passwords in docker-compose are not warned against
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `postgres`, `rabbitmq`, `minio`
- **Raised:** 2026-06-10
- **Description:** `docker-compose.yml` uses `${POSTGRES_PASSWORD:-registry}`, `${RABBITMQ_DEFAULT_PASS:-registry}`, and `${MINIO_ROOT_PASSWORD:-minioadmin}` — fallback defaults are weak well-known passwords. If a developer runs `docker compose up -d` without copying `.env.example` first, all infrastructure services start with trivially guessable credentials. There is no pre-flight check enforcing that secrets are set.
- **Remediation:**
  1. Add a `check-env` Makefile target that aborts if `POSTGRES_PASSWORD` is not set or equals `registry`
  2. Generate strong random defaults in a `scripts/generate-dev-secrets.sh` script and document in `local-setup.md`
  3. Add a comment in `docker-compose.yml` making clear the fallback defaults are insufficient for any shared or non-local environment

---

### SEC-029 — `registry-scanner` plugin path not sanitised with `filepath.Clean`
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `registry-scanner`
- **Raised:** 2026-06-10
- **Description:** `services/scanner/internal/plugin/process.go:166` constructs the plugin command path via string operations without calling `filepath.Clean`. The path comes from `SCANNER_PLUGIN_PATH` env var (loaded at startup — not direct user input), but CLAUDE.md §17 requires all file paths to be sanitised with `filepath.Clean` and checked against an allowed prefix. A path containing `..` segments or trailing slashes could resolve to an unexpected binary, especially if the env var value is derived from a config management system that allows substitution.
- **Remediation:**
  1. Apply `pluginPath = filepath.Clean(cfg.PluginPath)` immediately after loading the config
  2. Assert that the cleaned path is absolute: `if !filepath.IsAbs(pluginPath) { return fmt.Errorf(...) }`
  3. Optionally: assert the binary lives within a configured allowed directory (`SCANNER_PLUGIN_DIR`) to prevent path traversal via symlinks
- **References:** CLAUDE.md §17 — "All file paths sanitised with `filepath.Clean` and checked against an allowed prefix", `services/scanner/internal/plugin/process.go`

---

### SEC-028 — `context.Background()` used inside request handlers (breaks tracing and graceful shutdown)
- **Severity:** LOW
- **Status:** OPEN
- **Service:** `registry-core`, `registry-proxy`, `registry-auth`, `registry-scanner`
- **Raised:** 2026-06-10
- **Description:** Several request handlers create new root contexts via `context.Background()` instead of deriving from the incoming request context. Notable locations: `services/core/internal/service/registry.go` (DeleteBlob and event publish calls), `services/proxy/internal/handler/http.go` (background cache store goroutine), `services/auth/internal/service/auth.go` (LastUsed update). Consequences: (1) spans created in these operations are disconnected from the parent trace; (2) operations continue after the client disconnects, wasting resources; (3) operations do not receive the shutdown signal from the server's context cancellation.
- **Remediation:**
  1. Replace `context.Background()` with the request context (`ctx`) for all operations that are part of the request lifecycle
  2. For deliberate fire-and-forget background work (e.g., cache store), use a detached context with a bounded timeout: `ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second); defer cancel()` — and add a comment explaining the detachment is intentional
  3. Background workers (e.g., scanner job queue) are exempt — they correctly use `context.Background()` as they have no parent request

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

---

## Security Hardening Checklist Status

Tracked per service. `?` = not yet assessed.

| Rule | gateway | auth | core | storage | metadata | proxy | scanner | signer | webhook | audit | gc | tenant |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| No `unsafe` | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| No `exec.Command` with user input | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| No `os.Getenv` in handlers | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| File paths sanitised | N/A | N/A | N/A | ✓ | N/A | N/A | ✗ (SEC-029) | N/A | N/A | N/A | N/A | N/A |
| HTTP client timeouts set | N/A | N/A | N/A | N/A | N/A | ✓ | N/A | N/A | ✓ | N/A | N/A | N/A |
| No `http.DefaultClient` | ✓ | N/A | ✓ | ✓ | ✓ | ✓ | ✓ | N/A | ✓ | N/A | N/A | ✗ (SEC-021 in healthcheck) |
| `context.Background()` not in handlers | ✓ | ✗ (SEC-028) | ✗ (SEC-028) | ✓ | ✓ | ✗ (SEC-028) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `crypto/rand` used (not `math/rand`) | N/A | ✓ | ✓ | N/A | N/A | ✓ | N/A | ✓ | N/A | ✓ | N/A | ✓ |
| `ReadHeaderTimeout` set on HTTP server | ✗ (SEC-019) | ✗ (SEC-019) | ✗ (SEC-019) | ✗ (SEC-019) | ✗ (SEC-019) | ✗ (SEC-019) | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| `ReadTimeout`/`WriteTimeout` set | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) | ✗ (SEC-020) |
| CSP header on HTML responses | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| `X-Content-Type-Options: nosniff` | N/A | ✗ (SEC-007) | ✗ (SEC-007) | N/A | N/A | ✓ | N/A | N/A | N/A | ✗ (SEC-018) | N/A | N/A |
| CORS explicitly configured | N/A | ✗ (unassessed) | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| Request body size limits | ✗ (SEC-019) | ✓ | ✓ | ✓ | ✓ | ✓ | N/A | N/A | N/A | ✗ (SEC-018) | N/A | N/A |
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
