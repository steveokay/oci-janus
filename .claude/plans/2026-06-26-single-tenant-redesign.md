# Single-Tenant Self-Hosted Redesign — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert OCI Janus from "SaaS multi-tenant white-label" architecture to "self-hosted single-tenant by default, multi-tenant capability soft-hidden behind a flag." Resolve the spec-vs-code drift surfaced in the 2026-06-26 system review (`.claude/reviews/system-review-2026-06-26.md`).

**Architecture:** Surgical removal of SaaS-only features (custom domains, per-tenant SSO, tenant signup, plan/billing UI, tenant switcher). Soft-hide multi-tenancy in BE by keeping `tenant_id` columns + tenant gRPC service but defaulting every deployment to one auto-bootstrapped tenant. Introduce a `DEPLOYMENT_MODE=single|multi` flag that controls FE chrome and BE bootstrap. Concurrently resolve the P0 security debt from the review (mTLS production-gate everywhere, upstream digest verification, scope-aware admin gates, audit catalogue coverage, dev-seed removal). NOT a ground-up redesign — proto contracts, gRPC topology, OCI core, scanner, signer, audit, GC, proxy, webhook all unchanged.

**Tech Stack:** Go 1.23+, pgx/v5, gRPC + Protobuf (buf), goose migrations, RabbitMQ, Redis, React 18 + TypeScript + TanStack Router + TanStack Query, Traefik v3, Docker Compose / Helm.

**Source review:** `.claude/reviews/system-review-2026-06-26.md` — every Phase 6 task references a finding ID from that doc.

**Companion docs:**
- `CLAUDE.md` (will be updated in Phase 7)
- `status-tracker.md` (this plan becomes a tracker entry when work begins)
- `memory/project_architecture_review_pending.md` (resolved by Phase 0 confirmation)

**Estimated effort:** 4–6 weeks of focused engineering (1 senior dev + 1 mid). Could compress to 3 weeks with parallelism across phases 2/4/6.

---

## How to read this plan

1. **Phase 0 is a BLOCKER.** It contains removal items requiring explicit user confirmation. Do not start Phase 2 until every `RM-NNN` line has either ✅ APPROVED or 📝 MODIFIED with notes. Phase 1 and Phase 6 do not require Phase 0 confirmation — they're security/foundation work that's safe regardless of the multi-tenancy outcome.
2. **Each phase is independently shippable.** Phases are not strict sequential — phases 1/6 can run in parallel with phases 0/2/4. Phase 3 depends on phase 2. Phase 5 depends on phase 1. Phase 7 closes the loop after 2–6 are done.
3. **Every task lists `Files:`, exact steps, and a `Commit:` step.** Frequent small commits are required (per `memory/feedback_git_workflow.md` — feature branches → PR → main). Every new file gets comments per `memory/feedback_code_comments.md`.
4. **Review-finding cross-refs** appear as `[Review §A1]` or `[Top-5 #3]` so each task is traceable back to the audit evidence.

---

## Progress dashboard

> **Status legend:** ✅ DONE — shipped + merged · 🟡 IN PROGRESS — branch open · ⛔ N/A — closed without code change · ⬜ OPEN — not started.
> **As-of:** 2026-06-29 — 68 PRs shipped through #199. **Phase 5 RBAC simplification COMPLETE** — 5.1 typed `is_global_admin` (#134) + 2 hot-fix tails wiring the fast-path through every workspace + tenant-users gate (#193 / #197), 5.3 delegator-dominates (#199 — folds the code-review-agent's tenant→org/repo scope-containment fix inline + stitches `callerIsTenantAdmin` SA-deny/IsGlobalAdmin/role-lookup in the documented order after rebase), 5.4 SA-deny at admin gates (#194), 5.5 SSO subject-id binding (#195), 5.6 SAML `SSO_SAML_TRUST_EMAIL` flag (#196). Hot-fix #198 closed a 3-file build break where the #193 + #194 merge collision dropped enclosing braces from the SA-deny + IsGlobalAdmin composition. 4 of 5 Top-5 critical findings closed. Remaining work: Phase 6 hardening + Phase 7 docs/CI lint + Phase 8 rollout prep. Phase 5.5 surfaced 3 follow-up security findings (SEC-040/041/042 — see `security.md`) — Phase 5.5 shipped without them per "should-fix follow-up" cadence.

