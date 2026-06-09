# Security Issues

> Last updated: 2026-06-09
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

## Resolved Issues

| ID | Title | Service | Resolved | How |
|---|---|---|---|---|
| — | — | — | — | — |

---

## Security Hardening Checklist Status

Tracked per service. `?` = not yet assessed.

| Rule | gateway | auth | core | storage | metadata | proxy | scanner | signer | webhook | audit | gc | tenant |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| No `unsafe` | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| No `exec.Command` with user input | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| No `os.Getenv` in handlers | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| File paths sanitised | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| HTTP client timeouts set | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| No `http.DefaultClient` | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| `context.Background()` not in handlers | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| `crypto/rand` used (not `math/rand`) | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| CSP header on HTML responses | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| `X-Content-Type-Options: nosniff` | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? | ? |
| CORS explicitly configured | ? | ? | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | ? |
| Request body size limits | ? | ? | ? | N/A | N/A | ? | N/A | N/A | N/A | N/A | N/A | ? |
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
