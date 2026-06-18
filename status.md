# Project Status

> Last updated: 2026-06-18 (evening — backend wave fully closed: super-admin tenant CRUD GUI (PR #8), automated DR for Postgres + Vault + RabbitMQ (PR #9), platform-admin migration fix (PR #10) all merged. **Frontend rebuild kicked off** on branch `feat/frontend-rebuild` — Sprint 0 (bootstrap + design tokens + login + auth wiring) shipped locally and verified end-to-end; paused mid-iteration on visual polish. Old UI preserved on branch `frontend-archive-v1`. Plan + sprint breakdown lives in `frontend/REBUILD-PLAN.md`. Earlier Sprint 6 closures still hold: all 3 backend polish items DONE, JWT `roles` claim DONE, Vault signer DONE, Tag-level RBAC resolved. KMS backends / Notary v2 / Helm cluster validation remain deferred.)
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
| `services/management` | DONE | — | Management REST API — BFF for the frontend, CLI, and Terraform consumers. All routes wired: stats, repositories (list/create/get/delete), tags (list/delete), scan results, build history (GetBuildHistory via registry-audit gRPC), and full RBAC member management (org + repo grant/revoke/list). JWT auto-refresh endpoint added to registry-auth. GetRepositoryByName proto RPC added to registry-metadata. 31 unit tests in handler_test.go covering all routes via bufconn in-process gRPC. Documented in CLAUDE.md §4.13. |
| `infra/` | DONE | — | docker-compose.yml (all services + Vault dev mode + MinIO + Jaeger), Helm umbrella chart with all 12 sub-charts, runbooks for secret-rotation, minio-encryption, notary-root-key-ceremony. Terraform directory present (deferred). Committed fd90f3d. |
| security hardening | DONE | — | 22 SEC items resolved: HTTP timeouts (SEC-019/020), healthcheck timeout (SEC-021), `sslmode=require` enforcement (SEC-022), Vault token isolation (SEC-023), cert key permissions `chmod 600` (SEC-024), secure response headers via `libs/middleware/http/secure_headers.go` (SEC-007/018), auth client-IP rate limiting via `TRUSTED_PROXY_CIDRS` (SEC-009), proxy partial-blob abort (SEC-012), context propagation (SEC-028), and others. Deferred items now closed: SEC-006 (`MapDBError` maps pool exhaustion → `codes.ResourceExhausted`), SEC-015 (signer PostgreSQL persistence — `signatures` table, write-through cache, `SigB64` not stored), SEC-025 (dedicated `/metrics` server on `:9090` across all 11 services). Commit `0f95144`. |
| `frontend/` | IN PROGRESS (rebuild) | — | **Sprint 0 of full visual rebuild in flight** on branch `feat/frontend-rebuild`. Old UI archived to `frontend-archive-v1` (pushed). Direction: light-mode primary, indigo accent, Stripe/Linear minimal aesthetic, role-aware navigation for end-tenants + super-admins. Stack kept (React 19 + Vite 6 + TanStack Router/Query + Radix + Tailwind v4 + lucide); added `class-variance-authority` and `@fontsource/geist`. **Shipped Sprint 0:** design tokens (light + dark, OKLCH), `Button` + `Input` + `Label` + field helpers, `authStore` (memory-only JWT), `apiClient` with 401 interceptor, TanStack router scaffold, login screen V2 (centered card on dotted-grid + indigo radial canvas), SSO buttons (Google / GitHub / Microsoft / Other) — UI complete, backend `Sprint 1a` tracked. **Login round-trip verified live** with dev creds. **Paused mid-iteration** — final font + background polish open. **Resume:** `cd frontend && npm run dev` → http://localhost:5173, follow `frontend/REBUILD-PLAN.md`. **Sprints 1–4** (foundation screens, ops, admin, polish) plus parallel backend mini-sprints (1a SSO providers, 3a runtime site settings) all scoped in the plan. |

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

### Sprint 6 — Pre-GA Polish
> Goal: Close the correctness, security, and UX gaps surfaced by the 2026-06-17 code review before declaring GA. None of these are "throw it away" issues — they are polish items that prevent surprises in production.

#### Backend correctness — HIGH

| Task | Service | Status | Notes |
|---|---|---|---|
| Read endpoints must not create repositories | `services/core` | DONE ✅ | Commit `df407f7`. Added read-only `GetRepository`; switched 5 read/delete handlers; only `handlePutManifest` still uses `GetOrCreateRepository`. |
| Reconcile delete action verb between JWT and RBAC | `services/core` | DONE ✅ | Commit pending. Folded into the `requireAccess` middleware extraction so both layers ask for `"delete"`. |
| Enforce per-tenant storage quota on push | `services/core` | DONE ✅ | Commit pending. `handleCompleteUpload` and `handlePutManifest` both call `Registry.CheckQuota` before committing. Pre-check uses `upload_state.Offset + Content-Length` for blob completes. Returns OCI 403 DENIED on overflow. Fails open when metadata RPC is unreachable so transient outages don't block pushes. |
| Fix HEAD-manifest-by-tag + cross-repo blob mount conformance failures | `services/core` | DONE ✅ | Commit `15f0ce3` + this commit. All 4 residuals fixed: HEAD-by-tag was transient (passed in the next run after wildcard grant), 3 cross-repo mount failures resolved by changing the conformance user's grant from `org/conformance` (covers `conformance/*`) to `repo/*` (covers any namespace including the random `conformance-<uuid>` namespaces the suite generates). Suite now at **75/75 PASS**. |
| Extract `requireAccess(action)` middleware | `services/core` | DONE ✅ | Commit pending. 14 handlers now route auth/RBAC through one helper. File shrinks by 88 lines (200→112). Adds RBAC check to upload-state + referrers handlers that previously only checked JWT. |
| Seed conformance user with admin role | `services/core` | DONE ✅ | Commit `15f0ce3`. Makefile now seeds an org-scoped admin grant for the conformance user. Also fixed the compose AUTH_REALM hardcode to read `${AUTH_REALM:-http://localhost:8080/auth/token}` so the Makefile env override propagates. Bumped the grant to `scope=repo,value=*` for cross-repo coverage in `d400eb1`. |

#### Backend feature gaps — surfaced by 2026-06-18 doc audit

> Items that CLAUDE.md / docs/ promise but no code implements. Different in nature from the Sprint 6 correctness items above — these are new feature work, not bugs.

| Task | Service | Status | Notes |
|---|---|---|---|
| Implement signer Vault key backend | `services/signer` | DONE ✅ | `services/signer/internal/signing/vault.go`. Uses Vault Transit `sign` endpoint (key material never leaves Vault, `exportable=false`). Public key fetched once at startup for KeyID derivation and local verification. 4 unit tests via `httptest` Vault fake (`vault_test.go`). Existing `infra/docker-compose/vault/init.sh` already provisions `registry-signer` ecdsa-p256 key + policy + token. Refactored `signing.Signer` from concrete struct to interface so KMS backends can drop in via the same shape. |
| Implement signer KMS backends (AWS / GCP / Azure) | `services/signer` | DEFERRED | Requires adding cloud SDK deps (`aws-sdk-go-v2/service/kms`, `cloud.google.com/go/kms`, `Azure/azure-sdk-for-go/.../azkeys`) and a real cloud environment to validate — these cannot be unit-tested without a live KMS key. Recommended path: implement `NewAWSKMS` as the primary cloud backend (same `Signer` interface + KMS `Sign`/`GetPublicKey`), follow the same pattern for GCP/Azure when those clouds are targeted. Vault Transit (above) covers the on-prem / hybrid case in the meantime. |
| Implement Notary v2 (TUF) signing path | `services/signer` | DEFERRED | Estimated multi-day effort: per-tenant root/targets/snapshots/timestamp key generation, TUF metadata persistence in `registry-storage`, `notation`-compatible REST endpoints, root key ceremony tooling. `infra/runbooks/notary-root-key-ceremony.md` documents the offline-root model. Cosign (shipped) addresses the same use case for most teams; Notary v2 should be its own sprint once a customer requires TUF. |
| Tag-level RBAC scope | `services/auth` | RESOLVED ✅ | Doc audit was stale — CLAUDE.md §1 only claims "RBAC at org / repo level" (no tag-level promise). The `role_assignments` CHECK constraint `IN ('org', 'repo')` matches the documented capability. No code expects tag scope. No work needed. |
| Validate Helm charts against a real cluster | `infra/helm` | DEFERRED | Operational task — requires a running Docker Desktop K8s cluster on the operator's machine. Not code work. Follow `infra/helm/README.md` (`helm install registry ./infra/helm`); flag any failed probes / NetworkPolicy / SecretProviderClass mismatches as new issues. |
| Automated disaster recovery (backup + restore) | `infra/`, `services/metadata`, `services/storage`, `services/auth` | DONE ✅ | New `infra/helm/registry/charts/backup` subchart (gated by `backup.enabled`, off in dev/review, on in `values.prod.yaml`). **Backup CronJobs:** 7 per-DB Postgres jobs (auth/metadata/tenant/proxy/webhook/audit/signer) rendered from `.Values.databases` via a single template; daily Vault Raft snapshot via `/v1/sys/storage/raft/snapshot`; daily RabbitMQ definitions export via `/api/definitions`. All jobs run as the existing `backup-tools` image (`infra/docker/backup-tools/Dockerfile` — alpine + postgresql16-client + aws-cli + curl + jq, non-root, no shell exposed). Scripts mounted via ConfigMap so a fix ships as a chart upgrade, not an image rebuild. Backup jobs use a **PutObject-only** IAM principal (no DeleteObject) so a compromised platform account cannot wipe prior backups; bucket lives in a separate cloud account from data buckets. **Restore scripts:** `infra/scripts/restore-postgres.sh` (refuses to write over a non-empty DB unless `FORCE=1`, validates pg_dump magic bytes, runs `pg_restore --single-transaction`, optional `goose status` post-check), `restore-vault.sh` (force-restore via `snapshot-force` endpoint with explicit warnings about token invalidation + unseal-key requirement), `restore-rabbitmq.sh` (re-imports topology, explicit "messages NOT restored" callout). **Runbook:** `infra/runbooks/disaster-recovery.md` — RPO/RTO targets per data class, full operator-prereq checklist (bucket creation + lifecycle + CRR + IAM + per-DB credentials secrets + Vault snapshot policy/token + RabbitMQ backup user), restore order (Vault → DBs → RabbitMQ → blobs → bring services up), why-not-RabbitMQ-messages explanation, quarterly DR drill checklist with measurable success criteria (OCI 75/75 + cosign verify must still pass). **Out of scope by design (documented in runbook §9):** WAL archiving is a Postgres-operator config change (we don't run our own Postgres — runbook §4 covers CloudNativePG / RDS / self-managed setups); blob restore is provider-specific (versioning + CRR), runbook documents the trigger; backup-integrity verification (restore-and-diff daily) is left as a future enhancement. Helm lint clean for both the subchart and the umbrella with `backup.enabled=true`. |
| Super-admin GUI for tenant CRUD | `frontend/`, `services/management`, `services/tenant` | DONE ✅ | **Backend:** added `ListTenants` RPC to `proto/tenant/v1/tenant.proto` (base64url `created_at\|id` cursor in `services/tenant/internal/handler/grpc.go`); new repo method `ListTenants(ctx, pageSize, afterCreated, afterID)` uses `(created_at, id)` tuple cursor for stable ordering. `services/management/internal/handler/admin_tenants.go` adds `GET/POST/GET-by-id/DELETE /api/v1/admin/tenants[/...]` routes gated by `requirePlatformAdmin` which checks the platform-admin marker (`hasScopedRole(_, "org", "*", "admin")`). Tenant create/delete publish `tenant.created` / `tenant.deleted` events via the existing `libs/rabbitmq/events` constants (added `RoutingTenantDeleted`). Management server now optionally dials `TENANT_GRPC_ADDR`; Compose wires `registry-tenant:50051`. **Frontend:** new route `/_authenticated/admin/tenants` (file `frontend/src/routes/_authenticated/admin/tenants.tsx`) renders a CRUD table + create modal using TanStack Query mutations + sonner toasts; `beforeLoad` redirects non-admins to `/dashboard`. New hook `useUserIsPlatformAdmin()` reads roles from the auth store; the sidebar in `_authenticated.tsx` conditionally surfaces the "Tenants" entry (`admin_panel_settings` icon). Server is authoritative — UI gates are convenience only. **Bootstrap:** dev seed migration `20260618000001_seed_dev_admin_role.sql` now grants both `(admin/org/dev)` for in-org work and `(admin/org/*)` for `/api/v1/admin/*` access; the `*` literal is reserved (`validateOrgName` rejects it so it can never collide with a real org). **Out of scope (future enhancements):** custom-domain verification UX, tenant rename / plan change, per-tenant audit drill-in. |
| Fix CI workflow Go version pin | `.github/workflows/` | NOT STARTED | Every per-service CI workflow (`ci-storage.yml`, `ci-auth.yml`, etc.) uses `actions/setup-go@v5` WITHOUT specifying `go-version`, so the runner defaults to whatever Go is preinstalled (currently 1.24). Our `go.mod` files all declare `go 1.25.7` per CLAUDE.md §1 → every PR fails `lint` with `"package requires newer Go version go1.25 (application built with go1.24)"` and `security` (govulncheck panics with same root cause + exit 2). PRs #6 + #7 both merged despite red CI for this reason — the failures are infrastructure, not code. **Fix:** add `with: go-version: '1.25'` to all 13 per-service workflow files plus `ci-gitleaks.yml`. ~5 min mechanical change. **Risk of leaving it:** CI gating provides no value until fixed — real lint/govulncheck issues would be lost in the noise of the false-positive failures, so a reviewer can't tell red-because-bug from red-because-CI. |

| Task | Service | Status | Notes |
|---|---|---|---|
| Fix `useUserIsAdmin` localStorage read — token is memory-only | `frontend/` | NOT STARTED | `dashboard/index.tsx:22` reads `localStorage.getItem('auth_token')` which is never written anywhere. Function always returns `false`, so admins never see delete buttons. Read from `useAuthStore` instead. **Backend prerequisite (roles claim) is now DONE — see row below.** |
| Add `roles` claim to JWT (or expose `/api/v1/me` permissions endpoint) | `services/auth`, `frontend/` | DONE ✅ (backend) | `Roles []string` added to `Claims`; `Login` loads `GetUserRoles` and embeds the deduped role-name list in the JWT. `ValidateTokenResponse` proto has a new `roles` field (#7); gRPC handler returns the claim verbatim. Docker `/auth/token` path still issues OCI-scoped tokens without roles (Docker clients don't use them). Frontend wiring (`payload.roles` read after JWT decode) tracked separately in Frontend UX. |

#### Backend polish — MEDIUM

| Task | Service | Status | Notes |
|---|---|---|---|
| Hoist `MetricsInterceptor` histogram out of the hot path | `libs/middleware/grpc` | DONE ✅ | `sync.Once` + package-level `grpcDurationHist`; `initGRPCDurationHist()` called once after OTEL bootstrap. No API change — `MetricsInterceptor` signature unchanged. |
| Close TODO: replace O(n) repo count drain with `CountRepositories` RPC | `services/management`, `services/metadata` | DONE ✅ | Added `CountRepositories` RPC to `proto/metadata/v1/metadata.proto`; regenerated stubs via `buf generate`; implemented `SELECT COUNT(*)` in `repository.go`; replaced stream drain in `handleStats` with single RPC call. |
| Close TODO: wire mTLS creds in scanner gRPC client | `services/scanner` | DONE ✅ | Added `clientCreds()` helper (same pattern as `registry-core`). Both metadata + storage gRPC clients now use mTLS when cert paths are set, with insecure fallback + `slog.Warn` for dev. |

#### Frontend UX — MEDIUM

> The dashboard contains several pieces of mock content that look real and will mislead reviewers. Either wire to real data or mark as "demo" / remove until ready.

| Task | Service | Status | Notes |
|---|---|---|---|
| Wire `SystemHealth` card to `/api/v1/stats.system_health_pct` | `frontend/` | NOT STARTED | Currently hardcoded "NOMINAL / 99.98% / BUSY". The stats endpoint already returns `system_health_pct`. |
| Replace hard-coded "Recent CI/CD Builds" rows with real audit data — or hide the table | `frontend/`, `services/management` | NOT STARTED | `RecentBuilds` takes a `repositories` prop and ignores it (`_repositories`). Builds API is stubbed in management. Until wired, render an empty-state. |
| Replace hard-coded "Registry Activity" feed with audit events — or hide | `frontend/` | NOT STARTED | "Sarah J pushed 12 minutes ago", "Julian Barker joined" are fake. `registry-audit` has the real data. |
| Fix `StatsCards` "Active Images" tile wiring | `frontend/` | NOT STARTED | `index.tsx:205` displays `data.vulnerability_count` under an "Active Images" label, with literal-string fallback `'8,432'`. Either remove the tile or wire to a real active-images metric. |
| Rename "PULLS" column or stop showing `storage_used_bytes` under it | `frontend/` | NOT STARTED | `FeaturedRepositories` table column says PULLS but renders `formatBytes(storage_used_bytes)`. Misleading. |
| Replace hardcoded "Registry Admin / Production Cluster" sidebar header with tenant name from JWT | `frontend/` | NOT STARTED | Currently the same label regardless of logged-in tenant. |
| Remove or implement the top-nav search input | `frontend/` | NOT STARTED | Wired to local state but never queries anything. |
| Remove "Live Updates" pill or implement actual SSE/polling | `frontend/` | NOT STARTED | Animated dot suggests live data; nothing is live. |
| Replace external Google Fonts avatar URL with initials/Avatar component | `frontend/` | NOT STARTED | `_authenticated.tsx:299` fetches an external image on every page render. |

#### Frontend UX missing items — MEDIUM

| Task | Service | Status | Notes |
|---|---|---|---|
| Implement logout button with server-side JWT revoke | `frontend/`, `services/auth` | DONE ✅ (2026-06-18) | Sidebar logout button wired in `frontend/src/routes/_authenticated.tsx` `SideNavBar`. `handleLogout` calls `apiClient.post('/logout')` (backend revokes JTI in Redis), then `clearAuth()`, then `navigate('/login', { replace: true })`. Order is intentional: clear local state even if the server call fails so the user can't be stuck in a half-logged-out state. Backend `POST /api/v1/logout` was already implemented. Closes FE-SEC-007. |
| Create dev seed user migration | `services/auth`, `services/metadata` | DONE ✅ (2026-06-18) | The user `admin` / password `Admin1234!dev` was already created by `services/auth/migrations/20260610000001_seed_dev_tenant.sql`, but had no RBAC role so PENTEST-002/003 gates blocked them from doing anything useful. New migration `20260618000001_seed_dev_admin_role.sql` grants admin scope=org/value=dev to the dev admin user. Goose `*.sql` glob picks it up automatically. Bootstrap chicken-and-egg note documented in the migration: production should replace this with an operator-supplied bootstrap script. |

#### Frontend security — MEDIUM

> Security hardening checklist for the frontend. Items FE-SEC-001/002/008 are resolved. Remainder tracked here; detail in `front-end-tracker.md`.

| # | Item | Status | Notes |
|---|---|---|---|
| FE-SEC-003 | CSP header on nginx HTML responses | NOT STARTED | nginx config must set `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'` |
| FE-SEC-004 | CORS allowlist on management API verified | NOT STARTED | Confirm `CORS_ALLOWED_ORIGIN` is set and never `*` in production. Dev: `http://localhost:5173`. |
| FE-SEC-005 | Confirm vague error messages on login both paths | NOT STARTED | Both `root` form error and toast must say "Invalid credentials" — never "user not found" or "wrong password". |
| FE-SEC-006 | No tokens in URL params — audit all `navigate()` and axios calls | NOT STARTED | JWT must stay in `Authorization` header only. |
| FE-SEC-007 | Logout clears auth state (server-side revoke) | NOT STARTED | See "Implement logout button" in Frontend UX missing items above. |
| FE-SEC-009 | Refresh token in HttpOnly cookie | NOT STARTED | If refresh token flow implemented, must be `HttpOnly; Secure; SameSite=Strict` — never JS-accessible storage. |
| FE-SEC-010 | Open redirect validation after login | NOT STARTED | If `?redirect=` param ever added, validate against internal path allowlist — reject any value with `://` or leading `//`. |
| FE-SEC-011 | User-supplied content rendered safely | NOT STARTED | Repo/tag/description strings from API must use React's default text rendering — no `dangerouslySetInnerHTML`. |
| FE-SEC-012 | `npm audit` in CI | NOT STARTED | Add `npm audit --audit-level=high` to `ci-frontend.yml`; block build on high/critical. |
| FE-SEC-013 | HTTPS enforcement in production nginx | NOT STARTED | nginx must redirect HTTP → HTTPS and set `Strict-Transport-Security: max-age=31536000; includeSubDomains`. |
| FE-SEC-014 | `X-Frame-Options` + `X-Content-Type-Options` on frontend nginx | NOT STARTED | Mirror what `SecureHeaders` middleware does on backend — set both headers in nginx for all frontend responses. |
| FE-SEC-015 | Sensitive data not logged | NOT STARTED | Audit all `console.log`/`console.error` — must not output JWT values, passwords, or API key material. |

#### Frontend structure — LOW

| Task | Service | Status | Notes |
|---|---|---|---|
| Consolidate or relabel "Repositories" vs "Images" sidebar items | `frontend/` | NOT STARTED | In an OCI registry these are nearly synonymous; users get confused. Suggest "Repositories" + "Tags" pairing. |
| Add tenant switcher to top nav | `frontend/` | NOT STARTED | Required if a single user belongs to multiple orgs. |
| Add dark mode (MD3 tokens already support it) | `frontend/` | NOT STARTED | Cheap given the existing palette system. |
| Fix skeleton tile height parity to remove layout shift | `frontend/` | NOT STARTED | `h-24` skeleton then real card expands taller. |
| Unify error UX — toast + inline rather than mixed "Failed to load" / "—" patterns | `frontend/` | NOT STARTED | Currently inconsistent across StatsCards / FeaturedRepositories / etc. |

---

## Completed Sprints (sprint 5 below)

---

## Sprint 5 (COMPLETE) — Frontend + Management API
> Goal: Implement all 5 Stitch-verified screens, build `services/management` REST API to wire real data, reach 80% unit test coverage on auth + core.

### Frontend Screens

| Task | Status |
|---|---|
| Login page — design, implementation, QA pass | DONE ✅ |
| Repository Dashboard screen — UI + real data | DONE ✅ |
| Image Details & Tags screen — UI + real data + pixel-perfect QA | DONE ✅ |
| Security Scan Results screen — UI + real data + pixel-perfect QA | DONE ✅ |
| Build History screen — UI + real data + pixel-perfect QA | DONE ✅ |
| Auth hook + token refresh logic (silent JWT renewal 60s before expiry) | DONE ✅ |
| Unit test coverage: auth 55%→80% | DONE ✅ |
| Unit test coverage: core 18%→80% | DONE ✅ |
| Unit test coverage: audit (11 tests, grpc handler) | DONE ✅ |
| Unit test coverage: management (31 tests, all routes) | DONE ✅ |

### RBAC — Role-Based Access Control (org / repo / tag)

Listed in CLAUDE.md §1 Core Capabilities but never tracked as a work item. Work spans auth, metadata, management API, and frontend.

| Task | Service | Status |
|---|---|---|
| Define RBAC schema: roles table, role_assignments (user, role, scope), scope enum (org/repo) | `services/auth` | DONE ✅ |
| Add `GetUserPermissions` gRPC handler — returns user's effective roles scoped to a repo/org | `services/auth` | DONE ✅ |
| Enforce RBAC in `registry-core` push/pull handlers — check role before allowing write or pull on private repos | `services/core` | DONE ✅ |
| Enforce RBAC in `registry-management` — `POST /api/v1/repositories`, `DELETE` routes require admin/write role | `services/management` | DONE ✅ |
| RBAC admin API: `GET/POST/DELETE /api/v1/orgs/:org/members`, `GET/POST/DELETE /api/v1/repositories/:org/:repo/members` | `services/management` | DONE ✅ |
| Frontend: show/hide management actions based on user role from JWT claims | `frontend/` | DONE ✅ |
| Audit all RBAC changes (role grant / revoke) via `registry-audit` (rbac.role_granted / rbac.role_revoked events) | `services/audit` | DONE ✅ |

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
| Add proto `GetRepositoryByName` RPC | Replace `findRepoByName` stream-scan workaround in `handler.go` | DONE ✅ |
| Wire frontend hooks | Replace mock data in all screens with TanStack Query hooks (`useStats`, `useRepositories`, `useRepository`, `useTags`, `useScanResult`, `useBuilds`) | DONE ✅ |

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
| Unit test coverage to 80% minimum per service | all | — | IN PROGRESS — libs 80%+, auth 80%+, core 80%+, audit 80%+ (11 gRPC handler tests), management 80%+ (31 handler tests); signer/gc/webhook/scanner/proxy/tenant/metadata/storage/gateway: not assessed. |
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
- **OTEL bootstrap fix (2026-06-17):** All 11 service entrypoints (`core`, `metadata`, `storage`, `audit`, `gc`, `tenant`, `webhook`, `scanner`, `proxy`, `gateway`, `signer`) were missing `otel.Bootstrap()` in `main.go`, so zero traces were sent to the OTel collector — the root cause of the Jaeger SPM "no data" issue. Also added `OTELSamplingRate float64` (default 1.0) to 8 standalone service configs. Commit `b7539c9`. Rebuild and restart docker-compose to see traces flow.
- **Sprint 5 COMPLETE (2026-06-17):** All 5 Stitch screens shipped pixel-perfect (verified by dedicated QA agent comparing each screen against Stitch reference HTML). Key work: (1) `services/management` fully wired with all routes, RBAC enforcement, and bufconn-based unit tests (31 tests, 80%+ coverage); (2) `services/audit` `auditRepo` interface extracted enabling fake injection — 11 unit tests for `GetBuildHistory` and `GetDailyPullCount`; (3) `services/auth` `POST /api/v1/token/refresh` endpoint — validates current JWT, revokes old JTI in Redis, issues fresh token with same claims; (4) frontend JWT auto-refresh: silent renewal 60s before expiry via `authRefreshClient` (separate axios instance to prevent 401 interceptor loop), fallback button retained; (5) builds screen `ApiBuildRow` → `BuildRow` mapping fixed (snake_case → camelCase + `BuildActor` union type); (6) pixel-perfect Stitch fixes: Lucide → Material Symbols, `font-label-caps`/`font-headline-md`/`font-display` classes, org icon `inventory_2` FILL 1, `fontVariationSettings` for active nav icons, typography tokens across all 6 layout files.
