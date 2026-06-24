# Project Status — Completed Work Log

> **What this file is:** the historical record of remediation, sprint,
> and security work that has been **completed**. One row per shipped item.
>
> **What this file is NOT:** the place for currently-open work.
> Open remediation items live in [`status-tracker.md`](status-tracker.md),
> and prioritised future items live in [`futures.md`](futures.md).
>
> **Workflow:** when an item in `status-tracker.md` is finished, the
> entry is **removed** from the tracker and **appended** here.
> Resolution rationale lives in PR descriptions and git history;
> this file is just the index.

---

## Related trackers

- [`FE-STATUS.md`](FE-STATUS.md) — frontend route + sprint status (status.md is backend-only).
- [`futures.md`](futures.md) — prioritised backlog of items not yet sprinted.
- [`status-tracker.md`](status-tracker.md) — currently-open remediation + security items.
- [`security.md`](security.md) — SEC-NNN hardening audit log.

## Legend

| Status | Meaning |
|---|---|
| `DONE` | Shipped and verified |
| `DONE (Phase N)` | A specific phase shipped; later phases tracked elsewhere |
| `PARTIAL` | Core deliverable shipped; named sub-items deferred |
| `DEFERRED` | Explicit decision not to ship; rationale in linked PR/notes |
| `SUPERSEDED` | Replaced by a later item |
| `RESOLVED` | Investigated and closed without code change |

---

## Completed work

