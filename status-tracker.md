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

### REM-021 — Multi-arch image-index child manifests aren't tracked as reachable

**Surfaced:** 2026-06-25 from the same docker push smoke that surfaced
REM-020. Two operator-visible symptoms, one underlying data-model gap.

**Symptoms:**

1. **Tag size_bytes undercounts multi-arch images.** A docker push of a
   multi-arch image creates one manifest index (`media_type:
   application/vnd.oci.image.index.v1+json`) plus N per-platform child
   manifests. The index's `image_size_bytes` is parsed from
   `manifests[].size` in the raw JSON — but those `.size` values are
   the *child manifest doc sizes* (~400 bytes each), not the layer
   totals. In the smoke push the tag detail page showed 3340 bytes
   while the actual content was ~123 MB across the children.

2. **Retention picks "layers" not tags.** The retention evaluator
   classifies each manifest row in isolation. For a multi-arch push
   the parent index has `tags=[latest]` (protected); the N children
   have `tags=[]` and look dangling. The `dangling_grace_days` rule
   (and `max_age_days` for old children) marks the platform manifests
   for delete even though the tagged index still references them. UI
   reads as "retention is picking individual arches/layers inside my
   tag."

**Root cause:** the data model treats each row in `manifests` as
independent. The parent↔child relationship of an OCI image index +
Docker manifest list isn't persisted — child reachability via a
tagged index is invisible to `services/metadata`. Confirmed in
postgres: the smoke push left 3 rows (index + 2 children); the two
children had `tag_count=0` even though they're reachable via the
tagged parent.

**Affects:** `services/metadata` (proto + migration + repo + eval),
`services/management` (storage breakdown card). No FE change required —
both symptoms become correct the moment the backend exposes the right
numbers.

**Locked design (~1-2 days):**

1. New `manifest_children(repo_id, tenant_id, parent_digest,
   child_digest)` table. PRIMARY KEY (parent_digest, child_digest,
   tenant_id). FOREIGN KEY to `manifests(digest)`. Indexes on
   `(child_digest, tenant_id)` for the reachability lookup.
2. `PutManifest` parses `manifests[].digest` out of the raw JSON when
   `media_type` is `image.index.v1+json` /
   `manifest.list.v2+json` and inserts a row per child. Idempotent
   upsert mirrors the existing `INSERT … ON CONFLICT DO UPDATE` shape.
3. `loadManifestsForEval` extends its tag aggregate to include
   "tagged-by-parent" tags via a `LEFT JOIN manifest_children mc ON
   mc.child_digest = m.digest + LEFT JOIN tags pt ON pt.repo_id =
   mc.repo_id AND pt.manifest_digest = mc.parent_digest`. A child
   manifest's effective tag set is `tags ∪ parent_tags`.
4. `parseImageSize`: when the manifest is an index, walk
   `manifests[].digest` and look up each child's `image_size_bytes`
   (or sum at upsert time after the children land — race-safe via
   a re-derive on the parent insert).
5. Existing single-arch images (no index → no `manifest_children`
   rows) classify identically to today's behaviour. Zero migration
   risk on existing data.

**Tests:** unit tests for the parser path; integration test that
pushes a multi-arch fixture and asserts (a) parent tag size = sum of
child layer totals and (b) retention dry-run with
`dangling_grace_days=1, max_age_days=30` leaves both children
protected. Both invariants must hold across PutManifest + ListTags +
EvaluateRetention paths.

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
- **FUT-012** — Tenant-user lifecycle management (filed 2026-06-24): new `'tenant'` RBAC scope + `ListTenantUsers` / `InviteUser` / `SetUserDisabled` RPCs + `/tenant/users` route shared between tenant-admin and platform-admin. Strictly precedes Tier 1 #5 SCIM — ~1 sprint
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
