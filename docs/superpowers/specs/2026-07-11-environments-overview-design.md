# Environments-first repository navigation — design

> **Date:** 2026-07-11
> **Branch:** `feat/environments-overview`
> **Status:** approved (brainstorm) → ready for implementation plan
> **Deferred sibling:** cross-environment comparison matrix → `futures.md` **FUT-077**

## Problem

`/repositories` is a single flat table of `org/repo` rows
(`frontend/src/routes/_authenticated.repositories.index.tsx` +
`components/repositories/repositories-table.tsx`). It is cursor-paginated
with a client-side "load more", and — critically — **search and sort only
operate on already-loaded pages** (there is no server-side search/sort). As a
catalogue grows, the flat list gets progressively less usable, not just
visually crowded.

Operators in the target deployments use **orgs as environments**
(`dev` / `stage` / `prod`): a small number of orgs, each holding many repos.
A flat catalogue buries that structure and offers no per-environment glance
(how big is `prod`, when was it last pushed to).

## Goal

Replace the flat landing with an **environments overview** — a card per org —
that drills into a per-environment repository list. Each list is scoped to one
org, so the existing per-page search/sort limitation stops mattering (you're
never sorting the whole catalogue at once).

Non-goal (explicitly deferred to **FUT-077**): comparing the *same image*
across environments (promotion status — "is `prod/api` behind `dev/api`?").
That wants an image-centric matrix and backend data we don't expose yet. We
build the drill-down now with a clean seam so the matrix can be added later.

## Approach

Three navigation levels instead of two.

### Routing (TanStack file-based routes)

| Route | Today | After |
|---|---|---|
| `/repositories` (`_authenticated.repositories.index.tsx`) | flat table of all repos | **environments overview** — grid of org cards |
| `/repositories/$org` (**new** `_authenticated.repositories.$org.index.tsx`) | — | **per-environment repo list** — today's flat table, pre-filtered to one org |
| `/repositories/$org/$repo` + tag routes | unchanged | unchanged |

TanStack resolves `$org/index` vs `$org/$repo` cleanly (index segment vs
child param), so the new middle route does not collide with the existing
repo-detail route.

### Environments overview (`/repositories`)

- Fetches the new `GET /api/v1/orgs` endpoint (see Backend).
- Renders a responsive grid of **org cards**. Each card:
  - **Title:** the org name (reads naturally as `dev` / `stage` / `prod`) + an icon.
  - **Metrics (v1, exactly three):** repo count · total storage used ·
    last activity timestamp.
  - **Click target:** whole card → `/repositories/$org`.
- **Toolbar:** keep a search box that filters the *cards* by org name. (The
  visibility filter and the old repo-name search move down to the per-org
  list, where they already make sense.)
- **"Create repository":** present on the overview (opens the existing
  `CreateRepositoryDialog` with no org pre-selected) **and** on the per-org
  list (pre-selects that org). Both entry points, per approved design.
- **Single-org shortcut:** when `orgs.length === 1`, `/repositories`
  redirects straight to `/repositories/$org` so a small deployment never sees
  a lonely one-card page.
- **Empty state:** zero orgs → existing "No repositories yet / create your
  first repository" empty state.
- **Error state:** reuse `ErrorState` with retry, matching the current page.

### Per-environment list (`/repositories/$org`)

- Reuses `RepositoriesTable` **unchanged**, plus the existing
  `RepositoriesToolbar`, `CreateRepositoryDialog`, empty/error states.
- Fetches repositories via the **existing** `useRepositories` infinite query,
  narrowed to the one org. Two implementation options for the plan to pick:
  1. add an optional `org` filter param to `GET /api/v1/repositories` (BFF
     already scopes by org elsewhere), or
  2. keep the existing endpoint and filter client-side within the page.
  Preference: **option 1** (server-side `org` filter) so per-env pagination
  is genuinely scoped and the search/sort-covers-loaded-pages caveat shrinks
  to one environment. The plan confirms feasibility against the metadata RPC.
