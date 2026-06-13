# Project Status

> Last updated: 2026-06-13 (Sprint 5 active — Security Scan Results + Build History screens shipped; management docker-compose wiring merged; frontend still on mock data)
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
| `libs/` | DONE | — | All packages implemented: auth/jwt, auth/mtls, crypto/argon2, crypto/aes, middleware/grpc+http, observability/otel, rabbitmq/publisher+consumer+events, storage/driver, scanner/plugin, errors/codes, testutil, config/loader. REM-006 pool config fully implemented in `libs/config/loader`. `libs/middleware/http/secure_headers.go` (SecureHeaders middleware — CSP, X-Content-Type-Options, X-Frame-Options, HSTS) added during security hardening sprint. |
| `services/auth` | DONE | — | Full implementation: JWT RS256 issuance, API key management (argon2id), gRPC ValidateToken/ValidateAPIKey/GetUserPermissions, DB migrations, JWKS endpoint, lockout + rate limiting. Committed b9f5269. |
| `services/core` | DONE | — | Full OCI Distribution Spec v1.1: all 14 `/v2/` endpoints, chunked upload state in Redis, SHA256 digest verification, RabbitMQ push.completed publish, per-tenant quota enforcement, custom path dispatcher for `org/repo` names. Committed 9b46675. |
| `services/metadata` | DONE | — | All MetadataService gRPC handlers, DB migrations (repositories, manifests, tags, blobs, blob_links, scan_results), Redis client wired. REM-006 applied via shared DBConfig. Committed 5f4e526. |
| `services/storage` | DONE | — | All StorageService gRPC handlers (streaming PutBlob/GetBlob, multipart), MinIO + S3/GCS/Azure drivers, storage/driver subdirectory. Committed 5f4e526. |
| `services/gateway` | DONE | — | Full internal/ layout: config, handler, middleware, repository, server, service, testutil. Committed fd90f3d. |
| `services/proxy` | DONE | — | Pull-through proxy with upstream client, cache hit/miss logic, background blob store (goroutine), upstream credential encryption. Committed 3a9264a. REM-003 partially applied (see Remediation Plans). |
| `services/scanner` | DONE | — | External-process JSON-RPC plugin, checksum validation, worker pool, RabbitMQ consumer for push.completed, scan result storage. REM-001 substantially applied. Committed 2bcaf1c. |
| `services/webhook` | DONE | — | Webhook delivery worker, exponential backoff (5s/30s/5m/30m/2h), HMAC-SHA256 signing, SSRF protection, DLQ after 5 failures. Committed adc0dd8. |
| `services/audit` | DONE | — | Immutable audit event table (partitioned), event consumer from RabbitMQ, append-only enforcement via PostgreSQL RULE. REM-005 partially applied (see Remediation Plans). Committed 0c827c3. |
| `services/gc` | DONE | — | Mark-sweep GC algorithm (collector.go), dry-run / manifests / blobs / full modes, RabbitMQ event publishing on deletion. REM-009 not yet applied (see Remediation Plans). Committed f226e81. |
| `services/tenant` | DONE | — | Tenant CRUD, domain verification worker (5-minute poll, 48h cutoff), per-tenant quota config. REM-004 partially applied (see Remediation Plans). Committed ff5875e. |
| `services/signer` | DONE | — | Cosign-compatible ECDSA P-256 signing, Vault key backend, signing/sigstore subpackages, SignManifest/VerifyManifest/ListSignatures gRPC handlers. Committed e4ba6c7. |
| `services/management` | IN PROGRESS | — | Management REST API — BFF for the frontend, CLI, and Terraform consumers. Endpoint surface will grow (RBAC management, webhooks, API keys, audit log queries, tenant settings). Scaffolded and building cleanly: all middleware, handler routes, server, config, Dockerfile, go.sum. Pending: docker-compose wiring, proto `GetRepositoryByName` RPC, frontend data hooks. Documented in CLAUDE.md §4.13. |
| `infra/` | DONE | — | docker-compose.yml (all services + Vault dev mode + MinIO + Jaeger), Helm umbrella chart with all 12 sub-charts, runbooks for secret-rotation, minio-encryption, notary-root-key-ceremony. Terraform directory present (deferred). Committed fd90f3d. |
| security hardening | DONE | — | 19 SEC items resolved in Sprint 4: HTTP timeouts (SEC-019/020), healthcheck timeout (SEC-021), `sslmode=require` enforcement (SEC-022), Vault token isolation (SEC-023), cert key permissions `chmod 600` (SEC-024), secure response headers via `libs/middleware/http/secure_headers.go` (SEC-007/018), auth client-IP rate limiting via `TRUSTED_PROXY_CIDRS` (SEC-009), proxy partial-blob abort (SEC-012), context propagation (SEC-028), and others. Deferred: SEC-006, SEC-015, SEC-025. |
| `frontend/` | IN PROGRESS | — | Vite + React + TypeScript. Login page: QA-verified ✅. Repository Dashboard: QA-verified ✅ (new Stitch design — Operations Overview). Image Details & Tags: UI built, QA pass pending. Security Scan Results + Build History: not started. Vite dev proxy wired (`/api` → `localhost:8080`). JWT stored in Zustand memory only (FE-SEC-001/002 resolved). Blocked on `services/management` for data wiring. |

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
- **Status:** DONE ✅ — all tasks complete.
- **Tasks:**
  - [x] Remove `.so` loading code from `registry-scanner` (never added — external process only)
  - [x] Spawn plugins with `exec.CommandContext` (OS-level deadline)
  - [x] Validate plugin binary checksum (SHA256) against `SCANNER_PLUGIN_CHECKSUM` before loading
  - [x] Add `io.LimitedReader` on plugin stdout (max 10MB) — pipe+LimitReader pattern in process.go
  - [x] Define explicit env allowlist for plugin subprocess — `pluginEnv()` in process.go passes only PATH/HOME/TMPDIR/TRIVY_*/GRYPE_* prefixes
  - [x] Update `§4.7` in CLAUDE.md to remove Go plugin references (CLAUDE.md §4.7 describes external process only)

