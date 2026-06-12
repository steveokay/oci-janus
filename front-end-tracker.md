# Frontend Tracker

> Last updated: 2026-06-12
> Reference designs: `frontend/design/stitch/` — treat as law for every screen.
> Dev server: `cd frontend && npm run dev` → http://localhost:5173

---

## Legend

| Symbol | Meaning |
|---|---|
| ✅ | Done |
| 🔄 | In Progress |
| ⬜ | Not Started |
| 🔴 | Blocked |

---

## Screens

| Screen | Route | Reference File | Status |
|---|---|---|---|
| Login | `/login` | `stitch/login/code.html` | ✅ Done — QA verified |
| Repository Dashboard | `/dashboard` | `stitch/repository_dashboard/code.html` | ✅ UI Done — wiring pending |
| Image Details & Tags | `/dashboard/:org/:repo` | `stitch/image_details_tags/code.html` | ⬜ Not Started |
| Security Scan Results | `/dashboard/:org/:repo/security` | `stitch/security_scan_results/code.html` | ⬜ Not Started |
| Build History | `/dashboard/:org/:repo/builds` | `stitch/build_history/code.html` | ⬜ Not Started |

---

## Login Wiring

| Task | Detail | Status |
|---|---|---|
| Vite dev proxy | Add `server.proxy` in `vite.config.ts` — forward `/api` → `http://localhost:8081` so the browser avoids CORS in dev | ⬜ |
| CORS on auth service | Add `Access-Control-Allow-Origin: http://localhost:5173` to `services/auth` HTTP server | ⬜ |
| `VITE_TENANT_ID` env var | Create `frontend/.env.local` with `VITE_TENANT_ID=<dev-tenant-uuid>` — login form sends it as `tenant_id` | ⬜ |
| Dev seed user | Add seed migration / script that creates a test user + tenant so there is something to log in with | ⬜ |
| Post-login redirect | Verify `/dashboard` loads after `access_token` is stored and TanStack Router redirects | ⬜ |
| Error states | Test 401 (bad creds), 403 (locked), 429 (rate limit) — confirm toast messages | ⬜ |

---

## Dashboard Wiring

The dashboard UI is complete with mock data. Wiring it to real data requires:

### 1 — Backend: Management REST API (does not exist yet)

The existing backend only exposes OCI `/v2/` endpoints (Docker protocol) and internal gRPC.
There are **no REST endpoints** for listing repositories, tags, or scan results in JSON format.
These must be added before any frontend query hook can work.

| Endpoint to build | Service | Returns |
|---|---|---|
| `GET /api/v1/repositories` | `registry-core` or `registry-metadata` | Paginated list of repos with `name`, `is_public`, `storage_used`, `tag_count`, `pull_count`, `last_pushed_at`, `scan_status` |
| `GET /api/v1/repositories/:org/:repo` | same | Single repo detail |
| `GET /api/v1/stats` | `registry-core` | Tenant-level stats: `total_repos`, `daily_pulls`, `vulnerability_count`, `system_health_pct` |
| `GET /api/v1/repositories/:org/:repo/tags` | `registry-core` | Tag list with manifest digest, size, pushed_at — beyond the bare OCI `/v2/<name>/tags/list` |
| `GET /api/v1/repositories/:org/:repo/scan` | `registry-scanner` or `registry-metadata` | Latest scan result with severity counts |

All endpoints must:
- Require `Authorization: Bearer <jwt>` — validate via `registry-auth` gRPC
- Filter by `tenant_id` from the JWT — never return cross-tenant data
- Support `?visibility=public|private` and `?page=1&per_page=25` query params

### 2 — Backend: CORS on management endpoints

The management API will be called from the browser. Add CORS middleware to whatever service exposes these endpoints allowing `http://localhost:5173` in dev and the production domain in prod.

### 3 — Frontend: React Query hooks

| Hook | Calls | Used by |
|---|---|---|
| `useStats()` | `GET /api/v1/stats` | Stats cards (Total Repos, Daily Pulls, Vulnerabilities, System Health) |
| `useRepositories(filter, page)` | `GET /api/v1/repositories?visibility=&page=` | Repository table rows |
| `useRepository(org, repo)` | `GET /api/v1/repositories/:org/:repo` | Image Details screen |
| `useTags(org, repo)` | `GET /api/v1/repositories/:org/:repo/tags` | Image Details tag list |
| `useScanResult(org, repo)` | `GET /api/v1/repositories/:org/:repo/scan` | Security Scan Results screen |

