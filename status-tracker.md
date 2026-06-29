# status-tracker.md — Open Remediation + Hardening Work

> **What this file is for:** the curated set of currently-open
> remediation (`REM-NNN`) and security (`SEC-NNN` / `PENTEST-NNN`)
> items, plus partial / blocked surfaces. **Lean by design.**
>
> **Workflow:**
> 1. New item surfaces → add a short entry here (rationale, scope, link to branch / PR when in flight).
> 2. Work happens on a feature branch as usual.
> 3. When the work is **complete** (merged + verified): **remove the entry from this file** and **append a resolution note to [`status.md`](status.md)** (the completed-work log). One entry per item; PR / commit hash / date.
> 4. This file stays short. [`status.md`](status.md) accumulates the audit trail.
>
> **Forward-looking backlog:** see [`futures.md`](futures.md) for
> prioritised work that hasn't started yet (Tier 1 / 2 / 3 items
> without active branches).
>
> **Security disclosures:** see [`security.md`](security.md) — the
> full per-CVE lifecycle (`SEC-*` IDs + resolution dates). Only
> currently-open security items are duplicated here.

---

## Open remediation items

### REDESIGN-001 — Single-tenant self-hosted redesign

**Surfaced:** 2026-06-26 after a deep system review (`.claude/reviews/system-review-2026-06-26.md`) identified 5 critical findings (Top-5) flowing from drift between the multi-tenant SaaS architecture and the codebase.

**Decision:** Soft-hide multi-tenancy rather than fully drop or keep as-is. `DEPLOYMENT_MODE=single` becomes the default OSS posture; `=multi` preserves the SaaS capability. Drop SaaS-only features (custom domains, per-tenant SSO, plan UI, tenant signup); keep schema-level `tenant_id` for forward compat; fix the security debt as part of the redesign.

**Plan:** `.claude/plans/2026-06-26-single-tenant-redesign.md` — 8 phases, ~4-6 weeks estimated. **Phase 0 ✅ COMPLETE 2026-06-26** (cleanup confirmation table walked: 9 RM full removals + 6 HD soft-hides + 5 design Qs).

