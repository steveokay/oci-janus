# Project Status

> Last updated: 2026-06-09 (PM review)
> PM Agent last ran: 2026-06-09
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
| `proto/` | DONE | — | All `.proto` files written; Go stubs generated and committed to `proto/gen/go/`. |
| `libs/` | DONE | — | All packages implemented: auth/jwt, auth/mtls, crypto/argon2, crypto/aes, middleware/grpc+http, observability/otel, rabbitmq/publisher+consumer+events, storage/driver, scanner/plugin, errors/codes, testutil, config/loader. |
| `services/auth` | DONE | — | Full implementation: JWT RS256 issuance, API key management (argon2id), gRPC ValidateToken/ValidateAPIKey/GetUserPermissions, DB migrations, JWKS endpoint, lockout + rate limiting. Committed b9f5269. |
| `services/core` | DONE | — | Full OCI Distribution Spec v1.1: all 14 `/v2/` endpoints, chunked upload state in Redis, SHA256 digest verification, RabbitMQ push.completed publish, per-tenant quota enforcement, custom path dispatcher for `org/repo` names. Committed 9b46675. |
| `infra/` | IN PROGRESS | — | docker-compose.yml done (all infra services + Vault dev mode). Helm umbrella chart scaffolded. Terraform deferred. Runbook stubs present. |
| `services/metadata` | NOT STARTED | — | Next priority. DB migrations, all gRPC handlers for Repository/Tag/Manifest/Blob/Quota/ScanResult CRUD. `services/core` cannot be end-to-end tested without this. |
| `services/storage` | NOT STARTED | — | Next priority (parallel with metadata). MinIO driver minimum for local dev, then S3/GCS/Azure. All storage gRPC handlers. |
| `services/gateway` | NOT STARTED | — | Scaffold only. Depends on auth + core + tenant. |
| `services/proxy` | NOT STARTED | — | Scaffold only. Depends on core, metadata, storage. |
| `services/scanner` | NOT STARTED | — | Scaffold only. Depends on core (push.completed event), metadata, storage. Apply REM-001 (external process only). |
| `services/signer` | NOT STARTED | — | Scaffold only. Depends on core, Vault (dev mode ready in docker-compose). |
| `services/webhook` | NOT STARTED | — | Scaffold only. Depends on core events. |
| `services/audit` | NOT STARTED | — | Scaffold only. Depends on RabbitMQ events from multiple services. Apply REM-005. |
| `services/gc` | NOT STARTED | — | Scaffold only. Depends on metadata + storage. Apply REM-009. |
| `services/tenant` | NOT STARTED | — | Scaffold only. Depended on by gateway for domain resolution. |
| `ui/` | NOT STARTED | — | Scaffold only: Vite + React + TypeScript. No routes or components. Depended on by nothing — can be built last. |

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
| 9 | Monorepo over per-service repos — single `github.com/steveokay/oci-janus` repo with Go workspaces (`go.work`) | RESOLVED | 2026-06-09 |
| 10 | K8s target: Docker Desktop local cluster — no cloud provider; Terraform deferred | RESOLVED | 2026-06-09 |
| 11 | Default vulnerability scanner: **Trivy** — external process plugin via JSON-RPC | RESOLVED | 2026-06-09 |
| 12 | Local KMS: **HashiCorp Vault dev mode** in docker-compose; `SIGNER_KEY_BACKEND=vault` | RESOLVED | 2026-06-09 |

---

## Remediation Plans

### REM-001 — Drop Go Plugin Scanner Path
- **Affects:** `registry-scanner`
- **Status:** PLANNED (apply when services/scanner is built)
- **Tasks:**
  - [ ] Remove `.so` loading code from `registry-scanner`
  - [ ] Add `io.LimitedReader` on plugin stdout (max 10MB)
  - [ ] Spawn plugins with `exec.CommandContext` (OS-level deadline)
  - [ ] Define explicit env allowlist for plugin subprocess — never inherit parent env
  - [ ] Update `§4.7` in CLAUDE.md to remove Go plugin references

