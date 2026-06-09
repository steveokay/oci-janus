# Project Status

> Last updated: 2026-06-09
> This file tracks the status of all active work across the registry platform.

---

## Legend

| Status | Meaning |
|---|---|
| `NOT STARTED` | Planned, no work begun |
| `IN PROGRESS` | Actively being built |
| `BLOCKED` | Waiting on a dependency or decision |
| `IN REVIEW` | Code complete, under review |
| `DONE` | Shipped and deployed |

---

## Services

| Service | Status | Owner | Notes |
|---|---|---|---|
| `proto/` | NOT STARTED | — | Proto definitions + generated Go stubs |
| `libs/` | NOT STARTED | — | Shared Go modules |
| `registry-gateway` | NOT STARTED | — | Traefik ingress, TLS termination, tenant routing |
| `registry-auth` | NOT STARTED | — | JWT issuance, API key management |
| `registry-core` | NOT STARTED | — | OCI Distribution Spec v1.1 |
| `registry-storage` | NOT STARTED | — | Storage abstraction (MinIO/S3/GCS/Azure) |
| `registry-metadata` | NOT STARTED | — | PostgreSQL metadata service |
| `registry-proxy` | NOT STARTED | — | Pull-through proxy cache |
| `registry-scanner` | NOT STARTED | — | Vulnerability scan orchestration |
| `registry-signer` | NOT STARTED | — | Cosign + Notary v2 signing |
| `registry-webhook` | NOT STARTED | — | Webhook delivery worker |
| `registry-audit` | NOT STARTED | — | Audit log writer + query API |
| `registry-gc` | NOT STARTED | — | Garbage collection worker |
| `registry-tenant` | NOT STARTED | — | Tenant + custom domain management |
| `ui/` | NOT STARTED | — | React/TypeScript frontend |
| `infra/` | NOT STARTED | — | Helm charts, Compose, Terraform |

---

## Architecture Decisions Resolved

| # | Decision | Status | Date |
|---|---|---|---|
| 1 | Drop Go plugin (`.so`) scanner path — use external process JSON-RPC only | RESOLVED | 2026-06-09 |
| 2 | Audit table: use `FORCE ROW LEVEL SECURITY` + separate low-privilege DB role | RESOLVED | 2026-06-09 |
| 3 | GC advisory locks: use `pg_try_advisory_lock` (non-blocking), pin to single connection | RESOLVED | 2026-06-09 |
| 4 | Move `Scanner` interface from `libs/storage/driver` to `libs/scanner/plugin` | RESOLVED | 2026-06-09 |
| 5 | `registry-proxy` background store: route failures through RabbitMQ, not fire-and-forget goroutine | RESOLVED | 2026-06-09 |
| 6 | `registry-metadata` caching: Redis cache for read-heavy gRPC calls (GetManifest, GetTag, GetRepository) | RESOLVED | 2026-06-09 |
| 7 | Connection pool: add `MaxConnIdleTime`, `MaxConnLifetime`, `ConnectTimeout`; map exhaustion to `codes.ResourceExhausted` | RESOLVED | 2026-06-09 |
| 8 | Custom domain verification: add 24h notification + exponential backoff on DNS polling | RESOLVED | 2026-06-09 |
| 9 | Monorepo over per-service repos — single `github.com/<org>/registry` repo with Go workspaces (`go.work`); each service keeps its own `go.mod` for Docker build isolation; CI uses path-filtered jobs | RESOLVED | 2026-06-09 |

---

## Remediation Plans

Concrete implementation plans for each resolved architecture issue.

---

### REM-001 — Drop Go Plugin Scanner Path
- **Affects:** `registry-scanner`
- **Status:** PLANNED
- **Summary:** Remove `plugin.Open()` support. External process JSON-RPC is the only plugin path.
- **Tasks:**
  - [ ] Remove `.so` loading code from `registry-scanner`
  - [ ] Add `io.LimitedReader` on plugin stdout (max 10MB)
  - [ ] Spawn plugins with `exec.CommandContext` (OS-level deadline)
  - [ ] Define explicit env allowlist for plugin subprocess — never inherit parent env
  - [ ] Update `§4.7` in CLAUDE.md to remove Go plugin references
  - [ ] Update `SCANNER_PLUGIN_PATH` docs to reflect binary-only

