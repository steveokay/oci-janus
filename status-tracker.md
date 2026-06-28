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

**Status:** IN PROGRESS — Phase 4 fully shipped + Phase 2.x single-mode cleanup batch (2.3 + 2.4 + 2.5) shipped + CI pipeline fixed. 33 PRs through 2026-06-28, ~80% complete.

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

**Top-5 security findings status (4 of 5 closed):**
- #1 RLS missing — deferred per Phase 0 D4 decision
- #2 `require*Admin` scope creep — ✅ closed by Phase 5.2 (PR #131)
- #3 Custom-domain takeover — ✅ closed by Phase 2.1 (PR #132, feature removed)
- #4 Pull-through proxy missing digest verify — ✅ closed by Phase 6.1 (PR #123)
- #5 Dev-seed admin shipped in prod image — ✅ closed by Phase 2.6 (PR #129)

**Phases still OPEN:**
- 3.2 — Single-tenant guard in tenant gRPC `CreateTenant`
- 3.3 — Tenant context middleware (single-mode injector)
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