---

### REM-002 — JWT Revocation TTL Coupling
- **Affects:** `registry-auth`
- **Status:** DONE ✅ — implemented in services/auth with `time.Until(claims.ExpiresAt.Time)` as Redis TTL.

---

### REM-003 — Proxy Background Store via RabbitMQ
- **Affects:** `registry-proxy`
- **Status:** PLANNED (apply when services/proxy is built)
- **Tasks:**
  - [ ] Define `store.queued` event type in `libs/rabbitmq/events`
  - [ ] Publish `store.queued` event synchronously (confirm mode) before returning client response
  - [ ] Implement `store.queued` consumer worker in `registry-proxy`
  - [ ] On retry: re-fetch from upstream, verify `Content-Digest` matches original before storing
  - [ ] Dead-letter after 3 retries → alert tenant admin

---

### REM-004 — Custom Domain Verification Notifications
- **Affects:** `registry-tenant`
- **Status:** PLANNED (apply when services/tenant is built)
- **Tasks:**
  - [ ] Add `Notified24h bool` field to `DomainVerificationJob`
  - [ ] Send notification to tenant admin if unverified after 24h
  - [ ] Send failure notification and stop polling at 48h
  - [ ] Implement exponential backoff on DNS polling: 5m → 10m → 20m

---

### REM-005 — Audit Table RLS + Role Separation
- **Affects:** `registry-audit`
- **Status:** PLANNED (apply when services/audit is built)
- **Tasks:**
  - [ ] Create `registry_audit_app` role in migration: INSERT + SELECT only, no UPDATE/DELETE
  - [ ] Add `ALTER TABLE audit_events FORCE ROW LEVEL SECURITY` to migration
  - [ ] Add `INSERT` policy: `WITH CHECK (true)`
  - [ ] Add `SELECT` policy: `USING (tenant_id = current_setting('app.tenant_id')::uuid)`
  - [ ] Add startup check: refuse to start if `current_user` = schema owner role

---

### REM-006 — Connection Pool Exhaustion Handling
- **Affects:** `registry-auth`, `registry-audit`, `registry-metadata`, `registry-tenant`
- **Status:** PLANNED (apply during each service build)
- **Tasks:**
  - [ ] Set `ConnectTimeout: 5s`, `MaxConnLifetime: 30m`, `MaxConnIdleTime: 5m` in all pool configs
  - [ ] In each repository: detect `context.DeadlineExceeded` from `pool.Acquire` → return `codes.ResourceExhausted`
  - [ ] Update retry interceptor in `libs/middleware/grpc` to NOT retry on `codes.ResourceExhausted`

---

### REM-007 — Registry-Metadata Caching (Part A — Redis)
- **Affects:** `registry-metadata` (client-side caching in consuming services)
- **Status:** PLANNED (implement during services/metadata build)
- **Cacheable calls and TTLs:**
  - `GetRepository` → 30s
  - `GetManifest` → 5m
  - `GetTag` → 30s
  - `GetTenantQuotaUsage` → 10s
- **Tasks:**
  - [ ] Add cache interceptor to `libs/middleware/grpc` (client-side, wraps metadata calls)
  - [ ] Cache key format: `meta:<method>:<tenant_id>:<primary_key>`
  - [ ] Invalidation: publish cache-bust key alongside write gRPC calls

---

### REM-008 — Registry-Metadata Read Replica (Part B)
- **Affects:** `registry-metadata`
- **Status:** PLANNED (after REM-007)
- **Tasks:**
  - [ ] Add `DB_DSN_REPLICA` env var to `registry-metadata`
  - [ ] Create two `pgxpool` instances: primary (read-write), replica (read-only)
  - [ ] Route `ListTags`, `ListRepositories`, `ListOrphanedBlobs` to replica pool

---