---

### REM-002 — JWT Revocation TTL Coupling
- **Affects:** `registry-auth`
- **Status:** PLANNED
- **Summary:** Redis `jti` TTL must be derived from `time.Until(claims.ExpiresAt.Time)`, never a hardcoded constant.
- **Tasks:**
  - [ ] Change Redis SetEx call to use `time.Until(claims.ExpiresAt.Time)` as TTL
  - [ ] Add inline comment explaining the coupling to JWT exp
  - [ ] Add test: revoked token rejected; verify Redis TTL equals remaining token lifetime, not a fixed value
  - [ ] Add note to `§4.2` in CLAUDE.md documenting the coupling

---

### REM-003 — Proxy Background Store via RabbitMQ
- **Affects:** `registry-proxy`
- **Status:** PLANNED
- **Summary:** Replace fire-and-forget goroutine with a `store.queued` RabbitMQ event published before the client response returns.
- **Tasks:**
  - [ ] Define `store.queued` event type in `libs/rabbitmq/events`
  - [ ] Publish `store.queued` event synchronously (confirm mode) before returning client response
  - [ ] Implement `store.queued` consumer worker in `registry-proxy`
  - [ ] On retry: re-fetch from upstream, verify `Content-Digest` matches original before storing
  - [ ] Dead-letter after 3 retries → alert tenant admin
  - [ ] Update `§4.6` in CLAUDE.md to reflect the new flow

---

### REM-004 — Custom Domain Verification Notifications
- **Affects:** `registry-tenant`
- **Status:** PLANNED
- **Summary:** Add 24h notification and exponential backoff to DNS polling loop.
- **Tasks:**
  - [ ] Add `Notified24h bool` field to `DomainVerificationJob`
  - [ ] Send notification to tenant admin if unverified after 24h
  - [ ] Send failure notification and stop polling at 48h
  - [ ] Implement exponential backoff on DNS polling: 5m → 10m → 20m
  - [ ] Update `§4.12` in CLAUDE.md with the new polling behaviour

---

### REM-005 — Audit Table RLS + Role Separation
- **Affects:** `registry-audit`
- **Status:** PLANNED
- **Summary:** Use a low-privilege app role and `FORCE ROW LEVEL SECURITY` on the audit table.
- **Tasks:**
  - [ ] Create `registry_audit_app` role in migration: INSERT + SELECT only, no UPDATE/DELETE
  - [ ] Add `ALTER TABLE audit_events FORCE ROW LEVEL SECURITY` to migration
  - [ ] Add `INSERT` policy: `WITH CHECK (true)`
  - [ ] Add `SELECT` policy: `USING (tenant_id = current_setting('app.tenant_id')::uuid)`
  - [ ] Add startup check: refuse to start if `current_user` = schema owner role
  - [ ] Update `security.md` SEC-001 to RESOLVED when complete

---

### REM-006 — Connection Pool Exhaustion Handling
- **Affects:** `registry-auth`, `registry-audit`, `registry-metadata`, `registry-tenant`
- **Status:** PLANNED
- **Summary:** Map pool exhaustion to `codes.ResourceExhausted`; configure pool timeouts.
- **Tasks:**
  - [ ] Add to `libs/config/loader`: `DBMaxConns`, `DBMinConns`, `DBConnectTimeout`, `DBMaxConnLifetime`, `DBMaxConnIdleTime`
  - [ ] Set `ConnectTimeout: 5s`, `MaxConnLifetime: 30m`, `MaxConnIdleTime: 5m` in all pool configs
  - [ ] In each repository: detect `context.DeadlineExceeded` from `pool.Acquire` → return `codes.ResourceExhausted`
  - [ ] Update retry interceptor in `libs/middleware/grpc` to NOT retry on `codes.ResourceExhausted`
  - [ ] Update `§13` in CLAUDE.md with pool config fields