### 4 — Frontend: Replace mock data in dashboard

| Component | Mock to replace | With |
|---|---|---|
| `StatsCards` | Hardcoded 124 / 842K / 12 / 99.9% | `useStats()` data with loading skeleton |
| `RepositoryTable` | 4 hardcoded rows | `useRepositories(activeFilter, page)` with loading state |
| Filter tabs (ALL / PUBLIC / PRIVATE) | No-op buttons | Pass `visibility` param to `useRepositories` |
| Pagination | Static 1/2/3 buttons | Driven by total count from API response |
| Row click | `console.log` only | Navigate to `/dashboard/:org/:repo` |
| "New Repository" button | No-op | Open create-repo modal (future) |
| "View Security Report" button | No-op | Navigate to `/dashboard/:org/:repo/security` for first vulnerable repo |

### 5 — Frontend: Loading and empty states

| State | Component | Behaviour |
|---|---|---|
| Loading | Stats cards | Skeleton pulse placeholders matching card dimensions |
| Loading | Table rows | 4 skeleton rows with animated shimmer |
| Empty | Table | "No repositories yet" message with "New Repository" CTA |
| Error | Stats cards + table | Error banner with retry button |

---

## Auth & API Layer

| Task | Detail | Status |
|---|---|---|
| Token expiry handling | JWT TTL is 5 min — axios interceptor detects 401, clears token, redirects to `/login` | ⬜ |
| `useAuth` hook | Expand `src/lib/auth/useAuth.ts` to expose `logout()` and `tenantId` | ⬜ |
| Axios 401 interceptor | `src/lib/api/client.ts` response interceptor for global 401 handling | ⬜ |
| React Query provider | Wire `QueryClientProvider` in `src/main.tsx` | ⬜ |

---

## Layout & Navigation

| Task | Detail | Status |
|---|---|---|
| Top nav full-width spread | `justify-between` across full nav bar width | ✅ |
| Sidebar nav padding | `py-2.5` on each nav item | ✅ |
| Active route highlight | Driven by `useRouterState` | ✅ |
| Sidebar gap between items | `gap-[48px]` wordmark→links, `gap-[32px]` between links | ✅ |
| Logout button | Clear `access_token` + redirect to `/login` | ⬜ |
| Mobile sidebar | Collapse on narrow viewports (hamburger toggle) | ⬜ |

---

## Design System

| Task | Detail | Status |
|---|---|---|
| Color tokens | Full MD3 palette in `globals.css` `@theme` | ✅ |
| Typography utilities | `text-headline-lg/md`, `text-body-md`, `text-label-caps`, `text-code-md/sm` | ✅ |
| Spacing tokens | `--spacing-gutter/sm/md/lg/xl/xs/base` | ✅ |
| Border radius tokens | `--radius-DEFAULT/lg/xl/full` (capped at 12px per Stitch) | ✅ |
| Material Symbols font | Google Fonts CDN in `index.html` | ✅ |
| Hanken Grotesk 700 | `@fontsource/hanken-grotesk/700.css` in `main.tsx` | ✅ |
| JetBrains Mono | `@fontsource/jetbrains-mono` + Google Fonts CDN | ✅ |
| Scrollbar styling | Custom thin scrollbar from reference | ⬜ |

---

## Testing

| Task | Detail | Status |
|---|---|---|
| Login unit tests | Form validation, error states, token storage | ⬜ |
| Dashboard unit tests | Stats card rendering, table rows, filter tabs | ⬜ |
| Auth hook tests | Token detection, redirect logic | ⬜ |
| E2E — login flow | Playwright: load `/`, enter credentials, land on `/dashboard` | ⬜ |
| E2E — repo table | Playwright: rows render, filter tabs switch visibility | ⬜ |

---

## Security

### Critical — Fix Now

| # | Item | Detail | Status |
|---|---|---|---|
| FE-SEC-001 | JWT in localStorage | `login.tsx` saves `access_token` to `localStorage` — XSS can steal it. `CLAUDE-frontend.md §10` requires **memory-only** (Zustand store). Move token to Zustand; update `useAuth`, `_authenticated.tsx` beforeLoad, and the axios interceptor to read from the store instead. Also remove `localStorage.getItem('access_token')` from `index.tsx` guard. | 🔴 |
| FE-SEC-002 | Auth guard reads stale localStorage | `_authenticated.tsx` and `index.tsx` both call `localStorage.getItem('access_token')` — once token is moved to Zustand this breaks. Both guards must read from the Zustand store. | 🔴 |

