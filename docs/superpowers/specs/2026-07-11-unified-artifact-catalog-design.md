# Unified Artifact Catalog (images + Helm charts) — design

> **Date:** 2026-07-11
> **Branch:** `feat/unified-artifact-catalog`
> **Status:** direction approved (mockup + rationale) → spec for review
> **Builds on:** the environments overview (`2026-07-11-environments-overview-design.md`, shipped PR #320)
> **Mockup reviewed:** unified-catalog-v1 (org cards with type split · per-env list with type filter + badges · tag detail)

## Problem

Helm charts live in a separate top-level catalog (`/helm`) while container images live under `/repositories`, even though both share the **same `org/repo/tag` namespace**. Artifact type is a **per-tag derived property** (`deriveArtifactType(config_media_type)` → `image` / `helm` / `signature` / `sbom` / `other`), not a property of the repo — a single repo can hold both an image tag and a chart tag. So splitting images and charts into two trees fights the data model, and it's inconsistent with the repo-detail **Tags** panel, which already lists all types together with per-tag pills + a filter chip row.

## Goal

Fold Helm charts into the **environments → repository → tag** structure as a filterable lens:

- Per-environment repo list gains **type filter chips** (All / Images / Charts) and a **per-row type badge** (with a "both" state for mixed repos).
- Environment overview cards gain a quiet **per-type split** ("N images · M charts").
- **`/helm` retires** as a separate catalog — it redirects into the unified structure; "Charts" becomes a filter, not a place.
- Repo detail (tags) is **unchanged** — it already does this.

Non-goal (deferred): a dedicated cross-environment "all charts in the tenant" flat list. In the environment-first IA, charts are browsed per-environment; the overview cards' chart counts are the entry points. If a global chart view is wanted later, it's a separate slice.

## Key data decision — how a repo's type(s) are known

The repo-list response does **not** currently carry artifact-type info per repo (only the `?artifact_type=` EXISTS *filter* exists). Per-row badges need the repo to report **which artifact types it contains**. So the core backend addition is:

- **`Repository.artifact_types` (repeated string)** on the metadata proto + a metadata query that computes, per repo, the DISTINCT set of derived artifact types across its manifests. A repo with only images → `["image"]`; a chart repo → `["helm"]`; a mixed repo → `["image","helm"]`.
- The BFF passes it through as `artifact_types: string[]` on the repo JSON.
- The FE renders one badge per entry (cyan Image / amber Helm chart), so "mixed" is simply two badges — no separate "mixed" enum.

The **filter** chips reuse the **existing** `?artifact_type=` param on `GET /api/v1/repositories` (already implemented) — no new filter plumbing, just the badge data.

## Approach — slices

### Slice 1 — per-repo artifact types (backend)
- Proto: add `repeated string artifact_types = 14;` to `metadata.Repository`.
- Metadata `ListRepositories` query: add a correlated aggregate returning the distinct derived types for each repo. Derivation reuses `deriveArtifactType` / the `config_media_type` → type mapping already used by the `artifact_type` EXISTS filter (`configMediaTypesFor`). Likely shape: a `LEFT JOIN LATERAL` / subquery producing `array_agg(DISTINCT <derived type>)` filtered to the known types, ordered stably.
- BFF `repoToResponse`: map to `ArtifactTypes []string \`json:"artifact_types"\``.
- Tests: metadata integration (image-only, helm-only, mixed repos → correct arrays); BFF handler test asserts the JSON array.

### Slice 2 — per-env list: type filter chips + row badges (frontend)
- `frontend/src/lib/api/types.ts` `Repository`: add `artifact_types?: ArtifactType[]`.
- `useRepositories`: already accepts `artifactType`; thread the chip selection into it (chips set `artifactType` = `all` | `image` | `helm`).
- `_authenticated.repositories.$org.index.tsx`: add the chip row (All / Images / Charts) beside the search; pass the selection to `useRepositories`.
- `RepositoriesTable`: add a **Type** column rendering a badge per `artifact_types` entry (reuse a small `ArtifactTypeBadge` component; cyan image / amber chart, matching the tag-panel pills' semantics).
- Tests: chips filter the list (drive `artifactType`); badge renders one/two pills; mixed repo shows both.

### Slice 3 — overview card type split (backend + frontend)
- Metadata `ListOrgSummaries`: add `image_repo_count` + `helm_repo_count` (repos containing ≥1 of that type; a mixed repo counts in both) via `COUNT(...) FILTER (WHERE EXISTS ...)` or per-type EXISTS subqueries, tenant-scoped.
- Proto `OrgSummary`: `int64 image_repo_count = 6; int64 helm_repo_count = 7;`.
- BFF `OrgSummaryResponse` + FE `OrgSummary`: `image_repo_count` / `helm_repo_count`.
- `OrgCard`: add the quiet split line ("N images · M charts") under the three metrics, only when either count > 0.
- Tests: aggregate counts (mixed repo counted in both); card renders the split.

### Slice 4 — retire `/helm` as a catalog (frontend)
- Replace the `/helm` route with a redirect to `/repositories` (the environments overview). Keep the Helm install/pull helper affordances where they live on repo/tag detail (unaffected).
- Update the sidebar: remove the separate "Helm" catalog entry (the operator reaches charts via the environment view + Charts filter). Keep any "Charts" quick-filter entry only if it adds value — decision: **remove the standalone item**; charts are reached through the environment view.
- `?type=` deep links that previously pointed at `/helm` → `/repositories/$org?type=helm` where an org is known; the bare `/helm` → overview.
- Tests: `/helm` redirects; sidebar no longer renders the Helm catalog item.

## Data flow

```
/repositories                 → GET /api/v1/orgs           (+ image_repo_count / helm_repo_count)   [Slice 3]
   │ card
   ▼
/repositories/$org?type=helm  → GET /api/v1/repositories?org=<org>&artifact_type=helm               [Slice 1+2]
   │   rows carry artifact_types[] → cyan/amber badges; chips drive artifact_type
   ▼
/repositories/$org/$repo      → tag list w/ per-tag pills  (UNCHANGED — already unified)
```

## Visual language (from the approved mockup)
- **Image** = cyan badge; **Helm chart** = amber badge; both semantic, distinct from the blue interaction accent.
- A mixed repo shows both badges (row) / both counts (card).
- Filter chips mirror the tag-panel chip pattern; "All" is default.

## Error / edge handling
- Repo with no manifests (freshly created) → `artifact_types: []` → no badge (or a muted "empty" — decision: **no badge**, consistent with the tag view showing nothing until first push).
- `other`/`signature`/`sbom` types: v1 chips are **All / Images / Charts** only; other derived types still appear in `artifact_types` but have no chip and render with a neutral badge (or are omitted from the row badges — decision: **omit non-image/helm from row badges in v1**, since the catalog is image+chart focused; revisit if SBOM/signature deserve first-class surfacing).

## Testing
- Backend: metadata integration (artifact_types array + per-org type counts across image-only/helm-only/mixed seed); BFF handler tests (repo JSON array, org summary counts, `?artifact_type=` still filters).
- Frontend: vitest — chips drive the filter, row badge renders per type incl. mixed, card split renders, `/helm` redirects, sidebar item removed.
- Gates: backend Makefiles + `make openapi` regen (new response fields); FE 4 gates.

## Out of scope (deferred)
- Cross-environment "all charts" global list (env-first covers it; overview counts are entry points).
- First-class SBOM/signature filter chips (only image/helm in v1).
- FUT-077 cross-environment promotion/drift matrix (unchanged, still deferred).

## Open decisions folded in (override in review if wrong)
1. `/helm` **redirects** and the standalone sidebar item is **removed** (charts reached via environment view). ← the main reversible call.
2. Overview card split (Slice 3) is **included** (it was in the approved mockup) — but it's the most backend-heavy slice; it can be dropped to a fast-follow without affecting Slices 1–2 + 4.
3. Non-image/helm derived types are **not** badged in the row in v1.
