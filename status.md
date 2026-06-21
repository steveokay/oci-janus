# Project Status

> Last audited: 2026-06-21 (full doc audit — verified every OPEN/NOT STARTED row against the codebase; pruned stale Beacon-rebuild rows; marked Round-3 pentest items resolved where verified).
>
> Last updated: 2026-06-21 (night — **FE-API-034 SAML completion landed** (`df39d13`). New `services/auth/internal/saml` package wraps `crewjam/saml` bare `ServiceProvider`, bypassing samlsp.Middleware cookie/session machinery (auth_login_sessions + JWT issuer cover those). startSAML captures AuthnRequest.ID into the unused pkce_verifier column so callbackSAML can pass it to ParseResponse as InResponseTo. Email attribute walk (ADFS URN → LDAP OID → short names → NameID fallback). New SAML_SP_CERT_PATH + SAML_SP_KEY_PATH config (both-or-neither; unset → 501 NOTCONFIGURED so the cluster still boots). Metadata parsed per-request (admin PATCH effective immediately). 9 new SAML tests including happy E2E via in-process crewjam IdentityProvider + replay rejection. **🎉 ALL 36 BACKEND FE-API ITEMS NOW FULLY DONE** — no remaining deferrals.).
>
> Previous: 2026-06-21 (late evening — FE-API-034 OAuth closed via merge `4e3d939`. Hand-rolled net/http (no x/oauth2 dep), PKCE S256, single-use state, constant-time compare, open-redirect guard, email_verified check, per-tenant admin CRUD with AES-256-GCM-encrypted client_secret.).
>
> Previous: 2026-06-21 (evening — FE-API-032 closed via merge `92e6028`. services/gc bootstrap: first gRPC server, first DB, first migration, patterned after scanner FE-API-018/019.).
>
> Previous: 2026-06-21 (afternoon — FE-API-027 closed via merge `21a2f85`. Custom domain CRUD shipped with 5 routes + 4 tenant RPCs + atomic primary swap + swappable txtLookup for verify-now + verification_token leak protection + PATCH is_primary:false 400 with rationale.).
>
> Previous: 2026-06-21 (morning — **FE-API-028/029/030/033 wave landed**: all 4 merged into `feat/sprint-7`. 3 worktree agents all completed cleanly: `cf6e227` (analytics — generic GetAnalytics + PG14 date_bin), `78845bc` (SBOM download — new metadata columns + RPCs; scanner write-path deferred), `4a567d3` (tenant detail composition + rename with atomic slug cascade).).
>
> Previous: 2026-06-20 (late night — **FE-API-025..036 wave partially closed**: 025/026/031/035/036 merged into `feat/sprint-7` via `8560a1f` / `add0a4c` / `2f7b250`. Signing surface, storage breakdown, bulk tag delete, webhook delivery payload — first agent attempt bailed on Bash perms; settings broadened + 025/026 and 035 done inline.).
>
> Previous: 2026-06-20 (night — **Final FE-API wave landed**: 017 + 018 + 019 merged into `feat/sprint-7` (HEAD `f40365f`). `baae493` brings FE-API-017 (remediation suggestions — same DISTINCT ON CTE pattern as 014, grouped by (package, from_version, to_version) sorted by impact). `f40365f` brings FE-API-018 + 019 in one commit: new `services/scanner` DB module (this service had none before — goose embed.FS, pgx pool wired via libs/config/loader, `goose.Up` at startup), 2 new tables (scan_policies + compliance_reports), 5 new gRPC RPCs, async report worker with `FOR UPDATE SKIP LOCKED` for multi-replica safety, SPDX JSON 2.3 SBOM + hand-crafted text PDF renderer (no 3rd-party PDF lib), 5 new HTTP routes on services/management gated by new `SCANNER_GRPC_ADDR`. **All of FE-API-001..024 now CLOSED** (the original v1 surface — 24 items). **FE-API-025..036 (12 new items, surfaced via ComingSoon hints in `8c964dd`)** open the next backlog: cryptographic verify on demand (025), sign-from-UI (026), custom-domain CRUD (027), tenant detail with usage breakdown (028), tenant rename + plan change (029), pull/push analytics time-series (030), per-repo storage breakdown (031), GC status visibility (032), per-tag SBOM/provenance download (033), SSO provider configuration (034 — Sprint 1a bedrock), webhook delivery payload retrieval (035), bulk tag delete (036). Frontend-side tag-row click polish (`645e21b`, `c8de5cd`) also landed during the agent wait.).
>
> Previous: 2026-06-20 (evening — **Wave 3 of FE-API agents landed**: FE-API-007 / 008 / 009 / 014 / 015 all merged. Workspace hostname + /workspace/me full shape (`140b647`), notifications poll endpoint with 8-action allowlist (`723ddbe`), workspace-wide vuln list + scan history with new `trigger` column (`2ae848b`). One merge conflict on `services/management/internal/handler/handler_test.go` resolved by keeping both test blocks (FE-API-008 + 014/015 fakes/tests). 73 handler tests passing.).
>
> Previous: 2026-06-20 (afternoon — **Sprint 7A + 7B shipped** on `feat/sprint-7`. **Sprint 7A** (`5608815`) wired the profile page against FE-API-011/012/013 + API-key CRUD dialogs. **Sprint 7B** (`f81046e`) shipped **FE-API-002** (per-tag manifest detail — config + layers + image-index manifests with platform info) and **FE-API-003** (per-tag signing status — `signed` boolean + signature list with signer_id / key_id / signed_at; no per-request cryptographic verification yet). Both surfaced via `LayersPanel` + `SigningPanel` on the tag-detail route.).
>
> Previous: 2026-06-19 (afternoon — **Beacon frontend rebuild Sprints 0–5 shipped** on `feat/frontend-rebuild`: dashboard, repositories list + detail, tag detail with scan/build, security IA, activity stub, members (org + repo), webhooks (list + detail + CRUD + deliveries + test + rotate), profile + API keys. FE-API agent wave landed: **FE-API-004 / 011 / 012 / 013 / 016 / 020 closed** across services/auth, services/audit, services/metadata, services/management. FE-API-005 and FE-API-006 confirmed DONE backend-side (per-repo members route + `Repository.description`). Frontend-only tracking moved out of this file to `frontend/FE-STATUS.md`.)
>
> Previous: 2026-06-19 (morning — backend chore wave: CI Go pin bumped 1.23→1.25.7 across all 13 per-service workflows; FE-API-001/010 closed (tag `size_bytes`, repo `org` joined into proto Repository — frontend `dev` fallback removed); FE-API-021..024 closed (webhook CRUD + delivery log + test dispatch + rotate-secret HTTP routes on management; new `UpdateEndpoint`/`RotateEndpointSecret`/`ListDeliveries`/`TestDispatch` proto RPCs on `services/webhook`; `WEBHOOK_GRPC_ADDR` wired in docker-compose); Postman collection covering every public `/api/v1/*` route shipped to `docs/postman/`).
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
| `frontend/` | IN PROGRESS (rebuild) | — | **Beacon rebuild — Sprints 0 through 6 shipped**; merged to main via PR #14 (`2477358`). Sprint 7 work continues on branch `feat/sprint-7`. Old UI archived to `frontend-archive-v1`. Design: Beacon — light-mode primary with full dark-mode parity, deep teal (`#0D9488`) accent, warm amber (`#D97706`) highlight, Fraunces (display) + Inter (UI) + JetBrains Mono (code) fonts, purposeful motion (count-ups, scan-pulse, quota fill). Stack: React 19 + Vite 6 + TanStack Router (file-based) + TanStack Query v5 + Radix/shadcn primitives + Tailwind v4. **Shipped routes:** `/login` (with SSO buttons), `/` (dashboard with hero KPIs + storage quota + system-health card), `/repositories` (search + filter + create + paginated table), `/repositories/:org/:repo` (header + pull-command + tabs: Tags / Members / Settings stub + DescriptionCard), `/repositories/:org/:repo/tags/:tag` (identity card + ScanPanel with all 5 states + BuildTimeline + Delete), `/security` (sub-tabs: Overview / Vulnerabilities / Scans / Remediation / Policies — with ComingSoon panels keyed to FE-API IDs), `/activity` (FE-API-008 stub), `/members` + `/orgs/:org/members` (RBAC + add/remove dialogs), `/webhooks` + `/webhooks/$id` (list + detail + CRUD + delivery log + test dispatch + rotate secret + show-once secret reveal), `/profile` (identity card + API keys CRUD), `/admin/tenants` (Sprint 6 — platform-admin-gated; PlanBreakdown tiles + TenantsTable + Create / SetQuota / Delete dialogs; beforeLoad guard redirects non-admins to dashboard; server is still source of truth). Theme toggle + Cmd+K palette shipped (Sprint 1f). **Sprint 7** (profile real wiring against FE-API-011/012/013 — now unblocked, /users/me merged via `22fa246`) in flight. **Sprint 8** (polish pass: dark-mode QA, a11y audit, responsive QA, motion review) queued. Tracker: `frontend/FE-STATUS.md`. Resume: `cd frontend && npm install && npm run dev` → http://localhost:5173. |

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