---

### REM-007 — Registry-Metadata Caching (Part A — Redis)
- **Affects:** `registry-metadata` (client-side caching in consuming services)
- **Status:** PLANNED
- **Summary:** Cache read-heavy gRPC calls in Redis to reduce `registry-metadata` load.
- **Cacheable calls and TTLs:**
  - `GetRepository` → 30s, invalidate on `UpdateRepository`
  - `GetManifest` → 5m, invalidate on `DeleteManifest`
  - `GetTag` → 30s, invalidate on `PutTag` / `DeleteTag`
  - `GetTenantQuotaUsage` → 10s, invalidate on `Increment` / `Decrement`
- **Tasks:**
  - [ ] Add cache interceptor to `libs/middleware/grpc` (client-side, wraps metadata calls)
  - [ ] Cache key format: `meta:<method>:<tenant_id>:<primary_key>`
  - [ ] Invalidation: publish cache-bust key alongside write gRPC calls
  - [ ] Add `METADATA_CACHE_ENABLED` env var (default true) to allow disabling in tests

---

### REM-008 — Registry-Metadata Read Replica (Part B)
- **Affects:** `registry-metadata`
- **Status:** PLANNED (after Part A)
- **Summary:** Route list/reporting queries to a read replica.
- **Tasks:**
  - [ ] Add `DB_DSN_REPLICA` env var to `registry-metadata`
  - [ ] Create two `pgxpool` instances: primary (read-write), replica (read-only)
  - [ ] Route `ListTags`, `ListRepositories`, `ListOrphanedBlobs` to replica pool
  - [ ] All writes and transactional reads go to primary
  - [ ] Replica connection is optional — fall back to primary if `DB_DSN_REPLICA` unset

---

### REM-009 — GC Advisory Locks
- **Affects:** `registry-gc`
- **Status:** PLANNED
- **Summary:** Use `pg_try_advisory_lock` with FNV-64a key derivation, pinned to a single connection.
- **Tasks:**
  - [ ] Implement `gcLockKey(tenantID uuid.UUID) int64` using FNV-64a hash
  - [ ] Use `pg_try_advisory_lock($1)` — non-blocking, skip tenant on failure
  - [ ] Acquire and release on a single pinned connection from `pgxpool`
  - [ ] Emit `registry_gc_lock_skipped_total` metric when lock not acquired
  - [ ] Add `defer db.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)` after acquire
  - [ ] Update `security.md` SEC-002 to RESOLVED when complete

---

### REM-010 — Move Scanner Interface Location
- **Affects:** `libs/`, `services/scanner`
- **Status:** PLANNED
- **Summary:** Move `Scanner` interface from `libs/storage/driver` to `libs/scanner/plugin`.
- **Tasks:**
  - [ ] Create `libs/scanner/plugin/` package
  - [ ] Move `Scanner`, `ScanRequest`, `ScanResult`, `Finding`, `LayerRef`, `BlobFetcher` types there
  - [ ] Update all import paths in `services/scanner`
  - [ ] Update `§5` in CLAUDE.md to reflect correct location (already done)

---

## Open Decisions

| # | Question | Raised | Blocking |
|---|---|---|---|
| 1 | Which org/GitHub namespace to use for all repos? | 2026-06-09 | Everything |
| 2 | Cloud provider for initial production deployment (K8s target)? | 2026-06-09 | `infra/` |
| 3 | Which scanner plugin to ship as default (Trivy vs Grype)? | 2026-06-09 | `registry-scanner` |
| 4 | KMS provider for production signing keys? | 2026-06-09 | `registry-signer` |

---

## Current Sprint

> Update this section at the start of each sprint.

**Sprint goal:** — (not started)

| Task | Service | Status |
|---|---|---|
| — | — | — |

---

## Notes

- Build order recommendation: `proto/` → `libs/` → `services/auth` → `services/metadata` → `services/storage` → `services/core` → remaining services
- `registry-core` OCI conformance tests must pass before any other service builds on top of it