---

### REM-002 — JWT Revocation TTL Coupling
- **Affects:** `registry-auth`
- **Status:** DONE ✅ — implemented in services/auth with `time.Until(claims.ExpiresAt.Time)` as Redis TTL.

---

### REM-003 — Proxy Background Store via RabbitMQ
- **Affects:** `registry-proxy`
- **Status:** DONE ✅ — RabbitMQ retry path fully implemented.
- **Tasks:**
  - [x] Define `store.queued` event type in `libs/rabbitmq/events` (`StoreQueuedPayload`)
  - [x] On background goroutine failure: publish `store.queued` event via `publishStoreQueued()`
  - [x] Implement `store.queued` consumer in `registry-proxy` server.go (`HandleStoreQueued` + `retryStoreBlob`)
  - [x] On retry: re-fetch blob from upstream with original credentials
  - [x] Dead-letter after 3 retries (consumer.Config MaxRetries: 3)

---

### REM-004 — Custom Domain Verification Notifications
- **Affects:** `registry-tenant`
- **Status:** DONE ✅ — notifications + backoff + DB columns all implemented.
- **Tasks:**
  - [x] Add `Notified24h`, `Notified48h` bool fields to `DomainRecord`; migration `20260611000001_domain_notification.sql`
  - [x] Send 24h notification (logged + `MarkDomain24hNotified`) when age ≥ 24h, idempotent via flag
  - [x] Send 48h failure notification when age ≥ 47h, idempotent via flag
  - [x] Exponential backoff: age <1h → 5min, 1h–12h → 10min, >12h → 20min (`calcBackoff`)
  - [x] `next_poll_after` column + index; `ListUnverifiedDomains` filters `next_poll_after <= now()`
  - [x] 8 unit tests in `worker_test.go` — all passing

---