### REM-009 — GC Advisory Locks
- **Affects:** `registry-gc`
- **Status:** PLANNED (apply when services/gc is built)
- **Tasks:**
  - [ ] Implement `gcLockKey(tenantID uuid.UUID) int64` using FNV-64a hash
  - [ ] Use `pg_try_advisory_lock($1)` — non-blocking, skip tenant on failure
  - [ ] Acquire and release on a single pinned connection from `pgxpool`
  - [ ] Emit `registry_gc_lock_skipped_total` metric when lock not acquired

---

### REM-010 — Move Scanner Interface Location
- **Affects:** `libs/`, `services/scanner`
- **Status:** DONE ✅ — `libs/scanner/plugin/plugin.go` created during monorepo scaffold.

---

## Open Decisions

All decisions resolved. No blockers.

| # | Question | Status | Resolution |
|---|---|---|---|
| 1 | Which org/GitHub namespace? | ✅ RESOLVED | `github.com/steveokay/oci-janus` |
| 2 | Cloud provider / K8s target? | ✅ RESOLVED | Docker Desktop local cluster. Terraform deferred. |
| 3 | Default scanner plugin? | ✅ RESOLVED | **Trivy** as external process plugin. |
| 4 | Local KMS for signing keys? | ✅ RESOLVED | **HashiCorp Vault dev mode** in docker-compose. |

---

## Completed Sprints

### Sprint 1 — Foundation (COMPLETE)
> Goal: `libs/` foundations, local dev environment, `services/auth` + `services/core` functional.

| Task | Service | Status |
|---|---|---|
| Run `buf generate` — produce Go stubs in `proto/gen/go/` | `proto/` | DONE |
| Implement all `libs/` packages | `libs/` | DONE |
| Add Vault dev mode to `docker-compose.yml` | `infra/` | DONE |
| Write DB migrations for auth schema (users, api_keys) | `services/auth` | DONE |
| Implement auth HTTP + gRPC handlers | `services/auth` | DONE |
| Implement all OCI `/v2/` endpoints in `services/core` | `services/core` | DONE |
| Split `agents.md` into `.claude/agents/` individual agent files | tooling | DONE |

---

## Current Sprint

**Sprint 2 — Persistence Layer**
> Goal: `services/metadata` and `services/storage` fully implemented so that `services/core` can be tested end-to-end with a real local stack.
> Why: `services/core` is the OCI hub — it calls both metadata and storage on every push/pull. Neither integration testing nor OCI conformance testing can proceed without both services live.

| Task | Service | Status |
|---|---|---|
| DB migrations for metadata schema (repositories, manifests, tags, blobs, blob_links, scan_results, tenants, organizations) | `services/metadata` | NOT STARTED |
| Implement all `MetadataService` gRPC handlers | `services/metadata` | NOT STARTED |
| Apply REM-006 (pool exhaustion) and REM-007 (Redis caching) in metadata | `services/metadata` | NOT STARTED |
| Integration tests for metadata service | `services/metadata` | NOT STARTED |
| Implement MinIO storage driver (minimum for local dev) | `services/storage` | NOT STARTED |
| Implement all `StorageService` gRPC handlers (streaming PutBlob/GetBlob, multipart) | `services/storage` | NOT STARTED |
| Integration tests for storage service | `services/storage` | NOT STARTED |
| Docker Compose spin-up: verify all services start healthy together | `infra/` | NOT STARTED |
| OCI conformance suite against `services/core` + live metadata + storage | `services/core` | NOT STARTED |

---

## Notes

- **Build order:** `proto/` → `libs/` → `services/auth` → `services/metadata` → `services/storage` → `services/core` → (remaining services in parallel)
- First four steps of build order are DONE. Sprint 2 targets steps 5 and 6.
- `services/core` OCI conformance tests must pass before any downstream service (`proxy`, `scanner`, `gc`, `webhook`) is started.
- Services that depend on `services/core` being DONE: `registry-scanner`, `registry-proxy`, `registry-gc`, `registry-webhook`.
- Vault dev mode is already in docker-compose — `services/signer` can start once `services/core` is DONE.
- All architecture decisions are resolved. No blockers for Sprint 2.
