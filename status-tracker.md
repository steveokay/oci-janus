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

### REM-019 — Scanner trivy adapter exits with code 1 (empty stderr)

**Surfaced:** 2026-06-24 during scan smoke testing. Triggered scans on
`dev/rabbitmq:3.13-management-alpine` queue correctly + the scanner
service receives the event, but every plugin invocation fails with:

```
ERROR plugin process failed   path=/usr/local/bin/scanner-trivy-adapter
       stderr=""   error=exit status 1
ERROR scan job failed         error=plugin process exited with error
```

The downstream "scan stuck at pending" symptom that masked this was
fixed in PR #59 (scanner `persistScanStatus` now defaults `findings`
to `[]` so the failure status flips correctly). The underlying trivy
adapter exit-1 is still open.

**Affected:** `services/scanner` (calls `scanner-trivy-adapter` via
JSON-RPC stdio per `services/scanner/internal/plugin/process.go`).

**Likely candidates** (none confirmed yet):
- Adapter fails to read staged blobs because they're raw gzipped layer
  blobs not assembled into an OCI layout (Trivy expects an OCI image
  bundle with `index.json` + `manifest.json`).
- Trivy DB path / scratch dir permission issue inside the distroless
  scanner container.
- The Grype DB pre-warm at boot uses Grype, but the production adapter
  is `scanner-trivy-adapter` (different binary) — possible that the
  Trivy DB was never populated in the cache volume.
- Adapter swallowing its own stderr (would mask the real cause).

**Scope of fix:**
- Add stderr capture + an explicit error log inside the trivy adapter
  before any exit-1 path so we stop debugging blind.
- Verify the blob-staging step produces an OCI layout Trivy can consume.
- Confirm the cache-volume DB pre-warm matches the active adapter.

**Workaround for users right now:** in `/admin/scanner`, swap the
active adapter to the dev stub (it returns synthesised findings so the
scan flow can be exercised end-to-end without Trivy). REM-011 P2's
in-memory swap means no container restart is needed.

**Estimated:** ~half day to diagnose + ~1-2 hours to fix.

---

### REM-016 — `libs/errors/codes.MapDBError` doesn't recognise PostgreSQL error codes

**Surfaced:** 2026-06-23 (PR #32 custom-domain triage).
**Affects:** every service that catches a Postgres error and routes it through `errcodes.MapDBError` (i.e. all of them).
**Status:** OPEN. Tracker filed in PR #33.

**Why this matters:** `MapDBError` only special-cases `context.DeadlineExceeded` → `ResourceExhausted`. Everything else collapses to `codes.Internal` with the caller's fallback message. PgErr 23503 (foreign-key violation), 23505 (unique violation), 23514 (check constraint) all surface as generic 500s. Hides the actionable underlying error.

**Proposed fix:**

| PgErr code | Mapped gRPC code | Body hint |
|---|---|---|
| `23503` foreign_key_violation | `codes.NotFound` | Use `pgErr.ConstraintName` to point at the missing parent row |
| `23505` unique_violation | `codes.AlreadyExists` | e.g. "domain already registered for this tenant" |
| `23514` check_violation | `codes.InvalidArgument` | Catches `format CHECK (format IN (…))` etc. |
| everything else | `codes.Internal` (unchanged) | Same fallback contract |

**Estimated:** ~1-2h including unit tests + verification across services. No API change.

---

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
- **REM-017** — Platform-admin "claim a new org" route (chicken-egg gap, surfaced 2026-06-24) — ~1 day
- **REM-018** — UI user-ID → username (filed 2026-06-24): wire username + display_name into BFF list responses, replace UUID renders in members / activity / audit, enforce non-empty display_name on user creation — ~1-2 days
- **FUT-013** — Pull-through cache visibility (filed 2026-06-24): new sidebar menu item + `/proxy/cache` page backed by 3 new `services/proxy` RPCs (`ListCachedManifests`, `GetCacheStats`, `DeleteCachedManifest`) + `last_pulled_at` / `pull_count` columns on `proxy_manifests`. Surfaced by an operator noticing pulls through `:8084/cache/...` never appeared in the dashboard — ~1 sprint
- **FUT-012** — Tenant-user lifecycle management (filed 2026-06-24): new `'tenant'` RBAC scope + `ListTenantUsers` / `InviteUser` / `SetUserDisabled` RPCs + `/tenant/users` route shared between tenant-admin and platform-admin. Strictly precedes Tier 1 #5 SCIM. Pairs with REM-018 — ~1 sprint
- **FUT-009** — service-account-as-signing-identity — ~5h
- **FUT-010** — RBAC + FE-RBAC polish pass — ~1 sprint
- **FUT-011** — New-user onboarding flow end-to-end via FE (paired with DEPLOY-001) — ~half day + docs
- **DEPLOY-001** — SaaS vs self-hosted deployment docs + tenant-persona testing — ~half day
- Smaller Tier 2 items: FUT-007-FE, FUT-008, etc.
- Remaining DSGN: DSGN-002 / -008 / -009 / -018 / -021 / -023 / -024 (7 of 24 still open from the 2026-06-23 review batch)

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