- Breadcrumb / header: "Environments / `<org>`" with a back affordance to
  `/repositories`.
- Row links continue to `/repositories/$org/$repo` — unchanged.

### Backend: `GET /api/v1/orgs`

The one non-frontend piece. `org` is **already a first-class scope** in the
management BFF (`POST /api/v1/orgs/{org}/scan`, org-level retention, org-level
security policies), so this fits an established pattern — it's the *listing*
that's missing.

- **Route:** `GET /api/v1/orgs` in `services/management` (new handler,
  alongside the repository handlers in `internal/handler`).
- **Response:** unpaginated (few orgs by design):
  ```json
  { "orgs": [
    { "org": "dev",  "repo_count": 42, "storage_used_bytes": 12884901888, "last_activity_at": "2026-07-10T18:03:00Z" },
    { "org": "prod", "repo_count": 40, "storage_used_bytes": 20401094656, "last_activity_at": "2026-07-11T09:12:00Z" }
  ] }
  ```
- **Data source:** a `GROUP BY org` aggregate over the tenant's repositories
  in `registry-metadata` (repo count + `SUM(storage_used_bytes)` +
  `MAX(last_activity)`), tenant-scoped. The plan decides whether this is a new
  metadata RPC (e.g. `ListOrgs`) or an aggregate the BFF composes — preference
  is a dedicated metadata RPC so the aggregation runs in SQL, not in the BFF.
- **Why an endpoint rather than client-side derivation:** deriving org cards
  from the paginated repo list would require draining the entire catalogue to
  know every org and its rollups — reintroducing the exact scaling problem
  this feature removes.
- **Auth / tenancy:** same `authMW` + tenant injection as the sibling
  repository routes; `single`-mode injector applies unchanged.

### Naming / vocabulary

Keep the sidebar label **"Repositories"** and keep `org` as the term in code,
routes, and API. The cards *read* as environments only because the user names
their orgs that way — we do **not** bake "environment" into the platform
vocabulary, which would be wrong in `multi` mode where orgs are teams/tenants.

## Data flow

```
/repositories        ──► GET /api/v1/orgs ──► metadata ListOrgs (GROUP BY org)
   │ (card click)
   ▼
/repositories/$org   ──► GET /api/v1/repositories?org=<org> ──► metadata (existing, org-scoped)
   │ (row click)
   ▼
/repositories/$org/$repo  (unchanged)
```

## Error handling

- Overview: `GET /orgs` failure → `ErrorState` + retry (mirrors today's list
  page).
- Per-org list: unchanged from today's repository list behaviour.
- Unknown org in URL (`/repositories/does-not-exist`): the repo list returns
  empty → show the existing empty state (no hard 404 needed; an org is just a
  namespace prefix).

## Testing

- **Frontend (vitest):**
  - overview renders one card per org with correct metrics;
  - card click navigates to `/repositories/$org`;
  - single-org deployment redirects overview → `/repositories/$org`;
  - zero-org empty state;
  - card search filters by org name.
- **Backend (Go):**
  - `GET /api/v1/orgs` aggregation correctness (counts, storage sum, last
    activity) + tenant isolation (no cross-tenant leakage);
  - org-scoped `GET /api/v1/repositories?org=` returns only that org's repos
    (if option 1 is chosen).
- **Workflow gates:** frontend runs all four CI equivalents (lint / typecheck
  / test / build) before push; touched Go service runs its Makefile target.

## Out of scope (deferred → FUT-077)

- Cross-environment comparison matrix (rows = images, columns =
  `dev`/`stage`/`prod`, cells = promotion/drift status).
- Server-side repo **search** (the per-org narrowing already shrinks the
  loaded-pages-only caveat; global search stays FE-API-future).
- Per-environment vulnerability-posture rollup on the cards (easy follow-up
  once the card exists).
