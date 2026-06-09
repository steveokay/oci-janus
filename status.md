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
| `proto/` | IN PROGRESS | — | All 8 `.proto` files written with full service definitions. Generated Go stubs (`proto/gen/go/`) not yet produced — need `buf generate` run. |
| `libs/` | IN PROGRESS | — | Scaffold + 5 real files: Driver interface, Scanner interface, mTLS helpers, RabbitMQ events, gRPC error codes. ~15 package directories still empty stubs. |
| `services/gateway` | IN PROGRESS | — | Scaffold only: go.mod, main.go, config, server, Dockerfile, Makefile. No business logic. |
| `services/auth` | IN PROGRESS | — | Scaffold only. No JWT issuance, API key, DB access, or JWKS endpoint yet. |
| `services/core` | IN PROGRESS | — | Scaffold only. No OCI endpoints, blob handling, or upload state yet. |
| `services/storage` | IN PROGRESS | — | Scaffold only. No driver implementations (MinIO/S3/GCS/Azure) yet. |
| `services/metadata` | IN PROGRESS | — | Scaffold only. No DB migrations, gRPC handlers, or repo layer yet. |
| `services/proxy` | IN PROGRESS | — | Scaffold only. No upstream fetching, cache logic, or RabbitMQ publish yet. |
| `services/scanner` | IN PROGRESS | — | Scaffold only. No plugin runner, job queue, or scan logic yet. |
| `services/signer` | IN PROGRESS | — | Scaffold only. No Cosign/Notary integration yet. |
| `services/webhook` | IN PROGRESS | — | Scaffold only. No delivery worker, retry logic, or HMAC signing yet. |
| `services/audit` | IN PROGRESS | — | Scaffold only. No DB schema, event consumer, or append-only enforcement yet. |
| `services/gc` | IN PROGRESS | — | Scaffold only. No mark/sweep algorithm or advisory lock yet. |
| `services/tenant` | IN PROGRESS | — | Scaffold only. No tenant CRUD, domain verification, or policy management yet. |
| `ui/` | IN PROGRESS | — | Scaffold only: package.json, Vite config, tsconfig, main.tsx, globals.css (design tokens), Axios client. No routes or components yet. |
| `infra/` | IN PROGRESS | — | docker-compose.yml (all infra services), Helm umbrella chart + values, 3 runbook stubs. Terraform empty. |

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
| 10 | K8s target: Docker Desktop local cluster — no cloud provider; Helm charts deploy to local cluster; Terraform deferred until production target is chosen | RESOLVED | 2026-06-09 |
| 11 | Default vulnerability scanner: **Trivy** — runs as external process plugin via JSON-RPC; `SCANNER_PLUGIN_PATH` points to Trivy binary; no `.so` loading | RESOLVED | 2026-06-09 |
| 12 | Local KMS: **HashiCorp Vault dev mode** in docker-compose; `SIGNER_KEY_BACKEND=vault`; Vault Transit engine; prod path is swap `VAULT_ADDR` or change backend env var — zero code changes | RESOLVED | 2026-06-09 |

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
- **Status:** DONE ✅
- **Summary:** `libs/scanner/plugin/plugin.go` created during monorepo scaffold with full Scanner interface, ScanRequest, ScanResult, Finding, LayerRef, BlobFetcher types. CLAUDE.md already updated. No further action needed.

---

## Open Decisions

| # | Question | Status | Resolution |
|---|---|---|---|
| 1 | Which org/GitHub namespace? | ✅ RESOLVED | `github.com/steveokay/oci-janus` — in all `go.mod` files and CI |
| 2 | Cloud provider / K8s target? | ✅ RESOLVED | No cloud provider. K8s runs locally on **Docker Desktop** for all testing. No Terraform needed at this stage. Helm charts deploy to the local cluster. |
| 3 | Default scanner plugin? | ✅ RESOLVED | **Trivy** — add as default external process plugin. `SCANNER_PLUGIN_PATH` points to Trivy binary. |
| 4 | Local KMS for signing keys? | ✅ RESOLVED | **HashiCorp Vault in dev mode** (Docker container). `SIGNER_KEY_BACKEND=vault`, Vault Transit engine for key storage and signing. No raw key material in env vars or files. Prod path: swap `VAULT_ADDR` to a real Vault instance or change backend to `awskms`/`gcpkms` with zero code changes. |

---

## Current Sprint

**Sprint 1 — Foundation**
> Goal: `libs/` foundations complete, local dev environment running, `services/auth` functional end-to-end.
> Why: Every other service depends on auth for token validation. Nothing can be integration-tested without a working auth service and a live local stack.

| Task | Service | Status |
|---|---|---|
| Run `buf generate` — produce Go stubs in `proto/gen/go/` | `proto/` | NOT STARTED |
| Implement `libs/config/loader` — Viper loader with DB + pool config fields | `libs/` | NOT STARTED |
| Implement `libs/middleware/grpc` — server interceptors (recovery, auth, tracing, logging, metrics) | `libs/` | NOT STARTED |
| Implement `libs/observability/otel` — OTEL bootstrap (tracer + meter, pluggable exporter) | `libs/` | NOT STARTED |
| Implement `libs/crypto/argon2` — argon2id hash + verify helpers | `libs/` | NOT STARTED |
| Implement `libs/crypto/aes` — AES-256-GCM encrypt/decrypt helpers | `libs/` | NOT STARTED |
| Implement `libs/rabbitmq/publisher` — confirm-mode publisher | `libs/` | NOT STARTED |
| Implement `libs/rabbitmq/consumer` — manual-ack consumer with DLX | `libs/` | NOT STARTED |
| Add Vault dev mode to `docker-compose.yml` + init script (enable Transit engine, create signing key) | `infra/` | NOT STARTED |
| Spin up local stack — `docker compose up` in `infra/docker-compose/` | `infra/` | NOT STARTED |
| Write DB migrations for auth schema (users, api_keys) | `services/auth` | NOT STARTED |
| Implement auth HTTP endpoints (POST /auth/token, /api/v1/login, /api/v1/apikeys, JWKS) | `services/auth` | NOT STARTED |
| Implement auth gRPC handlers (ValidateToken, ValidateAPIKey, GetUserPermissions) | `services/auth` | NOT STARTED |
| Apply REM-002: JWT revocation TTL coupling | `services/auth` | NOT STARTED |

---

## Notes

- Build order: `proto/` (generate stubs) → `libs/` (foundations) → `services/auth` → `services/metadata` → `services/storage` → `services/core` → remaining services
- `services/core` OCI conformance tests must pass before any downstream service builds on top of it
- Do not start `services/core` until `services/auth`, `services/metadata`, and `services/storage` gRPC handlers are functional
- No cloud provider. K8s = Docker Desktop local cluster. Terraform is deferred — not needed until a production target is chosen.
- Vault dev mode goes into docker-compose alongside the other infra services. An init script enables the Transit engine and seeds the dev signing key on first boot.
- Trivy scanner: `services/scanner` will invoke the Trivy binary as an external process. Trivy installs into the scanner Docker image — no separate plugin download needed.
- All open decisions are now resolved. No blockers for Sprint 1 or Sprint 2.