### REM-005 — Audit Table RLS + Role Separation
- **Affects:** `registry-audit`
- **Status:** DONE ✅ — migration `20240101000002_audit_rls_role.sql` + server.go AfterConnect + checkRole().
- **Tasks:**
  - [x] Create `registry_audit_app` NOLOGIN role in migration: INSERT + SELECT on audit_events, DELETE on audit_events_default (retention path only)
  - [x] Add `ALTER TABLE audit_events FORCE ROW LEVEL SECURITY` to migration
  - [x] Add `INSERT` policy: `WITH CHECK (true)`
  - [x] Add `SELECT` policy: `USING (true)` (tenant isolation via app-layer WHERE clauses)
  - [x] Pool `AfterConnect` does `SET ROLE registry_audit_app` on every connection
  - [x] `checkRole()` startup check refuses to start if effective role ≠ `registry_audit_app`

---

### REM-006 — Connection Pool Exhaustion Handling
- **Affects:** `registry-auth`, `registry-audit`, `registry-metadata`, `registry-tenant`
- **Status:** DONE ✅ — `libs/config/loader.DBConfig.PoolConfig()` sets `ConnectTimeout: 5s`, `MaxConnLifetime: 30m`, `MaxConnIdleTime: 5m` with safe defaults. All DB-owning services use `DBConfig` + `pgxpool.NewWithConfig`. `sslmode=disable` rejected at startup.

---

### REM-007 — Registry-Metadata Caching (Part A — Redis)
- **Affects:** `registry-metadata`
- **Status:** DONE ✅ — server-side cache interceptor wired in metadata.
- **Tasks:**
  - [x] `CacheInterceptor` in `libs/middleware/grpc/cache.go` — server-side `UnaryServerInterceptor`
  - [x] Cache key format: `grpc:<full_method>:<tenant_id>:<primary_key>`; stored as proto.Marshal bytes
  - [x] TTLs: GetRepository→30s, GetManifest→5m, GetTag→30s, GetTenantQuotaUsage→10s
  - [x] Corrupted entries evicted automatically; cache failure is non-fatal (fallthrough to handler)
  - [x] Wired in `metadata/internal/server/buildGRPCOptions()`

---

### REM-008 — Registry-Metadata Read Replica (Part B)
- **Affects:** `registry-metadata`
- **Status:** DONE ✅ — replica pool wired and list queries routed.
- **Tasks:**
  - [x] `DBConfig.ReplicaPoolConfig()` added to `libs/config/loader/loader.go`
  - [x] `repository.NewWithReplica(pool, readPool)` + `reader()` helper — falls back to primary when readPool is nil
  - [x] `ListRepositories`, `ListTags`, `ListOrphanedBlobs` route to `r.reader()`
  - [x] Replica pool created in server.go when `DB_DSN_REPLICA` is set; warns and continues without when unset

---

### REM-009 — GC Advisory Locks
- **Affects:** `registry-gc`
- **Status:** DONE ✅ — advisory locking fully implemented.
- **Tasks:**
  - [x] `advisory.Locker` in `services/gc/internal/advisory/lock.go` — FNV-64a key from tenant UUID
  - [x] `pg_try_advisory_lock($1)` — non-blocking; tenant skipped when lock not acquired (`TenantsSkipped` counter in Result)
  - [x] Single pinned connection via `pgxpool.Acquire()`; explicit `pg_advisory_unlock` + `Release()` in deferred unlock func
  - [x] `GC_ADVISORY_LOCK_DB_DSN` env var; graceful no-op when unset (single-worker mode)

---

### REM-010 — Move Scanner Interface Location
- **Affects:** `libs/`, `services/scanner`
- **Status:** DONE ✅ — `libs/scanner/plugin/plugin.go` created during monorepo scaffold; `services/scanner` imports from there.

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

### Sprint 2 — Persistence Layer (COMPLETE)
> Goal: `services/metadata` and `services/storage` fully implemented so that `services/core` can be tested end-to-end with a real local stack.

