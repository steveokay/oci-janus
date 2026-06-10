# Security Issues

> Last updated: 2026-06-10
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

### SEC-001 — Audit Table: RLS bypass via schema owner role
- **Severity:** HIGH
- **Status:** OPEN
- **Service:** `registry-audit`
- **Raised:** 2026-06-09
- **Description:** PostgreSQL table owners bypass Row Level Security by default. If `registry-audit` connects as the schema owner role, the append-only RLS policy is silently ignored, allowing UPDATE and DELETE on audit records.
- **Remediation:**
  1. Create a separate low-privilege app role: `registry_audit_app` with only INSERT + SELECT grants
  2. Add `ALTER TABLE audit_events FORCE ROW LEVEL SECURITY` to the migration
  3. Add a startup check in `registry-audit` that refuses to start if `current_user` is the schema owner
  4. Document in migration file that the schema owner must never be used at runtime
- **References:** PostgreSQL docs — Row Security Policies, `FORCE ROW LEVEL SECURITY`

---

### SEC-002 — GC Advisory Locks: undefined locking behaviour under concurrent workers
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-gc`
- **Raised:** 2026-06-09
- **Description:** The CLAUDE.md specifies "advisory lock" for GC but does not specify `pg_try_advisory_lock` vs `pg_advisory_lock`, lock key derivation, or connection pinning. Two concurrent GC workers on the same tenant could corrupt `storage_used` quota figures.
- **Remediation:**
  1. Use `pg_try_advisory_lock(int8)` — non-blocking, skip tenant if lock not acquired
  2. Derive lock key from tenant UUID via FNV-64a hash (deterministic, collision-resistant)
  3. Acquire and release on a single pinned `pgxpool` connection
  4. Emit a metric on lock skip so skipped tenants are observable
- **References:** PostgreSQL advisory locks docs, `§4.11` in CLAUDE.md

---

### SEC-003 — Go Plugin Scanner Path: supply chain and ABI risk
- **Severity:** HIGH
- **Status:** OPEN
- **Service:** `registry-scanner`
- **Raised:** 2026-06-09
- **Description:** Loading scanner plugins as `.so` files via `plugin.Open()` requires exact Go toolchain + dependency version match. A compromised or malformed `.so` runs in-process with full access to the host service's memory. Checksum verification helps but does not eliminate ABI instability.
- **Remediation:**
  1. Remove `.so` plugin support entirely
  2. Support only the external process JSON-RPC path
  3. Enforce `io.LimitedReader` on plugin stdout (max 10MB) to prevent memory exhaustion
  4. Spawn plugin with `exec.CommandContext` (deadline enforced at OS level)
  5. Never inherit parent environment — pass an explicit allowlist only
- **References:** Go plugin package docs, `§4.7` in CLAUDE.md

---

### SEC-004 — Proxy Background Store: fire-and-forget failure creates silent inconsistency
- **Severity:** MEDIUM
- **Status:** OPEN
- **Service:** `registry-proxy`
- **Raised:** 2026-06-09
- **Description:** Background goroutine that stores upstream content to `registry-storage` has no retry or failure visibility. A silent failure means the next cache miss re-fetches from upstream but the failed store is never retried or alerted on.
- **Remediation:**
  1. Replace fire-and-forget goroutine with a `store.queued` RabbitMQ event published synchronously before returning the client response
  2. A worker consumes `store.queued`, performs the store, dead-letters after 3 retries
  3. On retry: re-fetch from upstream and verify `Content-Digest` matches original before storing
- **References:** `§4.6` in CLAUDE.md, `§14` (RabbitMQ event contracts)

---

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

### SEC-008 — `registry-core` gRPC clients use plaintext transport
- **Severity:** HIGH
- **Status:** OPEN
- **Service:** `registry-core`
- **Raised:** 2026-06-09
- **Description:** `services/core/internal/server/server.go` lines 34, 40, 46 use `insecure.NewCredentials()` for all three outgoing gRPC connections (to `registry-auth`, `registry-metadata`, `registry-storage`). The code comment acknowledges this as temporary. mTLS is a core security requirement (CLAUDE.md §7) and this gap means internal service communication is fully unencrypted and unauthenticated in current form.
- **Remediation:**
  1. Wire `libs/auth/mtls.ClientTLSConfig()` in `registry-core/server.go` the same way `registry-auth` server does for its gRPC server
  2. Add `MTLS_CA_CERT_PATH`, `MTLS_CERT_PATH`, `MTLS_KEY_PATH` to `registry-core` config (they are in `BaseConfig` already — just need to use them)
  3. Fail to start if the MTLS env vars are absent (remove the "insecure fallback")
  4. Add the same optional-mTLS pattern used in auth and storage if dev mode without certs is still required — warn loudly but allow dev to proceed
- **References:** `libs/auth/mtls`, CLAUDE.md §7, `services/auth/internal/server/server.go` (reference implementation)

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
- **Description:** The gRPC server in `services/core/internal/server/server.go` (line 78) is created with `grpc.NewServer()` — no interceptors, no mTLS, no recovery handler. Other services (auth, storage, metadata) all use `buildGRPCOptions()` which chains OTEL, logging, recovery, and optionally mTLS. An unhandled panic in a future gRPC handler would crash the process instead of returning `codes.Internal`.
- **Remediation:**
  1. Apply the same `buildGRPCOptions()` pattern from `registry-auth` to `registry-core`'s gRPC server
  2. This is a low-effort fix once SEC-008 is addressed (the mTLS path will be wired at the same time)
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

### SEC-014 — New services: gRPC servers have no interceptors and use plaintext transport
- **Severity:** HIGH
- **Status:** OPEN
- **Service:** `registry-signer`, `registry-gc`, `registry-tenant`, `registry-webhook`, `registry-audit`
- **Raised:** 2026-06-10
- **Description:** All five services create their gRPC servers with `grpc.NewServer()` — no interceptors (recovery, tracing, logging, auth), no mTLS credentials. Their gRPC clients (where present) use `insecure.NewCredentials()`. This mirrors the known gap in `registry-core` (SEC-008, SEC-010) but was not captured for the new services. In production, internal traffic is fully unencrypted and unauthenticated. A panic in any gRPC handler crashes the process.
- **Remediation:**
  1. For each service's gRPC server: apply `buildGRPCOptions()` pattern from `registry-auth` — wires recovery, OTEL tracing, structured logging, and optionally mTLS
  2. For each service's gRPC clients: replace `insecure.NewCredentials()` with `libs/auth/mtls.ClientTLSConfig()` once dev certs are wired (cert-init in docker-compose provides `/certs/` volume)
  3. This work is blocked on the mTLS wiring tracked in SEC-008 — tackle both together
- **References:** SEC-008, SEC-010, `services/auth/internal/server/server.go` (reference implementation)

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

## Resolved Issues

| ID | Title | Service | Resolved | How |
|---|---|---|---|---|
| — | — | — | — | — |

---

## Security Hardening Checklist Status

Tracked per service. `?` = not yet assessed.

| Rule | gateway | auth | core | storage | metadata | proxy | scanner | signer | webhook | audit | gc | tenant |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| No `unsafe` | ? | ✓ | ✓ | ? | ? | ✓ | ? | ✓ | ✓ | ✓ | ✓ | ✓ |
| No `exec.Command` with user input | ? | ✓ | ✓ | ? | ? | ✓ | ? | ✓ | ✓ | ✓ | ✓ | ✓ |
| No `os.Getenv` in handlers | ? | ✓ | ✓ | ? | ? | ✓ | ? | ✓ | ✓ | ✓ | ✓ | ✓ |
| File paths sanitised | ? | N/A | N/A | ? | ? | N/A | ? | N/A | N/A | N/A | N/A | N/A |
| HTTP client timeouts set | ? | N/A | N/A | ? | ? | ✓ | ? | N/A | ✓ | N/A | N/A | N/A |
| No `http.DefaultClient` | ? | N/A | ✓ | ? | ? | ✓ | ? | N/A | ✓ | N/A | N/A | N/A |
| `context.Background()` not in handlers | ? | ✓ | ✓ | ? | ? | ✓ | ? | ✓ | ✓ | ✓ | ✓ | ✓ |
| `crypto/rand` used (not `math/rand`) | ? | ✓ | ✓ | ? | ? | ✓ | ? | ✓ | N/A | ✓ | N/A | ✓ |
| CSP header on HTML responses | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| `X-Content-Type-Options: nosniff` | ? | ✗ (SEC-007) | ✗ (SEC-007) | ? | ? | ✓ | ? | N/A | N/A | ✗ (SEC-018) | N/A | N/A |
| CORS explicitly configured | ? | ? | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | ? |
| Request body size limits | ? | ✓ | ✓ | ? | N/A | ✓ | N/A | N/A | N/A | ✗ (SEC-018) | N/A | N/A |
| `govulncheck` in CI | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| `gosec` in CI | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| `gitleaks` pre-commit hook | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| No secrets in Docker layers | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |

---

## Recurring Security Tasks

| Task | Frequency | Owner | Last Run |
|---|---|---|---|
| OWASP ZAP baseline scan (staging) | Weekly | — | Never |
| `govulncheck` across all repos | Every PR | CI | Never |
| Dependency license check | Every PR | CI | Never |
| Secret rotation review | Quarterly | — | Never |
| Audit log retention review | Quarterly | — | Never |
| GC dry-run before production schedule change | Before each change | — | Never |
