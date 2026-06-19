# Frontend — Build Tracker

> Living tracker for the frontend rebuild on branch `feat/frontend-rebuild`.
> Started 2026-06-19. Owner: AI-assisted build. Aesthetic codename: **Beacon**.

---

## Design direction (locked)

- **Beacon** — light-primary with full dark-mode parity, deep teal (`#0D9488`) accent, warm amber (`#D97706`) secondary highlight.
- Typography: **Fraunces** (display numbers), **Inter** (UI), **JetBrains Mono** (digests / code).
- Cards + tables both used freely; data density is comfortable, not cramped.
- Motion is purposeful: number count-ups, staggered card entrance, scan-pulse, quota bar fill. Never decorative.
- Every data surface ships with skeleton / empty / error / loaded states. No "—" fallbacks.

## Stack (locked)

| Concern | Choice |
|---|---|
| Framework | React 19 |
| Build | Vite 6 |
| Router | TanStack Router (file-based) |
| Data | TanStack Query v5 |
| Forms | react-hook-form + zod |
| UI primitives | Radix + shadcn-style wrappers |
| Styling | Tailwind v4 (CSS-first theme tokens) |
| Charts | Recharts |
| Icons | lucide-react |
| Toasts | sonner |
| State | zustand (auth store, memory-only JWT) |
| HTTP | axios with 401 interceptor + silent JWT refresh |
| Fonts | `@fontsource-variable/inter`, `@fontsource-variable/fraunces`, `@fontsource/jetbrains-mono` |

## Backend wiring map

| API surface | Host (dev) | Mounted at |
|---|---|---|
| Management API (BFF) | `http://localhost:8091` | `/api/v1/*` |
| Auth (login, JWKS, API keys, refresh) | `http://localhost:8080` | `/api/v1/*` (login, refresh) + `/auth/*` |
| Gateway (TLS, prod-style routing) | `https://localhost:8443` | not used in dev |

Vite dev proxy: `/api/v1/*` → `:8091`, `/auth/*` → `:8080`.

---

## Sprints

| # | Title | Status | Key surfaces |
|---|---|---|---|
| S0 | Foundation | DONE ✅ | bootstrap, design tokens, auth store, API client, AppShell, login |
| S1 | Dashboard & Repositories | DONE ✅ | `/`, `/repositories`, `/repositories/:org/:repo` |
| S2 | Tags & Image detail | DONE ✅ | `/repositories/:org/:repo/tags/:tag`, scan result, build history |
| S3 | Security & Activity | DONE ✅ | `/security` (tabs), `/activity` |
| S4 | RBAC & Members | NOT STARTED | `/orgs/:org/members`, repo members tab on repo detail |
| S5 | Webhooks | NOT STARTED | `/webhooks`, create/edit, delivery log, test, rotate-secret |
| S6 | Platform Admin | NOT STARTED | `/admin/tenants`, tenant CRUD + quota |
| S7 | Profile & API keys | NOT STARTED | `/profile`, API key CRUD, password change (stubbed if NOT STARTED) |
| S8 | Polish pass | NOT STARTED | dark-mode QA, a11y audit, responsive QA, motion review |

### S0 — Foundation

- [x] `frontend/package.json` + lockfile
- [x] Vite + TypeScript + Tailwind v4 wiring
- [x] Tailwind theme: light + dark tokens, Beacon palette
- [x] Global fonts wired (Inter, Fraunces, JetBrains Mono)
- [x] TanStack Router file-based scaffold
- [x] TanStack Query client + devtools
- [x] `apiClient` axios wrapper with 401 interceptor
- [x] `authStore` zustand store (memory-only JWT)
- [x] Silent JWT refresh 60s before expiry
- [x] Login route (`/login`) — form + submit + redirect
- [x] `_authenticated` layout route — guard + AppShell
- [x] AppShell — Sidebar + Topbar + content slot
- [x] Base UI primitives: Button, Input, Label, Card, Skeleton, EmptyState, ErrorState, Badge
- [x] Dockerfile + nginx.conf for prod build
- [x] `.env.example`, `.gitignore`, `.dockerignore`
- [x] Build passes `npm run build` + `npm run typecheck` + `npm run lint`
- [x] SSO sign-in section on `/login` — Google / GitHub / Microsoft / SAML buttons; brand SVG icons inline; clicks toast "coming with next release" pending backend Sprint 1a wiring

### S1 — Dashboard & Repositories

- [x] Shared API types (Repository, Tag, StatsResponse, ListReposResponse, ScanResult, BuildRecord)
- [x] `useStats`, `useRepositories` (infinite cursor), `useRepository`, `useCreateRepository`, `useDeleteRepository`, `useTags`, `useDeleteTag`
- [x] Format helpers — `formatBytes`, `formatRelativeDate`, `formatAbsoluteDate`, `formatCompactNumber`, `pullCommand`
- [x] `AnimatedNumber` (framer-motion spring count-up)
- [x] Table, Dialog, Progress, Tabs, Switch, CopyButton primitives
- [x] Dashboard hero — greeting, KPI grid, storage quota visualization, system health card with status pill, Quick Actions ribbon
- [x] `/repositories` — toolbar (search + visibility filter + create), table, pagination (load more), skeleton/empty/error states
- [x] `CreateRepositoryDialog` — zod form (org + repo regex), public/private Switch with inline explanation
- [x] `/repositories/:org/:repo` — breadcrumb, header card with delete affordance, pull-command card, tabs (Tags / Members / Settings stubs)
- [x] TagsPanel — table with name pill, digest with copy, size, relative date; skeleton/empty/error states
- [x] `DeleteRepositoryDialog` — type-`org/repo`-to-confirm guard
- [x] Build + typecheck + lint pass