| Task | Service | Status |
|---|---|---|
| DB migrations for metadata schema (repositories, manifests, tags, blobs, blob_links, scan_results, tenants, organizations) | `services/metadata` | DONE |
| Implement all `MetadataService` gRPC handlers | `services/metadata` | DONE |
| Apply REM-006 (pool exhaustion) in metadata | `services/metadata` | DONE |
| Redis client wired in metadata server | `services/metadata` | DONE |
| Implement MinIO/S3/GCS/Azure storage drivers | `services/storage` | DONE |
| Implement all `StorageService` gRPC handlers (streaming PutBlob/GetBlob, multipart) | `services/storage` | DONE |
| Complete Helm sub-charts and docker-compose for all services | `infra/` | DONE |

---

### Sprint 3 — Remaining Services (COMPLETE)
> Goal: All 12 services implemented and building cleanly.

| Task | Service | Status |
|---|---|---|
| Implement pull-through proxy cache | `services/proxy` | DONE |
| Implement vulnerability scan orchestration + external process plugin | `services/scanner` | DONE |
| Implement reliable webhook delivery worker | `services/webhook` | DONE |
| Implement immutable audit trail service | `services/audit` | DONE |
| Implement mark-sweep garbage collection | `services/gc` | DONE |
| Implement tenant lifecycle + domain verification worker | `services/tenant` | DONE |
| Implement image signing service (Cosign-compatible ECDSA P-256) | `services/signer` | DONE |
| Implement API gateway | `services/gateway` | DONE |
| Standardise all go.mod files to go 1.25.7 | all services | DONE |

---

## Current Sprint

**Sprint 5 — Frontend + Management API**
> Goal: Implement all 5 Stitch-verified screens, build `services/management` REST API to wire real data, reach 80% unit test coverage on auth + core.

### Frontend Screens

| Task | Status |
|---|---|
| Login page — design, implementation, QA pass | DONE ✅ |
| Repository Dashboard screen — UI | DONE ✅ |
| Image Details & Tags screen — UI | IN PROGRESS |
| Security Scan Results screen — UI | DONE ✅ |
| Build History screen — UI | DONE ✅ |
| Auth hook + token refresh logic | NOT STARTED |
| Unit test coverage: auth 55%→80% | NOT STARTED |
| Unit test coverage: core 18%→80% | NOT STARTED |

### RBAC — Role-Based Access Control (org / repo / tag)

Listed in CLAUDE.md §1 Core Capabilities but never tracked as a work item. Work spans auth, metadata, management API, and frontend.

| Task | Service | Status |
|---|---|---|
| Define RBAC schema: roles table, role_assignments (user, role, scope), scope enum (org/repo/tag) | `services/auth` + `services/metadata` | NOT STARTED |
| Add `GetUserPermissions` gRPC handler — returns user's effective roles scoped to a repo/org | `services/auth` | NOT STARTED |
| Enforce RBAC in `registry-core` push/pull handlers — check role before allowing write or pull on private repos | `services/core` | NOT STARTED |
| Enforce RBAC in `registry-management` — `POST /api/v1/repositories`, `DELETE` routes require admin/write role | `services/management` | NOT STARTED |
| RBAC admin API: `GET/POST/DELETE /api/v1/orgs/:org/members`, `GET/POST/DELETE /api/v1/repositories/:org/:repo/members` | `services/management` | NOT STARTED |
| Frontend: show/hide management actions (delete repo, delete tag, add member) based on user role from JWT claims | `frontend/` | NOT STARTED |
| Audit all RBAC changes (role grant / revoke) via `registry-audit` | `services/audit` | NOT STARTED |

> **Prerequisite:** RBAC schema decisions (which roles: owner/admin/write/read? flat or hierarchical?) need to be finalised before implementation starts. Add an Architecture Decision entry when agreed.

---

### Management API (`services/management`) — Blocks all dashboard data-wiring

