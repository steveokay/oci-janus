# Frontend Rebuild Plan

> Started: 2026-06-18 · Archive of pre-rebuild UI: branch `frontend-archive-v1`
> Dev server: `cd frontend && npm run dev` → http://localhost:5173
> Dev creds: `admin` / `Admin1234!dev` (workspace `98dbe36b-ef28-4903-b25c-bff1b2921c9e`)

## Design direction

- **Light-mode primary** with system-preference dark mode (Sprint 0 ships light only; dark token values are already declared in `styles/globals.css`, Sprint 4 wires the toggle).
- **One warm accent**: indigo (Tailwind's classic indigo family in OKLCH).
- **Approachable, not dev-tooly**: friendly geometric sans (Hanken Grotesk), generous whitespace, soft shadows, rounded corners. Monospace (JetBrains Mono) reserved for digests / IDs / code where it earns its keep.
- **Audience**: both end-tenants (DevOps managing repos) AND super-admins (platform operators). Role-aware navigation surfaces admin sections only when the user has the platform-admin marker.
- **References**: Vercel Dashboard · Stripe Dashboard · Resend · Linear · GitHub's newer screens.

## Stack

Kept from previous build (it was good — see [verdict](#stack-verdict)):

| Layer | Library |
|---|---|
| Framework | React 19 + Vite 6 |
| Routing | TanStack Router (file-based) |
| Data | TanStack Query + axios |
| State | Zustand |
| Forms | react-hook-form + zod |
| UI primitives | Radix UI (Dialog / Dropdown / Tooltip / Tabs) |
| Styling | Tailwind v4 (`@tailwindcss/vite`) |
| Icons | lucide-react |
| Fonts | `@fontsource/hanken-grotesk` + `@fontsource/jetbrains-mono` |
| Toasts | sonner |
| Charts | recharts |
| Tables | TanStack Table |
| Testing | Vitest + Testing Library + Playwright |

Added in Sprint 0:

| Add | Why |
|---|---|
| `class-variance-authority` | Type-safe component variants without hand-rolled discriminated unions. |

Considered for later:

| Maybe | When | Why |
|---|---|---|
| `framer-motion` | Sprint 2+ | List stagger + modal transitions if Tailwind animation feels flat. |
| Storybook 8 | Optional | Component playground if the component count gets unwieldy. |
| MSW | Optional | Mock API for component tests + design review. |

## Legend

| Symbol | Meaning |
|---|---|
| ✅ | Done |
| 🔄 | In Progress |
| ⬜ | Not Started |
| 🔴 | Blocked |

---

## Deferred decisions

> Decisions surfaced during the rebuild that aren't blocking the next sprint
> but need an answer before the relevant feature ships to production.

| Decision | Context | Touches | Revisit when |
|---|---|---|---|
| **Super-admin login URL in production.** Where do platform operators log in? Three viable setups: (1) a system "platform" tenant resolved from the platform's naked domain (`registry.example.com/login`); (2) a dedicated admin subdomain (`admin.registry.example.com/login`); (3) platform-admin marker on a user inside a regular customer tenant. Today only #3 works because there's no platform tenant — the login form's "Use a different workspace" disclosure is the dev-only escape hatch. In production with custom domains the workspace field should disappear entirely (host resolves the tenant), so the super-admin path needs a real answer first. | `login.tsx`, gateway host→tenant routing, `services/tenant`, deployment docs | Before super-admin features ship to production / before Sprint 3 tenant work, whichever is sooner |
| **Page refresh logs the user out.** JWT is memory-only by design (`authStore.ts`, FE-SEC-001 / FE-SEC-002) — localStorage/sessionStorage would expose tokens to XSS. So any browser refresh drops the session and bounces to `/login`. The proper fix is an HttpOnly + Secure + SameSite=Strict refresh-token cookie issued by `services/auth` that the SPA exchanges for a fresh access JWT on boot (FE-SEC-009 in security.md). Until then, refresh = re-login. | `services/auth` (refresh-token issuance + rotation + revocation), `frontend/src/store/authStore.ts`, `frontend/src/lib/api/client.ts` (boot-time refresh attempt), `_authenticated.tsx` (handle pending refresh during beforeLoad) | Before any external user trial / before Sprint 2 — friction will be unacceptable once users have repos to navigate between |

---

## Sprint 0 — Bootstrap & foundation ✅

Shipped. UI tokens, login screen, auth wiring, and base components are all on `feat/frontend-rebuild` (PR #11). Switched from Geist to DM Sans after review; warm cream + amber-to-rose login band replaced the cold indigo radial; credential form leads, SSO follows as a compact icon row. Open-redirect-guarded `?from` round-trip lives in `validateSearch`.

## Sprint 1 — Foundation screens

App shell + the four screens every user touches daily.

| Sub-phase | Status | Notes |
|---|---|---|
| 1a — App shell (sidebar + topbar + breadcrumbs) | ✅ | `components/shell/*`. MAIN/MANAGE/ADMIN nav grouping; `usePlatformAdmin` probes `/admin/tenants` once per session to gate the admin section. Workspace switcher placeholder. Topbar Cmd+K trigger fixed-width to survive intermediate viewports. |
| 1b — Dashboard overview (real `/api/v1/stats`) | ✅ | `useStats` (60 s refetch). Repositories + Storage tiles + hero (repos / vulns / health pill) are live. Tags + Scans today stay on demo data with sparklines intact. Loading skeletons + inline error note. Demo banner enumerates what's live vs placeholder. |
| 1c — Repositories list (filter + sort + create + delete) | ✅ | `useRepositories` / `useCreateRepository` / `useDeleteRepository`. TanStack Table, client-side name search, server-applied visibility filter. Create dialog (Radix + RHF + zod). Delete dialog with GitHub-style "type the name to confirm". Page now opens with a Higgsfield banner header + 4-tile SummaryStrip (Total/Public/Private/Storage). Empty state ships a copy-pasteable `docker login/tag/push` snippet. |
| 1d — Image / tag detail page | ✅ | `/repositories/$org/$repo` with hero banner, pull-command card, stats strip, tag search (name OR digest substring), tags table with per-tag scan badge + per-row pull-copy + delete, polished empty / no-match / load-error states. Backend dependencies (manifest layers, signing status, repo activity) tracked in `status.md` as FE-API-002/003/004. |
| 1e — Profile page | ✅ | `/profile`. Identity card (read-only username / role / sub / tenant from JWT). Full API-key management (`useApiKeys` / `useCreateApiKey` / `useDeleteApiKey`): table + Radix-Dialog create flow with scope radio-cards + expiry picker, one-time-secret reveal dialog with checkbox-gated close, GitHub-style revoke confirmation. Name/email/password editing tracked as `FE-API-011/012/013` in `status.md`. |
| 1f — Theme toggle (light / dark / system) + Cmd+K palette | ⬜ Next | Theme toggle persists to `prefers-color-scheme`. Cmd+K palette wires the topbar search trigger. Both can move to Sprint 4 polish if time-boxed. |

### Sprint 1 extras shipped along the way

| Item | Notes |
|---|---|
| Time-of-day hero photographs | `HeroCard` buckets the hour into morning/afternoon/evening/night and renders matching Higgsfield images at `/hero/{period}.png` over a gradient + white veil. Image hides gracefully via `onError`. |
| Repositories hero banner | Same triple-layer composition over `/hero/repositories.png` (warm peach/coral with abstract floating glass cubes). Replaces the flat icon+title row. |
| Dashboard bento swap | TopRepos on the left of ActivityFeed (equal columns), Quickstart spans full width below as a side-by-side card with a macOS-styled terminal block. |

## Sprint 2 — Operations screens (NOT STARTED)

| Task | Notes |
|---|---|
| Security scan results — severity grouped, fix versions, copy CVE ID | Stream `findings` decoded from base64 proto bytes |
| Build history | Per-tag timeline; placeholder until backend builds API is real |
| Webhooks CRUD | URL allowlist, secret rotation, delivery log |
| API keys CRUD | Create / revoke; one-time secret display |
| RBAC: org members + repo members + role grants | Use the platform-admin marker pattern from PENTEST-024 |

## Sprint 3 — Admin & system (NOT STARTED)

| Task | Notes |
|---|---|
| Audit log viewer | Paged, filtered by actor / verb / resource |
| Tenants CRUD | Replace the placeholder shipped in PR #8 with real design |
| Tenant detail page | Domain verification status, storage usage, quota controls |
| System health | DR backup runs (last success/fail per data class), GC status, signer key health, storage usage |

## Sprint 1a (BACKEND) — SSO providers (NOT STARTED)

The login screen ships with Google / GitHub / Microsoft / "Other SSO" buttons
that currently show a "coming soon" toast. The UI is locked; this sprint
makes the buttons real.

| Task | Notes |
|---|---|
| `services/auth`: OIDC client + per-tenant provider config | Postgres table for client_id/client_secret/issuer URL; AES-256-GCM encryption of secrets (same pattern as proxy upstream creds) |
| `POST /auth/sso/:provider/start` | Redirects browser to provider's authorize endpoint with state + nonce |
| `GET /auth/sso/:provider/callback` | Validates state + nonce, exchanges code, fetches userinfo, provisions user on first login, issues a normal Janus JWT |
| Per-tenant provider allowlist | A tenant might want Google only, another wants Microsoft only — admin UI in Sprint 3 manages this |
| Login UI: replace coming-soon toast with real redirect | Same buttons, real `<a href>` to start endpoint |
| Audit events | `sso.login.success`, `sso.login.failure`, `sso.user_provisioned` |

## Sprint 3a (BACKEND, parallel) — runtime site settings (NOT STARTED)

Needed before Sprint 4 can wire the Admin Site Settings screen.

| Task | Notes |
|---|---|
| `site_settings` table + migrations | Key/value, audit-logged, per-tenant or global |
| `services/management` endpoints: `GET/PATCH /api/v1/admin/settings` | Gated by platform-admin marker |
| Allowlist: which `.env` items can move to runtime config | Feature flags, default quotas, scanner timeouts, retention windows. **NEVER**: DB passwords, JWT keys, mTLS paths, anything secret. |
| `settings.changed` RabbitMQ event + per-service in-memory reload subscriber | Avoids needing to restart services for every flag flip |

## Sprint 4 — Site Settings UI + polish (NOT STARTED)

| Task | Notes |
|---|---|
| Admin Site Settings screen | Wires Sprint 3a backend; grouped by category (Security / Limits / Features / Defaults) |
| Empty states across every screen | Illustrations + clear CTAs, no `—` placeholders |
| Loading skeletons | Replace spinner-on-blank with shimmer placeholders |
| Error states | Consistent retry UX, no naked stack traces |
| a11y audit | Keyboard nav, screen reader labels, ARIA where Radix needs help |
| Keyboard shortcuts | `g r` → repos, `g s` → security, etc. |
| Animation polish | Page transitions, list stagger, modal entrance |

---

## Stack verdict

**The current stack is excellent — no rewrite needed.** It's essentially "best-in-class 2026 React": Radix primitives for accessibility, TanStack for routing + data + tables, Tailwind v4 with semantic tokens, self-hosted fonts (no Google Fonts external load), lucide for icons. The previous UI's flatness was a *design system* problem, not a *stack* problem.

What we're changing:

| Was | Now | Why |
|---|---|---|
| Material Design-style tokens | Custom OKLCH palette with semantic surface/text/border tokens | MD always reads "Googley/corporate-blah"; same semantic structure with non-Material values |
| No motion system | Token-based motion (`--duration-*`, `--ease-*`) with hooks for `framer-motion` later | Cheap polish; the difference between "looks good" and "feels good" |
| External Google Fonts | All fonts self-hosted via `@fontsource` | Closes FE-SEC-001 (network-on-render) and removes a third-party SPOF |
| Hand-rolled buttons per screen | Single `Button` with CVA variants | One source of truth for visual + behavior, instant cross-app updates |

---

## Outside the rebuild

These continue to ship via the normal backend flow, not blocked by this work:

- ✅ Super-admin tenant CRUD (PR #8 — shipped, will be redesigned in Sprint 3)
- ✅ Automated DR backups (PR #9 — shipped)
- ✅ Platform-admin marker migration fix (PR #10 — shipped)
- ⬜ CI Go 1.24 → 1.25 pin (5 min mechanical)
- ⬜ KMS backends (DEFERRED — needs live cloud)
- ⬜ Notary v2 (DEFERRED — multi-day)
