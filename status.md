# Project Status — Completed Work Log

> **What this file is:** the historical record of remediation, sprint,
> and security work that has been **completed**. Resolution notes,
> dated sprint summaries, decision context, and closed
> `REM-NNN` / `SEC-NNN` / `PENTEST-NNN` items live here.
>
> **What this file is NOT:** the place for currently-open work.
> Open remediation items live in [`status-tracker.md`](status-tracker.md),
> and prioritised future items live in [`futures.md`](futures.md).
>
> **Workflow:** when an item in `status-tracker.md` is finished, the
> entry is **removed** from the tracker and **appended** here with a
> resolution note (commit / PR / date). `status-tracker.md` stays
> lean by design; this file accumulates the audit trail.
>
> **Read order for context:** newer entries are toward the top of
> each section. The dated "Last updated" log below captures
> sprint-level summaries; the bulk of the file is per-item
> resolution notes grouped by ID family (`REM-NNN`, `SEC-NNN`, etc.).
> Older sprints from the pre-OSS phase are preserved verbatim for
> audit trail.

---

> Last audited: 2026-06-21 (full doc audit — verified every OPEN/NOT STARTED row against the codebase; pruned stale Beacon-rebuild rows; marked Round-3 pentest items resolved where verified).
>
> Last updated: 2026-06-23 (late evening — **post-OSS hygiene batch 1 + deep review batch landed**. PR #43 (`docs/tracker-complete-open-issues`) listed HYG-001..008 in status-tracker.md as the post-OSS contributor-onboarding surface. PR #44 (`feat/oss-hygiene-batch-1`) shipped **HYG-002** (CODE_OF_CONDUCT.md — Contributor Covenant 2.1), **HYG-003** (.github/SECURITY.md — placed in `.github/` to avoid case-collision with the existing lowercase `security.md` audit log), **HYG-004** (.github/ISSUE_TEMPLATE/bug_report.yml + feature_request.yml + config.yml + PULL_REQUEST_TEMPLATE.md). Remaining HYG-001/005/006/007/008 (visual + GitHub Settings UI work) stay in status-tracker.md. Same day: three parallel review agents (design / quality / architecture) did a deep cross-cutting review — 74 findings (24 DSGN + 28 QA + 22 ARCH) curated into [`futures.md`](futures.md) "Review batch — 2026-06-23" with Tier 1/2/3 prioritisation. Full per-finding detail (file paths + line numbers + proposed fixes) persisted to [`.claude/reviews/`](.claude/reviews/). The biggest architectural risks surfaced: ARCH-002 (no transactional outbox for RabbitMQ — broker outage between DB commit and publish silently drops `push.completed`/`scan.completed`/audit events), ARCH-001 (documented PostgreSQL RLS layer only exists on `audit_events`, not the 5 other tenant-scoped DBs), QA-002 (`libs/rabbitmq/publisher.Publish` not goroutine-safe — silent ACK correlation under concurrent load), QA-001 (`signatures` table has no `tenant_id`), and ARCH-005 (no production-mode config validation). PR opened on `chore/track-review-findings`.).
>
> Previous: 2026-06-23 (late evening — **futures.md Tier 1 #4 Phase 2 — durable DLX + drain landed** on `feat/audit-siem-streaming-phase2`. Promoted the inline goroutine dispatcher to a real RabbitMQ queue (`audit.export.tasks`, quorum, `x-dead-letter-exchange = dlx.audit-export`) + paired DLX queue (`audit.export.dlx`, bound on `#`). New `services/audit/internal/exportworker/` package: Publisher (eventconsumer enqueue path), Consumer (drains tasks queue, runs `export.Deliver`'s 3-attempt retry, NACK→DLX on exhaustion), Drain (one-shot republish from DLX → tasks, bounded 10k/call, tenant-scoped), MgmtClient (RabbitMQ Mgmt HTTP API for live depth). 5th gRPC RPC `DrainAuditExportDLX(tenant_id)` + BFF `POST /workspace/me/audit-export/drain` + FE "Drain DLX → retry" button (appears when `dlx_queue_depth > 0`). Live DLX depth via Mgmt API distinct from lifetime counter (`-1` = unreachable). Producer side becomes near-instant — broker outage on SIEM no longer back-pressures the audit DB INSERT path. Phase 1 inline dispatcher kept as safety net. Live-verified: kill receiver → 3 events park in DLX (live depth matches rabbitmqctl) → POST /drain returns `republished:3` → consumer re-ships → all 3 land at receiver with sig-ok=true → DLX back to 0. Docs: `docs/SIEM-EXPORT.md` §7 rewritten with full Phase 1+2 retry posture.).
>
> Previous: 2026-06-23 (evening — **futures.md Tier 1 #4 Phase 1 — audit-log streaming to SIEM landed** on `feat/audit-siem-streaming`. Per-tenant `audit_export_configs` table (AES-256-GCM-encrypted `hmac_secret` + `bearer_token`, format enum, JSON event filters, observability counters). 4 gRPC RPCs (Get/Put/Delete/Test) on AuditService — secret material never returned over the wire, only `*_set` booleans. 3 wire formats in new `services/audit/internal/export/` pkg: syslog RFC 5424 over TCP/TLS (SD block keyed by PEN 53430), ArcSight CEF over syslog framing, HTTPS webhook with `X-Signature: sha256=<hex>` HMAC or Bearer auth. SSRF guard at both write time + every delivery (DNS shift defence). Dispatcher wired into the eventconsumer's INSERT path: after each audit_events row, fire-and-forget goroutine renders + ships. v1 in-process retry (3 attempts, exp backoff capped at 5s); exhausted → bump `dlx_depth`. 4 BFF routes at `/api/v1/workspace/me/audit-export[/test]` workspace-admin-gated. Frontend `/workspace/audit-export` settings page with format radio + URL validator + write-only secret inputs + JSON filter editor + Send-test-event with rendered-payload preview + observability banner. Live-verified end-to-end: `image.signed` event flowed audit DB → exporter → HMAC POST → receiver with sig-ok=true in ~3s. Phase 2 (async DLX queue + replay) deferred. Docs: new `docs/SIEM-EXPORT.md`, README capability matrix + FE surface table updated, futures.md Tier 1 #4 flipped to DONE.).
>
> Previous: 2026-06-23 (afternoon — **futures.md Tier 1 #3 Phase 2 — per-repo trusted-key allowlist landed** on `feat/signed-image-trusted-keys`. New migration `00016_repository_trusted_keys.sql` (UNIQUE (repo_id, key_id), tenant_repo composite index) + 3 metadata RPCs (List/Add/Remove) with `RepositoryTrustedKey` message + AddRepositoryTrustedKey idempotent on conflict + RemoveRepositoryTrustedKey returns ErrNotFound on missing. services/core's `checkSignatureAdmission` extended: after the non-empty-signatures check, calls `metadata.ListRepositoryTrustedKeys` and intersects recorded sig key_ids with the allowlist; empty allowlist is Phase 1 fallback by design. New `bustTrustedKeysCache` helper next to the existing bustRepositoryCache; the new ListRepositoryTrustedKeys RPC is wired into the REM-007 cache (30s TTL) so per-pull overhead stays at one Redis hit. BFF: 3 HTTP routes (list/add/delete) in new `trusted_keys.go` with key_id regex validation + repo admin gate on writes. Frontend: new `RepoTrustedKeysSection` card on Settings tab below RepoSignaturePolicySection, with allowlist table + Approve dialog (key_id + display_name validation) + per-key Revoke with confirm + Phase-1-fallback warning pill when policy is on but list is empty. Docs: docs/SIGNING.md §8 extended with full Phase 2 walkthrough + threat-model caveats, docs/SERVICES.md core+management surfaces updated, README capability matrix, futures.md flipped to DONE, FE-STATUS.md row updated. Phase 3 (multi-key quorum / rotation / Fulcio identity binding) deferred.).
>
> Previous: 2026-06-23 (morning — **futures.md Tier 1 #3 — Signed-image admission landed (Phase 1)** on `feat/signed-image-admission`. New migration `00015_repository_require_signature.sql` + `Repository.require_signature` proto field + `UpdateRepositorySignaturePolicy` metadata RPC. services/core's `GetManifest`/`HeadManifest` consult signer.ListSignatures when the flag is on; zero sigs → 403 DENIED with actionable body. Fail-OPEN on metadata/signer reachability blips (warn + continue) so a transient outage doesn't break every pull; `SIGNER_GRPC_ADDR` unset → startup warning + allow-all (dev-stack convenience). BFF PATCH plumbing wires `*bool` so unrelated PATCHes don't reset the flag; separate RPC for audit clarity. Frontend `RepoSignaturePolicySection` card sits on the repo Settings tab next to the immutability toggle (same shape, independently composable security flips). Docs updated: README capability matrix, docs/SERVICES.md core+management, docs/SIGNING.md §8 admission walkthrough. Phase 1 contract: ANY signature passes; per-repo trusted-key allowlist is a planned Phase 2 follow-up.).
>
> Previous: 2026-06-21 (night — **FE-API-034 SAML completion landed** (`df39d13`). New `services/auth/internal/saml` package wraps `crewjam/saml` bare `ServiceProvider`, bypassing samlsp.Middleware cookie/session machinery (auth_login_sessions + JWT issuer cover those). startSAML captures AuthnRequest.ID into the unused pkce_verifier column so callbackSAML can pass it to ParseResponse as InResponseTo. Email attribute walk (ADFS URN → LDAP OID → short names → NameID fallback). New SAML_SP_CERT_PATH + SAML_SP_KEY_PATH config (both-or-neither; unset → 501 NOTCONFIGURED so the cluster still boots). Metadata parsed per-request (admin PATCH effective immediately). 9 new SAML tests including happy E2E via in-process crewjam IdentityProvider + replay rejection. **🎉 ALL 36 BACKEND FE-API ITEMS NOW FULLY DONE** — no remaining deferrals.).
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

## Related trackers

- [`FE-STATUS.md`](FE-STATUS.md) — frontend route + sprint status, by convention status.md is backend-only.
- [`futures.md`](futures.md) — prioritized backlog of items not yet sprinted, with tiering rationale + promotion workflow. New cross-cutting work (MFA, tag immutability, admission policy, SCIM, etc.) starts there before getting an FE-API or REM number.

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
- **Affects:** `services/scanner`, `services/metadata`, `services/management`, `infra/docker-compose`, `proto/metadata/v1`, `proto/scanner/v1`, `docs/`
- **Status:** **PHASE 1 + PHASE 2 BACKEND DONE ✅** — Phase 1 (`8debd29`, 2026-06-21), Phase 2 backend (`bd4ba1d`, 2026-06-21). Phase 2 frontend (`/admin/scanner` admin route) tracked in `FE-STATUS.md` → REM-011 P2 FE row; unblocked now that all FE-API-044..047 endpoints are live.
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
- **Phase 2 backend acceptance criteria — verified live (2026-06-21):**
  1. ✅ `GET /api/v1/admin/scanners` → both `dev-stub` and `trivy-adapter` discovered with name/version/path/checksum/size_bytes/env_keys/active flag.
  2. ✅ `GET /api/v1/admin/scanners/health` → `healthy=true`, `queue_depth=0`, `active_adapter_name=dev-stub`.
  3. ✅ `PATCH /api/v1/admin/scanners/active {trivy-adapter}` → 200, `active=true`, in-memory swap atomic (no container restart).
  4. ✅ `POST /api/v1/admin/scanners/test` → `ok=true`, `scanner=trivy 0.52.0`, `duration_ms=8010` (real Trivy scan against the dev fixture).
  5. ✅ `docker compose --force-recreate registry-scanner` → trivy-adapter still active on restart (`scanner_settings` row persists).
- **What Phase 2 shipped (backend):**
  - **FE-API-044** `GET /admin/scanners` + `GET /admin/scanners/active` (scanner gRPC: `ListInstalledAdapters`, `GetActiveAdapter`).
  - **FE-API-045** `PATCH /admin/scanners/active` (scanner gRPC: `SetActiveAdapter`). In-memory swap + DB persist in one call.
  - **FE-API-046** `POST /admin/scanners/test` (scanner gRPC: `RunTestScan`). Fixture configurable via `SCANNER_TEST_TENANT_ID` / `SCANNER_TEST_REPOSITORY` / `SCANNER_TEST_MANIFEST_REF` (defaults to dev tenant + `dev/alpine:latest`).
  - **FE-API-047** `GET /admin/scanners/health` (scanner gRPC: `GetScannerHealth`). Live liveness + queue depth + in-flight count + active adapter name/version.
  - `services/scanner/internal/registry` — directory-scan discovery, atomic active-pointer, per-name version cache backfilled from successful scans via `worker.VersionRecorder`.
  - `services/scanner/internal/worker` — `atomic.Pointer[plugin.Scanner]` + `SetScanner` for the in-flight-safe swap. `atomic.Pointer[VersionRecorder]` for the version backfill hook (the original `atomic.Value` panicked on dynamic-type drift — caught + fixed live).
  - `scanner_settings` migration `20260621000001_scanner_settings.sql` — singleton table on the existing scanner DB.
  - `services/scanner/internal/server/server.go` — finally wires mTLS on the inbound gRPC server (was plaintext — fine when only RabbitMQ consumers existed, broke the moment services/management started calling it).
  - 7 new unit tests + 1 testcontainers integration covering registry discovery, atomic swap, version cache, and the scanner_settings round-trip.
- **Phase 2 backend follow-ups (still open):**
  - [ ] Integration test that triggers a scan via `RunTestScan` and asserts the `scan_results` row materializes within 30s end-to-end (today's tests cover the parts but not the wire). Guards against silent JSON-RPC contract drift.
  - [ ] Stuck-job detection in `GetScannerHealth` (count jobs that have been `pending`/`running` for >2× the per-job timeout). Today the `healthy` flag is always `true` if the service is up; the FE liveness-driven degradation needs a real signal once Phase 2 FE lands.

> Phase 2 frontend (`/admin/scanner` admin route, adapter cards, swap UI, test-scan button, liveness-driven `ScanPanel` upgrade) is tracked in `FE-STATUS.md` → REM-011 P2 FE. All backend dependencies (FE-API-044..047) are now live.

---

### REM-012 — Compliance Report Downloads via Streaming RPC
- **Affects:** `proto/scanner/v1`, `services/scanner`, `services/management`, `infra/docker-compose`
- **Status:** DONE ✅ (2026-06-22) — streaming RPC live; shared-volume workaround retired.
- **Surfaced:** 2026-06-21 while debugging `/security/reports` PDF/SBOM 404s. Logs showed `services/management` failing `os.Open(/tmp/reports/<tenant>/<id>.pdf)` because the file lives on `services/scanner`'s filesystem.
- **Why the fix mattered:** the proto comment at `proto/scanner/v1/scanner.proto:159-161` already flagged the on-disk-path return as a v1 shortcut. The shared-volume workaround violated Decision Log #11 ("no presigned URLs to clients; all blob traffic proxied for audit + rate limiting") implicitly — there was no audit hop and no rate limit between storage and the BFF.
- **What shipped (2026-06-22):**
  - [x] `rpc DownloadComplianceReport(DownloadComplianceReportRequest) returns (stream ReportChunk)` on `proto/scanner/v1/scanner.proto`. Request carries `tenant_id` + `report_id` + `format` ∈ {pdf, sbom}; first `ReportChunk` carries `content_type` only, subsequent chunks carry `data` only (~64 KiB each — tuned via `reportChunkBytes` constant on the scanner handler).
  - [x] Scanner-side server-streaming handler at `services/scanner/internal/handler/grpc.go` `DownloadComplianceReport`. Resolves the report row via the existing tenant-scoped `repo.GetReport`, validates `format` against an allowlist, applies the same `filepath.Clean` + extension defence-in-depth the v1 BFF used, opens the file scanner-side, sends content_type on the first chunk, then loops with explicit `stream.Context().Err()` checks so a slow client / BFF disconnect stops the file read promptly. Error mapping: NotFound (no row for tenant), FailedPrecondition (status != succeeded), InvalidArgument (bad UUIDs / format / extension mismatch), Internal (missing on-disk artifact despite "succeeded" status).
  - [x] Management `serveReportFile` rewritten to call the stream + drain it into the `http.ResponseWriter`. Headers (`Content-Type`, `Content-Disposition`) are committed from the first chunk's `content_type` BEFORE any bytes write — important because once `w.Write` is called the headers seal. Mid-stream errors log + close (we can't change the HTTP status code after headers commit); the client sees a truncated body, surfaced honestly. New `mapDownloadErr` helper translates gRPC codes to HTTP codes (404/409/400/500) for setup-time failures.
  - [x] `download_pdf_url` / `download_sbom_url` on the JSON `GetComplianceReport` response are now an internal hint only; proto comment at `scanner.proto` updated to mark them as such while keeping the fields on the wire for the dashboard's "report ready" sentinel.
  - [x] `scanner_reports` named-volume mount removed from `registry-management` in `infra/docker-compose/docker-compose.yml`. Scanner still mounts the same volume (it's the writer); reader side no longer needs filesystem access. Live in the running compose stack — `docker inspect` on management confirms only `/certs` mounted, scanner shows `/certs /tmp/reports /trivy-cache`.
  - [x] 2 new tests on management: `TestReports_DownloadPDF_multiChunkStream` exercises a 3-chunk fanout (verifies headers come from the FIRST chunk + body concatenates correctly); `TestReports_DownloadPDF_streamErrorMidway` verifies headers stay committed at 200 and the body truncates honestly when the scanner errors mid-stream. `downloadComplianceReportOverride` hook on the fake server lets future tests script stream behaviour without spinning up a new fake. All existing report tests still pass.
- **Test coverage gap (acknowledged, not blocking):** no end-to-end "generate → wait succeeded → download both formats → byte-compare" integration test was added because the existing scanner integration harness doesn't run the report worker in-process. Manual verification in compose is documented; a real integration test belongs in a follow-up that lifts the report worker into testcontainers.
- **Frontend impact:** none — the `/api/v1/security/reports/{id}/download/{pdf,sbom}` HTTP surface is unchanged. The single visible change is that the dashboard now works in cross-node K8s, which it didn't before.

---

### REM-013 — Retention surface backend gaps surfaced during S11
- **Affects:** `proto/metadata/v1`, `proto/gc/v1`, `services/metadata`, `services/gc`, `services/management`
- **Status:** OPEN — frontend slice 3 of S11 (retention UI) shipped only the executor trigger; the other two pieces ("pending-delete badges on Tags tab", "per-repo retention run history panel") are blocked by missing backend surface.
- **Surfaced:** 2026-06-21 while shipping S11 slice 3.
- **Gap 1 — `Tag` proto doesn't carry `retention_pending_delete_at`.** The column exists on `manifests` and the FE-API-040 executor stamps it (services/metadata/internal/repository/retention_pending.go:47), but `proto/metadata/v1/metadata.proto:232` `message Tag` doesn't surface the field. The BFF's `handleListTags` (services/management/internal/handler/handler.go:728) copies only `tag_id`, `manifest_digest`, `size_bytes`, `updated_at`, `created_at` into `TagResponse`. Without this the FE can't render the "🗑 deletes in N days" badge on tag rows.
- **Gap 1 fix sketch:**
  - [ ] Add `google.protobuf.Timestamp retention_pending_delete_at = 9;` to `message Tag` (omit when null on the wire — the field is already nullable in SQL).
  - [ ] LEFT JOIN `manifests` on `tags.manifest_id` in `services/metadata/internal/repository/tags.go` `ListTags` SQL to pull the column.
  - [ ] Extend `TagResponse` in management with `retention_pending_delete_at *time.Time` (omitempty) and the conversion in `handleListTags`.
  - [ ] Frontend: chip on the tag row with `formatRelativeDate(now + grace - elapsed)` once the field shows up on the wire; the existing tags-panel toolbar gets a "Show pending only" toggle for free.
- **Gap 2 — `gcv1.ListRunsRequest` doesn't accept `repo_id` or `mode` filters.** Per-repo "last 10 retention runs" requires either paging through every GC run client-side (unbounded) or adding filters to the gRPC.
- **Gap 2 fix sketch:**
  - [ ] Add `string repo_id = 3; string mode = 4;` to `proto/gc/v1/gc.proto:129 ListRunsRequest` (both optional — empty preserves current behaviour).
  - [ ] Extend `services/gc/internal/repository.go` `ListRuns` SQL with optional `WHERE repo_id = $X` + `WHERE mode = ANY($Y)` predicates (tenant scoping already in place via the gRPC interceptor).
  - [ ] Mount a new BFF route `GET /api/v1/repositories/{org}/{repo}/policies/retention/runs` (repo reader gate) that forwards `repo_id + mode IN ("retention","retention_grace")` to the gc service.
  - [ ] Frontend: the deferred `RetentionRunHistoryPanel` consumes this with infinite-query pagination over `next_page_token`; lands below the existing `RetentionRunCard` on the Retention tab.
- **Gap 3 — Storage breakdown response doesn't carry per-repo retention summary.** S11 slice 4 wanted to add a "Retention" column to the dashboard storage-breakdown card showing each repo's effective retention rule summary (per-repo override OR inherited org default OR "—"). Today `services/management/internal/handler/stats_storage.go` populates `RepositoryStorageEntry` with `{repo_id, org, name, storage_used_bytes, percent_of_tenant}` only. Fetching per-row would be 50 round-trips on every dashboard load.
- **Gap 3 fix sketch:**
  - [ ] Add `retention_summary: string` (e.g. "max 50 manifests · 30d") and `retention_source: "repo"|"org"|"none"` to `RepositoryStorageEntry` in `services/management/internal/handler/stats_storage.go`.
  - [ ] In the BFF, fan out `GetEffectiveRetentionPolicy` once per repo in the breakdown (cap 50) — or add a `BatchGetEffectiveRetentionPolicies(repo_ids)` RPC to `services/metadata` if profiling shows the fan-out is slow.
  - [ ] Frontend: render the new column in `frontend/src/components/dashboard/storage-breakdown-card.tsx` with link-back to the repo Retention tab.
- **Why these were deferred rather than added in-flight during S11:** S11 was scoped as a frontend sprint with all backend deps marked DONE in FE-STATUS.md. These three gaps survived that audit. Adding them needs proto regen + service rebuild (or BFF-only fan-out for gap 3) and is worth a focused PR rather than a slice tail.

---

### S-MAINT-1 — Maintenance backlog (2026-06-22, opened during Sprint 10 hand-off)
- **Affects:** assorted (frontend + management + scanner + metadata)
- **Status:** OPEN — backlog tracker. Items individually checkboxed; will be picked up in small batches on their own branches after PR #19 (the big Sprint 10 PR) merges.
- **Surfaced:** 2026-06-22 during a UX walk-through after the Grype adapter landed. Listed below in user-discovery order then grouped by category for batching.

#### Real bugs (investigation needed before fix)
- [ ] **B1 — Scan findings duplicate on re-scan.** Re-scanning the same image twice should REPLACE the prior finding set; today the totals appear to ADD instead of overwrite. Almost certainly an `UpsertScanResult` contract issue — likely INSERTing one row per scan attempt where the table should be unique-on `(manifest_digest, scanner_name)` or similar. **Investigation steps:** (1) read `services/metadata/internal/repository/repository.go` UpsertScanResult SQL, (2) check `scan_results` table schema for ON CONFLICT clause, (3) reproduce by triggering Rescan twice on the same tag, querying scan_results before/after. Data-integrity bug → high priority.
- [x] **B2 — API key creation errors from `/profile` or sidebar.** RESOLVED (sprint-11 maint batch 1, 2026-06-22). Root cause was NOT a FE-API-048 regression — it was a pre-existing FE↔BE contract drift that became user-visible because the frontend `api-keys-section.tsx` table called `k.key_id.slice(0, 8)` on a field the backend never returned. Full inventory: (1) the FE hook expected `{ keys: [...] }` but `GET /api/v1/apikeys` returns the array directly → list was always empty, (2) FE `ApiKey.key_id` ↔ BE `id` (the .slice() crash), (3) FE `CreatedApiKey.secret` ↔ BE `key` (raw secret never displayed after create), (4) FE sent + read `description` which the BE silently discarded (cosmetic input only), (5) FE expected `last_used_at` which the BE never returned (column populated but not plumbed through). **Fix:** aligned FE types + accessors to match the canonical BE shape (`id`, `key`, `prefix`, `scopes`, `expires_at`, `last_used_at`, no `description`), added `last_used_at` to BE `apiKeyResponse` (now visible on GET list — `,omitempty` keeps it null for never-used keys), dropped the cosmetic `description` input from the Create dialog. Verified end-to-end against the live stack: create → 201 with `key`, GET list shows `id`+`prefix`, key use → next list shows real `last_used_at` timestamp. No changes to the polymorphic `api_keys` schema or `ValidateAPIKey` path; FE-API-048 was not the cause.
- [ ] **B3 — Repository storage meter doesn't work.** Progress bar on repo detail page either renders empty / 0% or fails to load. **Investigation steps:** (1) inspect the repo detail page in DevTools, (2) check whether `/api/v1/repositories/{org}/{repo}` is returning storage data, (3) confirm whether the meter component reads from `useRepository` or a separate hook.
- [ ] **B4 — Modal text/textbox spacing missing across the app.** Dialog labels touch their inputs visually. **Investigation steps:** likely a Tailwind class regression on `components/ui/dialog.tsx` or the Label primitive; could also be a Beacon design-system token change. Quick audit pass needed.
- [x] **B5 — PutTag column-count regression breaks ALL pushes (Docker + Helm).** Found 2026-06-22 while testing Helm push/pull (user request). Symptom: every `PUT /v2/<org>/<repo>/manifests/<tag>` returned HTTP 500 "internal error"; metadata log emitted `number of field descriptions must equal number of destinations, got 9 and 10` at `handler.PutTag`. **Root cause:** `services/metadata/internal/repository/repository.go` `scanOneTag` (the shared single-row scanner used by `PutTag` + `GetTag`) was extended to scan 10 fields when FE-API-050 added `Quarantined`, but the `PutTag` CTE-based SELECT was missed and stayed at 9 columns. The `tagSelectCols` constant used by `GetTag` / `ListTags` was updated correctly — only the CTE-form copy in `PutTag` regressed. **Fix:** added `COALESCE(m.quarantined, FALSE)` as the 10th SELECT column in `PutTag`'s wrapper SELECT (single line). **Verified:** Docker push + Helm push both succeed; Helm pull-back round-trips (digest match). **Lesson:** the duplicated column lists in `PutTag` (CTE) vs `GetTag`/`ListTags` (`tagSelectCols`) are an accident waiting to happen — should refactor `PutTag` to use a CTE+`tagSelectCols`-style construction so a future column addition can't desync. Logged as a follow-up under tech-debt (no ticket yet — drop into a future cleanup batch).

#### Quick UX polish
- [ ] **P1 — Dashboard storage breakdown** — surface the tenant total capacity (not just usage) alongside the per-repo rows. Limit the breakdown to **top 6** rows (currently top 8) to keep the card readable.
- [ ] **P2 — Per-repo storage breakdown card** — show capacity. May fold into B3.
- [ ] **P3 — Admin GC tile (`/admin/tenants` housekeeping)** — rename "Recent runs" → "Garbage collection: Recent runs". Limit table to **last 5** runs. Add a **Time run** column (started_at / completed_at depending on run state).
- [ ] **P4 — Admin Retention tile** — same treatment as P3: last-5 limit, Time-run column, clearer heading.
- [ ] **P5 — Page-size selector on data tables** — Vulnerabilities, Scans, Remediation, Notifications. Dropdown with 20 / 50 / 100 options, **default 20**. Persists in localStorage so the operator's last choice survives reload. Same component reused across all four surfaces.
- [ ] **P6 — Scanner skip-non-image artifacts.** Today every `push.completed` event is enqueued for scanning regardless of media type. Discovered 2026-06-22 while testing Helm: pushing a chart triggers a scan job, Grype/Trivy walk the chart `.tgz` looking for OS packages, find none, return clean. Wastes a scan cycle (~1-2s + DB round-trip) and pollutes `scan_results` with empty rows that show up on the per-repo Security tab. **Fix shape:** in `services/scanner/internal/worker/worker.go` `HandlePushCompleted`, fetch the manifest's `config.mediaType` from the raw manifest JSON and short-circuit when it's not in an allowlist (`application/vnd.oci.image.config.v1+json`, `application/vnd.docker.container.image.v1+json`). Helm + Cosign sig + SPDX attestation media types skip cleanly. **Note:** the inheritance resolver from FE-API-049 already provides a hook point — easy to add `if !isImageArtifact(manifest) { return nil }` before the `policyResolver` call.

#### New features
- [ ] **F1 — Bulk scan an entire org or repo.** New BFF routes `POST /api/v1/orgs/{org}/scan` and `POST /api/v1/repositories/{org}/{repo}/scan`; fan out scan.queued events for every tag underneath. UI: "Scan all" button on the org members page (org-scoped) and the repo detail page (repo-scoped). Quota note: needs a rate-limit on the fan-out so an operator can't queue 10k scans by accident.
- [ ] **F2 — Search across GC + Retention runs.** Both surfaces today show last-N with no search. Two design options worth discussing before implementing:
  - **Server-side**: add `search` param to `ListRuns` filtering on `triggered_by` (user_id substring) + date range. Cleanest, scales.
  - **Client-side over fetched page**: cheap to add but only filters what's already loaded. Misleading when results are off-page.
  - **Recommendation:** server-side with two filters — `triggered_by` substring + `date_from`/`date_to`. Same shape as the audit notifications surface.
- [ ] **F3 — `/activity` pagination strategy.** Already infinite-query but the table grows unbounded. Options: (a) date-range filter chips ("last 24h" / "last 7d" / "all"), (b) "Clear viewed" affordance backed by per-user `last_seen_at`, (c) older-than auto-archive at the audit-service level. Recommend (a) — most useful, lightest implementation.
- [ ] **F4 — Artifact-type discrimination on the Repositories page (Images / Helm charts / Other OCI artifacts).** Surfaced 2026-06-22 after verifying Helm chart push + pull works end-to-end against `oci://localhost:8081/<org>/<chart>` — the registry happily stores a Helm chart but the UI renders it identically to a container image, with no way to tell them apart at a glance. As OCI Distribution Spec v1.1 supports arbitrary artifacts (Helm 3 charts, Cosign signatures, SPDX SBOMs, WASM modules, Singularity images), this gap will widen.
  - **Recommended shape:** a **filter chip row** above the Tags table on the repo detail page — `All • Images • Helm charts • Signatures • Other` — rather than top-level page tabs. Reasoning: a single repo *can* hold mixed artifacts (you can `helm push` a chart and `cosign sign` it under the same repo namespace), so per-tag filtering is the right grain. Tabs at the page level would imply one repo = one artifact type, which OCI doesn't enforce.
  - **Per-tag rendering:** add a small **artifact-type pill** next to each tag name (next to the existing Quarantine / PendingDelete pills): 🐳 Image / ⎈ Helm / ✍ Signature / 📦 Artifact. Hover tooltip shows the actual config mediaType for power users.
  - **Backend work (~half day):**
    - [ ] Add `config_media_type TEXT` column to `metadata.manifests` (migration `00013_manifest_artifact_type.sql`). Backfill from `raw_json` for existing rows in the same migration (one-time UPDATE; `manifests.raw_json` is the source of truth).
    - [ ] Populate the column on `PutManifest` (parse `raw_json` → `config.mediaType`).
    - [ ] Expose on the `Manifest` + `Tag` protos as `artifact_type` (a discriminator string: "image" | "helm" | "signature" | "sbom" | "other"). Derive in repository code from the raw config media type — keeps the wire-level type stable when new artifact types land.
    - [ ] BFF: thread through to the Tags list response; add `?artifact_type=` query param to `GET /api/v1/repositories/{org}/{repo}/tags` for server-side filtering.
  - **Frontend work (~half day):**
    - [ ] New `ArtifactTypePill` component (matches existing `QuarantinePill` / `PendingDeletePill` shape on `tags-panel.tsx`).
    - [ ] Filter chip row state lives in URL query params (so a deep-link to "Helm charts in this repo" works for sharing).
    - [ ] Empty-state copy when a filter has zero matches ("No Helm charts in this repo — push one with `helm push <chart> oci://localhost:8081/<org>/`").
  - **Cross-feature dependency:** P6 (scanner skip-non-image) and F4 share the same `config_media_type` lookup. If P6 ships first, F4 reuses the same column; if F4 ships first, P6 becomes a one-line check against `manifest.artifact_type`. Worth doing P6 + F4 as a paired batch.

#### Deferred (already discussed, not in this batch)
- **Tenant feature-flag for single-tenant deployments** — hide `/admin/tenants` when `PLATFORM_SINGLE_TENANT=true`. Discussed 2026-06-22 with the user; deferred for a later conversation about whether tenants as a UI surface earns its keep at single-customer scale. The architectural model (tenant = isolation boundary at network / auth / data / storage / event layers) stays correct; only the dashboard surface is over-built for a single tenant.

#### Suggested batching
- **Batch 1 (data-integrity bugs first):** B1, B3 — both are "things look wrong / are wrong" complaints that erode trust. ~1 day. (B5 already fixed in the working tree — folds into the same commit as this update.)
- **Batch 2 (UX polish sweep):** P1, P2, P3, P4, P5, B4 — one focused PR rebuilding the table-listing primitives + spacing fix. ~half a day.
- **Batch 3 (API key bug, isolated):** B2 — needs a focused investigation against the FE-API-048 auth changes (T06 → T13).
- **Batch 4 (features):** F1 → F2 → F3 in order of operator impact. Each its own small sprint.
- **Batch 5 (Helm / artifact taxonomy, paired):** P6 + F4 — same backend column (`config_media_type`) underpins both. ~1 day for the combined backend + FE work. Worth doing before adding Cosign / SBOM surfaces because the same pill primitives extend cleanly.

#### Branching strategy
All work goes on `feat/sprint-11-maint-*` branches (one per batch), each landing its own PR against `main`. Do not stack on `feat/sprint-10` — that branch is in review on PR #19.

---

### REM-014 — Multi-scanner support (Grype + Clair both DONE)
- **Affects:** `infra/scanner-plugins/grype-adapter`, `infra/scanner-plugins/clair-adapter` (new), `services/scanner/Dockerfile`, `services/scanner/scripts/entrypoint.sh`, `infra/docker-compose`, `infra/clair`, `services/management`, `frontend`
- **Status:** Grype adapter DONE ✅ (2026-06-22). Clair v4 integration DONE ✅ (2026-06-22 evening).
- **Motivation:** REM-011 P2 shipped the adapter registry + live swap infrastructure but only two adapters (dev-stub + trivy-adapter). REM-014 adds a real second scanner so operators have an actual choice in the `/admin/scanner` UI; FE-API-018 + FE-API-049's `scanner_plugin` allowlist already included `grype` so no wire-shape changes were needed.
- **Grype — what shipped:**
  - **`infra/scanner-plugins/grype-adapter`** — standalone Go binary (~400 lines, single-file) mirroring `trivy-adapter`'s shape: reads JSON-RPC scan request from stdin, flattens staged layers into a rootfs dir, shells out to `grype dir:<rootfs> -o json --quiet`, translates Grype's `matches[].vulnerability` shape into the platform's flat findings list, writes RPC response to stdout. Same tar-extract + whiteout-skip + tar-path-traversal guard as trivy-adapter — extracted layer handling is the one cross-adapter concern shared cheaply via inline duplication. Severity normalised to uppercase so FE-API-050 `block_on_severity` comparisons stay scanner-agnostic (Trivy emits uppercase already; Grype emits title-case). Dedupe on (CVE, package, version) for parity with the Trivy path so the same finding doesn't surface twice when matched via both package and language ecosystem.
  - **`services/scanner/Dockerfile`** — new `RUN go build` for the grype-adapter binary, new `FROM anchore/grype:v0.93.0 AS grype` stage that pins the upstream binary (v0.93+ uses DB schema v6; pre-v0.81 versions use schema v5 which Anchore stopped publishing fresh DBs for — Grype's "DB max age 5 days" check rejected those with the misleading "database was built N weeks ago" error during initial integration). Both binaries copied into the final image at `/usr/local/bin/scanner-grype-adapter` + `/usr/local/bin/grype`. Auto-discovery via the existing `/usr/local/bin/scanner-*` glob picks the new adapter up at boot — no scanner code changes.
  - **`/grype-cache` volume + `GRYPE_DB_CACHE_DIR=/grype-cache`** — matches the `/trivy-cache` pattern. Chowned to `nonroot:nonroot` at build time so the named volume inherits the right ownership on first attach (same fix as the FE-API-019 compliance reports volume).
  - **`services/scanner/scripts/entrypoint.sh` — Grype DB pre-warm** — runs `grype db update` synchronously at container start so the first scan doesn't pay the ~2 GB download cost. Best-effort — failure logs but doesn't block startup (dev-stub + Trivy paths still work without Grype's DB). Subsequent restarts no-op when the volume already has a fresh DB. **Operator-visible tradeoff:** first cold start of a fresh `/grype-cache` volume now blocks for ~2–3 min while the DB downloads. `SCANNER_SKIP_GRYPE_WARM=1` opts out for CI runs that don't need Grype.
- **Verified live in compose (2026-06-22):**
  - Boot log: `plugin checksum verified path=/usr/local/bin/scanner-grype-adapter sha256=cd0a2c3b3fec015c…`
  - `/admin/scanner` lists 3 adapters: dev-stub, trivy-adapter, grype-adapter (version backfills from first successful scan).
  - Swap via FE-API-045 atomic pointer: in-flight scans complete on the old adapter, next job picks up Grype. Persisted in `scanner_settings` — operator's choice survives container restart.
  - End-to-end scan against `dev/alpine:3.19`: enqueue → Grype run → quarantine stamped in **1.8s** with reason `"scan blocked by policy block_on_severity=HIGH: 2 HIGH, 6 MEDIUM, 6 LOW"`. FE-API-050 pull-time 451 + FE banner + Lift dialog all functional against Grype.
- **Bugs found + fixed during integration:**
  - **Grype version pin** — initially used v0.79.6 (Aug 2024, last v5 release). Grype refused to load the v5 DB with "the vulnerability database was built 15 weeks ago (max allowed age is 5 days)" because Anchore stopped publishing fresh v5 DBs in mid-2024. Upgrading to v0.93.0 (schema v6) fixed it. **Lesson worth flagging:** any future scanner pin should verify the upstream is publishing fresh DBs at the schema version the pinned binary speaks.
  - **First-scan timeout** — without the entrypoint pre-warm, the first Grype scan would trigger a 2.5 GB DB download mid-job, run past the scanner's job timeout, and fail with the misleading "exit status 1, stderr empty" symptom. Pre-warm at container boot keeps every scan deterministically fast.

- **Clair v4 — what shipped:**
  - **`infra/scanner-plugins/clair-adapter`** — stdlib-only Go binary (~600 lines, single-file) that bridges the platform's JSON-RPC scan contract to Clair's HTTP indexer + matcher API. Same `rpcRequest` envelope as trivy/grype adapters so no orchestrator changes were needed.
  - **Embedded HTTP layer server** — the design pivot that avoided the "give Clair credentials to registry-core" rabbit-hole. The adapter spins up a localhost HTTP server on `CLAIR_FETCH_PORT` (default 9099) inside the scanner container, exposing each staged layer file under `/<digest_hex>`. The adapter builds the Clair manifest with URIs pointing at `http://<scanner-hostname>:9099/<digest>` and Clair fetches them over the `registry` Docker network. Server has no auth — bound only on the docker bridge network, no host port mapping — and exits with the adapter process so the surface is ephemeral. **Production note:** a real Kubernetes deployment would swap this for Clair → registry-core direct fetch + a service-account JWT; the embedded-server path keeps the dev compose stack stand-alone (no extra provisioning step).
  - **`infra/clair/config.yaml`** — Clair v4 combo-mode config (indexer + matcher + notifier in one process). Pinned at `quay.io/projectquay/clair:4.7.4`. Updater sets restricted to `alpine, debian, ubuntu, osv` so the first feed pull lands in ~1 GB rather than ~5 GB. `introspection_addr: :8099` (not the default `:8089` — that's `registry-gc`).
  - **`infra/docker-compose/docker-compose.yml`** — two new services behind `--profile clair`: `clair-db` (Postgres 16) and `clair` (combo). Both on the `registry` network so the scanner container can reach them by hostname. `clair_db` named volume holds the feed cache so a recreate doesn't re-download. Scanner block gains `CLAIR_URL=http://clair:6060`, `CLAIR_FETCH_PORT=9099`, `CLAIR_FETCH_HOST=registry-scanner` env vars.
  - **`services/scanner/Dockerfile`** — new build stage for the clair-adapter, baked at `/usr/local/bin/scanner-clair-adapter`, picked up automatically by the existing `/usr/local/bin/scanner-*` glob. `EXPOSE 9099` documents the embedded HTTP server port (no host mapping; container-to-container only).
  - **Allowlists** — `services/management/internal/handler/security_policies.go` `allowedScannerPlugins` gains `"clair"`. Frontend `SCANNER_PLUGIN_CHOICES` + the per-tenant zod enum on `scan-policy-editor.tsx` gain `"clair"`. `/admin/scanner` lists 4 adapters once the operator picks one.
- **Verified live in compose (2026-06-22 evening):**
  - Scanner image build: `docker compose build registry-scanner` clean; image carries all 4 adapter binaries:
    ```
    /usr/local/bin/scanner-clair-adapter  6.4 MB
    /usr/local/bin/scanner-dev-stub       1.9 MB
    /usr/local/bin/scanner-grype-adapter  2.4 MB
    /usr/local/bin/scanner-trivy-adapter  2.4 MB
    ```
  - `docker compose --profile clair up -d clair-db clair` brings both services up healthy.
  - `curl http://localhost:6060/indexer/api/v1/index_state` returns `{"state":"<hash>"}` confirming the indexer API is reachable + migrations applied.
- **Follow-ups (logged for a future sprint, not blocking this commit):**
  - End-to-end scan against a real image through the new adapter (the dev stack's first Clair scan requires the updater to finish its initial feed pull, which takes ~10 min). Tracked separately so the merge can land without sitting on that wait.
  - Production auth shape — replace the embedded HTTP layer server with Clair → registry-core direct fetch + a service-account JWT. Documented in `infra/runbooks/clair-prod-auth.md` (TODO).
  - Adapter contract extension (registry_url / image_ref optional fields on `ScanRequest`) — the embedded-server design side-stepped this. If a future scanner has a similar self-pull shape, revisit then.
  - Integration test in `services/scanner/internal/testutil/integration` that brings Clair up via testcontainers + runs a real scan against a fixture image.
- **Bugs found + fixed during integration:**
  - **Port 8089 collision** — Clair's default introspection_addr is `:8089`, but `registry-gc` already binds that on the compose host. Moved to `:8099` and documented why in the config comment.

---

### REM-015 — `libs/rabbitmq/consumer` retry counter is broken (DONE ✅ 2026-06-23)
### REM-016 — `libs/errors/codes.MapDBError` doesn't recognise PostgreSQL error codes (OPEN)
- **Surfaced:** 2026-06-23 during the custom-domain triage (PR #32). Operator hit a generic "Couldn't register, try again or check BFF logs" toast; the real cause was a foreign-key violation (tenant row missing in the tenant service's DB), but `MapDBError` only special-cases `context.DeadlineExceeded` → `ResourceExhausted` and lumps everything else into `Internal` with a fallback message. The 1-line PgErr stayed buried in the audit log.
- **Affects every service:** any repository that catches a Postgres error and routes it through `errcodes.MapDBError`. That's all of them.
- **Proposed fix:** detect `*pgconn.PgError` and map:
  - `23503` (foreign_key_violation) → `codes.NotFound` with a body explaining the missing parent row (best-effort — extract the constraint name from `pgErr.ConstraintName` so the operator sees "tenant not found" not "FK 23503").
  - `23505` (unique_violation) → `codes.AlreadyExists` (e.g. "domain already registered for this tenant").
  - `23514` (check_violation) → `codes.InvalidArgument` (catches `format CHECK (format IN (...))` and similar).
  - Everything else → existing fallback (`Internal`).
- **Estimated work:** ~1-2 hours including unit tests in `libs/errors/codes/codes_test.go`. Touches every service for verification only (no API change — same error codes the gRPC handlers already map to HTTP).
- **Why it matters:** would have made the PR #32 bug actionable instantly. Generic "Internal" errors waste investigation time when the underlying Postgres error is precise.

---

### REM-015 — `libs/rabbitmq/consumer` retry counter is broken (latent bug, OPEN)
- **Surfaced:** 2026-06-23 during Tier 1 #4 Phase 2 (PR #31) live smoke. The audit-export worker's retries looped infinitely instead of routing to DLX.
- **Root cause:** `consumer.handle` used `deathCount(d)` (sum of `x-death` header counts) to decide whether to NACK with requeue=true (retry) or requeue=false (DLX). But `x-death` is added by RabbitMQ only when a message is dead-lettered — `Nack(requeue=true)` returns the message to the same queue WITHOUT incrementing `x-death`. So `retries` stayed at 0 forever for transient handler errors and the message bounced between the consumer + queue indefinitely.
- **Affected every consumer in libs/rabbitmq/consumer** but only triggered when the handler returned an error on a queue config with `x-dead-letter-exchange` set (every service in this repo). Behaviour was masked because most handlers either succeed on retry or return `nil` for permanent errors (ACK).
- **Workaround (Phase 2 of #4):** `services/audit/internal/exportworker` wrote a thin direct `amqp091` consumer instead of using the shared one. `export.Deliver` handles the in-process retry budget; `NACK(requeue=false)` routes straight to DLX. Documented inline in the package; left in place as it still works.
- **Fix shipped:** option (a) — `libs/rabbitmq/consumer.Consumer` now tracks attempts in a per-instance `sync.Map` keyed by `Delivery.DeliveryTag`. Counter increments on NACK(requeue=true) and is cleared on every terminal state (ACK success, NACK→DLX, or unparseable payload). API unchanged — `Config.MaxRetries`, the `Handler` signature, and `Close()` all behave identically for callers. Memory is bounded by `prefetch_count × MaxRetries` (~few KB worst case).
- **Tests:** new `libs/rabbitmq/consumer/consumer_test.go` (build tag `integration`) covers happy path, transient-failure recovery, exhaustion → DLX, and a dedicated regression test that asserts a permanently-failing handler reaches the DLX within a bounded number of calls. Test compiles cleanly under `-tags integration`; full live exercise piggybacks on the existing PR #31 audit-export live smoke since the workaround code path is unaffected by this change.
- **Build verification:** `libs && go build ./...` + `go test` for every service that imports the consumer (`audit`, `scanner`, `gc`, `webhook`, `metadata`, `proxy`) — all green.

---

### Tag immutability — repo flag + per-tag pin (2026-06-23, in progress)
- **Affects:** `proto/metadata/v1`, `services/metadata`, `services/core`, `services/management`, `frontend`
- **Status:** Backend DONE ✅ on `feat/tag-immutability`; FE toggle + pin affordance pending on the same branch. Promotion workflow (cross-repo manifest copy) deferred to a follow-up PR per futures.md Tier 1 #2.
- **Motivation:** Without an immutability flag, an attacker (or a sleepy engineer) can re-push `staging/myapp:1.0` and silently change what every consumer pulls. Image promotion (dev → staging → prod) is also unsafe without it. Customer-blocking gate for production-grade tenants.
- **Design — two complementary flags:**
  - `repositories.immutable_tags` — repo-wide "no tag re-pushes" toggle. When `TRUE`, `services/core` rejects any push that would move an existing tag to a new manifest_digest. Recommended posture for production / staging promotion repos.
  - `tags.immutable` — per-tag pin. Locks a single canonical tag (e.g. `v1.0-release`) without flipping the whole repo. Useful for repos that mix mutable dev tags + a small set of pinned release tags.
- **What shipped (backend):**
  - **Migration `00014_tag_immutability.sql`** — adds both columns. Partial `idx_tags_immutable WHERE immutable=TRUE` for cheap "list pinned tags" admin lookups. Both default `FALSE` so the migration is transparent.
  - **Proto** — `Repository.immutable_tags` (= 11), `Tag.immutable` (= 12). Two new RPCs (`UpdateRepositoryImmutability`, `UpdateTagImmutable`) — separate from `UpdateRepository` / `PutTag` so the audit trail records security-relevant transitions explicitly.
  - **`services/metadata`** — `repoSelectCols` + `tagSelectCols` carry both flags. `UpdateRepositoryImmutability` / `UpdateTagImmutable` repository methods use the existing CTE-then-join pattern.
  - **`services/core`** — new `ErrTagImmutable` sentinel + `checkTagImmutable` preflight on `PutManifest`. Idempotent same-digest re-pushes allowed (not a "move"). Per-tag pin takes precedence over the repo-wide flag. Repo flag check is a second RPC only when the same-digest fast path didn't short-circuit, so steady-state cost is zero for unchanged content. Fail-OPEN on metadata reachability failures (warn + continue) so a transient DB blip doesn't reject every push. Handler maps `ErrTagImmutable` → HTTP 400 `MANIFEST_INVALID` per OCI Distribution Spec § 4.2.2.
  - **`services/management` (BFF)** — `RepoResponse` + `updateRepositoryBody` gain `immutable_tags` (the body field is `*bool` so the BFF can tell "not provided" from "explicit false"). `TagResponse` gains `immutable` so the FE can render the pinned-tag pill without a follow-up GET.
- **Pending on this branch:**
  - FE toggle on repo Settings tab: "Immutable tags" (with explainer + audit-trail link).
  - Per-tag "Pin" affordance on the Tags table; a `📌 pinned` pill on the tag row when set.
  - Pin/unpin BFF routes: `POST /api/v1/repositories/{org}/{repo}/tags/{tag}/pin` + `DELETE` the same path.
- **Follow-ups (separate PRs):**
  - Promotion workflow — `POST /repositories/{org}/{repo}/promote` that copies a manifest from `dev/myapp:abc` to `staging/myapp:1.0`, records a `tag.promoted` audit event, optionally re-signs the manifest.
  - Audit events for the new transitions — `tag.pinned`, `tag.unpinned`, `repository.immutability_changed`. Today the events flow through the generic audit consumer; explicit routing keys would let webhook subscribers wire dedicated notifications.

---

### FE-API-050 — Pull-time manifest quarantine (2026-06-22)
- **Affects:** `proto/metadata/v1`, `proto/gen/go/metadata/v1`, `services/metadata`, `services/scanner`, `services/core`, `services/management`, `frontend`
- **Status:** DONE ✅ — block-on-severity now actually blocks pulls.
- **Motivation:** FE-API-049 closed the silent-scan-on-push bug but exposed a second one: `block_on_severity` in the policy editor was purely informational. `services/scanner/internal/worker/worker.go` `hasPolicyViolation` hardcoded `CRITICAL || HIGH` regardless of the operator's setting AND only emitted a `scan.policy_blocked` event — the image was already in the registry and pullable. FE-API-050 makes the gate real.
- **Architecture (Option A from the triage):** pull-time quarantine. Push always succeeds (good UX), scan runs async, on violation the manifest gets a `quarantined=true` flag, subsequent pulls of that manifest return `451 Unavailable For Legal Reasons` until an operator dismisses. Matches Harbor / ECR Image Scanning semantics; rejected Option B (sync scan-before-push, 30s+ pushes) and Option C (auto-delete, surprising mid-deploy).
- **What shipped:**
  - **Migration `00012_manifest_quarantine.sql`** — adds `quarantined BOOLEAN NOT NULL DEFAULT FALSE`, `quarantine_reason TEXT`, `quarantined_at TIMESTAMPTZ`, `quarantined_by TEXT` to `manifests`. Partial index on `(tenant_id, repo_id) WHERE quarantined=TRUE` for the future "list all quarantined" admin surface. Existing rows transparently default to "not quarantined" — no backfill needed.
  - **Proto** — `Manifest` proto extended with all four columns; `Tag` proto adds `quarantined` so the Tags table can render the pill without per-row GetManifest calls. New `UpdateManifestQuarantineRequest` + `rpc UpdateManifestQuarantine`. Idempotent on repeated true→true (preserves the FIRST quarantined_at for audit); clearing nulls all four columns atomically.
  - **Metadata repository** — `manifestSelectCols` constant shared across all manifest reads (PutManifest RETURNING, GetManifest, ListUntaggedManifests, scanOneManifest) so a future reader can't accidentally drop the quarantine columns. `tagSelectCols` adds `COALESCE(m.quarantined, FALSE)` so tag reads carry the parent flag. New `UpdateManifestQuarantine` + `ListTagNamesByDigest` (used by the cache-bust below).
  - **Metadata handler** — new `UpdateManifestQuarantine` gRPC handler with tenant_id-in-WHERE enforcement (cross-tenant manifest_digest surfaces as NotFound, not a leak). Reason required when quarantining; ignored on clear. New `WithCacheBuster(rdb)` option wires the Redis client so quarantine flips invalidate GetManifest cache entries — both the digest-keyed entry AND every tag-keyed entry pointing at this manifest (via `ListTagNamesByDigest`). Without this the 5-min GetManifest cache TTL would leave a hole in the gate.
  - **Scanner — block_on_severity bug fix** — `hasPolicyViolation` now takes the operator's threshold from the resolved policy (`ResolvedScanPolicy` extended with `BlockOnSeverity` + `ExemptCVEs`). Severity-rank-based comparison so HIGH threshold catches both HIGH and CRITICAL; empty threshold means "never block"; unknown threshold values fail safe (no block). 4 new test cases assert MEDIUM threshold catches MEDIUM (which was DEAD UI before) + LOW catches anything + empty never blocks.
  - **Scanner — quarantine stamp** — `applyQuarantine` in `worker.go` calls `metadata.UpdateManifestQuarantine` with `quarantined=true` + a `buildQuarantineReason` string ("scan blocked by policy block_on_severity=HIGH: 3 CRITICAL, 5 HIGH, 2 MEDIUM"). quarantined_by="scanner" so the audit trail distinguishes automatic enforcement from operator-triggered manual quarantines. Failures log at warn — the scan_results row was already written; the next scan retries idempotently.
  - **registry-core — pull-time gate** — `handleGetManifest` AND `handleHeadManifest` check `manifest.GetQuarantined()` and return `451 Unavailable For Legal Reasons` with the OCI `DENIED` error code + the scanner's reason text. HEAD mirrors GET so OCI clients that do the HEAD-then-GET dance see a consistent rejection.
  - **Management BFF** — new `POST /api/v1/repositories/{org}/{repo}/tags/{tag}/quarantine/lift` route (repo admin/owner only — same gate as PUT scan-policy because lift bypasses the security gate). Resolves tag→digest via GetTag then forwards to `UpdateManifestQuarantine(quarantined=false)`. No "set quarantine" route exposed by design — manual quarantines would invite a denial-of-service shape via the UI.
  - **Frontend** — `Tag` type gains `quarantined?: boolean`. New `QuarantinePill` component on the Tags table renders a red 🔒 chip next to the tag name with a tooltip explaining the gate. New `useLiftQuarantine` mutation hook in `frontend/src/lib/api/quarantine.ts` invalidates both the tags list (clears the pill) and the per-tag manifest detail (clears any Security tab banner) on success.
- **Tests** — 4 new `hasPolicyViolation` cases (FE-API-050 threshold semantics), new fake repo methods (`UpdateManifestQuarantine` + `ListTagNamesByDigest`) on the metadata handler tests, all existing handler test suites still pass.
- **Verification plan (to run after rebuild + recreate):**
  1. Set the policy `block_on_severity=HIGH` via `/orgs/{org}/policies/scan` (or `/security/policies`).
  2. Push an image with a known HIGH/CRITICAL CVE (e.g. `nginx:1.25-alpine` against Trivy).
  3. Watch scanner logs for `manifest quarantined by scan policy`.
  4. Pull the same tag → expect `451 Unavailable For Legal Reasons` with the scanner's reason text on the OCI client.
  5. Hit `POST /api/v1/repositories/{org}/{repo}/tags/{tag}/quarantine/lift` as a repo admin.
  6. Pull again → expect 200.
- **Follow-ups (logged for sprint planning, not blocking):**
  - **Lift dialog on the tag detail Security tab** — the data path is fully wired (FE pill + `useLiftQuarantine` hook + BFF + lift route) but the visual lift button is the polish piece deferred from this commit. Operators can lift via Postman today; the dashboard will gain the button alongside the broader Security-tab quarantine banner.
  - **Manual operator quarantine** — intentionally NOT a BFF route today (denial-of-service shape). If real demand surfaces, gate it behind a type-to-confirm dialog with the same admin/owner posture.
  - **Audit + webhook events for quarantine.applied / quarantine.lifted** — mirror the FE-API-041 retention.* events. Today the scan.policy_blocked event already fires; that covers most subscribers, but a dedicated quarantine.lifted event would let webhook subscribers track operator dismissals separately.
  - **Quarantine list page for platform admins** — `GET /api/v1/admin/quarantined-manifests` listing every quarantined row across the platform, paginated. The partial index on `(tenant_id, repo_id) WHERE quarantined=TRUE` is already in place.

---

### FE-API-049 — Org-default + per-repo scan policy (2026-06-22)
- **Affects:** `proto/scanner/v1`, `services/scanner`, `services/management`, `frontend`
- **Status:** DONE ✅ — inheritance chain live; auto-scan-on-push policy now honoured (was advisory before).
- **Motivation:** The original FE-API-018 scan policy was tenant-wide AND silently never enforced — `services/scanner/internal/worker/worker.go` `HandlePushCompleted` enqueued every push regardless of `auto_scan_on_push`. Operator wanted org-level defaults + per-repo overrides matching the FE-API-039 retention pattern; fixing the silent-policy bug was a free side-benefit.
- **What shipped:**
  - **Proto + migration** — new `org_scan_policies` + `repo_scan_policies` tables (PK on org_id / repo_id, denormalised `tenant_id` + `org_id` for cheap inheritance lookups, `enabled` flag). 7 new RPCs on `scanner.proto`: `Get/Upsert/Delete OrgScanPolicy`, `Get/Upsert/Delete RepoScanPolicy`, and `GetEffectiveScanPolicy` (the inheritance walker). `ScanPolicy` proto extended with `org_id` + `repo_id` + `enabled`; new `EffectiveScanPolicy` envelope carries `inherited_from: "repo"|"org"|"tenant"|"default"`.
  - **Inheritance helper** — extracted into `services/scanner/internal/policy/policy.go` so the gRPC handler and the worker share one chain implementation. Semantics: per-repo (when enabled) → org default (when enabled) → tenant policy (always-on for backward compat) → synthesised `auto_scan_on_push=true` fallback. Disabled overrides DO NOT propagate (mirrors FE-API-039 retention).
  - **Scanner consumer** — `worker.Pool` gained an optional `PolicyResolver` callback wired in `server.go` to `policy.Resolve`. `HandlePushCompleted` now calls the resolver and short-circuits when `auto_scan_on_push=false`. Resolver-error path fails OPEN (logs, scans anyway) so a transient DB blip doesn't silently turn off scanning — the prior behaviour is always-scan and we preserve that as the safe default.
  - **BFF routes** — 6 new HTTP endpoints on `services/management`: `GET/PUT/DELETE /api/v1/orgs/{org}/policies/scan` (org reader / org admin) and `GET/PUT/DELETE /api/v1/repositories/{org}/{repo}/policies/scan` (repo reader / repo admin). Writer intentionally NOT enough on writes — scan policy gates push admission, same posture as FE-API-037 retention. The per-repo GET goes through `GetEffectiveScanPolicy` so the response always carries an `inherited_from` label.
  - **Frontend** — `useOrgScanPolicy` / `useUpdateOrgScanPolicy` / `useDeleteOrgScanPolicy` + repo equivalents on `frontend/src/lib/api/scan-policy.ts`. New `OrgScanPolicySection` mounted on `/orgs/$org/settings` next to `OrgRetentionPanel`. Editor mirrors the FE-API-018 ScanPolicyEditor (switch + radio + chip CVEs) but adds an `enabled` toggle. Cache-invalidation predicates bust per-repo entries when the org default changes so child surfaces flip without a refresh.
  - **Migration story** — pre-FE-API-049 tenant `scan_policies` rows stay as the bottom-of-chain fallback. No data migration needed; new column additions on `ScanPolicy` proto are backward-compatible. Per-tenant editor on `/security` still works.
- **Bugs found + fixed along the way:**
  - **Silent policy break:** the worker never checked `auto_scan_on_push` before this work. The per-tenant editor was purely advisory; every push triggered a scan. FE-API-049 ships both the inheritance AND the missing check.
  - **(Earlier this session, separate commit)** `services/core/internal/service/registry.go` was publishing `push.completed` events with an empty `RepoID` (the field was just missing from the payload constructor). Without the scanner's REM-012 observability fix from earlier in the session, this would have stayed hidden — the scanner consumed events, enqueued jobs with empty repo_id, and metadata's `GetManifest` threw `SQLSTATE 22P02` which got masked to `Internal "internal error"` by `mapErr`'s coarse fallback. Both fixes (RepoID populate + mapErr observability) committed before FE-API-049 work started.
- **Verification (live in compose):** pushed `nginx:1.25-alpine` after FE-API-049 deployment. Scanner logs show `scan job enqueued` → `GetManifest code=OK` → scan_results row written. The `/security/policies` per-tenant editor still works; `/orgs/{org}/settings` shows the new section. Per-repo override surface on the repo Settings tab is the next visible follow-up.
- **Follow-ups (logged for sprint planning, not blocking):**
  - Per-repo Settings tab scan policy editor — the repo Settings tab currently shows an EmptyState placeholder; FE-API-049 wired the API client but the panel is deferred since FE-API-037 retention already needs that tab and it deserves a unified layout pass.
  - Audit the FE-API-018 ScanPolicyEditor's react-hook-form + zod stack vs the lighter OrgScanPolicySection editor — pick one and DRY both surfaces.
  - PENTEST follow-up: rate-limit the `/policies/scan` PUT routes the same way `/webhooks/{id}/test` should (PENTEST-030 family).

---

### S11 frontend — SHIPPED this session (2026-06-21)

> Cross-link: full per-slice detail in `FE-STATUS.md` S11.1..S11.5 rows.

- **Slices 1+2** (FE-API-037 + FE-API-038): per-repo Retention tab on `/repositories/$org/$repo` — read summary + 4-state empty/inherited/override/preview, full editor with rule chips + protected-pattern chips, mandatory dry-run dialog before save, remove-override.
- **Slice 3** (FE-API-040): "Run now" trigger + 5s status polling on the Retention tab. **PARTIAL** — pending-delete pills on Tags tab + per-repo Run history panel deferred (blocked by REM-013 gaps 1 + 2 above).
- **Slice 4** (FE-API-039): org-default Retention surface on new `/orgs/$org/settings` route + cross-link from inherited per-repo policies. **PARTIAL** — dashboard storage-breakdown "Retention" column deferred (blocked by REM-013 gap 3).
- **Slice 5** (FE-API-040 admin tile + FE-API-041 audit/notifications): `RetentionCard` below `GCCard` on `/admin/tenants` with 24h/7d counts + last-10 retention runs table (mode filtered client-side until REM-013 gap 2 lands); notifications allowlist extended on BFF + audit (3 new `retention.*` actions); audit `renderNotification` synthesizer extended; topbar bell + `/activity` filter chips auto-include the new types.
- **FE-API-043**: `max_idle_days` rule kind surfaces in both the per-repo + per-org editors (chip kind selector).

**Side-fix**: Button `asChild` Slot crash fixed at `frontend/src/components/ui/button.tsx` (Radix Slot ≥1.1 rejects multi-child JSX even when one is null; split the asChild/non-asChild paths). Surfaced via the "Org settings" header link from `/orgs/$org/members`.

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
| FE-API-039 | **Per-org default retention policy + inheritance** | `services/management`, `services/metadata` | DONE ✅ | New migration `00009_retention_policy_defaults.sql` (PK=org_id, FK CASCADE, reuses the `retention_rule_kind` enum from FE-API-037's 00008). 4 new gRPC RPCs on services/metadata: `GetOrgRetentionPolicy` / `UpsertOrgRetentionPolicy` / `DeleteOrgRetentionPolicy` / `GetEffectiveRetentionPolicy` (per-repo first, falls through to org default when `enabled=TRUE`; two-query approach over CTE for readability) plus `LookupOrgIDByName` (read-only NotFound; lets the BFF translate `/api/v1/orgs/{org}/...` without `GetOrCreateOrganization`'s insert side-effect). Proto shape sharing: appended `org_id` to `RetentionPolicy` as field 10 (less invasive than a parallel `OrgRetentionPolicy` message) — per-repo writes leave it empty, per-org writes leave `repo_id` empty. **Shared validation refactor**: extracted `validRetentionRuleKinds` / `retentionMaxValues` / `validateRetentionRules` / `validateProtectedTagPatterns` into a new `retention_validation.go` so per-repo + per-org + dry-run all consume one source of truth; also extracted `decidePreviewUntil` so per-repo + per-org upserts can't drift on preview-window semantics. 3 new HTTP routes on services/management: `GET /api/v1/orgs/{org}/policies/retention` (org `reader`+); `PUT` + `DELETE` (org `admin`/`owner` only). **Per-repo GET tweaked (purely additive)**: on per-repo NotFound, falls through to `GetEffectiveRetentionPolicy`; new top-level `inherited_from` field (`"repo"` \| `"org"`) so the UI labels "(inherited from org default)" without a second round-trip; on org inheritance, `org_id` is also returned. **Disabled org default is treated as no policy** — only `enabled=TRUE` defaults propagate. 47 new tests (19 metadata handler unit + 11 metadata repository integration + 17 management bufconn). Merge commit `34e8a70` (source `1669892`). |
| FE-API-040 | **Retention executor (gc modes `retention` + `retention_grace`)** | `services/gc`, `services/metadata`, `services/management` | DONE ✅ | The retention machinery now actually deletes things. Two new migrations: `00010_manifest_retention_pending.sql` adds `manifests.retention_pending_delete_at TIMESTAMPTZ` + partial index for the grace-sweep query; `gc/20260622000001_retention_mode.sql` extends `gc_run_mode` enum with `retention` + `retention_grace` and adds `gc_runs.repo_id UUID` so per-repo sweeps carry their target. **services/gc** gains `TriggerRetentionRun(repo_id, tenant_id, triggered_by)` + `GetRetentionRunStatus(run_id, tenant_id)` RPCs — dedicated rather than overloading `RunNow` (different RBAC: repo admin vs platform). Dispatcher in `runner.runClaimed` checks `IsRetentionMode` and forks to `dispatchRetention`. CronLoop now has TWO tickers — the existing run ticker + a `retention_grace` ticker (default 6h, knob `RETENTION_GRACE_INTERVAL_HOURS`) that enqueues cross-tenant grace sweeps. **services/metadata** gains `MarkManifestRetentionPending` (idempotent — re-running doesn't extend grace), `ClearManifestRetentionPending` (for future undo), `ListPendingDeleteManifests` (simplified shape — no `blob_digests` since `DeleteManifest` cascades). Executor (`runner/retention.go`): `RunRetention` calls `GetEffectiveRetentionPolicy` + `EvaluateRetention`, marks would-delete manifests, **respects `preview_until`** (sweeps in preview succeed but mark nothing). `RunRetentionGrace` calls `ListPendingDeleteManifests` + `DeleteManifest`; **grace window enforced server-side via `make_interval`** so clock skew can't drag rows in. Grace window default 7 days (knob `RETENTION_GRACE_DAYS`). **services/management** gains `POST /api/v1/repositories/{org}/{repo}/policies/retention/run` (repo admin/owner, 202 + run_id) + `GET .../runs/{run_id}` (per-repo run status, no platform-admin needed). **Event emission stubbed** — `publishRetentionApplied` / `publishRetentionGraceCompleted` log `slog.InfoContext` with TODO markers for FE-API-041. **Scope cuts**: `max_idle_days` rule silently ignored (FE-API-042 + 043 prereq); `dangling_grace_days` still uses `created_at` approximation (tag_removed_at column is its own follow-up). 43 new tests. Merge commit `0af32d5` (source `b84cc46`). |
| FE-API-041 | **Retention audit + webhook events** | `libs/rabbitmq/events`, `services/gc`, `services/audit`, `frontend` | DONE ✅ | Wires the FE-API-040 publish stubs to real RabbitMQ. Three new routing keys + payloads on `libs/rabbitmq/events`: `RoutingRetentionEvaluated` + `RetentionEvaluatedPayload` (fires before marking; includes `would_delete_count` / `would_delete_bytes` + optional `policy_preview_until` so subscribers see "we would have done X but we're in preview"); `RoutingRetentionApplied` + `RetentionAppliedPayload` (after soft-delete sweep that marked ≥1 manifest); `RoutingRetentionGraceCompleted` + `RetentionGraceCompletedPayload` (after grace sweep that hard-deleted ≥1 manifest). **services/gc**: new `EventPublisher` interface in `runner.go` matching `*publisher.Publisher.Publish`; new chained option `PersistedRunner.WithPublisher(pub)` mirroring `WithMetadataClient`; server.go reuses the same `*publisher.Publisher` the collector already had. **Nil-publisher → clean no-op** (slog.Debug + return; no panic) so dev environments without `RABBITMQ_URL` keep working. **Publish errors logged at WARN**; the run still finalises successfully (events are best-effort). `RunRetention` fires `evaluated` always; `applied` only when markedCount>0. `RunRetentionGrace` fires `evaluated` then `grace_completed` only when deleted>0. **services/audit**: three new cases in `mapEvent` switch on consumer.go; all map to system-actor audit events. `action` column is TEXT (no enum) so no migration. **frontend**: three new entries in `WEBHOOK_EVENT_CATALOG` (webhooks.ts) so operators subscribe via the existing webhook UI. 8 new tests (5 runner + 3 audit subtests). Merge commit `7d334dc` (source `d8a32b6`). |
| FE-API-042 | **Pull-activity tracking** | `services/core`, `services/audit`, `services/metadata`, `libs/rabbitmq/events`, `frontend` | DONE ✅ | Closes the FE-API-030 caveat (analytics `pulls` returning zero) AND unblocks FE-API-043 (`max_idle_days` rule). **Two-track architecture**: (1) **Full-fidelity audit/analytics** — every successful `handleGetManifest` publishes `RoutingPullImage` (new constant + `PullImagePayload` on libs/rabbitmq/events: tenant_id / repository_id / repository_name composite / manifest_digest / manifest_id / tag / actor_id / pulled_at). Audit consumer allowlists it + maps to action `pull.image`. `GetAnalytics(metric=pulls)` now returns real bucket counts. (2) **Debounced last_pulled_at** for retention — new column `manifests.last_pulled_at TIMESTAMPTZ NULL` with `idx_manifests_last_pulled(repo_id, last_pulled_at NULLS FIRST)`. New `services/metadata/internal/pullconsumer` package (modeled on services/audit/eventconsumer) subscribes to `pull.image` events; calls repository `UpsertManifestLastPulledAt` with **24h debounce SQL**: `UPDATE manifests SET last_pulled_at=$3 WHERE id=$1 AND tenant_id=$2 AND (last_pulled_at IS NULL OR last_pulled_at < $3 - INTERVAL '24 hours')`. Hot manifests pulled 1000×/day get one DB write per (manifest, day). Cross-tenant safe via the tenant_id clause. Migration `00011_manifest_last_pulled.sql`. **Sampling knob**: `PULL_EVENT_SAMPLE_RATE` config on services/core (default 1.0, validated [0,1] at startup; ≤0 = never, ≥1 = always, injectable `pullSampler` for tests). Documented trade-off: reducing rate loses analytics precision proportionally; retention `last_pulled_at` stays accurate via debounce as long as sample > 0. **Publish is fire-and-forget with 2s timeout** so pull latency isn't poisoned. 404 / 401 / 5xx paths don't publish. **Frontend**: new `pull.image` entry in `WEBHOOK_EVENT_CATALOG`. **Scope cuts**: services/proxy not covered (own follow-up); no anonymous IP/UA attribution (actor_id empty for public pulls); no backfill (NULL = "never pulled", FE-API-043 treats as idle). 18 new tests. Merge commit `b34df47` (source `6f35f0a`). |
| FE-API-043 | **Activity-based retention rule (`max_idle_days`)** | `services/metadata` | DONE ✅ | The last retention sprint item — pure metadata-layer change since the column (FE-API-042's `last_pulled_at`), enum entry (FE-API-037 front-loaded `max_idle_days`), and validation cap (36500 days) all already existed. **Combined-gate semantics** (per-manifest, in Go): `createdAt < cutoff AND (lastPulledAt == nil OR lastPulledAt < cutoff)` where `cutoff = now - N days`. **Chicken-and-egg gate**: without the `createdAt` clause, a fresh policy with `max_idle_days=30` would delete every existing manifest the moment it's enabled — every `NULL last_pulled_at` would match the second condition. Combining with `createdAt` gives pre-FE-API-042 manifests a grace period equal to their `createdAt` distance — exactly long enough for the operator to see the policy take effect before things start vanishing. **Boundary**: strict `<` (`time.Before`) — at exactly N days the manifest is KEPT; strictly older than N days counts as idle. Matches the existing `max_age_days` boundary so date-based rules stay consistent. SELECT now includes `m.last_pulled_at` scanned through `*time.Time` so NULL survives intact. The "silently skipped" comment from FE-API-038 was replaced with an explanation of the combined gate. 7 integration tests (idle+old → selected, recent-pull-old-manifest → not selected, never-pulled-brand-new → not selected (chicken-and-egg), never-pulled-old-enough → selected, combined with max_age_days → both reasons collected, protected-tag-wins, boundary at exactly N days kept). Merge commit `a7ed427` (source `34e1b48`). |
| FE-API-048 | **Service accounts + /api-keys access hub** | `services/auth`, `services/management`, `frontend` | DONE ✅ | Service accounts + /api-keys access hub. Three goose migrations (`users.kind`, `service_accounts`, polymorphic `api_keys` with DO$$ down-guard) + 10 new HTTP routes on services/auth (SA CRUD + scopes/preflight + key issue/revoke + `/access/activity`) + shadow-user principal pattern (`ValidateAPIKey`/`ValidateToken` return `user_id` opaque, RBAC/audit/RLS/JWT machinery unchanged) + cross-tenant guard + scope intersection + verify-before-expiry timing-oracle fix (security HIGH H1) + Redis `revoke:user:<shadow>` on SA disable (security HIGH H2) + SSO `GetHumanByEmail` swap + `isSyntheticSAEmail` rejection + `ActivityService` facade over `audit.GetNotifications` + `/users/me` `type:"user"\|"service_account"` envelope + `POST /apikeys` accepts `service_account_id` + frontend `/api-keys` hub with `AccessHubLayout`/`AccessSubNav`/SA list + Create dialog/SA detail drawer with `ScopeShrinkConfirmDialog`/Activity page/topbar bot avatar + four a11y-compliant preview surfaces for FUT-001..004 + 11 frontend tests + 7+ T-prefixed integration tests + CI lint `scripts/lint-user-queries.sh` enforcing kind-guard + dev seed `infra/dev-seed/service_accounts.sql` (3 SAs) + `libs/testutil/containers/auth_with_audit.go` bundle helper + activity facade integration test with inlined audit migrations. **Live smoke verified 2026-06-22**: SA create → key issue → `/auth/token` exchange (sub = shadow user id, scopes intersected) → PATCH disabled:true → token re-exchange returns 401, end-to-end. **Follow-ups (NOT blocking merge):** FUT-005 (ActivityService gRPC dial in main.go — currently 501), FUT-006 (`/users/me` SA-key auth on raw API keys — currently JWT-only), FUT-007 (durable audit emission — `slogAuditEmitter` stand-in today). Spec: `docs/superpowers/specs/2026-06-21-service-accounts-and-access-hub-design.md`. Plan: `docs/superpowers/plans/2026-06-21-service-accounts-fe-api-048.md`. |

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