| Task | Detail | Status |
|---|---|---|
| Scaffold service | `cmd/server/main.go`, `internal/` layout, `go.mod`, `Dockerfile`, add to `go.work` | DONE ✅ |
| JWT middleware | Validate Bearer token via `registry-auth` gRPC; extract `tenant_id` into request context | DONE ✅ |
| CORS + RequestID middleware | `CORS_ALLOWED_ORIGIN` env var; preflight 204; X-Request-ID injection | DONE ✅ |
| All route handlers | `GET /api/v1/stats`, `GET /api/v1/repositories`, single-repo, tags, scan, builds; all wrapped with `RequireAuth` | DONE ✅ |
| `go mod tidy` + compile check | Run `go mod tidy` in `services/management/`; verify `go build ./...` from workspace | DONE ✅ |
| Add to docker-compose | New container wired to `registry-auth` + `registry-metadata`; gateway routes `/api/v1/` to it | DONE ✅ |
| Add proto `GetRepositoryByName` RPC | Replace `findRepoByName` stream-scan workaround in `handler.go` | NOT STARTED |
| Wire frontend hooks | Replace mock data in dashboard with `useStats()` + `useRepositories()` TanStack Query hooks | NOT STARTED |

---

## Completed Sprints (continued)

### Sprint 4 — Hardening & Integration Testing (COMPLETE)
> Goal: Close all open remediation items, achieve OCI conformance test pass, bring up the full local stack in Docker Compose, and reach 80% unit test coverage per service.

### Highest Priority (blocking end-to-end testing)

| Task | Service | Blocks | Status |
|---|---|---|---|
| Docker Compose full-stack spin-up: verify all 16 containers start healthy | `infra/` | all E2E testing | DONE ✅ |
| Fix AUTH_REALM — WWW-Authenticate pointed to internal Compose hostname; docker push/pull from host failed | `services/core` | docker push/pull smoke test | DONE ✅ |
| Fix HasAction 403 → challengeAuth(401) — Docker only re-requests token on 401; 403 caused infinite retry loop | `services/core` | docker push smoke test | DONE ✅ |
| Fix Redis JWT cache losing Access claims — cachedClaims now serializes full access list as JSON | `services/core` | auth / push/pull | DONE ✅ |
| Fix MinIO bucket auto-creation — Ping() creates the bucket if absent; BlobExists was returning Internal | `services/storage` | blob operations | DONE ✅ |
| Fix missing dev tenant FK — migration 00002 seeds tenant `98dbe36b-ef28-4903-b25c-bff1b2921c9e` | `services/metadata` | CreateRepository | DONE ✅ |
| Fix CreateRepository empty OrgId — handler now parses `org/repo` name, upserts org, returns existing on conflict | `services/metadata` | push flow | DONE ✅ |
| Fix dev cert SANs — gen-dev-certs.sh now emits subjectAltName for Go 1.15+ TLS hostname verification | `cert-init` | mTLS / grpc conns | DONE ✅ |
| Wire SEC-008 fix — clientCreds() in core server uses mtls.ClientTLSConfig() when cert paths set | `services/core` | mTLS hardening | DONE ✅ |
| Wire SEC-008 fix — clientCreds() in proxy server uses mtls.ClientTLSConfig() when cert paths set | `services/proxy` | mTLS hardening | DONE ✅ |
| docker push/pull smoke test: `docker push localhost:8081/steveokay/alpine:3.20` passes end-to-end | all | E2E validation | DONE ✅ |
| Pull-through cache smoke test: `GET /v2/cache/dockerhub/library/alpine/manifests/3.20` returns 200, manifest cached in `proxy_manifests` DB table | `services/proxy` | proxy E2E | DONE ✅ |
| Fix proxy `challengeAuth` — pointed to non-existent `/v2/token` on proxy itself; add `AUTH_REALM` config field; `docker login localhost:8084` + `docker pull localhost:8084/cache/...` now work natively | `services/proxy` | proxy UX | DONE ✅ |
| Create ARCHITECTURE.md — full system architecture with ASCII diagrams, sequence flows, service descriptions | docs | — | DONE ✅ |
| OCI conformance suite against live stack (core + metadata + storage) | `services/core` | release | DONE ✅ |
| Apply REM-009: GC advisory locks (`pg_try_advisory_lock`, FNV-64a key) | `services/gc` | concurrent GC safety | DONE ✅ |
| Apply REM-005 (remaining): `FORCE ROW LEVEL SECURITY` + `registry_audit_app` role | `services/audit` | security hardening | DONE ✅ |