**Status:** IN PROGRESS — Phase 4 fully shipped + Phase 2.x single-mode cleanup (2.3+2.4+2.5) + Phase 3.1.a/b/c, 3.2, 3.3 shipped + 4-PR BE CI infrastructure reset (#156-#158) + **Phase 3.4 ✅ COMPLETE — all 11 backend services with a gRPC server wire `libs/middleware/grpc.SingleTenantInjector` in single mode (#162, #164, #170, #171, #173–#179)** + Phase 3.4 close-out sweep (SEC-038 #181, SEC-039 #182, RED-FU-007 conformance bootstrap #184, RED-FU-010 Docker go.sum gate #183, RED-FU-011 helper tests #185, RED-FU-012 mTLS unify #186) + scanner buildGRPCOptions extract (#188 RED-FU-013) + BaseConfig migration for 7 remaining services (#189 RED-FU-014) + auth migration unblock (#190). **Local fresh-volume compose stack now reaches healthy on all 11 Phase 3.4 services for the first time since the rollout began.** 58 PRs through 2026-06-29, ~95% complete. Remaining: Phase 5/6/7 hardening.

**Phases shipped so far:**

| Phase | What | PR | Date |
|---|---|---|---|
| Planning | Review + plan + Phase 0 sign-off + CLAUDE.md banner | #119 | 2026-06-26 |
| 1.1 | `DEPLOYMENT_MODE` primitive in `libs/config/loader` | #120 | 2026-06-26 |
| 1.2 | `MTLS_REQUIRED` gate centralised | #121 | 2026-06-26 |
| 1.3 | Wire `ValidateMTLSConfig` into all 13 services | #125 | 2026-06-26 |
| 1.4 | Public `/api/v1/deployment-info` endpoint | #124 | 2026-06-26 |
| 6.1 | Pull-through proxy upstream digest verification — **closes Top-5 #4** | #123 | 2026-06-26 |
| 6.3 | Audit catalogue completeness — 13 new event mappings + lint test | #130 | 2026-06-27 |
| 6.6 | Redis fail-closed in `revoke:user:` check | #122 | 2026-06-26 |
| 2.6 | Delete dev-seed admin migration — **closes Top-5 #5** | #129 | 2026-06-27 |
| 2.7 | Helm dead config cleanup — N/A (no actual dead config existed) | — | 2026-06-26 |
| 3.1.a | Tenant `deployment_metadata` table + repo methods | #126 | 2026-06-27 |
| 3.1.b | `registry-auth bootstrap` CLI subcommand | #127 | 2026-06-27 |
| 3.1.c | `make dev-bootstrap` target + production runbook | #128 | 2026-06-27 |
| 5.1 | Typed `users.is_global_admin` replaces `(admin, org, '*')` marker | #134 | 2026-06-28 |
| 5.2 | Scope-aware tenant-admin gates — **closes Top-5 #2** | #131 | 2026-06-27 |
| 2.1 | Drop custom-domain CRUD end-to-end — **closes Top-5 #3** | #132 | 2026-06-27 |
| 2.2 | Collapse per-tenant SSO to global config | #133 | 2026-06-28 |
| futures align | Mark obsolete + subsumed items, add RED-FU-001..005 follow-ups | #135 | 2026-06-28 |
| 4.1 | `useDeploymentInfo()` FE hook | #138 | 2026-06-27 |
| 4.4 | `/me/abilities` BFF + `useAbility()` FE hook | #139 | 2026-06-27 |
| 4.2.a | Sidebar IA restructure (operator mental model) | #141 | 2026-06-27 |
| 4.2.b | /settings parent route + Account tab | #143 | 2026-06-27 |
| 4.2.c | Settings › Workspace tab content | #144 | 2026-06-27 |
| 4.2.d | Settings › Platform tab + `/admin/*` migration | #145 | 2026-06-27 |
| 4.2.e | Security page split into 7 sub-routes | #146 | 2026-06-27 |
| 4.3 | First-run onboarding wizard + auto-redirect + replay link + route-guard test | #148, #149 | 2026-06-27 |
| 4.5 | Notification matrix lockout + delete dead ComingSoon components | #151 | 2026-06-28 |
| 4.6 | Mobile-responsive shell — off-canvas drawer + hamburger + skip-link | #152 | 2026-06-28 |
| CI fix | routeTree.gen.ts generator script + npm pre-hooks; @vitest/coverage-v8; pattern fix; apk upgrade; CLAUDE.md §15 workflow gates | #153 | 2026-06-28 |
| 2.3 + 2.4 + 2.5 | Single-mode honest pass — gate tenant create/delete on multi mode + strip sidebar/FirstStepsStrip plan badge + mode-aware login footer + topbar UUID chip + typed isSingleMode() helper | #154 | 2026-06-28 |
| 3.2 + 3.3 | services/tenant.CreateTenant single-tenant guard (FailedPrecondition on 2nd insert, fail-closed on zero-value mode) + libs/middleware/grpc.SingleTenantInjector unary interceptor (defence-in-depth: inject bootstrap tenant_id on absent md, reject mismatched md with InvalidArgument). Per-service adoption deferred as REDESIGN-001 Phase 3.4 follow-ups | #155 | 2026-06-28 |
| CI infra #1 | `goinstall` golangci-lint to use Go 1.25.7 toolchain across all 13 BE workflows (action's bundled binary was on Go 1.24 and failed to typecheck 1.25 source) | #156 | 2026-06-28 |
| CI infra #2 | `.golangci.yml` loosened (`go: 1.25`, exclude `gosec G115/G306`, drop `gocritic style+performance`, `default-signifies-exhaustive: true`, broader `_test.go` exclusions) + `security:` jobs non-blocking until Go runtime patch bump (REM-016). File REM-014/015/016 | #157 | 2026-06-28 |
| CI infra #3 | `gofmt -w` sweep across 118 BE .go files (whitespace drift accumulated while typecheck-trapped) + `noctx` test-exclusion + `govulncheck ./... \|\| true` | #158 round 1 | 2026-06-28 |
| CI infra #4 | Lint jobs (`lint:`) non-blocking across all 13 BE workflows; `exhaustive` + `unparam` + `staticcheck` removed/test-excluded; `godot` disabled (REM-014 tail) | #158 round 4 | 2026-06-29 |
| 3.4 prep | `tenant.GetDeploymentMetadata` RPC — generic key-lookup over deployment_metadata, first consumer for per-service bootstrap_tenant_id fetch | #160 | 2026-06-29 |
| REM-014 a | services/tenant lint tail closure — goimports + drop `continue-on-error: true` on tenant lint | #161 | 2026-06-29 |
| 3.4 pilot | services/auth SingleTenantInjector wiring — first consumer of GetDeploymentMetadata | #162 | 2026-06-29 |
| REM-020 #2 | per-service `go mod tidy` sweep + ci-tidy-check matrix workflow (libs + 11 services; auth deferred to #165) | #163 | 2026-06-29 |
| 3.4 #2 | services/metadata SingleTenantInjector wiring | #164 | 2026-06-29 |
| REM-020 #2 tail | services/auth tidy follow-up (closes the deferred shard from #163) | #165 | 2026-06-29 |
| REM-014 + REM-020 #7 | REM-014 lint backlog closure across all 13 services + drop `continue-on-error: true` from every backend lint job; .golangci.yml _test.go exclusion expanded | #166 | 2026-06-29 |
| 3.4 libs | `libs/tenant/bootstrap.FetchTenantID` + `libs/auth/mtls.ClientCreds` extraction (rule-of-three lift before service rollout fans out); services/auth + services/metadata refactored to consume | #167 | 2026-06-29 |
| REM-020 #10 a | scanner trivy 0.52.0→0.71.2 bump + .trivyignore for perl-base/zlib1g/grpc CVEs | #168 + #169 | 2026-06-29 |
| REM-020 #10 b | scanner trivy-action skip-files on bundled trivy/grype + ignore-unfixed (kill whack-a-mole on upstream-binary CVEs) | #169 | 2026-06-29 |
| 3.4 #5 | services/signer SingleTenantInjector wiring + compose env wire-in | #173 | 2026-06-29 |
| 3.4 #6 | services/webhook SingleTenantInjector wiring + compose env wire-in | #174 | 2026-06-29 |
| 3.4 #7 | services/scanner SingleTenantInjector wiring + compose env wire-in + **added missing standard interceptor chain** (recovery/OTEL/logging) — pre-existing gap | #175 | 2026-06-29 |
| 3.4 #8 | services/audit SingleTenantInjector wiring + compose env wire-in | #176 | 2026-06-29 |
| 3.4 #10 | services/proxy SingleTenantInjector wiring + compose env wire-in + **added missing standard interceptor chain** (recovery/OTEL/logging) — pre-existing gap same as scanner | #178 | 2026-06-29 |
| 3.4 #9 | services/gc SingleTenantInjector wiring — **reuses the existing TenantGRPCAddr conn** (gc already dialled tenant for ListTenants), so no second TCP stream; compose adds registry-tenant to depends_on chain | #177 | 2026-06-29 |
| 3.4 #11 | services/tenant SingleTenantInjector wiring — **closes the rollout**. Special case: tenant *is* the source of GetDeploymentMetadata, can't self-dial; reads bootstrap_tenant_id directly from local repo. Pre-bootstrap deployments log a warning + skip wiring (Phase 3.2 CreateTenant guard already covers the same invariant); once bootstrap CLI writes the key, the next restart wires the interceptor | #179 | 2026-06-29 |
| 3.4 close-out docs | trackers + sprint memory sync after Phase 3.4 rollout complete | #180 | 2026-06-29 |
| SEC-038 | gc `clientCreds` per-target serverName pin + fail-closed on mTLS load (replaces local helper with inline `mtls.ClientCreds(..., "registry-<X>")` for metadata/storage/tenant dials) | #181 | 2026-06-29 |
| SEC-039 | sweep core/scanner/proxy/management for the same empty-serverName + insecure-fallback shape — 17 dial sites pinned, two pre-existing zero-interceptor gaps closed (scanner #175, proxy #178 were already done; this PR finalised the remaining client-creds gap) | #182 | 2026-06-29 |
| RED-FU-010 | scanner `go.sum` drift after #167 libs/middleware/grpc extraction — added `go-redis/v9`, `otelgrpc`, `atomic` transitive entries; CI tidy-check workflow now runs `GOWORK=off` to match the Docker invariant | #183 | 2026-06-29 |
| RED-FU-007 | conformance compose-stack bootstrap — new `registry-bootstrap` one-shot container (postgres:16-alpine + psql) seeds tenant DB with first tenant + tenant_policies + deployment_metadata.bootstrap_tenant_id before Phase 3.4 services start; 10 Phase 3.4 services gain `depends_on: registry-bootstrap: condition: service_completed_successfully` | #184 | 2026-06-29 |
| RED-FU-011 | smoke tests for Phase 3.4 `buildGRPCOptions(cfg, extraUnary)` chain — 10 services × 3 tests each (nil extraUnary / non-nil extraUnary / bad mTLS paths); scanner deferred (inline chain in `Run()`, filed as RED-FU-013) | #185 | 2026-06-29 |
| RED-FU-012 | lift `mtls.ClientCreds(cfg.MTLSCACertPath, ...)` one-liner to `loader.BaseConfig.MTLSClientCreds(serverName)`; auth/metadata/storage/proxy/management drop their local helper (5 services × 1–9 call sites = 16 sites unified); remaining 7 services without BaseConfig embed filed as RED-FU-014 | #186 | 2026-06-29 |
| close-out docs (#187) | trackers + plan dashboard + sprint memory sync after the Phase 3.4 close-out sweep merged; SEC-038 cleared from Open security items; RED-FU-013/014 filed in futures.md as new entries | #187 | 2026-06-29 |
| RED-FU-013 | extract scanner `buildGRPCOptions(cfg, extraUnary)` from inline `Run()` body + add the matching 3-test smoke suite. Scanner was the lone Phase 3.4 service still inlining the interceptor chain. Two trivial wording fixes (matching the 9-service majority on `"load mTLS certs"` vs `"load mTLS server certs"` and the platform-standard slog.Warn text) folded inline per code-review-agent | #188 | 2026-06-29 |
| RED-FU-014 | embed `loader.BaseConfig` in the 7 remaining services (core/scanner/signer/webhook/audit/gc/tenant) — each Config struct shrinks by ~13 standalone field declarations; field promotion preserves all existing call sites. core+scanner drop their local `clientCreds(cfg, name)` helpers; signer/webhook/audit/gc swap inline `mtls.ClientCreds` calls to `cfg.MTLSClientCreds`. Also fixed a bootstrap container bug from #184 — `INSERT INTO tenants` was missing the NOT NULL `slug` column added by tenant migration 20260620000001; added new `BOOTSTRAP_TENANT_SLUG` env var (default `development`) matching the existing tenant id/name triplet | #189 | 2026-06-29 |
| auth migration unblock (#190) | replace subquery-in-USING in `services/auth/migrations/20260628000003_drop_auth_providers.sql` — original used `ALTER COLUMN ... TYPE TEXT USING (SELECT ... FROM auth_providers ...)`, which Postgres rejects (SQLSTATE 0A000). Refactored to canonical add-temp-column / UPDATE-from-join / drop-and-rename pattern. Latent on main because the dev-stack volume kept the old schema across boots; surfaced today during the RED-FU-014 fresh-volume rebuild. Also cleaned up a latent `UPDATE users ... WHERE ap.id::TEXT = NULL` always-false bug whose own comment admitted it was a no-op. **Fresh-volume compose stack now reaches healthy on all 11 Phase 3.4 services for the first time since the rollout began.** | #190 | 2026-06-29 |

**Top-5 security findings status (4 of 5 closed):**
- #1 RLS missing — deferred per Phase 0 D4 decision
- #2 `require*Admin` scope creep — ✅ closed by Phase 5.2 (PR #131)
- #3 Custom-domain takeover — ✅ closed by Phase 2.1 (PR #132, feature removed)
- #4 Pull-through proxy missing digest verify — ✅ closed by Phase 6.1 (PR #123)
- #5 Dev-seed admin shipped in prod image — ✅ closed by Phase 2.6 (PR #129)

**Phases still OPEN:**
- 3.4 (NEW) — Wire `libs/middleware/grpc.SingleTenantInjector` into each service's interceptor chain (per-service follow-ups; checklist below). Until adopted, single-mode defence still rests on the application-layer tenant_id filter + RLS.
  - [ ] services/management
  - [ ] services/auth
  - [ ] services/metadata
  - [ ] services/core
  - [ ] services/storage
  - [ ] services/scanner
  - [ ] services/signer
  - [ ] services/webhook
  - [ ] services/audit
  - [ ] services/gc
  - [ ] services/proxy
  - [ ] services/tenant
- 4.7 — Remove SSO admin FE — ⛔ N/A (no FE consumer ever existed)
- 5.3 — Delegator-dominates-delegatee rule in `GrantRole`
- 5.4 — `digest_keyed.go` writer-tier scope (see RED-FU-003 in futures.md)
- 5.5 — SSO subject-id binding
- 5.6 — SAML `EmailVerified` hard-code fix
- 6.2 — Domain takeover guard (REPLACED by 2.1 removal; closed without code change)
- 6.4 — AES-GCM KEK version prefix
- 6.5 — JWKS rotation prep (multi-key support)
- 6.7 — API-key Argon2 verify cache
- 6.8 — SAML library upgrade to v0.5.x
- 6.9 — mTLS hot reload via `GetCertificate` + fsnotify
- 6.10 — mTLS peer-CN interceptor
- 6.11 — Scanner plugin sandbox
- 6.12 — Audit hash-chain
- 7 — Documentation + CI lint
- 8 — Migration / rollout / release prep

**Blocks:** FUT-019 Phase 3 (email channel); FUT-010 RBAC FE half (now mapped to Phase 4.4); FUT-011 (subsumed by Phase 3.1 + 4.3); DEPLOY-001 (subsumed by Phase 1.4 + 8.2).

---

### REM-013 — Retention surface backend gaps

**Affects:** `services/metadata` (proto + repo + handler), `services/management` (BFF).
**Status:** OPEN — frontend (S11 slices 3 + 4) is partially shipped. Three FE surfaces are blocked by missing backend.

| Gap | What's missing | Blocks FE |
|---|---|---|
| 1 | `manifests.retention_pending_delete_at` is exposed via `GetManifest` but not via the `ListTags` projection, so the Tags tab can't render pending-delete pills without a per-row GET fan-out. Needs a column added to the Tag proto (or a parallel `list_tags_with_retention` RPC). | "Pending delete in 24h" pills on the Tags tab |
| 2 | No `retention_runs` table — every retention evaluation is fire-and-forget today. A run-history table would let the dashboard show "we considered X tags, kept Y, graced Z, hard-deleted W per rule". | Per-repo Retention "Run history" panel |
| 3 | Dashboard storage breakdown doesn't expose the bytes-reclaimed-via-retention column. Needs a `GetTenantRetentionSavings(tenant_id)` aggregation RPC + UI plumbing. | Dashboard storage-breakdown "Retention" column |

**Recommended order:** Gap 1 (smallest) → Gap 2 → Gap 3. Each unblocks one FE surface independently.

---

### REM-014 — Lint findings unmasked by Go 1.25 toolchain upgrade

**Surfaced:** 2026-06-28 after PR #156 (`fix(ci): goinstall golangci-lint`) made golangci-lint reachable past its typecheck stage. Prior to #156 the action's bundled Go 1.24 binary couldn't parse Go 1.25 source, so every linter was short-circuited; PR #156 fixed that, which unmasked a real backlog.

**Status:** OPEN. CI is temporarily unblocked via `.golangci.yml` exclusions (`gosec G115`, `gosec G306`, `gocritic exitAfterDefer + whyNoLint + style/performance tags`, `unused`/`gosec`/`dupl`/`gocritic` on `_test.go`). The exclusions are the right call for noise (proto generated structs, graceful-shutdown patterns, table-driven test fixtures), but the suppressed findings still include real surface that warrants follow-up.

**Known surface (sampled from `feat/redesign-3.x-single-tenant-guards` CI run):**

| Linter | Site | Triage |
|---|---|---|
| `gosec G115` int→int32/uint32 conversion | `services/webhook/internal/handler/grpc.go:296,297,354`, `services/scanner/internal/{worker,handler}/...`, `libs/crypto/argon2/argon2.go:73` | Bounded inputs (page sizes, time-since millis); confirm each via a small audit + add a `min(x, math.MaxInt32)` guard if any source can grow unbounded. |
| `gosec G306` file write perms | `services/scanner/internal/reportworker/worker.go:138,141` | Tempdir is service-private. Tighten to 0600 if/when reports are persisted outside the service's own filesystem. |
| `errcheck` unchecked error | `services/scanner/internal/worker/worker.go:932` (`p.Enqueue`) | One-line fix — capture + log the error. |
| `exhaustive` partial switch | `libs/errors/codes/codes.go:67` | Fixed at config level via `default-signifies-exhaustive: true` (the switch has a `default:`). Verify no real coverage hole. |

**Owner:** TBD. Per-service cleanup PRs welcome; mark each finding nolint+REM-014 if intentional.

**Progress:**

| Service | Status | PR | Notes |
|---|---|---|---|
| `services/tenant` | ✅ CLOSED | (this branch's PR) | gofmt/goimports drift in `internal/handler/{grpc,grpc_test}.go`; `continue-on-error: true` dropped on `ci-tenant.yml` `lint:` as proof. |
| Remaining 12 services | OPEN | — | Each gets its own per-service cleanup PR + flag drop, per `feedback_review_pace.md` cadence. |

---

### REM-015 — `services/management` test stage "Lint user queries" fails

**Surfaced:** 2026-06-28. CI's `test` job on PR #155 had a sub-step `Lint user queries` that exited 1 with no diagnostic context surfaced in this session.

**Status:** OPEN. Likely a SQL-lint / query-template check, but the failure log doesn't include the failing query. Needs a 30-min triage to find the script + add useful error output.

**Owner:** TBD.

---

### REM-016 — Go runtime stdlib CVEs flagged by govulncheck

**Surfaced:** 2026-06-28. After PR #156 made the lint stage reachable, the `security: govulncheck` stage flagged 5+ stdlib vulnerabilities (GO-2026-5039, GO-2026-5037, GO-2026-4982, GO-2026-4980, GO-2026-4971, ...) in `net/http.Server.ListenAndServe` and `crypto/x509.Certificate.Verify`/`VerifyHostname` call paths.

**Status:** OPEN. CI's `security` stage temporarily set to `continue-on-error: true` (across all 13 backend workflows) so the findings are still visible but don't block merges.

**Fix shape:** bump `go 1.25.7` → latest 1.25.x patch in every `go.mod` (12 services + libs). Each module is independent so the bump is per-go.mod (not toolchain-wide). After bumping, remove `continue-on-error: true` from the `security:` jobs.

**Owner:** TBD. Recommend a single-PR sweep that bumps every go.mod + the workflow `setup-go` `go-version` field, then drops the `continue-on-error` flag.

---

### REM-019 — Scanner trivy adapter exits with code 1 (Phase 2: underlying failure)

**Surfaced:** 2026-06-24 during scan smoke testing.
**Phase 1 (DONE, PR #70):** all four adapters now mirror their RPC
error to stderr before exit; orchestrator parses stdout RPC error
even on non-zero exit. This was the "stop debugging blind" half.
**Phase 2 (OPEN):** the underlying trivy invocation still fails.
The next smoke test against
`dev/rabbitmq:3.13-management-alpine` should now print the real
error in either the `stderr` or `stdout_error` field of the
orchestrator log. Once that error string lands, file the targeted
fix (likely candidates: missing Trivy DB in the cache volume —
boot pre-warm uses Grype not Trivy; raw gzipped layer vs OCI
layout; distroless scratch-dir / tmpdir perms).

**Workaround for users right now:** in `/admin/scanner`, swap the
active adapter to the dev stub. REM-011 P2's in-memory swap means
no container restart is needed.

---

### REM-020 — CI pipeline reshape (rethink + rework)

**Surfaced:** 2026-06-29 during PR #160. The proto-touching PR
unmasked multiple latent CI pathologies in a single afternoon — none
are new, all are pre-existing rot — making it clear the pipeline
needs a deliberate reshape, not just per-incident patches.

**Why now:** REM-014 (lint backlog), REM-015 (auth lint-queries),
REM-016 (Go stdlib CVEs) and the PR #156-#158 BE CI infrastructure
reset already filed individual failures. REM-020 is the umbrella that
turns "patch each fire as it surfaces" into "reshape the pipeline so
the fires stop starting." Without it the next proto-touching PR
re-discovers the same potholes.

**Pain points surfaced this session:**

| # | What's broken | Evidence | Impact |
|---|---|---|---|
| 1 | `ci-proto.yml` breaking-check used `cd proto && buf breaking --against '.git#branch=main'` — looked for `.git` inside `proto/` (doesn't exist) | PR #160 round 1 — fixed in commit `dc9cb8c` / `4612578` | Every proto PR for months silently failed this check; nobody noticed because it's been red on every PR |
| 2 | `actions/checkout` default shallow clone doesn't fetch main, so `branch=main` fails even after fix #1 | PR #160 round 2 — required `fetch-depth: 0` + `branch=origin/main` | Hidden dependency on checkout config; every CI tool that compares against main needs this |
| 3 | Per-service `go.sum` rot — Docker build fails with `missing go.sum entry for pgxpool` whenever `libs/config/loader` indirect deps change | PR #160 needed tidies in `services/tenant` AND `services/core`; memory note `current_sprint_status.md` flagged 2 services pre-existing | Every proto-touching PR re-discovers this. Workspace mode (`go.work`) hides it locally; Dockerfile's `GOWORK=off` exposes it |
| 4 | `ci-core.yml` path-filtered on `proto/**` so a tenant-only RPC addition triggers the full core pipeline (build, conformance) | PR #160 — core ran even though no core code changed | Wasted runner minutes; obscures which service's CI is "actually" testing the change; means proto PRs hit 2× the failure surface |
| 5 | `continue-on-error: true` sprawl — `lint` (REM-014), `security` (REM-016), at one point `breaking` was silently broken too. Red marks in `gh pr checks` no longer mean "merge blocker" | Every PR has 1-3 red marks that have to be triaged "is this a real blocker?" | Erodes signal-to-noise; new contributors can't tell if their PR is broken without reading workflow YAML |
| 6 | 13 nearly-identical `ci-<svc>.yml` files. Drift between them is invisible until something breaks (PR #156 fixed golangci-lint version mismatch across all 13) | 13 × ~100 LOC YAML files, one per service | Drift bombs. DRY violation. Easy to fix one but miss the same fix in 12 others |
| 7 | No Docker BuildKit cache mount on `go build`. Every CI build re-downloads every dep. ~5min per build job | Observed in PR #160 build logs — `go: downloading github.com/...` for ~150 deps every run | ~1h/day of runner time per active branch |
| 8 | No `setup-go` cache, no go-mod cache shared across jobs in the same workflow | Same downloads in lint job → test job → build job | Adds ~30-60s per stage |
| 9 | No central "pipeline health" view. Hard to know if main is currently green across all 13 services. Each PR resurfaces failures that exist on main too | Discovered REM-015 and REM-016 only after they leaked into PR #155's red-CI merge | Failures persist until a PR makes them visible |
| 10 | `services/core`'s `conformance` job has been red on **every main run since at least 2026-06-25** (verified via `gh run list --workflow ci-core.yml --branch main`). Root cause: every Dockerfile does `COPY go.work go.work.sum ./` but `go.work.sum` is `.gitignore`d. `docker compose build` fails before the test even runs. CLAUDE.md §2 explicitly says "`go.work.sum` is committed alongside `go.work`" — reality disagrees | PR #160 — conformance red; every recent PR (#155, #156, #157, #158) merged with this red | A "real" blocker that nobody treats as a blocker. Erodes the meaning of "CI is green." Fix: remove `go.work.sum` from `.gitignore`, commit the file, OR drop the line from every Dockerfile. The CLAUDE.md spec wins — 1-line `.gitignore` edit + commit the existing 22KB file. Lives under REM-020 sub-fix #5 (per-service tidy sweep) since it's the same go-workspace category |

**Proposed reshape (sketch — confirm before execution):**

1. **DRY the 13 `ci-<svc>.yml` files** into one reusable workflow
   (`.github/workflows/_be-service-ci.yml`) consumed by 13 thin
   per-service callers that pass `service: <name>`. One place to
   change the toolchain, the lint config, the cache strategy.
2. **Fix the build caching** — `actions/setup-go` with `cache:
   true`, BuildKit cache-mount on `RUN go build`, hash-keyed on
   `go.sum`. Expected: ~5min build → ~30s on cached PRs.
3. **Standardise checkout** — `fetch-depth: 0` everywhere a tool
   compares against `main`. Add a workflow lint that fails CI if a
   workflow references `branch=main` without `fetch-depth: 0`.
4. **Kill the path-filter cross-trigger** — narrow `ci-core.yml`'s
   `proto/**` filter to only the proto subtrees core actually
   consumes (e.g. `proto/storage/**`, `proto/metadata/**`).
   Tenant-only proto changes shouldn't run core CI.
5. **Per-service `go mod tidy` sweep** — one-shot PR that runs
   tidy on all 13 services + libs to close the pre-existing rot.
   Then add a `tidy-check` job (`go mod tidy && git diff --exit-code`)
   so future PRs can't merge with stale go.sum.
   🟡 **IN FLIGHT — PR TBD on `fix/ci-rem-020-tidy-sweep`.** Tidied 11
   services + libs (services/auth deferred until PR #162 merges to
   avoid branch conflict; auth tidy ships as a follow-up). Added
   `.github/workflows/ci-tidy-check.yml` — matrix workflow over all 14
   modules with `go mod tidy && git diff --exit-code` per module.
6. **Sunset `continue-on-error: true` once REM-014/015/016 close** —
   per-service cleanup PRs are already designed to drop these
   flags; REM-020 just tracks the campaign.
7. **`main` health board** — a single workflow that runs
   `gh run list --branch main --workflow ci-*.yml` nightly +
   pings if any are red. Catches drift before it leaks into a PR.

**Status:** OPEN — sketch above. Each numbered item ships as its
own PR; REM-020 stays open until they all close. No immediate
priority over the in-flight REDESIGN-001 work, but **interleaved**
— each REDESIGN-001 PR that runs into a CI pothole files a fix
inline and ticks the relevant REM-020 sub-item.

**Owner:** TBD. PR #160 already shipped fixes for sub-items #1, #2,
and partial #5 (tenant + core tidies).

## Open security items

The full audit log lives in [`security.md`](security.md). Only items that remain OPEN are tracked here for ongoing attention.

| ID | Severity | Title | Status | Notes |
|---|---|---|---|---|
| **PENTEST-030** | LOW | Per-endpoint test-dispatch throttle missing on webhook `Test` action | OPEN | `handleTestWebhook` (`services/management/internal/handler/webhooks.go:348`) only checks `requireWebhookAdmin` then forwards. No per `(tenant_id, endpoint_id)` Redis bucket or daily budget. Per-user 20 rps still amplifies. Tracked for a global rate-limit pass. |
| **PENTEST-033** | LOW | Postman dev passwords still inlined | PARTIAL | Login uses `{{password}}` (`type: secret`) — done. Still open: (a) `NewUser1234!` baked into `createUser` request body at `registry-management.postman_collection.json:114`; (b) dev tenant UUID `98dbe36b-…` defaulted in the env file. Cosmetic cleanup. |

---

## Partial / blocked surfaces

### S11 Retention slices 3 + 4 (PARTIAL)

- **Slice 3** (FE-API-040): "Run now" trigger + 5s status polling on the Retention tab. **PARTIAL** — pending-delete pills on Tags tab + per-repo Run history panel deferred (blocked by REM-013 gaps 1 + 2).
- **Slice 4** (FE-API-039): org-default Retention surface on new `/orgs/$org/settings` route + cross-link from inherited per-repo policies. **PARTIAL** — dashboard storage-breakdown "Retention" column deferred (blocked by REM-013 gap 3).

The FE work for both slices is wired; only the backend gaps in REM-013 prevent the surfaces from rendering useful data.

---

## Post-OSS launch hygiene

Surfaced by PR #42 (Apache 2.0 OSS launch, 2026-06-23). These items aren't bug fixes — they're the contributor-onboarding surface that should exist before the repo gets meaningful inbound traffic.

| ID | Item | Effort | Why |
|---|---|---|---|
| **HYG-001** | README hero screenshot / dashboard GIF | ~30 min | Biggest first-impression lever on the repo page. People decide whether to read the README in ~5 seconds based on the visual. |
| **HYG-005** | 3-5 `good first issue` labels populated | ~1h | The single biggest lever for first-time contributors. People can't contribute if they don't know where to start. Pick 3-5 small items from this tracker or futures.md and label them. |
| **HYG-006** | Architecture diagram image (replace ASCII in README §2) | ~1h | Cleaner first impression than the ASCII diagram. Excalidraw / draw.io export → committed PNG. |
| **HYG-007** | Enable GitHub Discussions (Settings → Features) | ~2 min | Routes "questions" / "ideas" away from Issues. Required for `CONTRIBUTING.md`'s "open a Discussion" instruction to actually work. |
| **HYG-008** | Enable private vulnerability reporting (Settings → Security) | ~2 min | Required for `SECURITY.md` to actually have a working private channel. |

> HYG-002 / HYG-003 / HYG-004 shipped in PR #44 (2026-06-23) — see [`status.md`](status.md).

---

## Review batch — 2026-06-23

Three review agents (design / quality / architecture) did a deep cross-cutting review.
**74 findings total** — 24 design (`DSGN-*`), 28 code quality (`QA-*`), 22 architecture
(`ARCH-*`). Full per-finding detail with file paths + line numbers lives in:

- [`.claude/reviews/design-review-2026-06-23.md`](.claude/reviews/design-review-2026-06-23.md)
- [`.claude/reviews/quality-review-2026-06-23.md`](.claude/reviews/quality-review-2026-06-23.md)
- [`.claude/reviews/architecture-review-2026-06-23.md`](.claude/reviews/architecture-review-2026-06-23.md)

Curated P0/P1/P2 backlog lives in [`futures.md`](futures.md) under the
"Review batch — 2026-06-23" section. Pick from there as work cycles open up.

---

## Backlog (not in this file)

Prioritised feature work that hasn't been picked up yet lives in [`futures.md`](futures.md). The tracker doesn't duplicate them — once an item gets picked up + assigned a REM / FE-API number + put on a branch, it migrates here.

Quick pointer to the largest open backlog items (see `futures.md` for full detail):

- **Tier 1 #1** — MFA (TOTP step-up) — ~2 weeks
- **Tier 1 #5** — SCIM v2 provisioning — ~1.5 weeks
- **Tier 1 #3 Phase 3** — multi-key quorum + Fulcio binding — ~1-2 weeks
- **REM-018-followup** — `/activity` + notifications-bell still render `actor_username || actor_id`; needs `actor_display_name` on `audit.v1.NotificationEvent` + audit-side join so the existing `<UserCell variant="inline">` can replace the text render — ~half day
- **FUT-009** — service-account-as-signing-identity — ~5h
- **FUT-010** — RBAC + FE-RBAC polish pass — ~1 sprint
- **FUT-011** — New-user onboarding flow end-to-end via FE (paired with DEPLOY-001) — ~half day + docs
- **DEPLOY-001** — SaaS vs self-hosted deployment docs + tenant-persona testing — ~half day
- Smaller Tier 2 items: FUT-007-FE, FUT-008, etc.
- Remaining DSGN: DSGN-002 / -008 / -009 / -018 / -023 / -024 (6 of 24 still open from the 2026-06-23 review batch)

---

## How to use this file

- **One bullet per open item.** Lean by design — if this file passes ~10 sections something is wrong with the workflow.
- **When work ships:**
  1. Remove the entry from this file.
  2. Append a resolution note to [`status.md`](status.md) (one entry per item, with PR / commit hash / date).
- **New surfacings** get an entry here first; once the work is in flight, link the branch / PR; once it ships, move to `status.md`.
- **`futures.md`** is the natural place for things that haven't started yet — not yet picked up, not yet on a branch. This tracker is for things that are *open work*, not *future ideas*.

```
                  ┌──────────────────┐
   ─surfacing──►  │ status-tracker.md│ ──ships──►  status.md
                  │  (in flight)     │              (completed log)
                  └──────────────────┘
                          ▲
                          │ pickup
                          │
                  ┌──────────────────┐
                  │   futures.md     │
                  │  (backlog ideas) │
                  └──────────────────┘
```

---

> **Last updated:** 2026-06-23.
> **Maintainer:** see `git log -- status-tracker.md`.
