# Frontend Tracker

> Last updated: 2026-06-17
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
| Login | `/login` | `stitch/login/code.html` | ✅ Done — QA verified, pixel-perfect |
| Repository Dashboard | `/dashboard` | `stitch/repository_dashboard/code.html` | ✅ Done — QA verified, real data wired, pixel-perfect |
| Image Details & Tags | `/dashboard/:org/:repo` | `stitch/image_details_tags/code.html` | ✅ Done — QA verified, real data wired, pixel-perfect |
| Security Scan Results | `/dashboard/:org/:repo/security` | `stitch/security_scan_results/code.html` | ✅ Done — QA verified, real data wired, pixel-perfect |
| Build History | `/dashboard/:org/:repo/builds` | `stitch/build_history/code.html` | ✅ Done — QA verified, real data wired, pixel-perfect |

---

## Login Wiring

| Task | Detail | Status |
|---|---|---|
| Vite dev proxy | Add `server.proxy` in `vite.config.ts` — forward `/api` → `http://localhost:8080` so the browser avoids CORS in dev | ✅ |
| CORS on auth service | Proxy avoids CORS entirely in dev — no change needed on auth service for local dev | ✅ |
| `VITE_TENANT_ID` env var | Created `frontend/.env.local` with `VITE_TENANT_ID=98dbe36b-ef28-4903-b25c-bff1b2921c9e` | ✅ |
| Dev seed user | `admin` / `Admin1234!dev` was already in `20260610000001_seed_dev_tenant.sql`. New `20260618000001_seed_dev_admin_role.sql` adds the org=dev admin role so the user passes PENTEST-002 / PENTEST-003 RBAC gates. | ✅ |
| Post-login redirect | `/dashboard` loads after token stored in Zustand; TanStack Router `beforeLoad` guard verified | ✅ |
| Error states | 401 (bad creds), 403 (locked), 429 (rate limit) show toast messages; generic "Invalid credentials" copy | ✅ |

---

## Dashboard Wiring

All dashboard data wiring is complete. `services/management` REST API is live.

### Backend: Management REST API ✅

| Endpoint | Service | Status |
|---|---|---|
| `GET /api/v1/stats` | `services/management` | ✅ Live |
| `GET /api/v1/repositories` | `services/management` | ✅ Live |
| `GET /api/v1/repositories/:org/:repo` | `services/management` | ✅ Live |
| `POST /api/v1/repositories` | `services/management` | ✅ Live |
| `DELETE /api/v1/repositories/:org/:repo` | `services/management` | ✅ Live |
| `GET /api/v1/repositories/:org/:repo/tags` | `services/management` | ✅ Live |
| `DELETE /api/v1/repositories/:org/:repo/tags/:tag` | `services/management` | ✅ Live |
| `GET /api/v1/repositories/:org/:repo/tags/:tag/scan` | `services/management` | ✅ Live |
| `POST /api/v1/repositories/:org/:repo/tags/:tag/scan` | `services/management` | ✅ Live |
| `GET /api/v1/repositories/:org/:repo/tags/:tag/builds` | `services/management` | ✅ Live |
| RBAC: `GET/POST/DELETE /api/v1/orgs/:org/members` | `services/management` | ✅ Live |
| RBAC: `GET/POST/DELETE /api/v1/repositories/:org/:repo/members` | `services/management` | ✅ Live |

### Frontend: TanStack Query hooks ✅

| Hook | Calls | Status |
|---|---|---|
| `useStats()` | `GET /api/v1/stats` | ✅ Wired |
| `useRepositories(filter, page)` | `GET /api/v1/repositories` | ✅ Wired |
| `useRepository(org, repo)` | `GET /api/v1/repositories/:org/:repo` | ✅ Wired |
| `useTags(org, repo)` | `GET /api/v1/repositories/:org/:repo/tags` | ✅ Wired |
| `useScanResult(org, repo, tag)` | `GET /api/v1/repositories/:org/:repo/tags/:tag/scan` | ✅ Wired |
| `useBuilds(org, repo, tag)` | `GET /api/v1/repositories/:org/:repo/tags/:tag/builds` | ✅ Wired |

### API shape notes

- Builds endpoint returns `ApiBuildRow` (snake_case: `build_id`, `commit_hash`, `triggered_by`, `duration`, `timestamp`); `mapBuildRow()` in `builds.tsx` converts to `BuildRow` with `BuildActor` union type.
- Scan `findings` field is base64-encoded proto bytes; `decodeFindings()` in `scan.tsx` does `JSON.parse(atob(findingsJson))`.
- Stats endpoint returns `total_repos`, `daily_pulls`, `vulnerability_count`, `system_health_pct`.

---

## Auth & API Layer