### Medium Priority (security hardening)

| Task | Service | REM | Status |
|---|---|---|---|
| Add `govulncheck` CI job to all 10 service workflows missing it | `infra/` | — | DONE ✅ |
| Add `gitleaks` CI workflow (`ci-gitleaks.yml`) on all pushes/PRs | `infra/` | — | DONE ✅ |
| Add `io.LimitedReader` on scanner plugin stdout (10MB cap) | `services/scanner` | REM-001 | DONE ✅ |
| Add explicit env allowlist for scanner plugin subprocess | `services/scanner` | REM-001 | DONE ✅ |
| Implement RabbitMQ `store.queued` event + consumer in proxy | `services/proxy` | REM-003 | DONE ✅ |
| Add 24h notification + exponential backoff to domain worker | `services/tenant` | REM-004 | DONE ✅ |

### Lower Priority (performance / observability)

| Task | Service | REM | Status |
|---|---|---|---|
| Add gRPC cache interceptor in `libs/middleware/grpc` | `libs/` | REM-007 | DONE ✅ |
| Create replica pgxpool in metadata and route list queries to it | `services/metadata` | REM-008 | DONE ✅ |
| Wire Prometheus metrics endpoint across all services | all | — | DONE ✅ |
| Integration tests (testcontainers) for auth, core, metadata, storage | `services/*` | — | DONE ✅ |
| Unit test coverage to 80% minimum per service | all | — | IN PROGRESS — libs 80%+, auth 55%, core 18%; signer/gc/webhook/scanner/proxy/tenant/metadata/storage/gateway: not assessed. |
| Troubleshooting guide — known errors + resolutions | `docs/` | — | NOT STARTED |

---

## Notes