### High — Before First Real User

| # | Item | Detail | Status |
|---|---|---|---|
| FE-SEC-003 | Content Security Policy | No CSP header on HTML responses. The nginx config (once the Docker image is built) must set `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'` | ⬜ |
| FE-SEC-004 | CORS allowlist on management API | When the management REST API is built (see Dashboard Wiring §1) the handler must set an explicit `Access-Control-Allow-Origin: <production-domain>` — never `*`. Dev allowlist: `http://localhost:5173`. | ⬜ |
| FE-SEC-005 | Vague error messages on login | Login should never say "user not found" or "wrong password" — only "Invalid credentials". Current toast shows "Invalid username or password." which is correct, but the `root` form error must match. Confirm both paths use the same generic string. | ⬜ |
| FE-SEC-006 | No tokens in URLs | Navigation (`router.navigate`) and API calls must never put the JWT in a query parameter or URL path segment. Audit all `navigate()` calls and axios requests — token must stay in `Authorization` header only. | ⬜ |
| FE-SEC-007 | Logout clears auth state | Sidebar logout button (not yet wired) must: (1) call `POST /api/v1/logout` to revoke the JWT server-side, (2) clear the Zustand auth store, (3) redirect to `/login`. Clearing only the store without revoking leaves the token valid for its remaining 5-min TTL. | ⬜ |
| FE-SEC-008 | Global 401 handling | Axios response interceptor must detect 401 on any API call, clear the Zustand auth store, and redirect to `/login`. Without this, expired or revoked tokens leave the user on a broken authenticated page. | ⬜ |

### Medium — Before Production

| # | Item | Detail | Status |
|---|---|---|---|
| FE-SEC-009 | Refresh token in HttpOnly cookie | `CLAUDE-frontend.md §12` specifies a refresh token flow. If implemented, the refresh token must be stored in an `HttpOnly; Secure; SameSite=Strict` cookie — never in JS-accessible storage. Backend must expose `POST /api/v1/auth/refresh`. | ⬜ |
| FE-SEC-010 | Open redirect after login | `index.tsx` redirects to `/dashboard` unconditionally. If a `?redirect=` param is ever added, validate it against an allowlist of internal paths before redirecting — reject any value with `://` or leading `//`. | ⬜ |
| FE-SEC-011 | User-supplied content rendered safely | Repo names, tag names, and descriptions fetched from the API must be rendered as text (React's default), never via `dangerouslySetInnerHTML`. Audit all components that render API strings. | ⬜ |
| FE-SEC-012 | `npm audit` in CI | Add `npm audit --audit-level=high` step to `ci-frontend.yml` to catch dependency CVEs. Block the build on high/critical findings. | ⬜ |
| FE-SEC-013 | HTTPS enforcement in production | The nginx Docker image must redirect HTTP → HTTPS and set `Strict-Transport-Security: max-age=31536000; includeSubDomains`. Do not serve the app over plain HTTP in any non-dev environment. | ⬜ |
| FE-SEC-014 | `X-Frame-Options` and `X-Content-Type-Options` on frontend | nginx must serve `X-Frame-Options: DENY` and `X-Content-Type-Options: nosniff` on all responses (already done on backend via `SecureHeaders` middleware — mirror it on the frontend nginx config). | ⬜ |
| FE-SEC-015 | Sensitive data not logged | Audit `console.log` / `console.error` calls — must not log JWT values, passwords, or API key material. Remove any dev-time logging that outputs auth state. | ⬜ |

---

## Build & CI

| Task | Detail | Status |
|---|---|---|
| CI path filter | `ci-frontend.yml` triggers on `frontend/**` | ✅ |
| TypeScript clean | Zero `tsc --noEmit` errors | ✅ |
| Lint in CI | ESLint step | ⬜ |
| Docker image | Multi-stage Dockerfile (Node build → nginx serve) | ⬜ |

---

## Notes

- All icons on authenticated screens use **Material Symbols Outlined** — no lucide-react.
- Every screen must be QA-verified against its Stitch reference HTML before marking Done.
- `frontend/.env.local` is gitignored — all required vars documented in `frontend/.env.example`.
- `VITE_TENANT_ID` is the dev tenant UUID seeded in metadata migration `00002`.
- The management REST API (Dashboard Wiring §1) is the critical path blocker for all data-wiring tasks.
