# Frontend Task Tracker

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
| Repository Dashboard | `/dashboard` | `stitch/repository_dashboard/code.html` | ✅ Done — QA verified |
| Image Details & Tags | `/dashboard/:org/:repo` | `stitch/image_details_tags/code.html` | ⬜ Not Started |
| Security Scan Results | `/dashboard/:org/:repo/security` | `stitch/security_scan_results/code.html` | ⬜ Not Started |
| Build History | `/dashboard/:org/:repo/builds` | `stitch/build_history/code.html` | ⬜ Not Started |

---

## Login Wiring (make login functional end-to-end)

| Task | Detail | Status |
|---|---|---|
| Vite dev proxy | Add `server.proxy` in `vite.config.ts` to forward `/api` → `http://localhost:8081` so the browser doesn't hit CORS on the dev server | ⬜ |
| CORS on auth service | Add `Access-Control-Allow-Origin: http://localhost:5173` headers to `services/auth` HTTP server | ⬜ |
| `VITE_TENANT_ID` env var | Create `frontend/.env.local` with `VITE_TENANT_ID=<dev-tenant-uuid>` — login form reads this and sends it as `tenant_id` | ⬜ |
| Dev seed user | Add a seed migration or script that creates a test user + tenant so there is something to log in with | ⬜ |
| Post-login redirect | Verify `/dashboard` loads correctly after `access_token` is stored and TanStack Router redirects | ⬜ |
| Error states | Test 401 (bad credentials), 403 (locked), 429 (rate limit) — confirm toast messages match | ⬜ |

---

## Auth & API Layer

| Task | Detail | Status |
|---|---|---|
| Token refresh / expiry | JWT TTL is 5 min — detect 401 on any API call, clear token, redirect to `/login` | ⬜ |
| `useAuth` hook | Expand current stub (`src/lib/auth/useAuth.ts`) to expose `logout()` and `tenantId` | ⬜ |
| Axios interceptor | Add response interceptor to `src/lib/api/client.ts` to handle 401 globally | ⬜ |
| React Query setup | Wire `QueryClientProvider` for data fetching on the dashboard and detail screens | ⬜ |
| API hooks — repos | `useRepositories()` — `GET /api/v1/repos` paginated | ⬜ |
| API hooks — image details | `useRepository(org, repo)` + `useTags(org, repo)` | ⬜ |
| API hooks — scan results | `useScanResult(digest)` | ⬜ |
| API hooks — build history | `useBuildHistory(org, repo)` | ⬜ |

---

## Layout & Navigation

| Task | Detail | Status |
|---|---|---|
| Top nav spacing | Left items (ContainerRegistry + nav) full-width spread | ✅ |
| Sidebar nav padding | py-2.5 on each nav item for breathing room | ✅ |
| Active route highlight | Sidebar active state driven by `useRouterState` | ✅ |
| Logout button | Wire sidebar logout to clear `access_token` + redirect `/login` | ⬜ |
| Mobile sidebar | Collapse sidebar on narrow viewports (hamburger toggle) | ⬜ |

---

## Design System

| Task | Detail | Status |
|---|---|---|
| Color tokens | Full MD3 palette in `globals.css` `@theme` | ✅ |
| Typography utilities | `text-headline-lg/md`, `text-body-md`, `text-label-caps`, `text-code-md/sm` via `@utility` | ✅ |
| Spacing tokens | `--spacing-gutter/sm/md/lg/xl/xs/base` | ✅ |
| Border radius tokens | `--radius-DEFAULT/lg/xl/full` (capped at 12px per Stitch spec) | ✅ |
| Material Symbols font | Google Fonts CDN in `index.html` | ✅ |
| Hanken Grotesk 700 | `@fontsource/hanken-grotesk/700.css` imported in `main.tsx` | ✅ |
| JetBrains Mono | `@fontsource/jetbrains-mono` + Google Fonts CDN | ✅ |
| Scrollbar styling | Custom thin scrollbar from reference applied globally | ⬜ |

---

## Testing

| Task | Detail | Status |
|---|---|---|
| Login unit tests | Form validation, error states, token storage | ⬜ |
| Dashboard unit tests | Stats card rendering, table row rendering, filter tabs | ⬜ |
| Auth hook tests | Token detection, redirect logic | ⬜ |
| E2E — login flow | Playwright: load `/`, enter credentials, land on `/dashboard` | ⬜ |
| E2E — repo table | Playwright: dashboard renders 4 rows, pagination shows 1/2/3 | ⬜ |

---

## Build & CI

| Task | Detail | Status |
|---|---|---|
| CI path filter | `.github/workflows/ci-frontend.yml` triggers on `frontend/**` | ✅ |
| `npm run build` clean | No TypeScript errors in production build | ✅ |
| Lint in CI | ESLint step in CI workflow | ⬜ |
| Docker image | `frontend/Dockerfile` multi-stage build (Node build → nginx serve) | ⬜ |

---

## Notes

- All icons must use **Material Symbols Outlined** (`<span className="material-symbols-outlined">`) — no lucide-react on authenticated screens.
- Every screen must be QA-verified against its Stitch reference HTML before marking Done.
- `frontend/.env.local` is gitignored — document required vars in `frontend/.env.example`.
- `VITE_TENANT_ID` is the dev tenant UUID seeded in the metadata DB migration `00002`.