- **Build order (reference):** `proto/` → `libs/` → `services/auth` → `services/metadata` → `services/storage` → `services/core` → (remaining services in parallel). All steps now DONE.
- **Go workspace:** `go.work` at repo root links all 14 modules (libs, proto/gen/go, 12 services). All go.mod files standardised to `go 1.25.7`. Last commit: `a9dc176`.
- **Module path:** `github.com/steveokay/oci-janus`
- **Full stack running (2026-06-10):** All 16 docker-compose containers (12 services + postgres, redis, rabbitmq, minio, jaeger, vault, cert-init) reach healthy/running state. Key fixes applied: `GOWORK=off` in all Dockerfiles, Viper env-seeding in all config loaders, `sslmode=prefer` for dev postgres, `embed.FS` for goose migrations, `PRIMARY KEY (id, occurred_at)` on partitioned audit table, static `/healthcheck` binary in distroless images, `chmod a+r` for cert volume permissions, OTLP endpoint without `http://` prefix.
- **OCI conformance 75/75 PASS (2026-06-12):** `make test-conformance` passes full OCI Distribution Spec v1.1 suite. Runs in CI on every PR to `main`.
- Vault dev mode in docker-compose is ready — `services/signer` can be tested locally now.
- `infra/terraform/` directory is present but empty — Terraform deferred per Decision #10.
- **Frontend (2026-06-12):** `ui/` renamed to `frontend/`. Login page shipped: TanStack Router, zod+react-hook-form, Hanken Grotesk font, frosted-glass card matching Stitch reference. Dev server: `cd frontend && npm run dev` → http://localhost:5173. Remaining screens: Dashboard, Image Details, Security Scan, Build History.
- Security audit completed 2026-06-10 — SEC-019 through SEC-028 added to `security.md`. Notable open items: HTTP server timeouts missing on 6 services (SEC-019/020), healthcheck binary lacks timeout (SEC-021), `sslmode=prefer` in dev compose (SEC-022), `context.Background()` in handlers (SEC-028).
- **CI security gaps closed (2026-06-10):** `govulncheck` added to all 12 service CI workflows; `ci-gitleaks.yml` added for secret scanning on all pushes/PRs. Commit `a919cd4`.
- **AUTH_REALM fix (2026-06-10):** `services/core` WWW-Authenticate realm was hardcoded to `https://registry/auth/token` (internal Compose hostname). Now reads from `AUTH_REALM` env var, defaulting to `http://localhost:8080/auth/token`. Docker push/pull from host now works. Commit `cb241bd`.
- **docker push/pull chain fixes (2026-06-10):** 6 root causes debugged and resolved to make `docker push localhost:8081/steveokay/alpine:3.20` work end-to-end: (1) HasAction 403→challengeAuth(401) in core; (2) Redis JWT cache losing Access claims (JSON serialization fix); (3) MinIO bucket auto-creation in storage Ping(); (4) dev tenant FK seeded in metadata 00002 migration; (5) CreateRepository auto-org-create from `org/repo` name; (6) dev cert SANs added in gen-dev-certs.sh for Go 1.15+ TLS. Also wired SEC-008 mTLS fix (clientCreds() helper in core server.go).
- **ARCHITECTURE.md created (2026-06-10):** Full system architecture document at repo root. Covers all 12 services, ASCII system diagram, docker login/push/pull sequence diagrams, async pipeline flow, custom domain resolution, multi-tenancy model, infrastructure components, and design decisions.
- **SEC-008 resolved (2026-06-10):** `registry-core` gRPC clients now use `libs/auth/mtls.ClientTLSConfig()` when cert paths are configured. Falls back to insecure with `slog.Warn` only in dev without certs. Moved to Resolved in `security.md`.
- **Proxy mTLS fix (2026-06-11):** `registry-proxy` gRPC clients also applied the SEC-008 `clientCreds()` pattern. Proxy was using `insecure.NewCredentials()` — TLS handshake failed silently → all auth calls returned error → all requests 401. Also triggered `go mod tidy` in `services/storage` (transitive redis dep from new `libs/middleware/grpc/cache.go`).
- **Pull-through cache E2E test (2026-06-11):** `GET /v2/cache/dockerhub/library/alpine/manifests/3.20` returns HTTP 200 with full OCI image index (multi-arch manifest list, 9226 bytes). Manifest stored in `proxy_manifests` DB table. Second request served from cache.
- **Proxy AUTH_REALM fix (2026-06-11):** `registry-proxy` `challengeAuth` previously pointed to `https://<host>/v2/token` — a non-existent endpoint on the proxy. Added `AUTH_REALM string` to proxy config (default `http://localhost:8080/auth/token`) and wired it into `HTTPHandler`. `WWW-Authenticate` now points to `registry-auth` exactly like `registry-core`. Docker follows the standard token-auth flow automatically. Tested: `docker login localhost:8084` and `docker pull localhost:8084/cache/dockerhub/library/alpine:3.20` both succeed. Commit `f2eb380`.
- **OCI conformance 75/75 PASS (2026-06-12):** `make test-conformance` in `services/core` passes the full OCI Distribution Spec v1.1 suite: 75 passed, 0 failed, 5 skipped (skips are optional spec features not advertised). Key fixes applied across this session: (1) gRPC cold-start 401 — first `ValidateToken` RPC also establishes TCP/TLS/HTTP2 connection; increased timeout 5s→15s + `Connect()` pre-warming at startup; (2) single-segment namespace routing — cross-repo blob mount targets like `conformance-<uuid>` have no `/`; all route thresholds lowered to `n≥3` and `ValidateName` removed from `handleInitiateUpload`; (3) OCI spec §4.4 compliance — `DeleteManifest` now deletes ONLY the tag when reference is a tag name, leaving the manifest accessible by digest; (4) Range header off-by-one in `handleGetUpload` — was returning `0-{offset}`, now correctly returns `0-{offset-1}`; (5) OCI referrer tracking — new `ReferrerStore` (Redis SADD/SMEMBERS keyed by `refs:<tenantID>:<repoName>:<subjectDigest>`), `PutManifest` parses `subject`/`artifactType`/`config.mediaType` (OCI §6.2 fallback) and registers referrers, `handlePutManifest` sets `OCI-Subject` header, `handleReferrers` returns real OCI image index with `?artifactType=` filter support.