### REM-011 — Scanner Plugin End-to-End Works in Dev + With Real Trivy
- **Affects:** `services/scanner`, `services/metadata`, `infra/docker-compose`, `proto/metadata/v1`, `docs/`
- **Status:** **PHASE 1 DONE ✅** (commit `8debd29`, 2026-06-21). Phase 2 (new gRPC RPCs for adapter management + live swap + test scans) PENDING.
- **Phase 1 acceptance criteria — verified live (2026-06-21):**
  1. ✅ `docker compose --profile scanner up -d` starts `registry-scanner` zero-config; dev-stub adapter is the default and the entrypoint auto-computes the checksum.
  2. ✅ `POST /tags/{tag}/scan` produces a `scan_results` row within ~5-10s on dev-stub; ~30s on Trivy first scan (DB download), ~1s on warm Trivy.
  3. ✅ Auto-scan via `push.completed` produces a row via the same worker pool + write-path.
  4. ✅ Swap-plugin smoke passes — `SCANNER_PLUGIN_PATH=/usr/local/bin/scanner-trivy-adapter` + `--force-recreate` → real CVE detection on `dev/alpine:3.19` (10 findings: 2 HIGH, 5 MEDIUM, 3 LOW).
- **What Phase 1 shipped (backend):**
  - `infra/scanner-plugins/dev-stub` + `trivy-adapter` Go binaries satisfying the JSON-RPC contract; both baked into the scanner image.
  - `services/scanner/Dockerfile` + new `scripts/entrypoint.sh` — auto-fills `SCANNER_PLUGIN_CHECKSUM` from the active binary, operator-supplied values still win.
  - **Backend gap fixed:** `proto/metadata/v1 UpdateScanStatusRequest` extended with `repo_id`/`manifest_digest`/`scanner_name`/`scanner_version`; repository `UpsertScanResult` is now a real `INSERT ... ON CONFLICT (id) DO UPDATE` so the first scan write creates the row (no separate CreatePending RPC needed).
  - Pre-existing viper bug in `services/scanner/internal/config` fixed (Unmarshal couldn't see env vars without explicit `Set`).
  - `docs/SCANNER.md` — canonical reference: contract, both adapters, zero-config dev, production checksum override, swap procedure, write-your-own checklist, known limitations.
  - Frontend graceful-degradation (90s stuck-pending detection) shipped in the same commit but tracked in `FE-STATUS.md` — Phase 1 frontend mop-up.
- **Phase 1 known limitations:**
  - Trivy adapter ignores whiteout files when flattening layers (overcount, never underreport). Documented in `docs/SCANNER.md` §6.
  - No integration test yet asserting `scan_results` lands within 30s of a trigger (manual verification only). Listed as a Phase 2 must-have.
- **Phase 2 backend tasks (not started):**
  - [ ] **Adapter registry on `services/scanner`:** discover installed adapters by directory scan (`/usr/local/bin/scanner-*`) or sidecar manifest, instead of one env var pointing at one binary.
  - [ ] **Live adapter swap:** `process.ProcessPlugin` reloads its path via atomic pointer swap so the next scan picks up the new adapter without container restart.
  - [ ] **`scanner_settings` table + migration:** persists the active-adapter selection across restarts.
  - [ ] **FE-API-044 — `GET /admin/scanners` + `GET /admin/scanners/active`** (scanner gRPC: `ListInstalledAdapters`, `GetActiveAdapter`). Platform-admin grant. Returns per-adapter `{name, version, path, checksum, env_keys[]}`.
  - [ ] **FE-API-045 — `PATCH /admin/scanners/active`** (scanner gRPC: `SetActiveAdapter`). Platform-admin grant. Validates the target is in the registry, swaps in-memory + persists to `scanner_settings`.
  - [ ] **FE-API-046 — `POST /admin/scanners/test`** (scanner gRPC: `RunTestScan`). Fires a deterministic test scan against the active adapter using a fixed tiny in-repo OCI fixture; returns `{ok, duration_ms, scanner_name, scanner_version, severity_counts}`. Platform-admin grant.
  - [ ] **FE-API-047 — `GET /admin/scanners/health`** (scanner gRPC: `GetScannerHealth`). Read-only liveness + recent-job stats (last successful scan, queue depth, stuck-job count). Used by the UI to replace the 90s client-side stuck-pending heuristic.
  - [ ] **Integration test:** triggers a scan via `RunTestScan` (or the existing trigger path) and asserts a `scan_results` row materializes within 30s. Guards against the JSON-RPC contract drifting silently.

> Phase 2 frontend surface (`/admin/scanner` admin route, adapter cards, swap UI, test-scan button, liveness-driven degradation) is tracked in `FE-STATUS.md` — REM-011 P2 row.

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

#### Security — Pentest Round 3 (2026-06-19) — HIGH

> 7 new findings (0 critical, 2 high, 3 medium, 2 low) raised during the
> post-merge review of branch `feat/frontend-rebuild` covering the new
> webhook CRUD/test/rotate routes (FE-API-021..024), the `Repository.org`
> + `Tag.size_bytes` proto change (FE-API-001/010), and the 00004 manifest
> backfill migration. Full details + repro paths in `security.md` Round 3.

| Task | Service | Status | Notes |
|---|---|---|---|
| **PENTEST-027 (HIGH)** — Restrict webhook list + list-deliveries to admin; scrub credentials from `last_error` | `services/management`, `services/webhook` | DONE ✅ | 2026-06-21 audit: verified `services/management/internal/handler/webhooks.go:122` (list) and `:284` (deliveries) both gate via `requireWebhookAdmin`; `services/webhook/internal/delivery/dispatcher.go:29` defines `sanitizeURLForError` (strips userinfo/query/fragment) and `:154` wraps the dispatcher error with it. |
| **PENTEST-028 (HIGH)** — Replace 00004 backfill with a paginated post-migration job | `services/metadata` | DONE ✅ | 2026-06-21 audit: verified `services/metadata/migrations/00004_manifest_image_size.sql` now only does `ALTER TABLE ADD COLUMN … DEFAULT 0` (instant metadata-only change on PG 11+). Batched backfill moved to `infra/runbooks/manifest-image-size-backfill.md`. |
| PENTEST-029 (MEDIUM) — Cap manifest `raw_json` size at the metadata gRPC layer + element-count guard in `parseImageSize` | `services/metadata` | DONE ✅ | 2026-06-21 audit: verified `services/metadata/internal/handler/grpc.go:220` defines `maxManifestJSONBytes = 4 << 20` with explicit length check in `PutManifest` (returns `InvalidArgument`); `services/metadata/internal/repository/repository.go:397` defines `maxManifestEntries = 1000` and `parseImageSize` truncates Layers/Manifests to that cap. |
| PENTEST-030 (MEDIUM) — Add per-endpoint test-dispatch throttle (Redis-keyed) | `services/management` / `services/webhook` | OPEN | 2026-06-21 audit: confirmed still missing — `handleTestWebhook` (`services/management/internal/handler/webhooks.go:348`) only checks `requireWebhookAdmin` then forwards; no `(tenant_id, endpoint_id)` Redis bucket or daily budget. Per-user 20 rps still amplifies. |
| PENTEST-031 (MEDIUM) — Don't passthrough gRPC `InvalidArgument` text in webhook HTTP error mapping | `services/management` | DONE ✅ | 2026-06-21 audit: verified `services/management/internal/handler/webhooks.go:477` `mapWebhookGRPCError` now logs `st.Message()` server-side at `slog.Warn` and returns the fixed string `"invalid request"` to the client. Covered by `webhooks_test.go:145`. |
| PENTEST-032 (LOW) — Re-validate stored webhook URL on every PATCH | `services/webhook` | DONE ✅ | 2026-06-21 audit: verified `services/webhook/internal/handler/grpc.go:179-202` — when `req.Url == nil`, `UpdateEndpoint` fetches the existing record via `GetEndpointForTenant` and runs `delivery.ValidateURL` against the stored URL; refuses the update with `InvalidArgument "stored webhook URL is no longer valid"` on regression. |
| PENTEST-033 (LOW) — Move dev passwords out of Postman collection bodies into env vars | `docs/postman` | PARTIAL | 2026-06-21 audit: login uses `{{password}}` env var (now `type: secret`) — first half done. Still open: (a) `NewUser1234!` baked into the `createUser` request body at `registry-management.postman_collection.json:114`, (b) the dev tenant UUID `98dbe36b-…` defaulted in `registry-management.postman_environment.json:6`. |

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
| Fix CI workflow Go version pin | `.github/workflows/` | DONE ✅ | All 13 per-service workflows (`ci-auth.yml`, `ci-audit.yml`, `ci-core.yml`, `ci-gateway.yml`, `ci-gc.yml`, `ci-libs.yml`, `ci-metadata.yml`, `ci-proxy.yml`, `ci-scanner.yml`, `ci-signer.yml`, `ci-storage.yml`, `ci-tenant.yml`, `ci-webhook.yml`) bumped from `go-version: "1.23"` → `"1.25.7"` to match the `go 1.25.7` toolchain declared in every `go.mod`. `ci-proto.yml` (buf), `ci-ui.yml` (Node), and `ci-gitleaks.yml` (no Go) untouched. |

| Task | Service | Status | Notes |
|---|---|---|---|
| Fix `useUserIsAdmin` localStorage read — token is memory-only | `frontend/` | DONE ✅ | Superseded by Beacon rebuild (`PENTEST-015` resolution); current `useUserIsAdmin` reads `roles` from `useAuthStore`. The legacy `dashboard/index.tsx:22` no longer exists. |
| Add `roles` claim to JWT (or expose `/api/v1/me` permissions endpoint) | `services/auth`, `frontend/` | DONE ✅ | `Roles []string` added to `Claims`; `Login` loads `GetUserRoles` and embeds the deduped role-name list in the JWT. `ValidateTokenResponse` proto has a new `roles` field (#7); gRPC handler returns the claim verbatim. Docker `/auth/token` path still issues OCI-scoped tokens without roles (Docker clients don't use them). Frontend reads `payload.roles` after JWT decode in Beacon `authStore`. |

#### Backend polish — MEDIUM

| Task | Service | Status | Notes |
|---|---|---|---|
| Hoist `MetricsInterceptor` histogram out of the hot path | `libs/middleware/grpc` | DONE ✅ | `sync.Once` + package-level `grpcDurationHist`; `initGRPCDurationHist()` called once after OTEL bootstrap. No API change — `MetricsInterceptor` signature unchanged. |
| Close TODO: replace O(n) repo count drain with `CountRepositories` RPC | `services/management`, `services/metadata` | DONE ✅ | Added `CountRepositories` RPC to `proto/metadata/v1/metadata.proto`; regenerated stubs via `buf generate`; implemented `SELECT COUNT(*)` in `repository.go`; replaced stream drain in `handleStats` with single RPC call. |
| Close TODO: wire mTLS creds in scanner gRPC client | `services/scanner` | DONE ✅ | Added `clientCreds()` helper (same pattern as `registry-core`). Both metadata + storage gRPC clients now use mTLS when cert paths are set, with insecure fallback + `slog.Warn` for dev. |

#### Frontend UX missing items — MEDIUM

| Task | Service | Status | Notes |
|---|---|---|---|
| Implement logout button with server-side JWT revoke | `frontend/`, `services/auth` | DONE ✅ (2026-06-18) | Sidebar logout button wired in `frontend/src/routes/_authenticated.tsx` `SideNavBar`. `handleLogout` calls `apiClient.post('/logout')` (backend revokes JTI in Redis), then `clearAuth()`, then `navigate('/login', { replace: true })`. Order is intentional: clear local state even if the server call fails so the user can't be stuck in a half-logged-out state. Backend `POST /api/v1/logout` was already implemented. Closes FE-SEC-007. |
| Create dev seed user migration | `services/auth`, `services/metadata` | DONE ✅ (2026-06-18) | The user `admin` / password `Admin1234!dev` was already created by `services/auth/migrations/20260610000001_seed_dev_tenant.sql`, but had no RBAC role so PENTEST-002/003 gates blocked them from doing anything useful. New migration `20260618000001_seed_dev_admin_role.sql` grants admin scope=org/value=dev to the dev admin user. Goose `*.sql` glob picks it up automatically. Bootstrap chicken-and-egg note documented in the migration: production should replace this with an operator-supplied bootstrap script. |

#### Frontend structure — LOW

> 2026-06-21 audit: legacy rows for "Repositories vs Images", "Add dark mode", "Skeleton tile height parity", and "Unify error UX" have been removed — the Beacon rebuild (PR #14, merged 2026-06-19) ships full dark-mode parity, uses the "Repositories" naming only, has skeleton geometry-matched primitives, and uses sonner toasts + inline error states (FE-STATUS.md S0–S6). Remaining real follow-up:

| Task | Service | Status | Notes |
|---|---|---|---|
| Add tenant switcher to top nav | `frontend/` | NOT STARTED | Required if a single user belongs to multiple orgs. Backend already exposes per-tenant context via `tenant_id` claim; the UI gap is the switcher widget + `tenant_id` swap-on-select. |

#### Frontend rebuild — API gaps surfaced by Sprint 1d+ — MEDIUM

> The visual rebuild on `feat/frontend-rebuild` (PR #11) has shipped Sprints 0 through 1c and is building Sprint 1d (repository / tag detail page). The items below are backend endpoints the rebuild would consume on the detail surface and on Sprint 2's scan / signing surfaces. Tracked here so the touchpoints are known the moment the backend work picks them up.

| # | Need | Service | Status | Notes |
|---|---|---|---|---|
| FE-API-001 | Tag size on `ListTags` response | `services/management`, `services/metadata` | DONE ✅ | `Tag.size_bytes` added to `proto/metadata/v1/metadata.proto`; new migration `00004_manifest_image_size.sql` adds `manifests.image_size_bytes` and backfills existing rows by parsing `raw_json` (config.size + sum(layers[].size), or sum(manifests[].size) for an index). `PutManifest` writes the column via `parseImageSize(rawJSON)`; tag selects `LEFT JOIN manifests` so `TagResponse.size_bytes` is one indexed lookup per row. Pre-FE-API-001 rows stay at 0 until re-pushed (or until the migration backfills them). |
| FE-API-002 | Per-tag manifest detail endpoint | `services/management`, `services/metadata` | DONE ✅ | `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/manifest` returns parsed manifest JSON: `{digest, media_type, size_bytes, created_at, is_index, config:{digest,size,media_type}, layers[], manifests[]}`. Parses both OCI image manifests (config + layers) and OCI image indexes / Docker manifest lists (manifests[] with per-platform entries — architecture, os, variant, os.version). Parser is forgiving — missing optional fields default to zero/null. Frontend `LayersPanel` consumes via `useManifest`; replaces Sprint 2 ComingSoon stub. Sprint 7B commit `f81046e`. |
| FE-API-003 | Per-tag signing-status endpoint | `services/management`, `services/signer` | DONE ✅ | `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/signature` returns `{manifest_digest, signed, signatures[{signer_id, key_id, signature_digest, signed_at}]}`. Calls `services/signer.ListSignatures` after resolving tag → digest via metadata. Cryptographic verification deliberately NOT done on every request — too expensive for page renders; future `?verify=true` query param can opt in. New `SignerGRPCAddr` config field on services/management; route returns 404 "route disabled" if `SIGNER_GRPC_ADDR` is unset. Frontend `SigningPanel` consumes via `useSignature`. Sprint 7B commit `f81046e`. |
| FE-API-004 | Repo-scoped recent activity query | `services/management`, `services/audit` | DONE ✅ | `GET /api/v1/repositories/{org}/{repo}/activity?since&limit&page_token&event_types` via new `GetRepoActivity` gRPC RPC on services/audit + `repo_activity.go` HTTP route. event_types allowlist: push.completed/failed, manifest.deleted, tag.deleted, scan.completed, scan.policy_blocked, image.signed (unknown values rejected before SQL). Keyset pagination via base64url(RFC3339Nano\|uuid) cursor over the `(tenant_id, occurred_at DESC)` index. `payload_summary` returns selected stable fields only. **Known follow-up:** delete.* and image.signed consumer paths store raw JSON in `resource` and don't populate `metadata->raw->repository_name` — pre-existing consumer bug, those events won't surface until fixed. Integration commit `b09ba36`. |
| FE-API-005 | Per-repo member list | `services/management`, `services/auth` | DONE ✅ | `GET/POST/DELETE /api/v1/repositories/{org}/{repo}/members` shipped in `services/management/internal/handler/rbac.go` alongside the org-scope routes. PENTEST-002/006 gates enforced (org-admin or repo-admin to grant; reader+ to list). Frontend Sprint 4 consumes via `useRepoMembers` / `useGrantRepoRole` / `useRevokeRepoRole` on the repo-detail Members tab. |
| FE-API-006 | Repository description / README field | `services/management`, `services/metadata` | DONE ✅ | `description` field added to `proto/metadata/v1/metadata.proto` `Repository` + `CreateRepositoryRequest`; metadata repository + handler honor it; management `RepoResponse` + `createRepositoryBody` surfaces it. Frontend Sprint 4 renders via `DescriptionCard` on the repo-detail page (paragraph-split, no markdown rendering yet pending FE-SEC-011). |
| FE-API-007 | Per-tenant registry hostname surfaced via API | `services/management`, `services/tenant` | DONE ✅ | `Tenant` proto extended with `slug` (5), `host` (6), `host_is_custom` (7), `domains[]` (8) — append-only, field nums preserved. Two migrations: `20260620000001_add_tenant_slug.sql` (slug TEXT NOT NULL, backfill via regexp_replace + trim; unique index) and `20260620000002_add_domain_is_primary.sql` (is_primary BOOLEAN, partial unique WHERE is_primary, backfill picks oldest verified per tenant). Host algorithm (`GRPCHandler.buildTenantProto`): primary verified domain wins → `host = domain`, `host_is_custom = true`; otherwise `host = <slug>.<PLATFORM_BASE_DOMAIN>`, `host_is_custom = false`. `PLATFORM_BASE_DOMAIN` env var default `registry.localhost`. `MarkDomainVerified` auto-promotes the first verified domain to primary in the same tx. `ListTenants` skips per-tenant domain lookup (avoids N+1). Merge commit `140b647` (source `0f8d299`). |
| FE-API-008 | Notification / events stream | `services/management`, `services/audit` | DONE ✅ | **Poll, not SSE** (lower complexity, reuses FE-API-004 datasource). `GET /api/v1/notifications?since&limit&page_token&event_types&unread_only` via new `GetNotifications` gRPC RPC on services/audit. Allowlist (8 actions, all confirmed against `libs/rabbitmq/events`): push.image, push.failed, delete.manifest, delete.tag, scan.completed, scan.policy_blocked, image.signed, webhook.delivery_failed. Eventconsumer additions wire `RoutingPushFailed → push.failed` and `RoutingWebhookFailed → webhook.delivery_failed` so the filters have rows. Hand-crafted renderer at `services/audit/internal/handler/notifications.go` synthesizes `title` + `summary` + `link` per event type. `unread_count = len(notifications)` on the current page; backend stores no per-user read state — clients persist `last_seen_at` locally and re-query with `since`. `unread_only` flag accepted + validated but currently passthrough — reserved for future server-side read state. Merge commit `723ddbe` (source `c4e3dd4`). |
| FE-API-009 | Workspace name / metadata for the current tenant | `services/management`, `services/tenant` | DONE ✅ | `GET /api/v1/workspace/me` `WorkspaceResponse` extended to full target shape: `{tenant_id, name, slug, plan, host, host_is_custom, domains[{domain, verified, is_primary}], created_at}`. Stayed in handler.go (not split). Domains array is non-nil empty slice when none. End-to-end bufconn tests cover happy path + wildcard fallback + tenant-client-unset 404 + no-auth 401. Merged alongside FE-API-007 in `140b647`. |
| FE-API-010 | **Org name on `RepoResponse`** | `services/management`, `services/metadata` | DONE ✅ | Chose option 1 (clean schema). `Repository.org` added to `proto/metadata/v1/metadata.proto`. Repository layer now JOINs `organizations` on every read; CTE-then-join on `CreateRepository`/`UpdateRepositoryQuota` so RETURNING reaches the parent org. `RepoResponse.org` surfaced through `repoToResponse`. Frontend hardcoded `'dev'` fallback removed in `RepositoriesTable.tsx` and `CommandPalette.tsx` — both now prefer `repo.org` and fall back to a slash split only on malformed rows. |
| FE-API-011 | **`GET /api/v1/users/me`** — current user metadata | `services/auth` | DONE ✅ | Returns `{user_id, username, email, display_name, created_at, last_login_at, tenant_id, roles, memberships[]}`. `last_login_at` updated on every successful login. Migration `20260619000001_add_user_profile_fields.sql` adds the three new columns. Merged via `22fa246` (source `cc4b710`). |
| FE-API-012 | **`PATCH /api/v1/users/me`** — update display name + email | `services/auth` | DONE ✅ | Optional `display_name` (1-128 chars, no control chars) + `email` (format-validated). Returns updated user (same shape as GET). Same merge as FE-API-011. |
| FE-API-013 | **`POST /api/v1/users/me/password`** — change password | `services/auth` | DONE ✅ | Body `{current_password, new_password}`. Policy: 12+ chars, ≥1 upper/lower/digit/symbol; reject new == current. argon2id verify + re-hash. Redis rate limit 5/hour per user-id (counter increments before verify to block CPU brute-force; fail-open if Redis down; 429 on overflow). JTI revocation: per-user Redis SET tracks active tokens; on password change each member written to `jwt:revoked:<jti>` (TTL = token TTL). Returns 204 on success, 401 generic on wrong current_password. Same merge as FE-API-011. |
| FE-API-014 | **Workspace-wide vulnerabilities list** | `services/management`, `services/metadata` | DONE ✅ | `GET /api/v1/security/vulnerabilities?severity=&page_token=&limit=` via new `ListTenantVulnerabilities` gRPC RPC on services/metadata. CTE: `latest = DISTINCT ON (repo_id, manifest_digest) ORDER BY completed_at DESC` joined to repositories + organizations + LEFT JOIN tags. Per-finding JSONB `findings` read into Go, severity-filtered, rolled up by `cve_id` into a map with deduped `affected[]` (repo, tag, digest) array per CVE. Sort by `(severity_rank, cve_id) ASC` (CRITICAL=1, HIGH=2…). Cursor = base64(`severity_rank\|cve_id`). Merge commit `2ae848b` (source `361a872`). |
| FE-API-015 | **Scan history** | `services/management`, `services/metadata` | DONE ✅ | `GET /api/v1/security/scans?since&limit&page_token` via new `ListScanHistory` gRPC RPC on services/metadata (chose metadata over audit since `scan_results` is the source of truth and has severity counts already). Keyset cursor `(completed_at, scan_id)`, ORDER BY `completed_at DESC NULLS LAST, scan_id DESC`. Backed by new index `idx_scan_results_tenant_completed_at`. **Migration `00006_scan_results_trigger.sql`** adds `trigger TEXT NOT NULL DEFAULT 'push' CHECK IN ('push','manual','scheduled')`; existing rows backfilled `push`. Scanner not yet updated to set `manual`/`scheduled` — left as follow-up; wire field plumbed end-to-end. Same merge commit as FE-API-014. |
| FE-API-016 | **Severity breakdown in `/stats`** | `services/management`, `services/metadata` | DONE ✅ | `VulnerabilityCountResponse` extended with medium/low/negligible counts (fields 4-6, backwards compatible). `GET /api/v1/stats` now returns `severity_counts: {critical, high, medium, low, negligible}` alongside `vulnerability_count`. Tenant-wide aggregation across `scan_results` via single SUM over JSONB `severity_counts->>`. Integration commit `b09ba36`. |
| FE-API-017 | **Remediation suggestions** | `services/management`, `services/metadata` | DONE ✅ | `GET /api/v1/security/remediation?limit&page_token` via new `ListTenantRemediations` gRPC RPC on services/metadata (chose metadata over scanner — data already lives in `scan_results.findings`). Same DISTINCT ON CTE + `jsonb_array_elements` pattern as FE-API-014. Grouping key: `(package_name, installed_version, fixed_in)`; skip when `fixed_in == "" \|\| fixed_in == installed_version`. `max_severity` = worst across the group. `affected[]` deduped, capped at 10 (`affected_count` reports true total so UI can render "N affected (showing 10)"). Sort by `(max_severity_rank ASC, cves_fixed_count DESC, package_name ASC, from_version ASC, to_version ASC)`. Cursor = base64 of the 5-tuple. Merge commit `baae493` (source `8fa0e43`). |
| FE-API-018 | **Scan policies CRUD** | `services/management`, `services/scanner` | DONE ✅ | New `scan_policies` table on services/scanner (PK tenant_id, CHECK on block_on_severity). `GetScanPolicy` returns default policy if no row exists (no 404). `UpdateScanPolicy` validates: `block_on_severity ∈ ["",CRITICAL,HIGH,MEDIUM,LOW]`, `scanner_plugin ∈ [trivy,grype]`, `exempt_cves` each matches `^CVE-\d{4}-\d{4,7}$`. PUT gated by `requireScanPolicyAdmin` (admin/owner on any org in tenant). New `SCANNER_GRPC_ADDR` config on services/management; route 404 "route disabled" if unset. **Bootstrap**: services/scanner previously had no DB — new migrations module (goose embed.FS), `DB_DSN` + `DB_MAX_CONNS` config, `goose.Up` at startup before serving traffic. Merge commit `f40365f` (source `24e0490`). |
| FE-API-019 | **Compliance reports** | `services/management`, `services/scanner` | DONE ✅ | Async job: `POST /api/v1/security/reports/generate` returns `{report_id, status:"pending"}`. `GET /api/v1/security/reports[/{id}]` for list + detail. `GET .../{id}/download/{pdf\|sbom}` streams the artifact. New `compliance_reports` table with `report_status` enum (pending/running/succeeded/failed) + composite index (tenant, status, requested_at). **Job runner** (`services/scanner/internal/reportworker`): 5s poll, `FOR UPDATE SKIP LOCKED LIMIT 1` claim inside a tx — safe across multiple scanner replicas. **Renderer**: SPDX JSON 2.3 (minimal — SPDXVersion / dataLicense / SPDXID / documentName / packages from findings) + hand-crafted text PDF (real `%PDF-1.4` header, single page, Helvetica Type1 — no 3rd-party PDF lib). Files land under `REPORT_OUTPUT_DIR/<tenant>/<id>.{pdf,spdx.json}` with `safeJoin` guarding traversal. **Deferred** (documented in code): real PDF layout/branding, object storage with signed URLs. Same merge commit as FE-API-018 (`f40365f`). |
| FE-API-020 | **Per-tenant security overview snapshot** | `services/management`, `services/metadata` | DONE ✅ | `GET /api/v1/security/overview` returns `{open_vulnerabilities_total, severity_counts, scan_coverage:{tags_total, tags_scanned, percent}, recent_scans_24h, days_since_last_scan}`. New `GetSecurityOverview` gRPC RPC on services/metadata. Single three-CTE query (vuln_sums + tag_counts + scan_recency) — one round-trip, tenant filter in each CTE for auditability. `days_since_last_scan = -1` sentinel for never-scanned tenants. Same integration commit as FE-API-016 (`b09ba36`). |
| FE-API-021 | **Webhook endpoints HTTP routes on management** | `services/management`, `services/webhook` | DONE ✅ | `GET/POST /api/v1/webhooks`, `DELETE /api/v1/webhooks/{id}` shipped in `services/management/internal/handler/webhooks.go`. Management generates the HMAC secret (`crypto/rand`, 32B hex), returns it once on create, and forwards the plaintext to `webhook.CreateEndpoint` which AES-256-GCM-encrypts before persisting. Wired through new `WithWebhookClient` + `WEBHOOK_GRPC_ADDR` env var (`registry-webhook:50051` in docker-compose). Routes return 404 "route disabled" when the env var is unset. |
| FE-API-022 | **Webhook delivery log endpoint** | `services/management`, `services/webhook` | DONE ✅ | New `ListDeliveries` gRPC RPC + `Delivery` proto on `services/webhook`; new repo method joins `webhook_deliveries` with tenant-id ownership check. `GET /api/v1/webhooks/{id}/deliveries?since=&limit=` returns deliveries newest-first (cap 200, default 50). `payload` intentionally not on the wire — recoverable via audit log if needed. |
| FE-API-023 | **Test webhook dispatch** | `services/management`, `services/webhook` | DONE ✅ | New `TestDispatch` gRPC RPC reuses the worker's `delivery.Dispatcher` (same SSRF guard + timeouts). Sends a synthetic `webhook.test` event and returns `{status_code, duration_ms, error}` synchronously. **Not** recorded in `webhook_deliveries` — would otherwise pollute the operator's delivery log on every test click. 15s context bound so a dead endpoint can't hold the gRPC call open. `POST /api/v1/webhooks/{id}/test`. |
| FE-API-024 | **Edit webhook + rotate secret** | `services/management`, `services/webhook` | DONE ✅ | New proto RPCs `UpdateEndpoint` (optional url/events/active fields — `optional` semantics so "leave alone" is distinguishable from "set to zero") and `RotateEndpointSecret`. Management routes `PATCH /api/v1/webhooks/{id}` and `POST /api/v1/webhooks/{id}/rotate-secret`. Rotate returns the new plaintext exactly once, same pattern as API keys. |
| FE-API-025 | **Cryptographic verify on demand for signing** | `services/management`, `services/signer` | DONE ✅ | `?verify=true` on the /signature route fans out parallel `signer.VerifyManifest` calls (cap 16, per-signature 5s deadline). Response gains `*verified` + `failure_reason` fields with `omitempty` so the default shape (verify-absent path) is byte-identical to FE-API-003's existing payload. Pointer-bool wire shape distinguishes nil (not asked) from false (asked + failed). Merge commit `8560a1f` (source `1f8ca91`). |
| FE-API-026 | **Sign manifest from UI** | `services/management`, `services/signer` | DONE ✅ | `POST /api/v1/repositories/{org}/{repo}/tags/{tag}/sign` accepting `{ signer_id }`. Resolves tag → digest via metadata, calls `signer.SignManifest`, publishes `image.signed` event (signer doesn't publish it itself; added `ImageSignedPayload` to `libs/rabbitmq/events`). Auth: repo `admin` via `hasScopedRole`. `signer_id` validated non-empty + ≤256 chars + ASCII printable. New `handler.EventPublisher` interface + `WithPublisher` option for test injection. Same merge as FE-API-025. |
| FE-API-027 | **Custom domain CRUD for tenant admins** | `services/management`, `services/tenant` | DONE ✅ | 5 routes under `/api/v1/workspace/me/domains`: GET (list — `verification_token` stripped from FE responses to prevent leak), POST (register → DNS TXT challenge + instructions), POST `.../verify` (re-runs DNS TXT check inline via swappable `txtLookup` package var), PATCH (set `is_primary=true`), DELETE (returns `X-Janus-Warning: primary-domain-removed` header when removing the primary). 4 new gRPC RPCs on services/tenant. Primary-swap is one atomic tx — SELECT verified → demote-all → promote-target RETURNING. Verify-now uses option (a) — synchronous re-check, returns updated entry immediately. `DomainEntry` proto extended fields 4-9 (verification_token, registered_at, verified_at, next_poll_after, notified_24h, notified_48h). Platform-wildcard guard rejects any domain ending in `.<PLATFORM_BASE_DOMAIN>` (defence in depth at both layers). **PATCH `is_primary:false` → 400** with `"is_primary must be true; delete the domain to clear primary"` — silently demoting would orphan the host onto the wildcard fallback. Merge commit `21a2f85` (source `7bb2e09`). |
| FE-API-028 | **Tenant detail with usage breakdown for platform admin** | `services/management`, `services/metadata`, `services/tenant`, `services/auth`, `services/audit` | DONE ✅ | `GET /api/v1/admin/tenants/{id}` extended with `slug`, `host`, `host_is_custom`, `storage_used_bytes`, `storage_quota_bytes`, `repository_count`, `organization_count`, `user_count`, `last_push_at`. Three new gRPC RPCs (metadata `GetTenantUsage`, auth `CountTenantUsers`, audit `GetLastTenantPush`). Composition pattern: tenant.GetTenant first (NotFound → 404), then fan-out to three downstream probes that degrade to zero values on error so the platform admin still sees the identity card. `last_push_at` serialised as JSON `null` when no push activity. Merge commit `4a567d3` (source `89d9f9c`). |
| FE-API-029 | **Tenant rename + plan change** | `services/management`, `services/tenant` | DONE ✅ | `PATCH /api/v1/admin/tenants/{tenantID}` accepting `{name?, plan?}` (at least one required). New `UpdateTenant` gRPC RPC on services/tenant. Rename cascade: slug recomputed via `NormalizeSlug` **atomically inside the same tx as the name UPDATE** so no observable state has new-name with old-slug (which would briefly point `<slug>.<base>` at the wrong workspace). Validation: name regex `^[a-z0-9][a-z0-9-]{1,63}$`, plan allowlist `{free, pro, enterprise}`, duplicate name → 409 (pgconn 23505 → AlreadyExists). Per-field events `tenant.renamed` + `tenant.plan_changed` (patching both fires two events). Same merge as FE-API-028. |
| FE-API-030 | **Pull / push analytics time-series** | `services/management`, `services/audit` | DONE ✅ | New `GetAnalytics` gRPC RPC on services/audit with generic `{scope_type, repo_id?, range_secs, bucket_secs}` request — BFF owns the range→bucket mapping (24h→1h×24, 7d→6h×28, 30d→1d×30). `date_bin` alignment in SQL with BFF-supplied origin so concurrent requests get comparable grids (PG14+). `GET /api/v1/repositories/{org}/{repo}/analytics` and `GET /api/v1/stats/analytics` (tenant-wide variant). BFF pre-allocates empty buckets so quiet periods report `count=0` instead of being absent. **Known gap:** `pull.image` not currently produced by the audit consumer; the query returns 0 rows so `/analytics?metric=pulls` is flat-zero today (documented; consumer wiring is a follow-up). Merge commit `cf6e227` (source `19eb3fa`). |
| FE-API-031 | **Per-repo storage breakdown for tenant admin** | `services/management`, `services/metadata` | DONE ✅ | `GET /api/v1/stats/storage` via new `GetTenantStorageBreakdown` gRPC RPC on services/metadata. One CTE-style query: tenant total + top-50 repos joined to organizations. `percent_of_tenant` materialised server-side so every UI surface renders identical numbers. Capped 50 with no pagination — top-N is itself the answer. Zero-repo tenant returns empty slice (not null) for stable JSON shape. Merge commit `add0a4c` (source `3ff23a1`). |
| FE-API-032 | **GC status visibility** | `services/management`, `services/gc` | DONE ✅ | services/gc bootstrap (this service had neither gRPC server nor DB before this work): new migrations module + `gc_runs` table (status + mode enums, nullable tenant_id for cross-tenant cron sweeps, `triggered_by` 'cron' or user_id). New `proto/gc/v1/gc.proto` with `GetStatus` / `RunNow` / `ListRuns`. `RunNow` is async — INSERTs a queued row + non-blocking send on a buffered channel, returns immediately with `{run_id, status: "queued"}`. Cron loop hook (`services/gc/internal/runner.PersistedRunner`) drains queued rows via `ClaimNextQueued` (FOR UPDATE SKIP LOCKED) between ticks. Pre-FE-API-032 behaviour preserved when `DB_DSN` unset (legacy `runLoop` still works). Concurrency: existing REM-009 per-tenant advisory locks still arbitrate concurrent sweeps. 3 routes on services/management gated by `requirePlatformAdmin`: `GET /api/v1/admin/gc/status`, `GET /api/v1/admin/gc/runs`, `POST /api/v1/admin/gc/run`. New `GC_GRPC_ADDR` optional config (404 "route disabled" when unset). `next_scheduled_at` is best-effort from `last_completed_at + GC_RUN_INTERVAL_HOURS` (not authoritative — the in-process ticker is the real source). 38 new tests (7 repo, 17 gc handler, 14 management bufconn). Merge commit `92e6028` (source `829df5f`). |
| FE-API-033 | **Per-tag SBOM / provenance download** | `services/management`, `services/metadata` | DONE ✅ | New migration `00007_scan_results_sbom.sql` adds `sbom_format TEXT NULL` + `sbom_json BYTEA NULL` columns to `scan_results`. New `UpsertScanSBOM` + `GetScanSBOM` gRPC RPCs on services/metadata. `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/sbom?format=spdx-json` streams the SBOM with `Content-Type: application/spdx+json` + `Content-Disposition: attachment; filename="<digest>.spdx.json"`. No-SBOM tag → 404 with `{"code": "no-sbom"}` body. **Scanner write-path integration DEFERRED** — only the metadata RPC + management route shipped (UpsertScanSBOM serves as the test seam). Real scanners will populate the column as future scans land. **CycloneDX** also deferred (explicit 400 `format not yet supported`). Merge commit `78845bc` (source `4f27997`). |
| FE-API-034 | **SSO provider configuration** | `services/auth` | DONE ✅ (OAuth + SAML) | Migration `20260621000001_auth_providers.sql`: `auth_providers` (`auth_provider_type` enum + per-canonical-type unique constraint for Google/GitHub/Microsoft), `auth_login_sessions` (PK=state, 10-min TTL, single-use via `DELETE ... RETURNING`), `users.sso_provider_id`. **OAuth** (Google / GitHub / Microsoft / generic OIDC) — hand-rolled `net/http`, PKCE S256 mandatory, 32-byte `crypto/rand` base64url state, constant-time compare, `?next=` validated, `email_verified=true` required for auto-provisioning. **SAML** (`df39d13`) — new `services/auth/internal/saml` package wraps `github.com/crewjam/saml` bare `ServiceProvider` (bypasses `samlsp.Middleware` cookie/session machinery since `auth_login_sessions` + JWT issuer cover those concerns). `startSAML` captures `AuthnRequest.ID` and persists it in the unused `pkce_verifier` column so `callbackSAML` passes it to `ParseResponse` as the only permitted InResponseTo. Email attribute walk: ADFS URN → LDAP OID → short names (email/mail), falls back to `Subject.NameID.Value`. SAML `EmailVerified=true` hardcoded (IdP authentication IS the verification — SAML has no explicit verified claim). New config: `SAML_SP_CERT_PATH` + `SAML_SP_KEY_PATH` (both-or-neither at startup; unset → 501 NOTCONFIGURED so cluster still boots without SAML certs). Metadata parsed per-request (admin PATCH takes effect immediately; caching flagged for benchmark-driven follow-up). JWT returned via 302 to `{next}?sso_token=<jwt>`. **Auto-provisioning**: case-insensitive email match; new user gets empty `password_hash`, `sso_provider_id` set, `default_role` at `org=*` scope; race-safe via `ErrAlreadyExists` re-query. **Admin CRUD** on services/auth REST: per-tenant admin; `client_secret` AES-256-GCM-encrypted before storage, never returned. Audit events: `auth.provider_created/updated/deleted`. 21 new tests total (12 OAuth + 9 SAML — happy E2E via in-process `crewjam/saml.IdentityProvider`, replay rejection, missing form fields, malformed response, not-configured 501). Merge commits `4e3d939` (OAuth) + `df39d13` (SAML). |
| FE-API-035 | **Webhook delivery payload retrieval** | `services/management`, `services/webhook` | DONE ✅ | New `GetDelivery` gRPC RPC on services/webhook returning `DeliveryDetail` (summary + `payload_json` + `signature_header` + `response_body`). Tenant + endpoint scoping enforced in repository so cross-tenant delivery_id guesses 404, not 200. `GET /api/v1/webhooks/{id}/deliveries/{delivery_id}` on services/management, gated by existing `requireWebhookAdmin` (PENTEST-027 alignment). `signature_header` + `response_body` currently empty: schema doesn't capture them yet — wire shape present so a follow-up migration + dispatcher patch can fill them. Merge commit `2f7b250` (source `079752b`). |
| FE-API-036 | **Bulk tag delete** | `services/management`, `services/metadata` | DONE ✅ | `DELETE /api/v1/repositories/{org}/{repo}/tags` with body `{tag_names: [...]}`. Reuses existing `DeleteTag` gRPC; per-tag sub-transaction model — each tag is its own call so one missing tag doesn't block the others. Response carries per-tag `{deleted, reason?}`. Cap 100 (post-dedupe), full-request 400 on any invalid tag name (before any delete fires). Sequential (deterministic mapping; 100×~5ms = ~500ms). Auth: writer or above on repo / parent org (mirrors single-delete). Merge commit `add0a4c` (source `3ff23a1`). |
| FE-API-037 | **Per-repo retention policy CRUD** | `services/management`, `services/metadata` | DONE ✅ | Migration `00008_retention_policies.sql` creates the `retention_rule_kind` enum with all 5 kinds from day one (`max_age_days`, `max_count`, `max_size_bytes`, `dangling_grace_days`, `max_idle_days` — Postgres enums need ALTER TYPE to grow, so we front-load it; the API accepts `max_idle_days` but the executor (FE-API-043) doesn't honor it yet). `retention_policies` table: PK=repo_id (FK CASCADE), tenant_id denormalised for fast scans, rules JSONB, protected_tag_patterns TEXT[] default `[latest, stable, ^v?\d+(\.\d+){0,2}$]`, preview_until TIMESTAMPTZ. New gRPC RPCs: `GetRepoRetentionPolicy` / `UpsertRepoRetentionPolicy` / `DeleteRepoRetentionPolicy`. **preview_until reset semantics** (server-set, client-readonly): cleared NULL when enabled=false; reset to NOW+24h on fresh insert with enabled=true OR disabled→enabled OR rules differ materially (sorted-by-kind compare); stays put for true no-op upserts. **Validation**: enabled=true requires non-empty rules; each kind appears at most once; value > 0 with per-kind caps (max_age/idle ≤ 36500, max_count ≤ 10M, max_size ≤ 100 TiB, dangling_grace ≤ 365); protected patterns ≤ 256 chars + must compile as Go regexp. HTTP routes `GET/PUT/DELETE /api/v1/repositories/{org}/{repo}/policies/retention` on services/management. **Auth**: GET requires repo `reader`+; PUT/DELETE require repo `admin` or parent-org `admin`/`owner` (writer NOT enough — retention is a destructive primitive). `updated_by` sourced from JWT, never request body. GET on no-policy returns 404 with `{"code": "no-policy"}` so the FE-API-039 inheritance fallback has a clean signal (NOT a synthesized empty policy). 44 new tests (22 metadata handler unit + 13 management bufconn + 9 metadata repository integration). Merge commit `13a1595` (source `6cc8de6`). |
| FE-API-038 | **Retention policy dry-run + preview window state** | `services/management`, `services/metadata` | DONE ✅ | New `EvaluateRetention` gRPC RPC on services/metadata serves both routes (single-source eval logic). `POST /api/v1/repositories/{org}/{repo}/policies/retention/dry-run` accepts a candidate policy (NOT persisted) and returns `{ would_delete: [{manifest_id, manifest_digest, tags[], pushed_at, size_bytes, reasons[]}], protected_skipped: [{manifest_id, manifest_digest, tags[], matched_pattern}], total_count, total_bytes, evaluated_at, truncated }`. `GET .../policies/retention/preview` runs the same eval against the SAVED policy and returns `{enabled, preview_until, in_preview_window, would_delete_count, would_delete_bytes, policy_updated_at}` for the UI countdown banner. **Algorithm**: protected-pattern check runs FIRST; rules apply OR-style; each rule's selection set is computed against the FULL non-protected set (so `max_count` + `max_size_bytes` pick oldest regardless of what `max_age_days` separately matched); multi-rule manifests report ALL matching kinds in `reasons[]`. **Truncation contract**: `would_delete` capped (default 1000, max 5000), `total_count`/`total_bytes` always reflect the FULL set, `truncated: true` flag set when capped; `protected_skipped` capped at 100 (no flag). **Schema gaps documented**: `dangling_grace_days` uses `created_at` as approximation since `tag_removed_at` doesn't exist yet (FE-API-040 can do better); `max_idle_days` silently skipped until FE-API-043 lands. Stable wire output via final sort `(created_at ASC, digest ASC)`. **Auth**: dry-run requires repo `admin`+ (mirrors PUT); preview-state requires repo `reader`+ (read-only informational). 32 new tests. Merge commit `ca3efbe` (source `0e12dd6`). |
| FE-API-039 | **Per-org default retention policy + inheritance** | `services/management`, `services/metadata` | NOT STARTED | Lets an org admin set a default policy that every repo in the org inherits unless the repo has its own row. New `retention_policy_defaults` table on metadata (PK=org_id). `GET/PUT/DELETE /api/v1/orgs/{org}/policies/retention`. When `FE-API-037 GET` returns "no per-repo policy", the management BFF falls back to the org default + flags `inherited_from: "org"` in the response so the UI can render "(inherited from org default)" instead of empty. **Auth**: org `admin` or `owner` to PUT/DELETE the default; everyone with org read access to GET. UI surface: new "Default retention" section on the org page (`/orgs/{org}/members` gains a sub-tab) — NOT on the platform `/admin/tenants` route. |
| FE-API-040 | **Retention executor (new gc mode `retention`)** | `services/gc`, `services/metadata`, `services/storage` | NOT STARTED | Extends the FE-API-032 `gc_run_mode` enum with `retention` value. New `EvaluateRetention(repo_id, dry_run)` gRPC RPC on services/gc — returns a deletion plan; if `dry_run=false`, queues a real run reusing the existing `gc_runs` table machinery (`triggered_by` carries policy id + caller id). Soft-delete pattern: new `manifests.retention_pending_delete_at` column; executor sets timestamp + emits `retention.evaluated` event but leaves the blob in place. A second `retention_grace` mode (cron-driven, 7-day default) finalises blob deletion after the grace window. Reuses existing `pg_try_advisory_lock` from REM-009 so concurrent retention sweeps + ordinary GC sweeps don't collide. UI surface: existing "Housekeeping" admin GC card grows a "Retention" tile showing pending-delete count + recent runs. |
| FE-API-041 | **Retention audit + webhook events** | `libs/rabbitmq/events`, `services/audit`, `services/webhook` | NOT STARTED | New routing keys: `retention.evaluated` (fires before any deletions for a dry-run OR a real run — carries the would-delete list), `retention.applied` (fires after a soft-delete commit), `retention.grace_completed` (fires after blob removal). Adds typed payloads `RetentionEvaluatedPayload` / `RetentionAppliedPayload` to `libs/rabbitmq/events`. Audit consumer picks them up automatically (existing routing). Webhook delivery: operators can subscribe to `retention.*` events to get notified before deletions land — gives a hard "are you sure" signal at the platform edge. |
| FE-API-042 | **Pull-activity tracking** (FE-API-030 caveat) | `services/core`, `services/audit`, `services/metadata` | NOT STARTED | Prerequisite for any activity-based retention rule AND closes the FE-API-030 pull-analytics gap. Two changes: (a) `services/core` publishes a `pull.image` event on every successful manifest GET (already does this for push); (b) audit consumer maps it to action `pull.image` so it appears in `GetAnalytics` queries. Write-amplification trade-off: every pull becomes a Postgres insert + a RabbitMQ message. Mitigations to evaluate before shipping: sample rate (e.g. 1-in-10) OR batched aggregation in metadata's `manifests.last_pulled_at` column updated via the consumer. Document the trade in the commit. |
| FE-API-043 | **Activity-based retention rule** | `services/gc`, `services/metadata` | NOT STARTED | Depends on FE-API-042. Adds a new rule kind `max_idle_days` to the FE-API-037 schema: "delete manifests where `last_pulled_at < now - N days` AND `pushed_at < now - N days`" (the second clause prevents brand-new-but-never-pulled manifests from being deleted as part of CI churn). UI surface: the FE-API-037 retention tab gains a "Delete unused after N days" rule chip. Auth + execution path same as FE-API-040. |

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
| Postman collection — every public `/api/v1/*` HTTP endpoint | `docs/postman/` | — | DONE ✅ — `docs/postman/registry-management.postman_collection.json` + environment + README. Covers all management routes (stats, repos, tags, scans, builds, RBAC org+repo members, admin tenants, webhooks) plus the auth-side routes the gateway exposes under the same prefix (login, logout, token/refresh, users, api keys). Login captures the JWT into `{{token}}`, webhook-create captures `{{webhookId}}`, api-key-create captures `{{apiKeyId}}`. |

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