| Task | Detail | Status |
|---|---|---|
| Token expiry handling — 401 redirect | Axios interceptor detects 401, clears Zustand store, redirects to `/login?reason=session_expired` | ✅ |
| Token auto-refresh (silent) | `_authenticated.tsx` schedules silent refresh 60s before expiry via `authRefreshClient` (bare axios instance, bypasses 401 interceptor). Backend: `POST /api/v1/token/refresh` on `services/auth`. | ✅ |
| `useAuth` hook | Exposes `token`, `tenantId`, `setAuth()`, `clearAuth()` from Zustand store | ✅ |
| Axios 401 interceptor | `src/lib/api/client.ts` response interceptor for global 401 handling | ✅ |
| React Query provider | `QueryClientProvider` wired in `src/main.tsx` | ✅ |
| Logout button | `handleLogout` in `_authenticated.tsx` `SideNavBar`: calls `apiClient.post('/logout')` (server revokes JTI), then `clearAuth()`, then `navigate('/login', { replace: true })`. Local clear runs even if the server call fails so the user can't be stuck half-logged-out. | ✅ |

---

## Layout & Navigation

| Task | Detail | Status |
|---|---|---|
| Top nav full-width spread | `justify-between` across full nav bar width | ✅ |
| Sidebar nav padding | `py-2.5` on each nav item | ✅ |
| Active route highlight | Driven by `useRouterState` | ✅ |
| Sidebar gap between items | `gap-[48px]` wordmark→links, `gap-[32px]` between links | ✅ |
| Logout button | DONE — see Auth & API Layer table above. Sidebar button at the bottom of `SideNavBar` with `logout` icon, calls `handleLogout`. | ✅ |
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
| FE-SEC-001 | JWT in localStorage | `login.tsx` saves `access_token` to `localStorage` — XSS can steal it. `CLAUDE-frontend.md §10` requires **memory-only** (Zustand store). Move token to Zustand; update `useAuth`, `_authenticated.tsx` beforeLoad, and the axios interceptor to read from the store instead. Also remove `localStorage.getItem('access_token')` from `index.tsx` guard. | ✅ |
| FE-SEC-002 | Auth guard reads stale localStorage | `_authenticated.tsx` and `index.tsx` both call `localStorage.getItem('access_token')` — once token is moved to Zustand this breaks. Both guards must read from the Zustand store. | ✅ |

### High — Before First Real User

| # | Item | Detail | Status |
|---|---|---|---|
| FE-SEC-003 | Content Security Policy | No CSP header on HTML responses. The nginx config (once the Docker image is built) must set `Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src 'self'` | ⬜ |
| FE-SEC-004 | CORS allowlist on management API | When the management REST API is built (see Dashboard Wiring §1) the handler must set an explicit `Access-Control-Allow-Origin: <production-domain>` — never `*`. Dev allowlist: `http://localhost:5173`. | ⬜ |
| FE-SEC-005 | Vague error messages on login | Login should never say "user not found" or "wrong password" — only "Invalid credentials". Current toast shows "Invalid username or password." which is correct, but the `root` form error must match. Confirm both paths use the same generic string. | ⬜ |
| FE-SEC-006 | No tokens in URLs | Navigation (`router.navigate`) and API calls must never put the JWT in a query parameter or URL path segment. Audit all `navigate()` calls and axios requests — token must stay in `Authorization` header only. | ⬜ |
| FE-SEC-007 | Logout clears auth state | DONE 2026-06-18. Sidebar logout button in `_authenticated.tsx` `SideNavBar` calls `apiClient.post('/logout')` (server revokes JTI in Redis), then `clearAuth()`, then `navigate('/login', { replace: true })`. Order is intentional so the local session always clears even on server-side failure. | ✅ |
| FE-SEC-008 | Global 401 handling | Axios response interceptor detects 401 on any API call, clears the Zustand auth store, and redirects to `/login?reason=session_expired`. `authRefreshClient` (bare axios instance) bypasses the interceptor to prevent infinite loop during token refresh. | ✅ |

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

- All icons on authenticated screens use **Material Symbols Outlined** — no lucide-react. Active nav icon uses `fontVariationSettings: "'FILL' 1"` for filled variant.
- Every screen has been QA-verified against its Stitch reference HTML (pixel-perfect pass 2026-06-17).
- `frontend/.env.local` is gitignored — all required vars documented in `frontend/.env.example`.
- `VITE_TENANT_ID` is the dev tenant UUID seeded in metadata migration `00002`.
- `services/management` REST API is live on `:8085`; Vite dev proxy forwards `/api` → `http://localhost:8085`.
- JWT auto-refresh: `_authenticated.tsx` schedules a timer that fires 60s before expiry; uses `authRefreshClient` (bare `axios.create()`) — NOT the shared `apiClient` — to avoid the 401 interceptor triggering on a refresh 401.
- Builds `ApiBuildRow` type: API returns snake_case (`build_id`, `commit_hash`, `triggered_by`); `mapBuildRow()` converts to camelCase `BuildRow` with `BuildActor` union `{ kind: 'user' | 'ci' }`.
- Next work: E2E Playwright tests, Docker nginx image with CSP headers (FE-SEC-003), npm audit in CI (FE-SEC-012), remaining frontend mock-content cleanup in status.md.