### S2 — Tags & Image detail

- [x] API hooks — `useScan` (auto-poll while pending/running), `useTriggerScan`, `useBuilds`
- [x] Severity primitives — `SeverityBar` stacked horizontal bar with 2px floor + `SeverityLegend` for counts
- [x] `parseFindings` for the Trivy `findings_json` payload (forgiving — every field optional)
- [x] Tag detail route `/repositories/:org/:repo/tags/:tag` — breadcrumb back through repo, identity card with monospace digest + copy, pull command for `org/repo:tag`, Rescan + Delete action ribbon
- [x] Repo detail Tags table rows now navigate to the new tag detail page
- [x] ScanPanel — five distinct states: not-yet, pending (pulse badge), running (pulse badge), failed (with retry CTA), complete (clean / warning / danger top-border + findings table). Findings table shows severity badge, CVE id + title + reference link, package + installed version, fixed version
- [x] BuildTimeline — vertical timeline rail with success/failure dots, triggered_by, duration, occurred_at, relative + absolute date tooltip
- [x] DeleteTagDialog — type-tag-name-to-confirm
- [x] FE-API-002 (layers) and FE-API-003 (signing) tabs render explicit "arrives with X" placeholders so the surface is honest
- [x] Build + typecheck + lint pass

### S3 — Security & Activity

> Most backend endpoints in this domain are explicitly NOT STARTED (FE-API-014..020,
> FE-API-008). Strategy: build the `/security` IA with sub-tabs + a polished Overview
> using what `/stats` already gives us; tabs for vulnerabilities / scans / remediation /
> policies render branded empty states pointing at the exact API id they'll consume
> when the backend ships. `/activity` ships as a single stub for FE-API-008.

- [x] `/security` route — sub-tabs: Overview / Vulnerabilities / Scans / Remediation / Policies
- [x] Reusable `ComingSoon` primitive — apiId chip + dotted-grid wash + highlight bullets, used per tab
- [x] Header tile — total open findings + real severity bar (FE-API-016 just shipped backend-side)
- [x] Overview tab — full severity breakdown card (SeverityBar + SeverityLegend) + FE-API-020 coming-soon panel for scan coverage / freshness
- [x] Dashboard vulnerability tile now renders a mini SeverityBar (FE-API-016 wired)
- [x] Tab stubs keyed to FE-API-014/015/017/018 with concrete "what this will show" copy
- [x] `/activity` route — header + activity-stream card with sketched preview rows + FE-API-008 badge
- [x] Build + typecheck + lint pass

> **Backend follow-up surfaced this sprint:** `types.ts` now also includes
> `Repository.description` (FE-API-006 done). The repository detail page should
> render this on a tab in S4 follow-up — out of scope here, tracked in S4 checklist.

### S4..S8 — checklist deferred until each sprint kicks off

---

## Backend endpoint dependencies

### Implemented (wire-and-go)

- `POST /api/v1/login`, `POST /api/v1/logout`, `POST /api/v1/token/refresh`
- `POST /api/v1/apikeys`, `GET /api/v1/apikeys`, `DELETE /api/v1/apikeys/{id}`
- `GET /api/v1/stats`
- `GET/POST /api/v1/repositories`, `GET/DELETE /api/v1/repositories/{org}/{repo}`
- `GET/DELETE /api/v1/repositories/{org}/{repo}/tags[/{tag}]`
- `GET/POST /api/v1/repositories/{org}/{repo}/tags/{tag}/scan`
- `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/builds`
- `GET/POST/DELETE /api/v1/orgs/{org}/members`
- `GET/POST/DELETE /api/v1/repositories/{org}/{repo}/members`
- `GET/POST/PATCH/DELETE /api/v1/webhooks[/{id}]` + `/deliveries`, `/test`, `/rotate-secret`
- `GET/POST/DELETE /api/v1/admin/tenants[/{id}]` + `/quota`

### NOT STARTED on backend (frontend stubs with "coming soon" panels + TanStack Query fetchers ready to swap)

- `FE-API-002` per-tag manifest detail (layers)
- `FE-API-003` per-tag signing verification
- `FE-API-004` per-repo activity feed
- `FE-API-006` repo description / README
- `FE-API-007` per-tenant registry hostname
- `FE-API-008` notifications stream
- `FE-API-009` workspace metadata
- `FE-API-011..013` `/users/me` GET / PATCH / password change
- `FE-API-014..020` security overview / vulnerability list / scan history / remediation / policies / reports / overview snapshot

---

## How to resume

```bash
cd frontend && npm install
npm run dev    # → http://localhost:5173
```

Backend stack: `docker compose -f infra/docker-compose/docker-compose.yml up -d`