| ID | Description | Reference | Completed | Status |
|---|---|---|---|---|
| FUT-014 doc-expand | Expanded FUT-014 from "bump pull_count column" to "proxy publishes pull.image events" — collapses cache-counter undercount + dashboard 24h card missing cache traffic into one design | PR #80 | 2026-06-24 | DONE (docs) |
| FUT-015/016/017 filed | Three proxy-cache follow-ups filed in futures.md: row expander, click-through detail page, scan + sign on cached images | PR #79 | 2026-06-24 | DONE (docs) |
| proxy-mtls-server | services/proxy gRPC server now wraps mTLS server credentials when MTLS_* env vars are set; PROXY_GRPC_ADDR wired in docker-compose | PR #76 | 2026-06-24 | DONE |
| sidebar-proxy-cache-placement | Moved Pull-through cache nav item from Integrations → Operate (between Repositories and Helm charts) per operator feedback | PR #78 | 2026-06-24 | DONE |
| FUT-013 Phase C | Frontend /workspace/proxy-cache route + sidebar entry under Operate; useCacheStats null-on-403/404 probe-and-hide; type-to-confirm evict dialog; 4 new vitest cases | PR #75 | 2026-06-24 | DONE (Phase C) |
| FUT-013 Phase B | services/management BFF — `GET /api/v1/proxy/cache`, `/stats`, `DELETE /{id}`; workspace-admin gated; 404 when PROXY_GRPC_ADDR unset (FE probe-and-hide) | PR #74 | 2026-06-24 | DONE (Phase B) |
| FUT-013 Phase A | services/proxy backend for cache visibility — migration 00003 (`last_pulled_at` / `pull_count` / `size_bytes`) + 3 new RPCs (`ListCachedManifests` / `GetCacheStats` / `DeleteCachedManifest`) + async pull-bump on cache hit | PR #73 | 2026-06-24 | DONE (Phase A) |
| status-md-cleanup | Collapsed `status.md` from 851 → 208 lines into a single completed-work table | PR #72 | 2026-06-24 | DONE |
| REM-016 | `libs/errors/codes.MapDBError` now maps PG SQLSTATE 23503/23505/23514/23502 onto NotFound/AlreadyExists/InvalidArgument | PR #66 | 2026-06-24 | DONE |
| QA-002a | `Publisher.Close()` takes `p.mu` + sets `closed=true`; concurrent `Publish` returns `ErrPublisherClosed` sentinel | PR #66 | 2026-06-24 | DONE |
| QA-002b | `publisher.New(url, exchange, WithPublishTimeout(d))` — 10s default; non-positive ignored | PR #66 | 2026-06-24 | DONE |
| DSGN-021 | Custom-domain TXT row-expand with copy buttons + countdown to next backend re-check | PR #67 | 2026-06-24 | DONE |
| REM-017 | Platform-admin `/admin/orgs/{org}/claim` route closes the chicken-egg for fresh-org repo creation | PR #68 | 2026-06-24 | DONE |
| REM-019 P1 | Scanner adapter stderr-mirror + orchestrator parses RPC error from stdout on non-zero exit (diagnostics only — underlying scan failure still open) | PR #70 | 2026-06-24 | DONE (Phase 1) |
| QA-001 | Signer `tenant_id` propagation — migration 000002 + composite UNIQUE + Cosign payload `optional.tenant` binds signature to tenant | PR #64 | 2026-06-24 | DONE |
| QA-003 | Webhook `PollDueDeliveries` wrapped in tx + leases by pushing `next_attempt_at` 5 min forward; overlapping ticks can't re-dispatch | PR #62 | 2026-06-24 | DONE |
| QA-004 | JWT cache key uses `jti` not raw token in `services/core` + `services/proxy`; new `parseJTI` helper | PR #59 | 2026-06-24 | DONE |
| QA-005 | `services/scanner.Store.Sweep(maxAge)` + `StartSweeper`; terminal-status rows older than 24h dropped hourly | PR #59 | 2026-06-24 | DONE |
| QA-006 | Auth `init()` no longer reads `TRUSTED_PROXY_CIDRS` from env; `ParseTrustedProxyCIDRs` + `SetTrustedProxies` called from `server.go` | PR #59 | 2026-06-24 | DONE |
| QA-007 | Webhook SSRF dialer picks first validated resolved IP and dials by IP literal; HTTPS SNI preserved | PR #62 | 2026-06-24 | DONE |
| QA-015 | Subsumed by QA-001 (tenant_id now in Cosign payload) | PR #64 | 2026-06-24 | SUPERSEDED |
| scanner-bonus | Scanner `persistScanStatus` defaults findings to `[]byte("[]")` so failed scans flip pending→failed instead of stuck | PR #59 | 2026-06-24 | DONE |
| DSGN-001 | `isWorkspaceAdmin` helper + 3 call-site swaps + 5 vitest cases | PR #53 | 2026-06-24 | DONE |
| DSGN-005 | Dashboard first-run guidance + v2 hybrid (5-card stat row + minified Get-Started strip) | PR #56, PR #63 | 2026-06-24 | DONE |
| DSGN-006 | Repo Settings sub-sections + sticky `xl:` ToC via IntersectionObserver | PR #55 | 2026-06-24 | DONE |
| DSGN-010 | Scanner adapter sort + "Replace `<currentActiveName>` with this" button copy | PR #57 | 2026-06-24 | DONE |
| DSGN-011 | `/api-keys` preview flyout with localStorage persistence | PR #57 | 2026-06-24 | DONE |
| DSGN-015 | `CoverageCard` promoted from Overview tab to top-row; filler card dropped | PR #57 | 2026-06-24 | DONE |
| DSGN-016 | Notifications-bell footer with "See all activity" + "Failures only" deep-link + `/activity` `event_types` hydration | PR #57 | 2026-06-24 | DONE |
| DSGN-019 | Tag-detail empty-scan inline sibling-tab links; `EmptyState.description` widened to `ReactNode` | PR #57 | 2026-06-24 | DONE |
| DSGN-020 | Webhook detail Pause/Resume button | PR #57 | 2026-06-24 | DONE |
| QA-002 | `libs/rabbitmq/publisher.Publish` serialised with sync.Mutex + drainStaleConfirm; deterministic regression tests | PR #46, PR #47 | 2026-06-23 | DONE |
| HYG-002 | CODE_OF_CONDUCT.md — Contributor Covenant 2.1 | PR #44 | 2026-06-23 | DONE |
| HYG-003 | .github/SECURITY.md vulnerability disclosure policy | PR #44 | 2026-06-23 | DONE |
| HYG-004 | .github/ISSUE_TEMPLATE/ bug_report.yml + feature_request.yml + config.yml + PULL_REQUEST_TEMPLATE.md | PR #44 | 2026-06-23 | DONE |
| review-batch-tracking | 74 review findings (24 DSGN + 28 QA + 22 ARCH) curated into futures.md + .claude/reviews/ | PR on chore/track-review-findings | 2026-06-23 | DONE |
| audit-siem-phase2 | Durable DLX + drain for audit→SIEM export; new exportworker package, 5th gRPC RPC `DrainAuditExportDLX`, FE button | feat/audit-siem-streaming-phase2 | 2026-06-23 | DONE |
| audit-siem-phase1 | Per-tenant audit-log streaming to SIEM (syslog 5424 / CEF / HTTPS webhook with HMAC); SSRF guards, in-process retry | feat/audit-siem-streaming | 2026-06-23 | DONE |
| signed-image-admission-p2 | Per-repo trusted-key allowlist for signed-image admission; migration 00016 + 3 metadata RPCs + FE allowlist card | feat/signed-image-trusted-keys | 2026-06-23 | DONE |
| signed-image-admission-p1 | Signed-image admission phase 1; `Repository.require_signature` + `UpdateRepositorySignaturePolicy`; pulls 403 on zero sigs | feat/signed-image-admission | 2026-06-23 | DONE |
| REM-015 | `libs/rabbitmq/consumer` retry counter tracks attempts in per-instance sync.Map keyed by DeliveryTag (was broken via x-death) | feat/libs-rabbitmq-retry-counter | 2026-06-23 | DONE |
| tag-immutability | Backend for repo-wide `immutable_tags` flag + per-tag `tags.immutable` pin; migration 00014 + 2 RPCs + core preflight | feat/tag-immutability | 2026-06-23 | DONE (backend) |
| FUT-006 | Unified Bearer dispatch in `requireAuth` accepting JWT and `key.<id>.<secret>` API-key shapes | — | 2026-06-23 | DONE |
| FE-API-050 | Pull-time manifest quarantine; `block_on_severity` now actually blocks pulls (451 Unavailable For Legal Reasons) | — | 2026-06-22 | DONE |
| FE-API-049 | Org-default + per-repo scan policy with inheritance chain; fixes silent `auto_scan_on_push` bug | — | 2026-06-22 | DONE |
| FE-API-048 | Service accounts + /api-keys access hub; shadow-user principal pattern + 10 new auth routes + frontend hub | — | 2026-06-22 | DONE |
| REM-012 | Compliance report downloads via streaming gRPC (`DownloadComplianceReport`); shared-volume workaround retired | — | 2026-06-22 | DONE |
| REM-014-grype | Grype scanner adapter (v0.93 schema v6) + entrypoint pre-warm; live-verified end-to-end | — | 2026-06-22 | DONE |
| REM-014-clair | Clair v4 scanner adapter via embedded HTTP layer server; new compose profile | — | 2026-06-22 | DONE |
| B2 | API key creation FE↔BE contract drift fix (`id`/`key`/`prefix`/`last_used_at`); FE-API-048 was not the cause | sprint-11-maint-batch-1 | 2026-06-22 | DONE |
| B5 | PutTag column-count regression (CTE missed `quarantined` 10th column); broke all Docker + Helm pushes | — | 2026-06-22 | DONE |
| REM-011 P2 | Scanner adapter registry + live swap; FE-API-044..047 (`ListInstalledAdapters`, `SetActiveAdapter`, `RunTestScan`, `GetScannerHealth`); `scanner_settings` migration | bd4ba1d | 2026-06-21 | DONE |
| REM-011 P1 | Scanner plugin end-to-end works with dev-stub + real Trivy adapter; `UpdateScanStatusRequest` extended; zero-config startup | 8debd29 | 2026-06-21 | DONE |
| FE-API-034 | Per-tenant SSO — OAuth (PKCE S256) + SAML 2.0 SP via crewjam/saml; `client_secret` AES-256-GCM; per-tenant admin CRUD | df39d13, 4e3d939 | 2026-06-21 | DONE |
| FE-API-032 | GC status visibility — services/gc bootstrap (first gRPC server + DB + migration); `gc_runs` table; 3 admin routes | 92e6028 | 2026-06-21 | DONE |
| FE-API-027 | Custom domain CRUD — 5 routes + 4 tenant RPCs + atomic primary swap + swappable txtLookup + verification_token leak protection | 21a2f85 | 2026-06-21 | DONE |
| FE-API-028 | Tenant detail with usage breakdown for platform admin; 3 new fan-out RPCs (metadata/auth/audit) | 4a567d3 | 2026-06-21 | DONE |
| FE-API-029 | Tenant rename + plan change; slug recomputed atomically; per-field `tenant.renamed` / `tenant.plan_changed` events | 4a567d3 | 2026-06-21 | DONE |
| FE-API-030 | Pull/push analytics time-series via generic `GetAnalytics` RPC; BFF-owned range→bucket; PG14 `date_bin` | cf6e227 | 2026-06-21 | DONE |
| FE-API-033 | Per-tag SBOM/provenance download; `scan_results` SBOM columns; CycloneDX deferred; scanner write-path deferred | 78845bc | 2026-06-21 | DONE |
| FE-API-025 | Cryptographic verify on demand for signing; `?verify=true` fans out parallel VerifyManifest with `*verified` + `failure_reason` | 8560a1f | 2026-06-20 | DONE |
| FE-API-026 | Sign manifest from UI; `POST .../sign`; publishes `image.signed`; new `ImageSignedPayload` | 8560a1f | 2026-06-20 | DONE |
| FE-API-031 | Per-repo storage breakdown for tenant admin; `GetTenantStorageBreakdown` RPC; capped top-50 | add0a4c | 2026-06-20 | DONE |
| FE-API-035 | Webhook delivery payload retrieval; new `GetDelivery` gRPC; tenant + endpoint scoping | 2f7b250 | 2026-06-20 | DONE |
| FE-API-036 | Bulk tag delete; per-tag sub-transaction; cap 100 post-dedupe; writer+ on repo or parent org | add0a4c | 2026-06-20 | DONE |
| FE-API-017 | Remediation suggestions via `ListTenantRemediations`; DISTINCT ON CTE + jsonb_array_elements grouping | baae493 | 2026-06-20 | DONE |
| FE-API-018 | Scan policies CRUD; new `scan_policies` table; services/scanner DB bootstrap | f40365f | 2026-06-20 | DONE |
| FE-API-019 | Compliance reports — async job, FOR UPDATE SKIP LOCKED claim, SPDX JSON 2.3 + hand-crafted PDF | f40365f | 2026-06-20 | DONE |
| FE-API-007 | Per-tenant registry hostname surfaced via API; `Tenant` proto extended with `slug`/`host`/`host_is_custom`/`domains[]` | 140b647 | 2026-06-20 | DONE |
| FE-API-008 | Notifications poll endpoint; 8-action allowlist; client-local read state | 723ddbe | 2026-06-20 | DONE |
| FE-API-009 | `GET /api/v1/workspace/me` full shape; integrated with FE-API-007 | 140b647 | 2026-06-20 | DONE |
| FE-API-014 | Workspace-wide vulnerabilities list; CTE rollup by CVE with deduped affected[] | 2ae848b | 2026-06-20 | DONE |
| FE-API-015 | Scan history with new `trigger` column; keyset cursor; new `idx_scan_results_tenant_completed_at` | 2ae848b | 2026-06-20 | DONE |
| FE-API-037 | Per-repo retention policy CRUD; `retention_rule_kind` enum (5 kinds front-loaded); 44 new tests | 13a1595 | 2026-06-20 | DONE |
| FE-API-038 | Retention dry-run + preview window state; `EvaluateRetention` RPC serves both routes; 32 new tests | ca3efbe | 2026-06-20 | DONE |
| FE-API-039 | Per-org default retention policy + inheritance; per-repo GET falls through with `inherited_from` label; 47 new tests | 34e8a70 | 2026-06-20 | DONE |
| FE-API-040 | Retention executor (gc modes `retention` + `retention_grace`); 2 migrations + dedicated RPCs + cron ticker | 0af32d5 | 2026-06-20 | DONE |
| FE-API-041 | Retention audit + webhook events; 3 new routing keys; nil-publisher no-op | 7d334dc | 2026-06-20 | DONE |
| FE-API-042 | Pull-activity tracking; 2-track (full audit + debounced `manifests.last_pulled_at`); fills FE-API-030 caveat | b34df47 | 2026-06-20 | DONE |
| FE-API-043 | Activity-based retention rule `max_idle_days`; combined-gate semantics avoid chicken-and-egg | a7ed427 | 2026-06-20 | DONE |
| FE-API-002 | Per-tag manifest detail endpoint; parses image + index manifests; LayersPanel consumes via `useManifest` | f81046e | 2026-06-20 | DONE |
| FE-API-003 | Per-tag signing-status endpoint; SigningPanel consumes via `useSignature`; no per-request crypto verify | f81046e | 2026-06-20 | DONE |
| FE-API-016 | Severity breakdown in `/stats`; medium/low/negligible fields added (backwards compatible) | b09ba36 | 2026-06-19 | DONE |
| FE-API-020 | Per-tenant security overview snapshot; `GetSecurityOverview` 3-CTE query | b09ba36 | 2026-06-19 | DONE |
| FE-API-001 | Tag `size_bytes` on ListTags via new `manifests.image_size_bytes`; migration 00004 + parseImageSize | — | 2026-06-19 | DONE |
| FE-API-004 | Repo-scoped recent activity query; new `GetRepoActivity` RPC + keyset pagination | b09ba36 | 2026-06-19 | DONE |
| FE-API-005 | Per-repo member list; org+repo scope routes; PENTEST-002/006 gates | — | 2026-06-19 | DONE |
| FE-API-006 | Repository description / README field added to proto + DescriptionCard | — | 2026-06-19 | DONE |
| FE-API-010 | Org name on `RepoResponse`; metadata JOIN organizations; frontend `dev` fallback removed | — | 2026-06-19 | DONE |
| FE-API-011 | `GET /api/v1/users/me` current user metadata; migration adds display_name/last_login_at/email cols | 22fa246 | 2026-06-19 | DONE |
| FE-API-012 | `PATCH /api/v1/users/me` update display name + email; format-validated | 22fa246 | 2026-06-19 | DONE |
| FE-API-013 | `POST /api/v1/users/me/password` with policy + Redis rate limit + JTI revocation on change | 22fa246 | 2026-06-19 | DONE |
| FE-API-021 | Webhook endpoints HTTP routes on management; HMAC secret returned once on create | — | 2026-06-19 | DONE |
| FE-API-022 | Webhook delivery log endpoint; payload not on wire | — | 2026-06-19 | DONE |
| FE-API-023 | Test webhook dispatch; reuses worker dispatcher with SSRF guard; not recorded in deliveries | — | 2026-06-19 | DONE |
| FE-API-024 | Edit webhook + rotate secret; optional fields; rotate returns plaintext once | — | 2026-06-19 | DONE |
| ci-go-pin | CI Go version bumped 1.23 → 1.25.7 across 13 per-service workflows | — | 2026-06-19 | DONE |
| postman-collection | Postman collection covering every public `/api/v1/*` route | — | 2026-06-19 | DONE |
| frontend-rebuild-s0-s5 | Beacon frontend rebuild Sprints 0-5: dashboard, repositories, tags, security IA, members, webhooks, profile | PR #14 (`2477358`) | 2026-06-19 | DONE |
| PENTEST-027 | Webhook list + list-deliveries restricted to admin; `sanitizeURLForError` scrubs credentials from last_error | — | 2026-06-19 | DONE |
| PENTEST-028 | 00004 backfill replaced with instant ALTER + paginated post-migration runbook | — | 2026-06-19 | DONE |
| PENTEST-029 | Cap manifest `raw_json` at 4 MiB at metadata gRPC layer + element-count guard | — | 2026-06-19 | DONE |
| PENTEST-031 | Webhook gRPC `InvalidArgument` text no longer passthrough; logs server-side, returns fixed string | — | 2026-06-19 | DONE |
| PENTEST-032 | Re-validate stored webhook URL on every PATCH via `delivery.ValidateURL` | — | 2026-06-19 | DONE |
| PENTEST-015 | `useUserIsAdmin` localStorage read replaced by Beacon `useAuthStore` roles | — | 2026-06-19 | SUPERSEDED |
| jwt-roles-claim | `Roles []string` added to Claims; `Login` embeds deduped role-name list; `ValidateTokenResponse` proto field 7 | — | 2026-06-18 | DONE |
| logout-button | Sidebar logout button + server-side JTI revoke in Redis | — | 2026-06-18 | DONE |
| dev-seed-admin | Dev admin role seed migration `20260618000001_seed_dev_admin_role.sql` (grants `org=dev` + `org=*`) | — | 2026-06-18 | DONE |
| signer-vault | Signer Vault key backend using Transit `sign` endpoint; key material never leaves Vault; 4 unit tests | — | 2026-06-18 | DONE |
| signer-kms | KMS backends (AWS/GCP/Azure) — requires cloud SDK deps + live env to validate | — | 2026-06-18 | DEFERRED |
| notary-v2 | Notary v2 (TUF) signing path — Cosign covers same use case; deferred to dedicated sprint | — | 2026-06-18 | DEFERRED |
| tag-rbac | Tag-level RBAC scope — doc audit stale; CLAUDE.md only claims org/repo | — | 2026-06-18 | RESOLVED |
| helm-validate | Validate Helm charts against real cluster — operational task, not code work | — | 2026-06-18 | DEFERRED |
| disaster-recovery | Backup CronJobs + restore scripts + runbook; PutObject-only IAM, separate cloud account, Vault snapshot, RabbitMQ topology | — | 2026-06-18 | DONE |
| admin-tenants-gui | Super-admin GUI for tenant CRUD; `ListTenants` RPC + `/admin/tenants` route + platform-admin marker `(admin, org, *)` | — | 2026-06-18 | DONE |
| metrics-hist-hoist | `MetricsInterceptor` histogram lifted via sync.Once + package-level | — | 2026-06-18 | DONE |
| count-repos-rpc | `CountRepositories` RPC replaces O(n) stream drain | — | 2026-06-18 | DONE |
| scanner-mtls | Scanner gRPC client mTLS via `clientCreds()` helper | — | 2026-06-18 | DONE |
| sprint-6-core-correctness | Read endpoints no longer create repos; delete-verb reconciled; per-tenant storage quota enforced; `requireAccess` middleware extracted | df407f7, 15f0ce3 | 2026-06-18 | DONE |
| oci-conformance-75 | OCI Distribution Spec v1.1 conformance suite 75/75 PASS via cross-repo blob mount + grants fix | 15f0ce3, d400eb1 | 2026-06-18 | DONE |
| otel-bootstrap | All 11 services missing `otel.Bootstrap()` in main.go added; Jaeger SPM now flows | b7539c9 | 2026-06-17 | DONE |
| sprint-5-frontend | All 5 Stitch screens pixel-perfect; Lucide → Material Symbols; auth + token refresh; coverage 80% on auth/core | — | 2026-06-17 | DONE |
| sprint-5-management | services/management fully wired with bufconn unit tests (31 tests, 80%+ coverage) | — | 2026-06-17 | DONE |
| sprint-5-audit | services/audit `auditRepo` interface extracted; 11 unit tests for GetBuildHistory + GetDailyPullCount | — | 2026-06-17 | DONE |
| sprint-5-token-refresh | `POST /api/v1/token/refresh` validates current JWT, revokes JTI, issues fresh with same claims | — | 2026-06-17 | DONE |
| rbac-shipped | RBAC schema (roles/role_assignments, org/repo scope) + `GetUserPermissions` + admin API + audit events | — | 2026-06-17 | DONE |
| oci-conformance-shipped | Initial OCI conformance pass; referrer tracking; gRPC cold-start fix; range header off-by-one | — | 2026-06-12 | DONE |
| pull-through-e2e | Pull-through cache E2E verified; `cache/dockerhub/library/alpine:3.20` round-trips | — | 2026-06-11 | DONE |
| proxy-auth-realm | `registry-proxy` `AUTH_REALM` config wired; WWW-Authenticate now points to registry-auth | f2eb380 | 2026-06-11 | DONE |
| proxy-mtls-fix | `registry-proxy` gRPC clients applied SEC-008 `clientCreds()` pattern | — | 2026-06-11 | DONE |
| docker-pushpull-chain | 6 root causes resolved for end-to-end docker push: AUTH_REALM, 403→401, Redis JWT JSON, MinIO auto-create, dev tenant FK, org auto-create, cert SANs | cb241bd | 2026-06-10 | DONE |
| architecture-doc | ARCHITECTURE.md created with ASCII diagrams + sequence flows | — | 2026-06-10 | DONE |
| ci-security-gaps | `govulncheck` added to all 12 service CI workflows; `ci-gitleaks.yml` added | a919cd4 | 2026-06-10 | DONE |
| sec-008 | Core + proxy gRPC clients now use `mtls.ClientTLSConfig()` when cert paths set | — | 2026-06-10 | DONE |
| full-stack-up | All 16 docker-compose containers healthy (GOWORK=off, viper env seeding, sslmode=prefer, embed.FS goose, partitioned PK, distroless healthcheck) | — | 2026-06-10 | DONE |
| sec-019-020 | HTTP server timeouts (ReadHeaderTimeout 10s + Read/Write per service) | — | 2026-06-10 | DONE |
| sec-021 | Healthcheck binary timeout | — | 2026-06-10 | DONE |
| sec-022 | `sslmode=disable` rejected at startup; `sslmode=require` mandatory in prod | — | 2026-06-10 | DONE |
| sec-023 | Vault token isolation | — | 2026-06-10 | DONE |
| sec-024 | Cert key file permissions chmod 600 | — | 2026-06-10 | DONE |
| sec-007-018 | Secure response headers via `libs/middleware/http/secure_headers.go` (CSP, X-Content-Type-Options, X-Frame-Options, HSTS) | — | 2026-06-10 | DONE |
| sec-009 | Client IP trust via `TRUSTED_PROXY_CIDRS`; malformed CIDRs logged + skipped | — | 2026-06-10 | DONE |
| sec-012 | Proxy partial-blob abort | — | 2026-06-10 | DONE |
| sec-028 | Context propagation in handlers (no more `context.Background()`) | — | 2026-06-10 | DONE |
| sec-006 | `MapDBError` maps pool exhaustion → `codes.ResourceExhausted` | 0f95144 | 2026-06-10 | DONE |
| sec-015 | Signer PostgreSQL persistence; `signatures` table + write-through cache; SigB64 not stored | 0f95144 | 2026-06-10 | DONE |
| sec-025 | Dedicated `/metrics` server on :9090 across all 11 services | 0f95144 | 2026-06-10 | DONE |
| sprint-3 | Sprint 3 complete: proxy + scanner + webhook + audit + gc + tenant + signer + gateway implemented | — | 2026-06-09 | DONE |
| sprint-2 | Sprint 2 complete: metadata + storage fully implemented; Helm sub-charts; docker-compose | — | 2026-06-09 | DONE |
| sprint-1 | Sprint 1 complete: libs/ foundations, auth + core functional, Vault dev mode | — | 2026-06-09 | DONE |
| decision-1 | Drop Go plugin (.so) scanner path — external process JSON-RPC only | — | 2026-06-09 | DONE |
| decision-2 | Audit table FORCE RLS + low-privilege `registry_audit_app` role | — | 2026-06-09 | DONE |
| decision-3 | GC advisory locks via `pg_try_advisory_lock` + FNV-64a key | — | 2026-06-09 | DONE |
| decision-4 | Move Scanner interface from `libs/storage/driver` to `libs/scanner/plugin` | — | 2026-06-09 | DONE |
| decision-5 | Proxy background store routes failures through RabbitMQ, not fire-and-forget | — | 2026-06-09 | DONE |
| decision-6 | Metadata Redis cache for read-heavy gRPC (GetManifest/GetTag/GetRepository) | — | 2026-06-09 | DONE |
| decision-7 | Connection pool MaxConnIdleTime/MaxConnLifetime/ConnectTimeout; exhaustion → ResourceExhausted | — | 2026-06-09 | DONE |
| decision-8 | Custom domain verification: 24h notification + exponential backoff | — | 2026-06-09 | DONE |
| decision-9 | Monorepo with Go workspaces (`go.work`) over per-service repos | — | 2026-06-09 | DONE |
| decision-10 | K8s target Docker Desktop; Terraform deferred | — | 2026-06-09 | DONE |
| decision-11 | Default vulnerability scanner: Trivy | — | 2026-06-09 | DONE |
| decision-12 | Local KMS: HashiCorp Vault dev mode | — | 2026-06-09 | DONE |
| REM-001 | Drop Go plugin scanner path; exec.CommandContext + SHA256 checksum + io.LimitedReader 10MB + env allowlist | — | 2026-06-09 | DONE |
| REM-002 | JWT revocation TTL coupling — Redis TTL = `time.Until(claims.ExpiresAt.Time)` | — | 2026-06-09 | DONE |
| REM-003 | Proxy background store via RabbitMQ `store.queued`; DLQ after 3 retries | — | 2026-06-09 | DONE |
| REM-004 | Custom domain verification notifications + exp backoff + DB columns | — | 2026-06-09 | DONE |
| REM-005 | Audit table `FORCE ROW LEVEL SECURITY` + `registry_audit_app` role + startup checkRole | — | 2026-06-09 | DONE |
| REM-006 | Connection pool exhaustion handling via `DBConfig.PoolConfig()` + ResourceExhausted mapping | — | 2026-06-09 | DONE |
| REM-007 | Metadata Redis caching via server-side `CacheInterceptor` | — | 2026-06-09 | DONE |
| REM-008 | Metadata read-replica routing; `DB_DSN_REPLICA` config | — | 2026-06-09 | DONE |
| REM-009 | GC advisory locks via FNV-64a + `pg_try_advisory_lock` | — | 2026-06-09 | DONE |
| REM-010 | Scanner interface moved to `libs/scanner/plugin` | — | 2026-06-09 | DONE |

---

## Notes

- **Build order:** `proto/` → `libs/` → `services/auth` → `services/metadata` → `services/storage` → `services/core` → remaining services in parallel.
- **Go workspace:** `go.work` at repo root links all 14 modules; all `go.mod` files standardised to `go 1.25.7`.
- **Module path:** `github.com/steveokay/oci-janus`.
- **OCI conformance:** 75/75 PASS via `make test-conformance` in `services/core`.
- **Open work** lives in [`status-tracker.md`](status-tracker.md); future items live in [`futures.md`](futures.md).