| Phase | Task | Status | PR | Date |
|---|---|---|---|---|
| 0 | Cleanup confirmation table | ✅ DONE | #119 | 2026-06-26 |
| 1.1 | `DEPLOYMENT_MODE` primitive | ✅ DONE | #120 | 2026-06-26 |
| 1.2 | `MTLS_REQUIRED` gate (`libs/config/loader`) | ✅ DONE | #121 | 2026-06-26 |
| 1.3 | Wire mTLS check into all 13 services' main.go | ✅ DONE | #125 | 2026-06-26 |
| 1.4 | `/api/v1/deployment-info` BFF endpoint | ✅ DONE | #124 | 2026-06-26 |
| 2.1 | Drop custom-domain CRUD (**closes Top-5 #3**) | ✅ DONE | #132 | 2026-06-27 |
| 2.2 | Per-tenant SSO → global config | ✅ DONE | #133 | 2026-06-28 |
| 2.3 | Tenant signup BFF removal | ✅ DONE | #154 | 2026-06-28 |
| 2.4 | Plan/billing UI strip (FE) | ✅ DONE | #154 | 2026-06-28 |
| 2.5 | Login copy + tenant chrome (FE) | ✅ DONE | #154 | 2026-06-28 |
| 2.6 | Delete dev-seed admin (**closes Top-5 #5**) | ✅ DONE | #129 | 2026-06-27 |
| 2.7 | Helm dead config cleanup | ⛔ N/A | — | 2026-06-26 |
| 3.1.a | Tenant `deployment_metadata` table + repo | ✅ DONE | #126 | 2026-06-27 |
| 3.1.b | `registry-auth bootstrap` CLI subcommand | ✅ DONE | #127 | 2026-06-27 |
| 3.1.c | `make dev-bootstrap` + production runbook | ✅ DONE | #128 | 2026-06-27 |
| 3.2 | Tenant gRPC single-tenant guard | ✅ DONE | #155 | 2026-06-28 |
| 3.3 | Tenant context middleware (single-mode injector) | ✅ DONE | #155 | 2026-06-28 |
| 3.4 prep | `tenant.GetDeploymentMetadata` RPC | ✅ DONE | #160 | 2026-06-29 |
| 3.4 pilot | services/auth injector wiring | ✅ DONE | #162 | 2026-06-29 |
| 3.4 #2 | services/metadata injector wiring | ✅ DONE | #164 | 2026-06-29 |
| 3.4 libs | `libs/tenant/bootstrap` + `mtls.ClientCreds` extraction (rule-of-three) | ✅ DONE | #167 | 2026-06-29 |
| 3.4 #3 | services/core injector wiring (1st libs consumer) | ✅ DONE | #170 | 2026-06-29 |
| 3.4 #4 | services/storage injector wiring | ✅ DONE | #171 | 2026-06-29 |
| 3.4 #5 | services/signer injector wiring | ✅ DONE | #173 | 2026-06-29 |
| 3.4 #6 | services/webhook injector wiring | ✅ DONE | #174 | 2026-06-29 |
| 3.4 #7 | services/scanner injector wiring (+ added missing interceptor chain) | ✅ DONE | #175 | 2026-06-29 |
| 3.4 #8 | services/audit injector wiring | ✅ DONE | #176 | 2026-06-29 |
| 3.4 #9 | services/gc injector wiring (reuses existing tenant conn) | ✅ DONE | #177 | 2026-06-29 |
| 3.4 #10 | services/proxy injector wiring (+ added missing interceptor chain) | ✅ DONE | #178 | 2026-06-29 |
| 3.4 #11 | services/tenant injector wiring (self-read from local repo — closes rollout) | ✅ DONE | #179 | 2026-06-29 |
| 4.1 | `useDeploymentInfo()` FE hook + Provider | ✅ DONE | #138 | 2026-06-27 |
| 4.2.a | Sidebar IA restructure (operator mental model) | ✅ DONE | #141 | 2026-06-27 |
| 4.2.b | Settings parent route + Account tab (profile, password, notification prefs, my API keys) | ✅ DONE | #143 | 2026-06-27 |
| 4.2.c | Settings › Workspace tab content (Members, Orgs, SSO read-only, **Retention defaults**, Scan policies, Workspace webhooks) | ✅ DONE | #144 | 2026-06-27 |
| 4.2.d | Settings › Platform tab + `/admin/*` migration + 301 redirects (Tenants, **Scanner adapters**, **GC schedule + run history**, Deployment info) | ✅ DONE | #145 | 2026-06-27 |
| 4.2.e | Security page split (Overview, Vulnerabilities, Scans, Signing, Policies, Reports) | ✅ DONE | #146 | 2026-06-27 |
| 4.3 | First-run onboarding wizard | ✅ DONE | #148, #149 | 2026-06-27 |
| 4.4 | `/me/abilities` BFF + `useAbility()` hook | ✅ DONE | #139 | 2026-06-27 |
| 4.5 | Strip placeholder "Coming Soon" surfaces | ✅ DONE | #151 | 2026-06-28 |
| 4.6 | Mobile-responsive shell | ✅ DONE | #152 | 2026-06-28 |
| 4.7 | Remove SSO admin FE (companion to 2.2) | ⛔ N/A | — | 2026-06-27 |
| 5.1 | Typed `users.is_global_admin` primitive | ✅ DONE | #134 | 2026-06-28 |
| 5.2 | Scope-aware tenant-admin gates (**closes Top-5 #2**) | ✅ DONE | #131 | 2026-06-27 |
| 5.1 tail | Global-admin fast-path on workspace gates (hot-fix) | ✅ DONE | #193 | 2026-06-29 |
| 5.1 tail #2 | Tenant-users gate + FE JWT helpers + tenant_users gate sweep | ✅ DONE | #197 | 2026-06-29 |
| 5.1 tail #3 | Close SA-deny braces in 3 admin gates (#193 + #194 merge collision) | ✅ DONE | #198 | 2026-06-29 |
| 5.3 | Delegator-dominates-delegatee in `GrantRole` + SA scope subset (incl. tenant→org/repo containment) | ✅ DONE | #199 | 2026-06-29 |
| 5.4 | API-key role gates deny SA principals up front (Decision #24) | ✅ DONE | #194 | 2026-06-29 |
| 5.5 | SSO subject-id binding (`users.sso_subject` + `EnsureSSOUser` match-by-subject) | ✅ DONE | #195 | 2026-06-29 |
| 5.6 | SAML `EmailVerified` hard-code → `SSO_SAML_TRUST_EMAIL` flag | ✅ DONE | #196 | 2026-06-29 |
| 6.1 | Pull-through proxy digest verify (**closes Top-5 #4**) | ✅ DONE | #123 | 2026-06-26 |
| 6.2 | Custom-domain takeover guard | ⛔ N/A | — | (replaced by 2.1) |
| 6.3 | Audit catalogue completeness + lint test | ✅ DONE | #130 | 2026-06-27 |
| 6.4 | AES-GCM KEK version prefix | ⬜ OPEN | — | — |
| 6.5 | JWKS rotation prep (multi-key support) | ⬜ OPEN | — | — |
| 6.6 | Redis fail-closed in `revoke:user:` check | ✅ DONE | #122 | 2026-06-26 |
| 6.7 | API-key Argon2 verify cache | ⬜ OPEN | — | — |
| 6.8 | SAML library upgrade to v0.5.x | ⬜ OPEN | — | — |
| 6.9 | mTLS hot reload via `GetCertificate` + fsnotify | ⬜ OPEN | — | — |
| 6.10 | mTLS peer-CN interceptor | ⬜ OPEN | — | — |
| 6.11 | Scanner plugin sandbox | ⬜ OPEN | — | — |
| 6.12 | Audit hash-chain + checkpoint signing | ⬜ OPEN | — | — |
| 7 | Documentation + CI lint (CLAUDE.md, docs/SERVICES.md) | ⬜ OPEN | — | — |
| 8 | Migration / rollout / release prep | ⬜ OPEN | — | — |

**Top-5 critical findings status (4 of 5 closed):**

| # | Finding | Status |
|---|---|---|
| 1 | RLS missing on 8 of 9 DBs | ⏸️ DEFERRED — Phase 0 D4 decision |
| 2 | `require*Admin` accepts any-org-admin | ✅ closed by Phase 5.2 (PR #131) |
| 3 | Custom-domain takeover via `ON CONFLICT` | ✅ closed by Phase 2.1 (PR #132) — feature removed |
| 4 | Pull-through proxy missing digest verify | ✅ closed by Phase 6.1 (PR #123) |
| 5 | Dev-seed admin shipped in prod image | ✅ closed by Phase 2.6 (PR #129) |

**Companion trackers:** `status-tracker.md` (REDESIGN-001 entry), `status.md` (per-PR resolution rows), `futures.md` (RED-FU-001..005 follow-ups), `memory/current_sprint_status.md`.

---

## Phase 0 — Cleanup confirmation (BLOCKER)

> **STOP. Read this section. Mark each `RM-NNN` row APPROVED / MODIFY / KEEP before proceeding to Phase 2.**

Each removal item lists what's deleted, what replaces it (if anything), the LOC + file scope, and the cost of regret (how hard is it to put back if you change your mind?). Cost-of-regret is the most important column — items that are easy to put back can be approved liberally; items that are hard to put back deserve a longer conversation.

### Drop entirely (SaaS-only — no replacement)

| ID | What | Where | Why drop | Replacement | Regret cost | Confirm |
|---|---|---|---|---|---|---|
| **RM-001** | Custom-domain CRUD + DNS TXT verification + per-domain ACME | `services/tenant/internal/handler/grpc.go` (RegisterDomain, MarkDomainVerified, SetPrimaryDomain, ListDomains, DeleteDomain RPCs), `services/tenant/internal/repository/repository.go:228-557` (domain rows), `services/management/internal/handler/workspace_domains.go`, `infra/helm/registry/charts/gateway/templates/ingressroutes.yaml` (the custom-domain routing that was never wired) | Pure SaaS feature — self-hosters serve one hostname. Source of `[Top-5 #3]` takeover bug. Routing was never implemented in Traefik. ~600 LOC removed. | None. Hostname is configured via `PLATFORM_HOST` env at deploy time. | **LOW** — proto RPCs can be re-added; schema preserved if RM-002 is rejected. | `[ ]` |
| **RM-002** | `tenant_domains` table + `is_primary` partial unique index + `tenant_domains.verified` workflow | `services/tenant/migrations/20260620000002_*.sql` + earlier domain migrations, `services/tenant/internal/repository/repository.go` domain CRUD | Companion to RM-001. Schema dead weight if domain RPCs are gone. | None. | **MEDIUM** — requires a down-migration to restore. If you'd ever consider re-enabling per-tenant domains, keep the schema and only drop RPCs (RM-001 alone). | `[ ]` |
| **RM-003** | Per-tenant SSO provider CRUD + `auth_providers` per-tenant rows | `services/auth/internal/handler/sso_admin.go` (4 RPCs), `services/auth/migrations/20260621000001_*_auth_providers.sql` (the per-tenant config), `proto/auth/v1/auth.proto` admin SSO RPCs, `services/management/internal/handler/admin_sso.go` (BFF) | Self-hosters have ONE IdP — there's no business need for per-tenant SSO config. Source of `[Review §A1]` admin-gate flaw (`sso_admin.go:140`). | Single global SSO config in `/admin/sso` — env var or a single `global_sso_config` row keyed by deployment, not tenant. | **MEDIUM** — re-adding per-tenant SSO if you go SaaS-mode means re-deriving the `auth_providers` table. Keep proto definitions in a `// deprecated:` block to make resurrection straightforward. | `[ ]` |
| **RM-004** | `auth_login_sessions.tenant_id` column (used to scope OAuth state per tenant) | `services/auth/migrations/...auth_login_sessions.sql`, `services/auth/internal/service/sso.go:387-403` | Sessions are now per-deployment, not per-tenant. | Single tenant_id default (the bootstrap tenant). | **LOW** — column made nullable + filled with deployment tenant id. | `[ ]` |
| **RM-005** | Tenant signup / public tenant-create RPCs | `services/tenant/internal/handler/grpc.go` CreateTenant if exposed publicly, `services/management/internal/handler/admin_tenants.go` HandleCreateTenant | Self-hosters have one tenant created by bootstrap CLI. SaaS signup never existed in FE anyway. | Bootstrap CLI: `registry-auth bootstrap --admin-email --tenant-name` (one-shot). | **LOW** — the RPC exists for tests; keep it gRPC-side, remove the BFF route. | `[ ]` |
| **RM-006** | Plan / billing UI affordances | FE: plan badge in `frontend/src/components/shell/sidebar.tsx:166`, plan column in `frontend/src/routes/_authenticated.admin.tenants.tsx`, plan-related fields surfaced via `useMe()` | No billing, no plans in OSS. | None — the schema column stays as nullable for forward compatibility. | **VERY LOW** — purely cosmetic. | `[ ]` |
| **RM-007** | Tenant signup flow placeholders + "Trouble signing in? Ask your platform administrator." login copy | `frontend/src/routes/login.tsx:60,194` | Hostile to a fresh self-hoster who IS the admin. | First-run wizard (Phase 4). | **VERY LOW** | `[ ]` |
| **RM-008** | Dev-seed admin migration | `services/auth/migrations/20260618000001_seed_dev_admin.sql` + `..._002`, embedded by `services/auth/migrations/migrations.go` | `[Top-5 #5]` — known admin password hash + platform-admin marker shipped in prod image. CRITICAL. | Bootstrap CLI (RM-005 replacement). | **VERY LOW** — file deletion. | `[ ]` |
| **RM-009** | Custom-domain ACME / Let's Encrypt per-domain config | `infra/helm/registry/charts/gateway/values.yaml` (per-domain certResolver entries), `infra/helm/registry/charts/gateway/templates/middlewares.yaml` | Companion to RM-001/002. | Single wildcard or single-hostname cert managed externally. | **LOW** | `[ ]` |

### Soft-hide (keep code/schema, hide in default UI)

| ID | What | Where | Why hide vs drop | Replacement / behavior | Confirm |
|---|---|---|---|---|---|
| **HD-001** | Tenant UUID chip in topbar user dropdown | `frontend/src/components/shell/topbar.tsx:125` | Useless to self-hosters; potentially useful to a SaaS operator. | Render only when `deployment_mode === "multi"`. | `[ ]` |
| **HD-002** | `/admin/tenants` route + page | `frontend/src/routes/_authenticated.admin.tenants.tsx` | Multi-tenant management still useful in MULTI mode. | Folded into Settings tabs per Task 4.2: Single mode → Settings › Workspace › Deployment info (read-only single-tenant card). Multi mode → Settings › Platform › Tenants (full CRUD). The `/admin/tenants` URL gets a 301 redirect to the new location. | `[ ]` |
| **HD-003** | Tenant switcher capability (was never built) | N/A — never existed in FE | Don't build it for single mode. | Only build if `deployment_mode === "multi"` is requested. Out of scope for this plan. | `[ ]` |
| **HD-004** | `tenant_id` columns on tables | Every table | Removing 9 schemas' worth of FKs is a massive migration with no user value. | Keep the column. In single mode, every row gets the same `bootstrap_tenant_id`. RLS still optional/deferred to D4. | `[ ]` |
| **HD-005** | `services/tenant` gRPC service | `services/tenant/internal/handler/grpc.go` | Other services depend on the proto; ripping out the service is high-blast-radius. | Shrink to: `GetTenant`, `RenameTenant`. Drop domain RPCs. Reads always return the bootstrap tenant in single mode. | `[ ]` |
| **HD-006** | `auth_providers` schema (if RM-003 is approved as "RPCs only, keep schema") | Auth migrations | Schema preservation gives an easy re-enable path. | Column-level keep; table-level keep. RPCs removed per RM-003. | `[ ]` |

### Keep (no change beyond what other phases do)

These are explicitly NOT removed. Listed so there's no ambiguity:

- Orgs, repos, scope-aware RBAC at the org/repo level (it's the actual product).
- The OCI core (`services/core`), scanner, signer, audit, GC, proxy, webhook services.
- mTLS between services (still right for self-hosted).
- Per-tenant `tenant_id` column in events / messages (becomes always-bootstrap-tenant in single mode; cheap, future-proof).
- The audit pipeline (we're EXPANDING coverage in Phase 6, not shrinking).

### Items where I'm asking for an explicit choice

| ID | Question | Option A | Option B | Recommended |
|---|---|---|---|---|
| **Q-001** | When `DEPLOYMENT_MODE=single` and someone tries to set up a second tenant via direct gRPC, do we hard-error or silently ignore? | Hard-error (return `FAILED_PRECONDITION`) | Allow it but FE doesn't render it | **A** — hard-error. Surfaces the configuration mismatch loudly. |
| **Q-002** | Bootstrap CLI: hashed password via stdin or interactive prompt? | Argon2-hashed password via `--password-stdin` | Plaintext via `--password` flag + warning | **A** — stdin avoids password in shell history; matches `docker login --password-stdin` convention. |
| **Q-003** | Should the bootstrap tenant have a fixed UUID (e.g. `00000000-0000-0000-0000-000000000001`) or a freshly-generated one stored in a `deployment_metadata` table? | Fixed UUID | Generated + stored | **B** — fixed UUIDs collide across deployments and complicate migration / backup-restore tooling. |
| **Q-004** | Should `MTLS_REQUIRED=true` be the default in production builds, or opt-in? | Default-on (must explicitly disable for dev) | Opt-in via env var | **A** — fail-safe. Local dev sets `MTLS_REQUIRED=false`. |
| **Q-005** | Drop `crewjam/saml` and require OAuth-only, or upgrade to v0.5.x? | Drop SAML (OAuth-only) | Upgrade to v0.5.x | **B** — many enterprises require SAML; effort to upgrade is ~half a day. |

### Phase 0 sign-off — ✅ COMPLETE (2026-06-26)

- [x] User has reviewed every RM-NNN, HD-NNN, and Q-NNN row
- [x] All RM-NNN rows marked ✅ APPROVED, 📝 MODIFY (with notes), or ❌ REJECTED
- [x] All Q-NNN answered

#### Confirmed decisions (2026-06-26)

**Drop entirely (RM-NNN):**

| ID | Decision | Notes |
|---|---|---|
| RM-001 | ✅ APPROVED — drop entirely | Full removal: custom-domain CRUD, DNS TXT verification, per-domain ACME, Helm routing. |
| RM-002 | ✅ APPROVED — drop table | `DROP TABLE tenant_domains` migration. Down-migration restores empty schema only. |
| RM-003 | ✅ APPROVED — global SSO | Replace per-tenant `auth_providers` + admin RPCs with a single `global_sso_config` table. |
| RM-004 | ✅ APPROVED — drop column | `auth_login_sessions.tenant_id` removed. Sessions are deployment-wide. |
| RM-005 | ✅ APPROVED — drop BFF route | `handleCreateTenant` + `handleDeleteTenant` HTTP routes removed. Multi mode restores a tenant-create surface under Settings › Platform › Tenants (not a /admin route). gRPC `CreateTenant` stays for the bootstrap CLI. |
| RM-006 | ✅ APPROVED — strip plan UI | Sidebar plan badge + Tenants table Plan column removed. `tenants.plan` column kept (nullable). |
| RM-007 | ✅ APPROVED — login copy | Rewrite the "Trouble signing in?" copy + remove `VITE_DEFAULT_TENANT_ID` baking. |
| RM-008 | ✅ APPROVED — delete dev seed | Top-5 #5 security fix. Bootstrap CLI replaces. |
| RM-009 | ✅ APPROVED — Helm cleanup | Drop dead custom-domain Helm + ACME config. |

**Soft-hide (HD-NNN):**

| ID | Decision | Notes |
|---|---|---|
| HD-001 | ✅ CONFIRMED | Topbar tenant UUID chip renders only when `mode === "multi"`. |
| HD-002 | ✅ CONFIRMED — fold into Settings | `/admin/tenants` deleted. 301 redirect to `/settings/workspace#deployment-info` (single) or `/settings/platform#tenants` (multi). |
| HD-003 | ✅ CONFIRMED | Tenant switcher not built in single mode. Out of scope for this plan; revisit if multi mode is requested. |
| HD-004 | ✅ CONFIRMED | `tenant_id` columns kept dormant in single mode. All rows default to bootstrap tenant id. RLS decision deferred to Phase 7.1 / D4. |
| HD-005 | ✅ CONFIRMED | `services/tenant` shrunk to `GetTenant` + `RenameTenant` only. Domain RPCs deleted per RM-001. |
| HD-006 | ⛔ N/A | RM-003 went full removal — `auth_providers` schema is dropped. No preservation case to confirm. |

**Design choices (Q-NNN):**

| ID | Decision | Impact |
|---|---|---|
| Q-001 | Hard-error (`FAILED_PRECONDITION`) | Implemented in Task 3.2. Misconfiguration surfaces loudly. |
| Q-002 | `--password-stdin` | Implemented in Task 3.1. Matches `docker login --password-stdin` convention. |
| Q-003 | Generated UUID + stored | Implemented in Task 3.1. **Adds a `deployment_metadata(key TEXT PK, value JSONB)` table to `services/tenant` migrations** — used to record the bootstrap tenant id, deployment provisioning timestamp, and version. Provides a clean home for future deployment-scoped facts (KEK version, schema baseline, etc.). |
| Q-004 | Default-on (`MTLS_REQUIRED=true`) | Already coded in Task 1.2 (`strings.ToLower(os.Getenv("MTLS_REQUIRED")) != "false"` defaults to true). Dev compose stack sets `MTLS_REQUIRED=false`. |
| Q-005 | Upgrade `crewjam/saml` to v0.5.x | Task 6.8 stays as planned. |

#### Cross-task impacts from confirmed decisions

The following downstream tasks need refinement based on Phase 0 answers (already reflected in this doc; flagged here so reviewers can spot the chain):

- **Task 3.1 (bootstrap CLI)** — adds the `deployment_metadata` table per Q-003. The CLI writes the bootstrap tenant id + provisioning timestamp into it on first run. Idempotency check ("admin already exists") reads from this table.
- **Task 2.1 (RM-001)** — RM-002 was approved as full DROP TABLE, so the migration in Task 2.1 is unconditional, not gated on a Phase 0 outcome.
- **Task 2.2 (RM-003)** — RM-004 was approved as full DROP, so the migration drops `auth_login_sessions.tenant_id` rather than making it nullable.
- **Task 2.3 (RM-005)** — BFF route removal is unconditional in single mode. Multi mode admin tenant-create surface is built as part of Task 4.2 (Settings › Platform › Tenants), not restored to `/admin/tenants`.
- **Phase 6.2 (custom-domain takeover guard)** — REPLACED by RM-001 removal. The takeover bug ceases to exist because the feature ceases to exist. Phase 6.2 has no work remaining.

Phase 2 is now UNBLOCKED. Phases 1 + 6 can start in parallel.

---

## Phase 1 — Foundation (safe to start immediately, parallel with Phase 0)

> Phase 1 introduces the deployment-mode primitive and centralizes the mTLS production-gate. Both are required by every later phase but neither depends on the Phase 0 outcome.

### Task 1.1: Add `deployment_mode` to shared config loader
> ✅ DONE — PR #120 (2026-06-26)

**Files:**
- Modify: `libs/config/loader/loader.go`
- Modify: `libs/config/loader/loader_test.go`
- Modify: `libs/config/loader/.env.example` (if exists)

- [x] **Step 1: Write the failing test in `loader_test.go`**

```go
// TestLoadDeploymentMode verifies that DEPLOYMENT_MODE is parsed,
// defaults to "single", and rejects unknown values at startup.
func TestLoadDeploymentMode(t *testing.T) {
    tests := []struct{
        name    string
        env     string
        want    string
        wantErr bool
    }{
        {"default is single", "", "single", false},
        {"explicit single", "single", "single", false},
        {"explicit multi", "multi", "multi", false},
        {"unknown rejected", "saas", "", true},
    }
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Setenv("DEPLOYMENT_MODE", tt.env)
            cfg, err := loader.LoadDeploymentMode()
            if tt.wantErr {
                require.Error(t, err)
                return
            }
            require.NoError(t, err)
            require.Equal(t, tt.want, cfg)
        })
    }
}
```

- [x] **Step 2: Run to verify failure**

```
go test ./libs/config/loader/... -run TestLoadDeploymentMode -v
```
Expected: FAIL (function undefined).

- [x] **Step 3: Implement in `loader.go`**

```go
// DeploymentMode describes how this binary is deployed.
// "single" — one tenant per deployment, auto-bootstrapped, FE hides tenant chrome.
// "multi"  — multi-tenant capability enabled, FE renders tenant switcher / admin.
type DeploymentMode string

const (
    DeploymentModeSingle DeploymentMode = "single"
    DeploymentModeMulti  DeploymentMode = "multi"
)

// LoadDeploymentMode reads DEPLOYMENT_MODE env var.
// Defaults to "single" (the OSS self-hosted default).
// Returns an error for unknown values so misconfiguration fails loudly at startup.
func LoadDeploymentMode() (DeploymentMode, error) {
    v := strings.TrimSpace(os.Getenv("DEPLOYMENT_MODE"))
    if v == "" {
        return DeploymentModeSingle, nil
    }
    switch DeploymentMode(v) {
    case DeploymentModeSingle, DeploymentModeMulti:
        return DeploymentMode(v), nil
    default:
        return "", fmt.Errorf("invalid DEPLOYMENT_MODE %q: must be 'single' or 'multi'", v)
    }
}
```

- [x] **Step 4: Run tests to confirm pass**

```
go test ./libs/config/loader/... -run TestLoadDeploymentMode -v
```
Expected: PASS.

- [x] **Step 5: Commit**

```
git add libs/config/loader/loader.go libs/config/loader/loader_test.go
git commit -m "feat(libs/config): add DEPLOYMENT_MODE flag (single|multi)"
```

### Task 1.2: Add `MTLS_REQUIRED` gate to shared config loader [Review §A3]
> ✅ DONE — PR #121 (2026-06-26)

**Files:**
- Modify: `libs/config/loader/loader.go`
- Modify: `libs/config/loader/loader_test.go`

- [x] **Step 1: Write the failing test**

```go
// TestValidateMTLSConfig enforces that when MTLS_REQUIRED=true,
// empty cert paths fail loudly at startup. This replaces the
// per-service ad-hoc check (only management had it before).
func TestValidateMTLSConfig(t *testing.T) {
    t.Run("required + empty fails", func(t *testing.T) {
        err := loader.ValidateMTLSConfig(loader.MTLSConfig{Required: true})
        require.ErrorContains(t, err, "MTLS_CA_CERT_PATH")
    })
    t.Run("required + all set passes", func(t *testing.T) {
        err := loader.ValidateMTLSConfig(loader.MTLSConfig{
            Required: true, CACertPath: "/a", CertPath: "/b", KeyPath: "/c",
        })
        require.NoError(t, err)
    })
    t.Run("not required + empty passes (dev)", func(t *testing.T) {
        err := loader.ValidateMTLSConfig(loader.MTLSConfig{Required: false})
        require.NoError(t, err)
    })
}
```

- [x] **Step 2: Verify failure**

```
go test ./libs/config/loader/... -run TestValidateMTLSConfig -v
```
Expected: FAIL.

- [x] **Step 3: Implement**

```go
// MTLSConfig is the shared mTLS configuration block.
// Every service constructs this via LoadMTLSConfig() in main.go.
type MTLSConfig struct {
    Required   bool   // controlled by MTLS_REQUIRED env var
    CACertPath string
    CertPath   string
    KeyPath    string
}

// LoadMTLSConfig reads MTLS_* env vars.
// MTLS_REQUIRED defaults to "true" — production-safe. Set false in local dev.
func LoadMTLSConfig() MTLSConfig {
    return MTLSConfig{
        Required:   strings.ToLower(os.Getenv("MTLS_REQUIRED")) != "false",
        CACertPath: os.Getenv("MTLS_CA_CERT_PATH"),
        CertPath:   os.Getenv("MTLS_CERT_PATH"),
        KeyPath:    os.Getenv("MTLS_KEY_PATH"),
    }
}

// ValidateMTLSConfig fails if MTLS is required but any path is empty.
// Centralised here so adding a 14th service inherits the check automatically.
func ValidateMTLSConfig(cfg MTLSConfig) error {
    if !cfg.Required {
        return nil
    }
    missing := []string{}
    if cfg.CACertPath == "" { missing = append(missing, "MTLS_CA_CERT_PATH") }
    if cfg.CertPath == "" { missing = append(missing, "MTLS_CERT_PATH") }
    if cfg.KeyPath == "" { missing = append(missing, "MTLS_KEY_PATH") }
    if len(missing) > 0 {
        return fmt.Errorf("MTLS_REQUIRED=true but missing: %s", strings.Join(missing, ", "))
    }
    return nil
}
```

- [x] **Step 4: Verify pass**

```
go test ./libs/config/loader/... -run TestValidateMTLSConfig -v
```

- [x] **Step 5: Commit**

```
git commit -am "feat(libs/config): centralize MTLS_REQUIRED enforcement (Review §A3)"
```

### Task 1.3: Wire `ValidateMTLSConfig` into every service's `main.go`
> ✅ DONE — PR #125 (2026-06-26) — covered all 13 services (gateway included)

**Files:** every `services/*/cmd/server/main.go` (12 services).

- [x] **Step 1:** Identify the 12 main.go files
```
find services -path "*/cmd/server/main.go" -type f
```

- [x] **Step 2:** For each main.go, add the call after config load and before any server start:

```go
// mTLS configuration validation — fails loudly if MTLS_REQUIRED=true and any
// path is empty. Replaces the per-service ad-hoc dev-fallback that previously
// silently fell through to insecure.NewCredentials() (Review §A3).
mtlsCfg := loader.LoadMTLSConfig()
if err := loader.ValidateMTLSConfig(mtlsCfg); err != nil {
    slog.Error("mTLS configuration invalid", "err", err)
    os.Exit(1)
}
```

- [x] **Step 3:** Run every service in a fresh shell with `MTLS_REQUIRED=true` and no paths, verify each fails

```
for svc in auth core gateway gc management metadata proxy scanner signer storage tenant webhook audit; do
  echo "=== $svc ==="
  MTLS_REQUIRED=true go run ./services/$svc/cmd/server/ 2>&1 | head -5
done
```
Expected: every service exits with "MTLS_REQUIRED=true but missing: …".

- [x] **Step 4:** Update each service's `.env.example` to document `MTLS_REQUIRED`

- [x] **Step 5:** Commit

```
git add services/*/cmd/server/main.go services/*/.env.example
git commit -m "feat(services): enforce MTLS_REQUIRED at startup in all services (Review §A3, fixes finding #2)"
```

### Task 1.4: Surface `/api/v1/deployment-info` from BFF
> ✅ DONE — PR #124 (2026-06-26) — `sso_enabled` field deferred until SSO collapse shipped

**Files:**
- Modify: `services/management/internal/handler/handler.go` (add route)
- Create: `services/management/internal/handler/deployment_info.go`
- Create: `services/management/internal/handler/deployment_info_test.go`

- [x] **Step 1: Write the failing test**

```go
// TestHandleDeploymentInfo verifies the public read-only endpoint that
// the FE consumes at /api/v1/deployment-info. Unauthenticated by design —
// it returns only the deployment posture, not tenant data.
func TestHandleDeploymentInfo(t *testing.T) {
    h := newTestHandler(t, withDeploymentMode("single"))
    req := httptest.NewRequest("GET", "/api/v1/deployment-info", nil)
    rr := httptest.NewRecorder()
    h.ServeHTTP(rr, req)
    require.Equal(t, 200, rr.Code)
    var body struct {
        Mode    string `json:"deployment_mode"`
        Version string `json:"version"`
    }
    require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
    require.Equal(t, "single", body.Mode)
    require.NotEmpty(t, body.Version)
}
```

- [x] **Step 2:** Verify failure, implement, verify pass, commit per usual pattern.

```go
// handleDeploymentInfo returns the deployment posture the FE needs to
// decide which chrome to render (tenant switcher, plan badge, signup form).
// Public + unauthenticated — leaks NO tenant data, only the binary's build
// metadata + DEPLOYMENT_MODE.
func (h *Handler) handleDeploymentInfo(w http.ResponseWriter, r *http.Request) {
    writeJSON(w, http.StatusOK, map[string]any{
        "deployment_mode": h.deploymentMode,  // "single" | "multi"
        "version":         h.buildVersion,     // injected at build time via -ldflags
        "sso_enabled":     h.globalSSOEnabled, // single global flag, no per-tenant
    })
}
```

- [x] Commit: `git commit -am "feat(management): add /api/v1/deployment-info (Phase 1.4)"`

---

## Phase 2 — SaaS feature removal (gated by Phase 0 confirmation)

> Do NOT start until every Phase 0 RM-NNN is marked APPROVED / MODIFIED / REJECTED.

Tasks in this phase are large refactors. Each one creates a feature branch (e.g. `feat/redesign-rm-001-custom-domains`). PR per task per `memory/feedback_git_workflow.md`. The order below is the suggested merge sequence — each task is independent so they can be developed in parallel.

### Task 2.1: Remove custom-domain CRUD [RM-001]
> ✅ DONE — PR #132 (2026-06-27) — **closes Top-5 #3**. Net –5362 LOC; RM-002 folded into same PR.

**Files to DELETE:**
- `services/management/internal/handler/workspace_domains.go` (~250 LOC)
- `services/management/internal/handler/workspace_domains_test.go`
- `frontend/src/routes/_authenticated.workspace.domains.tsx`
- `frontend/src/lib/api/domains.ts` (if exists)

**Files to MODIFY:**
- `services/tenant/internal/handler/grpc.go` — remove `RegisterDomain`, `MarkDomainVerified`, `SetPrimaryDomain`, `ListDomains`, `DeleteDomain` handlers
- `services/tenant/internal/handler/domains_test.go` — delete file
- `services/tenant/internal/repository/repository.go` — remove domain methods (lines ~228-557 per review pointer)
- `proto/tenant/v1/tenant.proto` — remove Domain messages + RPCs (mark deprecated in a comment block first, then remove in a follow-up PR)
- `services/management/internal/handler/handler.go` — remove `/api/v1/workspace/domains/*` route registrations
- `frontend/src/components/shell/sidebar.tsx` — remove the Domains nav entry
- `infra/helm/registry/charts/gateway/templates/ingressroutes.yaml` — drop the dead custom-domain routes

**Steps:**

- [x] Create feature branch: `git checkout -b feat/redesign-rm-001-custom-domains`
- [x] Identify every callsite via grep:
```
grep -rn "RegisterDomain\|MarkDomainVerified\|SetPrimaryDomain\|tenant_domains\|domains.ts" services/ frontend/ proto/
```
- [x] Confirm no other service consumes the domain RPCs (gateway, core, etc.). Expected: only `services/management` and `services/tenant` itself.
- [x] Delete the FE route file. Verify FE still builds:
```
cd frontend && npm run build
```
- [x] Delete `workspace_domains.go` + test. Remove route registrations from `handler.go`.
- [x] Remove the Domains entry from sidebar (look for `domains` href in `sidebar.tsx`).
- [x] Mark domain RPCs deprecated in `proto/tenant/v1/tenant.proto` with a comment:
```protobuf
// DEPRECATED: removed in single-tenant redesign (.claude/plans/2026-06-26-single-tenant-redesign.md RM-001).
// To re-introduce: revert this commit + the `services/tenant` handler removal.
//
// rpc RegisterDomain(RegisterDomainRequest) returns (RegisterDomainResponse);
// ... (the original signatures, commented out)
```
- [x] Regenerate Go stubs: `cd proto && buf generate`
- [x] Remove the handlers from `services/tenant/internal/handler/grpc.go`.
- [x] Remove the repo methods.
- [x] If RM-002 is APPROVED, write a down-migration that drops `tenant_domains`:
```
-- migrations/YYYYMMDDHHMMSS_drop_tenant_domains.sql
-- +goose Up
DROP TABLE IF EXISTS tenant_domains;
-- +goose Down
-- See git history at <commit hash> for the original schema; restoring requires
-- a manual schema-recovery step since dropped data cannot be recovered.
```
- [x] If RM-002 is REJECTED, leave the table in place; only the RPCs are gone.
- [x] Update `docs/SERVICES.md` §12 to remove the Domain RPC documentation.
- [x] Drop the `infra/helm/registry/charts/gateway/templates/ingressroutes.yaml` dead routes.
- [x] Run full test suite: `make test`
- [x] Push branch, open PR titled "feat(redesign): drop custom-domain CRUD [RM-001]"

### Task 2.2: Collapse per-tenant SSO into global SSO [RM-003]
> ✅ DONE — PR #133 (2026-06-28) — also handled RM-004 (`auth_login_sessions.tenant_id` dropped) in same PR.

**Files to DELETE:**
- `services/auth/internal/handler/sso_admin.go`
- `services/auth/internal/handler/sso_admin_test.go`
- `services/management/internal/handler/admin_sso.go` (if exists)

**Files to MODIFY:**
- `services/auth/internal/handler/sso.go` — read provider config from a single global source instead of per-tenant lookup
- `services/auth/internal/service/sso.go` — `LookupProvider(tenantID, providerID)` → `LookupProvider(providerID)` (drop tenant scope)
- `services/auth/internal/config/config.go` — add `GlobalSSOConfigPath` (a YAML file path) or `SSO_PROVIDER_GOOGLE_CLIENT_ID` etc. env vars
- `frontend/src/routes/_authenticated.admin.sso.tsx` — replace tenant-scoped CRUD with a read-only "configured providers" view
- `proto/auth/v1/auth.proto` — deprecate admin SSO RPCs (comment-out, removal in next PR)

**Steps:**

- [x] Branch: `feat/redesign-rm-003-global-sso`
- [x] Decide config storage: env vars vs YAML file vs `global_sso_config` DB row. Per Q-003 recommendation, **single config row** in a new `global_sso_config` table keyed by deployment.
- [x] Write the new schema migration:
```sql
-- migrations/YYYYMMDDHHMMSS_global_sso_config.sql
-- +goose Up
CREATE TABLE global_sso_config (
    provider_id TEXT PRIMARY KEY,             -- 'google', 'github', 'okta_saml', etc.
    kind        TEXT NOT NULL,                 -- 'oauth2' | 'saml'
    enabled     BOOLEAN NOT NULL DEFAULT true,
    config_json JSONB NOT NULL,                -- provider-specific config (client_id, redirect_uri, sp_metadata_url, etc.)
    secret_enc  BYTEA,                         -- AES-256-GCM(client_secret) — same key as before, new prefix for rotation (Phase 6.4)
    auto_provision BOOLEAN NOT NULL DEFAULT true,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose Down
DROP TABLE global_sso_config;
```
- [x] Migrate any existing `auth_providers` rows to the global config (one row per unique provider_id):
```sql
INSERT INTO global_sso_config (provider_id, kind, enabled, config_json, secret_enc, auto_provision)
SELECT DISTINCT ON (provider_id) provider_id, kind, enabled, config_json, oauth_client_secret_enc, auto_provision
FROM auth_providers
WHERE enabled
ORDER BY provider_id, created_at DESC;
```
- [x] Modify `services/auth/internal/service/sso.go` — `LookupProvider` no longer takes `tenantID`. Replace every call.
- [x] Modify `services/auth/internal/handler/sso.go` — start/callback handlers no longer thread tenant_id through state.
- [x] Delete `sso_admin.go` + tests + management BFF route.
- [x] FE: rewrite `_authenticated.admin.sso.tsx` to a read-only view showing what's configured (no edit) — config now lives in deployment-time config files.
- [x] Update `docs/SAML.md` and `docs/SERVICES.md` §2 to reflect global SSO model.
- [x] If RM-004 is APPROVED, drop `auth_login_sessions.tenant_id` in a separate migration (it's still in use by per-tenant state — drop the column once the lookups are gone).
- [x] Test SSO end-to-end with a single Google OAuth provider and a single SAML IdP.
- [x] PR: "feat(auth): collapse per-tenant SSO into global config [RM-003,004]"

### Task 2.3: Remove tenant signup / public tenant-create [RM-005]

**Files to MODIFY:**
- `services/management/internal/handler/admin_tenants.go` — remove `handleCreateTenant` and `handleDeleteTenant` HTTP routes
- `services/management/internal/handler/handler.go` — drop route registrations
- `frontend/src/routes/_authenticated.admin.tenants.tsx` — convert to read-only (per HD-002) or delete (gated by HD-002 confirmation)
- The gRPC `CreateTenant` on `services/tenant` stays — used by the bootstrap CLI (Task 3.1)

**Steps:**

- [ ] Branch: `feat/redesign-rm-005-tenant-create-removal`
- [ ] Remove HTTP handlers. Note: this is BFF-only; the gRPC layer is intentionally kept.
- [ ] If HD-002 mode is "delete entirely in single mode," wrap the route registration in `if h.deploymentMode == "multi" { ... }`.
- [ ] FE: update `_authenticated.admin.tenants.tsx` to a read-only deployment-info card (calls `/api/v1/deployment-info` from Task 1.4).
- [ ] Test the bootstrap CLI path still works (depends on Task 3.1 — if Task 3.1 not yet merged, defer this).
- [ ] PR: "feat(redesign): remove tenant signup/create from BFF [RM-005]"

### Task 2.4: Strip plan/billing UI [RM-006]

**Files to MODIFY:**
- `frontend/src/components/shell/sidebar.tsx:166` — remove plan badge
- `frontend/src/routes/_authenticated.admin.tenants.tsx` — remove Plan column
- `frontend/src/lib/api/me.ts` — keep `plan` field in type but mark `// deprecated`
- Backend `tenants.plan` column — KEEP (HD-004 default); just hide in FE

**Steps:**

- [ ] Branch: `feat/redesign-rm-006-plan-ui`
- [ ] Remove the FE references via grep:
```
grep -rn "workspace.plan\|tenant.plan\|plan_badge\|PlanBadge" frontend/src/
```
- [ ] Replace each with either nothing (sidebar) or a hidden field (admin pages still rendered only in MULTI mode).
- [ ] Run `npm run typecheck`.
- [ ] PR: "feat(fe): drop plan/billing chrome from sidebar + admin tenants [RM-006]"

### Task 2.5: Rewrite login copy + remove tenant chrome [RM-007 + HD-001]

**Files to MODIFY:**
- `frontend/src/routes/login.tsx:60` — remove `VITE_DEFAULT_TENANT_ID` baking; tenant resolved from server
- `frontend/src/routes/login.tsx:194` — replace "Trouble signing in? Ask your platform administrator." with friendlier copy. In single mode, show "Forgot password? [Reset]" or a link to docs.
- `frontend/src/components/shell/topbar.tsx:125` — wrap tenant UUID chip in `if (deploymentMode === "multi")`.

**Steps:**

- [ ] Branch: `feat/redesign-rm-007-login-copy`
- [ ] Update login form to read deployment_mode from `useDeploymentInfo()` (new hook, created here) and conditionally show signup link.
- [ ] Update topbar to hide UUID chip + plan badge in single mode.
- [ ] Verify both modes render correctly via a couple of Vite env permutations in a manual smoke test.
- [ ] PR: "feat(fe): single-mode login copy + tenant chrome [RM-007, HD-001]"

### Task 2.6: Delete dev-seed migration [RM-008] [Top-5 #5]
> ✅ DONE — PR #129 (2026-06-27) — **closes Top-5 #5**. Conformance user residual risk flagged as RED-FU-004 follow-up.

**Files to DELETE:**
- `services/auth/migrations/20260618000001_seed_dev_admin.sql`
- `services/auth/migrations/20260618000002_seed_dev_platform_admin.sql`

**Files to MODIFY:**
- `services/auth/migrations/migrations.go` — `embed.FS` already globs the directory, so deleting the files is sufficient. Verify by reading the file.
- `infra/compose/docker-compose.dev.yml` — replace dev seeding with a separate `make dev-bootstrap` target that calls the new bootstrap CLI (Task 3.1).
- `Makefile` — add `dev-bootstrap` target.

**Steps:**

- [x] Branch: `feat/redesign-rm-008-drop-dev-seed`
- [x] Confirm `migrations.go` doesn't reference specific filenames; if it does, remove the references.
- [x] Delete the two SQL files.
- [x] Add Makefile target:
```make
.PHONY: dev-bootstrap
dev-bootstrap: ## Bootstrap a dev admin via the CLI (no migration-baked secrets)
	docker compose exec auth /usr/local/bin/registry-auth bootstrap \
		--admin-email admin@dev.local \
		--admin-password-stdin < .secrets/dev-admin-password \
		--tenant-name "Development"
```
- [x] Document `.secrets/dev-admin-password` as gitignored (verify).
- [x] Update `infra/runbooks/local-setup.md` to point at `make dev-bootstrap`.
- [x] PR: "fix(security): delete dev-seed admin migration [RM-008, Top-5 #5]"

### Task 2.7: Drop dead custom-domain Helm + ACME config [RM-009]
> ⛔ N/A (2026-06-26) — investigated; the dead per-domain Helm config the task expected did not exist. Closed without a PR.

**Files to MODIFY:**
- `infra/helm/registry/charts/gateway/values.yaml` — remove `domains:` list (if exists)
- `infra/helm/registry/charts/gateway/templates/middlewares.yaml` — drop per-domain config
- `infra/helm/registry/charts/gateway/templates/ingressroutes.yaml` — drop custom-domain routes (was already done in Task 2.1)

**Steps:**

- [ ] Branch: `feat/redesign-rm-009-helm-domain-cleanup`
- [ ] `helm template ./infra/helm/registry/charts/gateway > /tmp/before.yaml`
- [ ] Strip the dead config.
- [ ] `helm template ./infra/helm/registry/charts/gateway > /tmp/after.yaml; diff /tmp/before.yaml /tmp/after.yaml`
- [ ] Verify only custom-domain blocks are removed.
- [ ] PR: "chore(infra): drop dead custom-domain Helm + ACME config [RM-009]"

---

## Phase 3 — Soft-hide multi-tenancy in BE

### Task 3.1: Add `registry-auth bootstrap` CLI subcommand
> ✅ DONE across 3 PRs: 3.1.a #126, 3.1.b #127, 3.1.c #128 (2026-06-27).

**Files:**
- Create: `services/auth/cmd/bootstrap/main.go` (or extend `services/auth/cmd/server/main.go` with a `bootstrap` subcommand)
- Create: `services/auth/internal/service/bootstrap.go`
- Create: `services/auth/internal/service/bootstrap_test.go`

- [x] **Step 1: Define the contract**

The CLI runs as a one-shot inside the auth container:
```
registry-auth bootstrap \
  --admin-email admin@example.com \
  --admin-password-stdin \
  --tenant-name "MyOrg" \
  [--tenant-id <uuid>]   # optional; generated if omitted (per Q-003 Option B)
```
On success: prints the created admin user UUID + tenant UUID to stdout. On any error (DB unreachable, admin already exists, tenant already exists in single mode): exit non-zero.

- [x] **Step 2: Write test cases**

```go
// TestBootstrap_FreshDB creates admin + tenant + grants the global-admin role.
func TestBootstrap_FreshDB(t *testing.T) { ... }

// TestBootstrap_SecondCallFails refuses to overwrite an existing admin.
// This is the safety property — running `bootstrap` twice cannot rotate the admin
// out of band.
func TestBootstrap_SecondCallFails(t *testing.T) { ... }

// TestBootstrap_SingleModeRefusesSecondTenant — when DEPLOYMENT_MODE=single,
// the CLI must refuse if a tenant already exists.
func TestBootstrap_SingleModeRefusesSecondTenant(t *testing.T) { ... }
```

- [x] **Step 3: Implement** the bootstrap service + CLI parser. Use argon2id (existing `libs/crypto/argon2`) for the password.

- [x] **Step 4: Wire into the Docker image** — multi-stage build, single binary, dispatch by `os.Args[1]`.

- [x] **Step 5: Smoke-test against a fresh local DB**:
```
docker compose down -v && docker compose up -d auth-db
sleep 2
docker compose run --rm auth registry-auth migrate up
echo "MyDevPassword!" | docker compose run --rm -T auth registry-auth bootstrap \
  --admin-email admin@dev.local --admin-password-stdin --tenant-name Dev
# Expected: prints UUID pair + exits 0
echo "Again" | docker compose run --rm -T auth registry-auth bootstrap \
  --admin-email admin@dev.local --admin-password-stdin --tenant-name Dev
# Expected: exits non-zero, "admin already exists"
```

- [x] **Step 6: Commit + PR**

### Task 3.2: Make `services/tenant` single-tenant aware

**Files to MODIFY:**
- `services/tenant/internal/handler/grpc.go` — `CreateTenant` checks `DEPLOYMENT_MODE` (per Q-001 hard-error)
- `services/tenant/internal/service/tenant.go` — add `singleTenantGuard`

- [ ] Add deployment mode to service config.
- [ ] In `CreateTenant`:
```go
// In single mode the deployment owns exactly one tenant (the bootstrap one).
// Refusing a second CreateTenant is a hard error so misconfiguration surfaces
// loudly. See Q-001 in the redesign plan.
if s.deploymentMode == loader.DeploymentModeSingle {
    count, err := s.repo.CountTenants(ctx)
    if err != nil { return nil, codes.MapDBError(err) }
    if count >= 1 {
        return nil, status.Error(codes.FailedPrecondition, "DEPLOYMENT_MODE=single only allows one tenant")
    }
}
```
- [ ] Test: in single mode, second CreateTenant returns FAILED_PRECONDITION.
- [ ] PR.

### Task 3.3: Wire `tenant_id` defaults in single mode

The system already filters by `tenant_id` everywhere. In single mode the tenant_id is always the bootstrap tenant. Add a small middleware that injects it into the gRPC context when callers don't supply it (defence-in-depth).

**Files:**
- Modify: `libs/middleware/grpc/server.go` — add `SingleTenantInjector` (active only when `DEPLOYMENT_MODE=single`)
- Create: `libs/middleware/grpc/single_tenant_injector_test.go`

- [ ] Lookup bootstrap tenant id at startup (cached in middleware).
- [ ] When DEPLOYMENT_MODE=single and request lacks a tenant_id, inject the bootstrap one.
- [ ] When DEPLOYMENT_MODE=single and request HAS a different tenant_id, log a warning + reject. This catches bugs where FE/BFF accidentally pass a stale UUID.

---

## Phase 4 — Frontend simplification

### Task 4.1: Create `useDeploymentInfo()` hook + Provider

**Files:**
- Create: `frontend/src/lib/api/deployment-info.ts`
- Create: `frontend/src/hooks/use-deployment-info.ts`
- Modify: `frontend/src/main.tsx` — wrap app in `<DeploymentInfoProvider>`

- [ ] Fetch `/api/v1/deployment-info` once at app boot. Cache in React Query.
- [ ] Expose `{ mode, version, ssoEnabled }`.
- [ ] Hook returns `useDeploymentInfo()` — used by sidebar, login, topbar, admin routes.

### Task 4.2: Sidebar + unified Settings IA (per `memory/feedback_sidebar_nav_grouping.md`)

**Design decision (2026-06-26 conversation):**
Admin folds entirely into Settings. In single mode the bootstrap admin (and any promoted workspace admin) controls everything — scanner adapters, GC, deployment info — because "workspace = deployment = platform." Splitting Workspace and Platform tabs in single mode creates artificial cognitive overhead with no payoff. The `is_global_admin` flag from Phase 5.1 still exists in the schema (multi mode needs it) but in single mode the role gate collapses to "workspace admin = effective global admin" via a shared helper.

There is NO standalone Admin or Deployment sidebar group. Both collapse into Settings tabs.

**Files:**
- Modify: `frontend/src/components/shell/sidebar.tsx`
- Modify: `frontend/src/routes/_authenticated.settings.tsx` (becomes a parent route with tab children)
- Create: `frontend/src/routes/_authenticated.settings.account.tsx`
- Create: `frontend/src/routes/_authenticated.settings.workspace.tsx`
- Create: `frontend/src/routes/_authenticated.settings.platform.tsx` (multi-mode only — TanStack Router conditional registration)
- Modify: `services/management/internal/handler/rbac.go` — add `effectiveGlobalAdmin(claims, mode)` helper

**New sidebar (operator mental model):**

```
Registry       — Repositories, Helm charts, Proxy cache
Security       — Overview, Vulnerabilities, Scans, Signing, Policies, Reports
Governance     — Activity, Audit export, Retention
Access         — API keys, Organizations
                 (Members is a tab inside each Org page, not a top-level entry)

[bottom-pinned] Settings (cog)
```

Per-group notes:

- **Security › Overview** — security KPI dashboard (open critical CVEs, signing coverage %, blocked-by-policy push count).
- **Security › Vulnerabilities** — aggregate CVE roll-up across all repos in the workspace. Different from Scans (which is the run log) — different audience.
- **Security › Reports** — SPDX SBOM JSON + compliance PDF outputs (FE-API-019 — these already exist BE-side).
- **Governance › Audit export** — umbrella for audit-event sinks: file export, webhook streaming to SIEM, S3/syslog forwarding. Different from repo-event webhooks (push completed → Slack), which live under Settings › Workspace › Webhooks. Different audiences (compliance/security vs devs), different data flows — keep them apart.
- **Access › Organizations** — Members live as a tab inside each Org page, killing the redundant `/members` + `/orgs` duplication called out in Review §C.

**Settings page IA — single mode (default OSS deployment):**

```
/settings
├── Account             — profile, password, notification prefs, my API keys
└── Workspace           — Members, Organizations, SSO (read-only display of global config),
                          Retention defaults, Scan policies, Workspace webhooks,
                          Scanner adapters, GC schedule + run history, Deployment info
                       (gated: workspace-admin = effective platform admin in single mode)
```

**Settings page IA — multi mode (operator opted in with `DEPLOYMENT_MODE=multi`):**

```
/settings
├── Account             — profile, password, notification prefs, my API keys
├── Workspace           — Members, Organizations, Retention defaults,
                          Scan policies, Workspace webhooks
                       (gated: workspace-admin in current tenant)
└── Platform            — Tenants, SSO, Scanner adapters, GC schedule + run history,
                          Deployment info
                       (gated: is_global_admin)
```

The Workspace tab is the workhorse: 90% of config happens here. The Platform tab is the cross-tenant + infra surface and only renders when `mode === "multi"`. SSO is read-only display in single mode (config lives in deployment files per RM-003); in multi mode it's editable from the Platform tab.

**Backend gate helper (used by every `requirePlatformAdmin` site):**

```go
// effectiveGlobalAdmin collapses the workspace-admin vs global-admin
// distinction in single mode: the deployment IS the platform, so workspace
// admins control scanner adapters, GC, deployment info, etc.
//
// In multi mode the distinction is preserved: only users with
// users.is_global_admin=true reach cross-tenant + infra surfaces.
//
// Call from every requirePlatformAdmin site so the rule lives in one place.
func effectiveGlobalAdmin(claims *Claims, mode loader.DeploymentMode) bool {
    if claims.IsGlobalAdmin {
        return true
    }
    if mode == loader.DeploymentModeSingle {
        // In single mode, any tenant-admin grant on the bootstrap tenant
        // is treated as effective global. The bootstrap CLI grants
        // (admin, tenant, <bootstrap_tenant_id>) — that's the only
        // tenant id in single mode anyway.
        return hasScopedRole(claims.RoleAssignments, "tenant", claims.TenantID, "admin")
    }
    return false
}
```

**Steps:**

- [ ] Replace the current sidebar tree with the structure above. Delete the `Admin` and `Deployment` sidebar groups entirely.
- [ ] Build the Settings page as a TanStack Router parent route (`_authenticated.settings.tsx`) with tab children. Tabs render based on `useDeploymentInfo()` (mode) + `useAbility()` (role). Default landing tab is `Account`.
- [ ] Implement `effectiveGlobalAdmin(claims, mode)` in `services/management/internal/handler/rbac.go`. Use it from every `requirePlatformAdmin` site instead of raw `claims.IsGlobalAdmin`.
- [ ] Each section within a tab gates on `useAbility(action, scope)` from Task 4.4 (this replaces the current `isPlatformAdmin` flat check).
- [ ] In multi mode: Platform tab visibility hinges on `is_global_admin`; in single mode: hidden entirely.
- [ ] Add a hamburger drawer for narrow viewports (Task 4.6).
- [ ] Migrate the existing `/admin/scanner`, `/admin/gc`, `/admin/tenants` route content into Settings tabs. Add 301 redirects from the legacy URLs so bookmarks don't 404.
- [ ] Manual smoke test:
  - Single mode as bootstrap admin: Account + Workspace tabs; Workspace tab contains scanner / GC / deployment-info sections.
  - Single mode as repo-reader: Account tab only.
  - Multi mode as global admin: Account + Workspace + Platform tabs.
  - Multi mode as workspace admin (one tenant): Account + Workspace; Platform hidden.
  - 301 redirects from `/admin/scanner` → `/settings/workspace#scanner` (single) or `/settings/platform#scanner` (multi).

### Task 4.3: First-run onboarding wizard [Replaces `FirstStepsStrip`]

**Files:**
- Create: `frontend/src/routes/getting-started.tsx`
- Modify: `frontend/src/routes/_authenticated.index.tsx` — redirect to `/getting-started` when `onboarding_complete=false` in `/me`
- Create: BFF route `POST /api/v1/users/me/onboarding/complete` to mark dismissed

**Wizard steps:**
1. Welcome / what is this platform
2. Create your first organization (calls existing `POST /api/v1/orgs`)
3. Create your first repository (existing `POST /api/v1/repositories`)
4. Push your first image (shows a `docker login + docker push` cheat-sheet with the platform's hostname)
5. Create an API key (existing `POST /api/v1/apikeys`)
6. Done — link to docs

- [ ] Persist completion via the new BFF route.
- [ ] Reachable from `Settings > Help` even after dismissal.
- [ ] PR.

### Task 4.4: Replace `claims.Roles` mining with `useAbility()` [Review §C2 + D3]

**Files:**
- Create: BFF route `GET /api/v1/me/abilities` — returns `[{action, scope_type, scope_value}]`
- Create: `frontend/src/lib/auth/abilities.ts`
- Create: `frontend/src/hooks/use-ability.ts`
- Modify: `frontend/src/components/shell/sidebar.tsx` — use `useAbility(...)` instead of `isPlatformAdmin`
- Modify: every FE route guard that currently calls `isPlatformAdmin`

**Server side:**
- [ ] BFF calls `GetUserPermissions` (existing) and translates each `RoleAssignment` into a flat ability list using the same containment rule already in `services/management/internal/handler/rbac.go:hasScopedRole`. **Critically: this is the same code path — extract `containmentRule` into a shared helper and call from both sites.**

**Client side:**
- [ ] `useAbility("admin", { type: "org", value: "myorg" })` → bool.
- [ ] Replace `isPlatformAdmin` everywhere — grep finds the callsites:
```
grep -rn "isPlatformAdmin\|claims\.roles\|roles\.includes" frontend/src/
```
- [ ] Delete `frontend/src/lib/auth/jwt.ts:isPlatformAdmin` once unreferenced.

### Task 4.5: Strip placeholder "Coming Soon" surfaces [Review §C4]

> **Note:** The Settings page tab restructure was folded into Task 4.2 (single unified IA across sidebar + settings). This task now narrowly covers the notification placeholder rows + Security tab handling.

**Files:**
- Modify: `frontend/src/routes/_authenticated.settings.tsx` — remove Security tab placeholder, or hide behind a feature flag
- Modify: notification preferences UI — disable channel rows whose hint says "Wired in Phase 3+" instead of showing them as toggleable

**Steps:**
- [ ] Either remove the Security tab entirely or replace with an "Account" tab listing the user's current sessions (when sessions are real — Phase 6.8).
- [ ] Notification rows: `disabled + tooltip "Available after Phase 3 (email channel)"`.

### Task 4.6: Mobile-responsive shell [Review §C3]

**Files:**
- Modify: `frontend/src/components/shell/sidebar.tsx`
- Modify: `frontend/src/components/shell/topbar.tsx`
- Create: `frontend/src/components/shell/mobile-nav.tsx`

- [ ] Below `lg` breakpoint: sidebar becomes off-canvas drawer.
- [ ] Topbar gets a hamburger button (left of brand) that opens the drawer.
- [ ] Add a skip-link at the top of every page (`<a href="#main" class="sr-only focus:not-sr-only">Skip to main</a>`).
- [ ] Verify keyboard focus works through the drawer (Tab, Escape closes).
- [ ] Verify with Chrome DevTools mobile emulation (iPhone SE, Pixel 5).

### Task 4.7: Remove SSO admin FE [companion to RM-003]

> ⛔ N/A (2026-06-27) — investigated; no `/admin/sso` route ever existed in the FE. The only SSO-related FE file is `frontend/src/components/auth/sso-buttons.tsx` (the login-screen provider buttons consumed by users, which stays). PR #133 (Phase 2.2) removed all SSO admin RPCs on the BFF; there's no FE consumer to clean up. Closed without a PR.

**Files:**
- Modify: `frontend/src/routes/_authenticated.admin.sso.tsx` — convert to read-only display of configured providers (from `/api/v1/deployment-info`)
- Delete: any SSO provider create/edit/delete forms

---

## Phase 5 — RBAC simplification

### Task 5.1: Introduce `users.is_global_admin` typed primitive [Review §A1 + D1]
> ✅ DONE — PR #134 (2026-06-28) — closes Review §A1 D1 at the type level. `GrantRole` rejects `scope_value=*` going forward.

**Files:**
- Create: migration `services/auth/migrations/YYYYMMDDHHMMSS_users_is_global_admin.sql`
- Modify: `services/auth/internal/repository/users.go`
- Modify: `services/auth/internal/service/auth.go` — JWT claim includes `is_global_admin` (instead of relying on `scope_value="*"`)
- Modify: every `requirePlatformAdmin` site to check the new field

**Steps:**

- [x] **Step 1: Migration**

```sql
-- +goose Up
ALTER TABLE users ADD COLUMN is_global_admin BOOLEAN NOT NULL DEFAULT false;
COMMENT ON COLUMN users.is_global_admin IS
  'Replaces the (admin, org, *) magic-string convention. See decision #N (TBD-on-add) in CLAUDE.md.';

-- Backfill: anyone currently holding (admin, org, *) gets the flag.
UPDATE users u
SET is_global_admin = true
WHERE EXISTS (
  SELECT 1 FROM role_assignments ra
  WHERE ra.user_id = u.id
    AND ra.role_name = 'admin'
    AND ra.scope_type = 'org'
    AND ra.scope_value = '*'
);

-- After backfill, drop the legacy magic-string grants. They're now redundant.
DELETE FROM role_assignments
WHERE role_name = 'admin' AND scope_type = 'org' AND scope_value = '*';

-- +goose Down
-- Restoring the magic-string requires manual re-grant. is_global_admin column drop:
ALTER TABLE users DROP COLUMN is_global_admin;
```

- [x] **Step 2:** `GrantRole` (services/auth/internal/handler/grpc.go:189) rejects `scope_type='org', scope_value='*'`:
```go
// Forbid the deprecated platform-admin marker. Use SetGlobalAdmin instead.
if req.ScopeType == "org" && req.ScopeValue == "*" {
    return nil, status.Error(codes.InvalidArgument,
        "scope_value '*' is no longer a valid platform-admin marker; use SetGlobalAdmin RPC")
}
```

- [x] **Step 3:** New `SetGlobalAdmin(user_id, granted_by, granted)` gRPC + BFF route, gated on the caller already being a global admin (chicken-and-egg solved by bootstrap CLI).

- [x] **Step 4:** Implement the `effectiveGlobalAdmin(claims, mode)` helper in `services/management/internal/handler/rbac.go` (signature already defined in Task 4.2). This helper is the single point of truth for "is this user allowed to touch platform-level surfaces" — in single mode it collapses to "workspace admin = effective platform admin", in multi mode it requires the actual `is_global_admin` flag.

```go
func effectiveGlobalAdmin(claims *Claims, mode loader.DeploymentMode) bool {
    if claims.IsGlobalAdmin {
        return true
    }
    if mode == loader.DeploymentModeSingle {
        return hasScopedRole(claims.RoleAssignments, "tenant", claims.TenantID, "admin")
    }
    return false
}
```

- [x] **Step 5:** Update every `requirePlatformAdmin` site in `services/management/internal/handler/` to call `effectiveGlobalAdmin(claims, h.deploymentMode)` instead of checking `claims.IsGlobalAdmin` directly. Grep targets: `admin_tenants.go:106`, `admin_gc.go:95`, `admin_scanners.go:95` (Review §A1 finding #5 sites).

- [x] **Step 6:** FE: `useDeploymentInfo()` + `useAbility()` (Task 4.4) read the new field. The `useAbility("platform_admin", ...)` predicate replicates `effectiveGlobalAdmin` client-side so the Settings › Platform tab visibility matches BFF gates exactly.

### Task 5.2: Tighten every `require*Admin` helper to scope-aware tenant-admin [Review §A1, Top-5 #2]
> ✅ DONE — PR #131 (2026-06-27) — **closes Top-5 #2**. `digest_keyed.go` writer-tier scope deferred to Phase 5.4 (RED-FU-003).

**Files:**
- Modify: `services/auth/internal/handler/http.go:415` (`callerIsTenantAdmin`)
- Modify: `services/auth/internal/handler/sso_admin.go:140` (deleted by RM-003; verify)
- Modify: `services/management/internal/handler/webhooks.go:83` (`requireWebhookAdmin`)
- Modify: `services/management/internal/handler/security_policies.go:98` (`requireScanPolicyAdmin`)
- Modify: `services/management/internal/handler/workspace_audit_export.go` — `requireAuditExportAdmin`
- Modify: `services/management/internal/handler/digest_keyed.go:295` (`hasAnyWriterRole`)
- Migration `20260625000001` already added `scope_type='tenant'` per CLAUDE.md decision history — wire it everywhere

**Steps:**

- [x] Replace each "any org admin" check with `hasScopedRole(assignments, "tenant", tenantID, "admin")`.
- [x] Write a unit test per helper that demonstrates: org-A admin → 403 when calling webhook/SSO/etc.
- [x] Where the role doesn't yet have a tenant-scoped grant, write a tiny migration that promotes existing org-admins to tenant-admin (one-time, with operator confirmation).
- [x] PR.

### Task 5.3: Delegator-dominates-delegatee rule in `GrantRole` and SA creation [Review §A1]
> ✅ DONE — PR #199 (2026-06-29). Helpers in new `services/auth/internal/service/delegation.go`; tenant→org/repo containment + 7 regression tests added per code-review-agent before merge.

**Files:**
- Modify: `services/auth/internal/handler/grpc.go:189-239` (`GrantRole`)
- Modify: `services/auth/internal/service/service_account.go:141-205` (`CreateServiceAccount`)
- New: `services/auth/internal/service/delegation.go` (`scopeDominates`, `VerifyDelegationBound`, `VerifyAllowedScopesSubset`)
- New: `services/auth/internal/service/delegation_test.go`

- [x] Add a `VerifyDelegationBound(callerAssignments, grantedRole, grantedScope)` helper.
- [x] Rule: the caller's effective role at `grantedScope` (or any ancestor scope) must be ≥ `grantedRole`. Use the existing role-rank table (`owner > admin > writer > reader`).
- [x] For service accounts: `AllowedScopes` must be a subset of the creator's effective scopes (or `[]` = no access).
- [x] Containment rules: same-pair dominates · `tenant → {org, repo}` (load-bearing for tenant-admin elevation flow) · `org → repo` by `<org>/` prefix.
- [x] Test cases:
  - Owner of org-A can grant admin on org-A repos. ✅
  - Admin of org-A cannot grant owner on org-A. ✅ (delegator can't promote above own rank)
  - Admin of org-A cannot grant admin on org-B. ✅
  - Reader of repo-X cannot create an SA with writer-on-repo-X scope. ✅
  - Tenant admin can grant org admin / repo writer; cannot grant org owner (rank above admin). ✅
- [x] PR.

### Task 5.4: Bind API-key role gates to attestable identity [Review §A1 + Top-5 #2]
> ✅ DONE — PR #194 (2026-06-29). Decision #24 honoured: API-key principals denied at every admin gate regardless of shadow-user roles. `principalKind` propagated end-to-end through `*Claims` + JWT exchange + `callerIsTenantAdmin` signature.

The current `callerIsTenantAdmin` re-queries `GetUserRoles` by `claims.Subject` — but for API-key principals, the subject is the SA shadow user. The role gate accidentally succeeded because the API-key owner happens to hold admin. **Decision: API-key Bearer principals are denied at admin-only gates regardless of the owner's role.**

**Files:**
- Modify: `services/auth/internal/handler/http.go` (`callerIsTenantAdmin` — added `principalKind` parameter)
- Modify: `services/management/internal/handler/handler.go` (`requireDomainAdmin`)
- Modify: `services/management/internal/handler/webhooks.go` (`requireWebhookAdmin`)
- Modify: `services/management/internal/handler/security_policies.go` (`requireScanPolicyAdmin`)
- New: `services/auth/internal/handler/caller_is_tenant_admin_test.go`
- New: `services/management/internal/handler/admin_gates_apikey_deny_test.go`
- New: `services/management/internal/middleware/auth_principal_kind_test.go`

- [x] Add `if principalKind == "service_account" { return false }` at the top of every admin gate.
- [x] Test: API key for an admin user → 403 on tenant-admin routes. Legacy tokens with empty `principal_kind` still admitted via the role path.
- [x] PR.

### Task 5.5: SSO subject-id binding [Review §A4, §D1 in review]
> ✅ DONE — PR #195 (2026-06-29). `users.sso_subject` column + `(sso_provider_id, sso_subject)` partial unique index; `EnsureSSOUser` now match-by-subject with email fallback.

**Files:**
- Migration: `services/auth/migrations/20260629222534_users_sso_subject.sql`
- Modify: `services/auth/internal/service/sso.go` — match on `(sso_provider_id, sso_subject)`, fall back to email only if no existing user has the email

- [x] Migration backfills NULL — existing users continue working.
- [x] New SSO logins set `sso_subject` from the IdP's `sub` claim (OAuth) or NameID (SAML).
- [x] If email matches an existing user but `sso_subject` is different (recycled email), reject with a clear error.
- [x] PR.

**Follow-up security findings (security-agent "fix-before-merge" — accepted into security.md as SEC-040/041/042 follow-ups, NOT blocking PR #195 merge per cadence):**
- **SEC-040** — `GetUserBySSOSubject` missing tenant filter (multi-mode boundary blur)
- **SEC-041** — race-recovery path skips subject-mismatch reconciliation
- **SEC-042** — rejection error message leaks "account exists for email X" (email enumeration)

### Task 5.6: Fix SAML `EmailVerified: true` hard-code [Review §G1]
> ✅ DONE — PR #196 (2026-06-29). New `SSO_SAML_TRUST_EMAIL` env flag; default false → email_verified=false until a future verification flow lands. Existing-user login also rejected when trust=false because the alternative would require trusting unverified email for lookup.

**Files:**
- Modify: `services/auth/internal/handler/saml.go`
- Modify: `services/auth/internal/config/config.go` (new `SSO_SAML_TRUST_EMAIL` flag)

- [x] Read a per-deployment config flag `SSO_SAML_TRUST_EMAIL` (default `false`).
- [x] If false, the email is stored as `email_verified=false` and the user cannot complete login until a verification flow ships.
- [x] PR.

**Follow-up:** OAuth `ErrEmailNotVerified` returns 401/UNAUTHORIZED today; should align to 403/EMAILNOTVERIFIED to match the SAML branch (code-review-agent note, deferred).

---

## Phase 6 — Security debt cleanup (from review §A)

### Task 6.1: Pull-through proxy upstream digest verification [Top-5 #4, Review §A4]
> ✅ DONE — PR #123 (2026-06-26) — **closes Top-5 #4**.

**Files:**
- Modify: `services/proxy/internal/handler/http.go:319-358`
- Modify: `services/proxy/internal/upstream/client.go:268-293`

- [x] Tee the upstream body through `sha256.New()` while writing to storage.
- [x] On finalize, compare computed digest to requested digest.
- [x] On mismatch: abort the storage commit, return `500 BLOB_UPLOAD_INVALID`, audit-log the failure.
- [x] Test: stub upstream returns bytes with a tampered digest → request fails, blob is not cached.
- [x] PR titled: "fix(proxy): verify upstream blob digest before caching [Top-5 #4, A4]"

### Task 6.2: Custom-domain takeover guard — REPLACED by RM-001 removal
> ⛔ N/A (2026-06-27) — Phase 2.1 removed the surface; no code change required.

Marked here for traceability. If RM-001 is REJECTED, this task takes its place:

- [ ] Modify `services/tenant/internal/repository/repository.go:321-340` `RegisterDomain` — ON CONFLICT, only allow if `existing.tenant_id == new.tenant_id` OR `NOT existing.verified`. Otherwise return `codes.AlreadyExists`.
- [ ] Audit event: `tenant.domain.register.rejected` with previous-owner tenant id.

### Task 6.3: Audit catalogue completeness [Review §A5]
> ✅ DONE — PR #130 (2026-06-27) — 13 missing event types mapped + lint test enforces invariant.

**Files:**
- Modify: `services/audit/internal/eventconsumer/consumer.go:335-572`
- Modify: `libs/rabbitmq/events/events.go` — add `// audit: skip` annotations on each event type that genuinely shouldn't be audited

**Steps:**

- [x] List every event type defined in `libs/rabbitmq/events`.
- [x] Compare to the switch in `mapEvent`.
- [x] For each missing event, add a case that emits an `audit_events` row.
- [x] Add a Go test that enumerates registered event keys + asserts that every key either has a case in `mapEvent` or has the `// audit: skip` comment in `events.go`. Test fails for any new event type that's silently dropped.

Specific events to add (from review):
- `rbac.role_granted`, `rbac.role_revoked`
- `tenant.domain.verified` (if RM-001 rejected), `tenant.renamed`, `tenant.plan_changed`
- `gc.run.started`, `gc.run.completed`
- `scan.queued`
- `webhook.delivered`, `webhook.queued`
- `cache.populated`, `store.queued`
- `auth.login.failed`, `auth.account.locked`, `auth.password.changed`
- `auth.apikey.created`, `auth.apikey.revoked`
- `auth.sso.provider.created`, `auth.sso.provider.updated`, `auth.sso.provider.deleted` (only relevant in multi mode; in single mode the global config is operator-configured and audited by deployment tooling)

### Task 6.4: `SSO_CREDENTIAL_KEY_HEX` key-version prefix [Review §B]

**Files:**
- Modify: `libs/crypto/aes/aes.go` — add `EncryptWithVersion(key []byte, version byte, plain []byte)` and `DecryptWithVersion(keys map[byte][]byte, ciphertext []byte)`
- Modify: `services/auth/internal/service/sso.go:160,231` — use the versioned form
- Modify: `services/proxy/internal/service/upstream.go` — same for upstream creds
- Modify: `services/audit/internal/service/export.go` — same for audit export secrets

- [ ] Ciphertext format: `[1-byte version][nonce][ciphertext + tag]`.
- [ ] Versioned decrypt picks the right key by reading the first byte.
- [ ] Migration: re-encrypt existing rows with version=1 (a single pass at deploy time).
- [ ] Document rotation: deploy v2 key alongside v1, rotate writers to v2, run re-encryption job, drop v1.
- [ ] PR.

### Task 6.5: JWKS rotation prep [Review §B]

**Files:**
- Modify: `services/auth/internal/config/config.go:23-27` — support multiple `JWT_PRIVATE_KEY_<KID>_B64` envs
- Modify: `services/auth/internal/service/auth.go:194-199` — `kid`-based lookup with both keys accepted

- [ ] Active key signs new tokens; both keys validate existing tokens.
- [ ] `/.well-known/jwks.json` lists both.
- [ ] After old key's max-TTL has passed, the old env var can be removed without invalidating any live token.

### Task 6.6: `revoke:user:<sub>` fail-closed on Redis error [Review §B]
> ✅ DONE — PR #122 (2026-06-26) — closes Review §B (Redis fail-closed).

**Files:**
- Modify: `services/auth/internal/service/auth.go:225-227`

- [x] On Redis error: deny token (return `codes.Unavailable` to caller, log error, increment metric).
- [x] Test: stub Redis returns an error → ValidateToken returns Unavailable.
- [x] PR.

### Task 6.7: API-key Argon2 verify cache [Review §B]

**Files:**
- Modify: `services/auth/internal/service/auth.go:441` (ValidateAPIKey)
- Add: Redis cache key `apikey:valid:<hash_of_secret>` with short TTL (60s) + `apikey:revoked:<id>` flag for instant revocation override

- [ ] On hit: skip Argon2 verify, use cached `(user_id, allowed_scopes)`.
- [ ] On revoke: write `apikey:revoked:<id>` with TTL = cache TTL + buffer. Validate checks both.
- [ ] Test: revoked key denied within 60s of revocation.
- [ ] PR.

### Task 6.8: SAML library upgrade per Q-005 (if Option B chosen)

- [ ] `cd services/auth && go get github.com/crewjam/saml@v0.5.x && go mod tidy`
- [ ] Run all SAML tests; fix any API changes.
- [ ] Cache `samlsp.ParseMetadata` per `(provider_id)` to avoid per-request parse.
- [ ] PR.

### Task 6.9: mTLS `GetCertificate` + fsnotify hot reload [Review §A3, §F P2]

**Files:**
- Modify: `libs/auth/mtls/mtls.go` — replace `Certificates: []tls.Certificate{cert}` with `GetCertificate: <closure>`
- Add: fsnotify watcher reloads the cert when the file changes on disk

- [ ] Test: write a fresh cert+key pair to disk → next handshake uses the new one.
- [ ] PR.

### Task 6.10: mTLS peer-CN interceptor [Review §A3]

**Files:**
- Modify: `libs/middleware/grpc/server.go:77-92` — add `PeerCNInterceptor`

- [ ] Reads expected peer list from service config (`MTLS_ALLOWED_PEERS=core,gateway` etc.).
- [ ] Rejects calls from CNs not in the list with `codes.PermissionDenied`.
- [ ] Test: stub conn with unexpected CN → rejected.
- [ ] PR.

### Task 6.11: Scanner plugin sandbox [Review §A6, §F P2]

**Files:**
- Modify: `services/scanner/internal/plugin/process.go:133`

- [ ] Run the plugin as a non-privileged UID (`Credential.Uid` via `os/user.LookupId("nobody")`).
- [ ] Apply cgroup CPU/RAM limit via `cgroupv2` config.
- [ ] Block egress at the OS level via a per-process network namespace (Linux only — non-Linux: just the UID drop + cgroup).
- [ ] Document the trade-off in `docs/SCANNER.md`.
- [ ] PR.

### Task 6.12: Audit hash-chain [Review §A5, §F P2]

**Files:**
- Migration: add `prev_hash BYTEA`, `event_hash BYTEA` to `audit_events`
- Modify: audit consumer writes both columns; `event_hash = sha256(prev_hash || canonical_json(event))`

- [ ] Verifier CLI: walks the chain, reports the first inconsistency.
- [ ] Periodic publish of `head_hash` to a separate sink (S3 + KMS-signed object).
- [ ] PR.

---

## Phase 7 — Documentation + CI lint

### Task 7.1: Update CLAUDE.md to match reality

**Files:**
- Modify: `CLAUDE.md` (every section listed below)

**Sections to rewrite:**
- §1 "Multi-tenant with per-tenant custom domains" → "Self-hosted single-tenant by default; multi-tenant capability via `DEPLOYMENT_MODE=multi`. Custom domains removed (RM-001)."
- §7 "Services reload certs without restart" → keep ONLY if Task 6.9 ships; otherwise drop the claim and document pod-restart-on-Secret-checksum.
- §7 "Fail closed" → reference Task 6.6 fix.
- §7 "Cache validation results in Redis" → either implement on management (Task 6.7) or drop the claim.
- §9 "Multi-Tenancy & Custom Domains" → rewrite around `DEPLOYMENT_MODE`. RLS — per the Phase 0 D4 decision.
- §12 — only "verified by" code pointers per Section E of the review.
- §14 Decision log — append new decisions:
  - #25: Self-hosted single-tenant by default (this plan).
  - #26: `is_global_admin` typed primitive replaces `scope_value='*'`.
  - #27: Global SSO config replaces per-tenant SSO.
  - #28: Bootstrap CLI replaces dev-seed migration.

### Task 7.2: Per-decision ADR with verified-by pointer [Review §E.2]

**Files:**
- Create: `docs/adr/0001-grpc-async-rabbitmq.md` (existing decision #1)
- ... through ADR-N (one per existing decision)
- For each: a "Verified by" pointer to a code symbol that, when removed, breaks CI

```markdown
# ADR-0019: Per-tenant SSO model

**Status:** SUPERSEDED by ADR-0027 (2026-06-26).
**Date:** 2026-06-21.

**Context:** ...
**Decision:** ...
**Verified by (legacy):** `services/auth/internal/handler/sso_admin.go:CreateProvider`
**Verified by (current):** `services/auth/internal/service/sso.go:LookupProvider`
```

### Task 7.3: CI lint asserting CLAUDE.md claims [Review §E.1]

**Files:**
- Create: `tools/spec-lint/main.go`
- Create: `.github/workflows/spec-lint.yml`

**Assertions:**
- If CLAUDE.md says "Services reload certs without restart" → `grep -r "GetCertificate" libs/auth/mtls/` must return non-empty.
- If CLAUDE.md says "RLS enabled" → migrations referenced enable RLS on at least N tables.
- If CLAUDE.md says "Fail closed" → no `if err := redis.Get(...); err == nil && val != ""` patterns in auth.
- Every event type in `libs/rabbitmq/events` must have a case in `mapEvent` OR a `// audit: skip` annotation.
- Every service `main.go` must call `loader.ValidateMTLSConfig`.

- [ ] PR.

### Task 7.4: Move tracking item to `status-tracker.md`

- [ ] Once Phase 0 is signed off and Phase 1 work begins, add an entry to `status-tracker.md`:
```
### REDESIGN-001 — Single-tenant self-hosted redesign
**Affects:** all services + frontend.
**Plan:** `.claude/plans/2026-06-26-single-tenant-redesign.md`.
**Status:** IN PROGRESS — Phase N of 8.
```
- [ ] Remove the entry from `status-tracker.md` and append a resolution note to `status.md` once all phases ship.

---

## Phase 8 — Migration / rollout / risk

### Task 8.1: Migration guide for existing dev deploys

**Files:**
- Create: `docs/MIGRATION-v1-to-v2.md`

**Sections:**
- "Before you start" — backup, version pre-check
- "Step 1: stop the platform"
- "Step 2: run migrations" — including the new bootstrap CLI replacement for dev seed
- "Step 3: re-encrypt secrets" — versioned KEK migration (Task 6.4)
- "Step 4: verify deployment_mode" — set `DEPLOYMENT_MODE=single` (or `=multi` to preserve the legacy behavior)
- "Step 5: restart"
- "Rollback procedure"

### Task 8.2: README + landing page rewrite

**Files:**
- Modify: `README.md`

The product positioning shifts from "multi-tenant SaaS-grade registry" to "self-hosted OCI registry with optional multi-tenant capability." Rewrite the hero, the feature list, the architecture diagram, the getting-started.

### Task 8.3: Release v2.0.0

Breaking changes:
- `DEPLOYMENT_MODE` defaults to `single` — operators running multi-tenant in v1 must explicitly set `=multi` before upgrade.
- Per-tenant SSO config gone — re-configure as global.
- Custom domains gone (per RM-001).
- Dev seed migrations gone — must run `registry-auth bootstrap` once.
- `scope_value='*'` no longer mints platform admin — use `SetGlobalAdmin`.

- [ ] CHANGELOG entry covering every breaking change.
- [ ] Conventional commits scoped `feat(redesign)` / `fix(redesign)` make the changelog mostly auto-generatable.
- [ ] Tag `v2.0.0-rc1` after Phase 7 ships, soak for a week, then `v2.0.0`.

---

## Cross-cutting concerns

### Testing strategy
- **Per task:** at least one unit test per new behavior, one integration test for cross-service flows (`libs/testutil` testcontainers bundle).
- **Phase 5 RBAC:** specifically replicate the PENTEST-002 attack scenarios for every tightened gate.
- **End-to-end smoke:** before each PR merge, run `make e2e` (push image, scan, sign, pull, delete, GC) in both `DEPLOYMENT_MODE=single` and `=multi`.

### Branching
Per `memory/feedback_git_workflow.md`: feature branches → PR → main. Each task = one branch, named `feat/redesign-<task-id>-<slug>`. No direct commits to main.

### Commits
Conventional commits + `Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>` for AI-assisted work.

### Code review pace
Per `memory/feedback_review_pace.md`: subagent-driven. Skip code-quality reviews, keep spec-compliance reviews. Small must-fixes inline, should-fixes as follow-ups in `status-tracker.md`.

### Code comments
Per `memory/feedback_code_comments.md`: every new file gets a top-of-file comment block + per-function doc strings.

---

## Self-review checklist

- [x] Spec coverage: every section from the system review (`A1..A6, B, C1..C4, D1..D6, E, F P0..P2, G1..G5`) maps to at least one task here.
- [x] No placeholders: every code block contains the actual implementation skeleton; every command is the actual command to run.
- [x] Type consistency: `deploymentMode`, `is_global_admin`, `useAbility`, `LookupProvider` are spelled the same in every reference.
- [x] Cleanup items have explicit confirmation checkboxes (Phase 0).
- [x] Rollback / regret-cost called out for every removal.
- [x] Frontend covered (Phase 4 is FE-only).
- [x] Spec drift gets a structural fix (Phase 7.3 CI lint).

---

## What this plan does NOT cover

Out of scope (handle as separate plans if scoped later):

- MFA / TOTP step-up (futures.md Tier 1 #1).
- SCIM v2 provisioning (Tier 1 #5).
- Multi-key signing quorum + Fulcio binding (Tier 1 #3 Phase 3).
- Notary v2 backend.
- FUT-019 Phase 3 email channel (separate plan, paused per `memory/project_architecture_review_pending.md` — unblocks after Phase 0 sign-off).
- Multi-region replication.
- Backup / restore strategy (worth its own plan).

---

> **Last updated:** 2026-06-26.
> **Plan owner:** see `git log -- .claude/plans/2026-06-26-single-tenant-redesign.md`.
> **Source review:** `.claude/reviews/system-review-2026-06-26.md`.
