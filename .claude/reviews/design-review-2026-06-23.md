# Design Review — 2026-06-23

> Reviewer: design-ux review agent
> Scope: dashboard frontend + operator-facing surfaces
> Total findings: 24

## Top-5 high-impact findings

1. **DSGN-001** — `AccessSubNav` + `ServiceAccountsPage` gate workspace-admin surfaces behind `isPlatformAdmin`; tenant admins lose Service accounts + Workspace settings until they hold the platform marker.
2. **DSGN-002** — Sidebar IA muddles "Access (RBAC)" with "Workspace (tenant admin)". Custom domains under Access; Audit streaming under Integrations. No single "settings for my tenant" cluster.
3. **DSGN-003** — Destructive confirmation patterns are inconsistent: GC + scanner swap use type-to-confirm; trusted-key remove + audit-export clear use **native `window.confirm`**.
4. **DSGN-004** — `ErrorState` shows generic prose ("couldn't load — check BFF logs") and hides HTTP status / `response.data.error`. Self-hosted operators are the ones with logs — surface the error.
5. **DSGN-005** — Brand-new tenants land on zeroed stats + four QuickActions linking to empty pages. No `docker login` walkthrough at the most product-critical moment.

## Detailed findings

### DSGN-001 — Workspace-admin gating reuses `isPlatformAdmin`
- **Where:** `frontend/src/components/access/AccessSubNav.tsx:101-106`; `frontend/src/routes/_authenticated.api-keys.service-accounts.tsx:26-32`
- **Issue:** Both files comment "reuse `isPlatformAdmin` until backend ships workspace-scoped admin roles" — but tenant-scoped admin role already exists (BFF returns 403 "Tenant admin role required" in `domains-table.tsx:108`, `audit-export.tsx:203`). The FE just lacks an `isWorkspaceAdmin` helper.
- **Proposed:** Add `isWorkspaceAdmin(claims)` in `lib/auth/jwt.ts`. Replace 3 misuses. Unit-test that workspace admin without platform marker sees Service accounts + Audit streaming.
- **Effort:** M · **Priority:** P0

### DSGN-002 — Sidebar IA muddles Access with Workspace
- **Where:** `frontend/src/components/shell/sidebar.tsx:33-85`
- **Proposed:** Operate / Access (Members, API keys) / Workspace (Custom domains, Audit streaming, Service accounts, SSO, settings) / Integrations (Webhooks, future Slack/PagerDuty) / Platform (admin-marker only).
- **Effort:** S · **Priority:** P1

### DSGN-003 — Inconsistent destructive confirmation patterns
- **Where:** GC trigger `components/admin/gc-card.tsx:506-524` (type "RUN GC"); scanner swap (type-by-name); delete tenant + delete repo (type-by-name); **delete trusted key uses `window.confirm`** (`repo-trusted-keys-section.tsx:122`); **clear audit export config uses `window.confirm`** (`_authenticated.workspace.audit-export.tsx:212`); delete primary domain uses plain Dialog with **no** typed-confirm (`domains-table.tsx:220-296`).
- **Proposed:** Single `ConfirmDestructiveDialog` primitive with 3 severity levels (Cancel+Confirm / type resource name / type fixed phrase). Replace every `window.confirm`. Primary-domain delete → medium.
- **Effort:** M · **Priority:** P0

### DSGN-004 — Generic error prose hides HTTP status + actionable detail
- **Where:** `routes/_authenticated.index.tsx:42`, `_authenticated.activity.tsx:281`, `_authenticated.repositories.index.tsx:91`, `_authenticated.workspace.audit-export.tsx:166`
- **Proposed:** Extend `ErrorState` with `code?: number` + `detail?: string`. Monospace pill when `code >= 400`. "Show request details" expander with URL + correlation id.
- **Effort:** M · **Priority:** P0

### DSGN-005 — Brand-new tenant has no first-run guidance
- **Where:** `routes/_authenticated.index.tsx`; `components/dashboard/quick-actions.tsx`
- **Proposed:** When `data.total_repos === 0`, replace stat row with **First steps** card stack: registry endpoint + `docker login` + `docker tag` + `docker push` + polling indicator that flips green when `total_repos > 0`.
- **Effort:** L · **Priority:** P1

### DSGN-006 — Repo Settings tab is vertical wall of cards
- **Where:** `routes/_authenticated.repositories.$org.$repo.tsx:122-147`
- **Proposed:** Three sub-sections — Security (immutability, signature, trusted keys) / Quality (scan policy) / General (rename/transfer/description). Optional sticky right-side ToC on xl+.
- **Effort:** S · **Priority:** P1

### DSGN-007 — `EmptyState` doesn't support secondary action / docs link
- **Where:** `components/ui/empty-state.tsx:5-49`
- **Proposed:** Add `secondaryAction?: React.ReactNode`. Update 4 busiest empty states (webhooks, audit-export, domains) to add "Read the docs" links.
- **Effort:** S · **Priority:** P2

### DSGN-008 — Topbar breadcrumb slot exists but no route uses it
- **Where:** `components/shell/app-shell.tsx:6-24`; `components/shell/topbar.tsx:53-85`
- **Proposed:** Drive `<Breadcrumbs />` from `useMatches()` in Topbar. Routes pass display labels via loader. Remove inline duplicates (`repository-header.tsx:35-51`, `_authenticated.webhooks.$id.tsx:66-81`).
- **Effort:** M · **Priority:** P2

### DSGN-009 — Audit-export tiles confuse live vs cumulative metrics
- **Where:** `routes/_authenticated.workspace.audit-export.tsx:509-583`
- **Proposed:** 3-tile mini-grid: Status + last success / DLX backlog + Drain button / Last error multi-line with copy. Move lifetime `dlx_depth` to tooltip.
- **Effort:** S · **Priority:** P2

### DSGN-010 — `/admin/scanner` doesn't make active adapter obvious
- **Where:** `components/admin/scanner/adapter-card.tsx:30-142`
- **Proposed:** Sort grid so active is always first. Non-active button reads "Replace **`<currentActiveName>`** with this" instead of "Make active".
- **Effort:** S · **Priority:** P1

### DSGN-011 — Preview routes dominate `/api-keys` nav rail for admins
- **Where:** `components/access/AccessSubNav.tsx:66-95`
- **Proposed:** Either (a) gate Preview behind `/profile` "Show preview features" toggle, or (b) collapse 4 entries behind single "Preview" flyout. Default-off.
- **Effort:** S · **Priority:** P2

### DSGN-012 — Trusted-key remove `window.confirm` doesn't warn about Phase 1 fallback
- **Where:** `components/repositories/repo-trusted-keys-section.tsx:120-142`
- **Proposed:** Proper Dialog. When removing last key under `require_signature=true`, show yellow-bordered warning "admission returns to Phase 1 fallback" + type-to-confirm.
- **Effort:** S · **Priority:** P1

### DSGN-013 — Relative-date / key-shortening helpers duplicated inline
- **Where:** `components/repositories/repo-trusted-keys-section.tsx:617-657` (4 inline helpers) while `lib/format.ts` already exports `formatRelativeDate`.
- **Proposed:** Move all 4 helpers into `lib/format.ts`. Export single `formatShortRelativeDate`.
- **Effort:** S · **Priority:** P2

### DSGN-014 — Login exposes default tenant UUID pre-auth
- **Where:** `routes/login.tsx:54-60`
- **Proposed:** Drop the public display. Keep post-login chip in `topbar.tsx:124`.
- **Effort:** S · **Priority:** P2

### DSGN-015 — `/security` "What you can do here" card is filler
- **Where:** `routes/_authenticated.security.tsx:131-189`
- **Proposed:** Replace with freshness view OR promote `CoverageCard` to the top-row slot.
- **Effort:** S · **Priority:** P2

### DSGN-016 — Notifications bell + `/activity` share model but no nav path
- **Where:** `components/shell/notifications-bell.tsx`; `routes/_authenticated.activity.tsx`
- **Proposed:** Footer on bell dropdown: "See all activity" → `/activity`, plus "Failures only" → pre-filtered.
- **Effort:** S · **Priority:** P2

### DSGN-017 — Dialog close button has `focus-visible:outline-none` with no ring
- **Where:** `components/ui/dialog.tsx:48-57`
- **Issue:** WCAG 2.4.7 violation; keyboard / screen-reader users can't tell where focus is.
- **Proposed:** Swap for `focus-visible:ring-2 focus-visible:ring-[var(--color-accent)] focus-visible:ring-offset-1`. Audit Button, Switch, Input for same pattern.
- **Effort:** S · **Priority:** P1

### DSGN-018 — Inconsistent secret-input handling across forms
- **Where:** `routes/_authenticated.workspace.audit-export.tsx:364-438` (gold standard). Other forms reinvent.
- **Proposed:** Create `components/ui/secret-input.tsx` encapsulating type=password + "(saved)" badge + Replace toggle + Clear-on-save sub-checkbox. Refactor audit-export, then future SSO config.
- **Effort:** M · **Priority:** P2

### DSGN-019 — Tag detail defaults to Security; empty scan hides sibling tabs
- **Where:** `routes/_authenticated.repositories.$org.$repo_.tags.$tag.tsx:110-149`
- **Proposed:** Auto-switch to Push history when `scan.status === "missing" && builds.length > 0`, or add "Other views: Layers · Signing" affordance inside empty scan panel.
- **Effort:** S · **Priority:** P2

### DSGN-020 — Webhook detail has no quick-pause action
- **Where:** `routes/_authenticated.webhooks.$id.tsx:124-155`
- **Proposed:** Pause/Resume button next to Edit using `useUpdateWebhook({ active: !active })`.
- **Effort:** S · **Priority:** P2

### DSGN-021 — Custom-domain "Verify now" can't re-display TXT challenge
- **Where:** `components/workspace/domains-table.tsx:177-187`, `97-113`
- **Proposed:** Row-expand chevron on unverified domains revealing TXT name + value + copy button, "Check DNS now" affordance, `next_poll_after` countdown.
- **Effort:** M · **Priority:** P1

### DSGN-022 — Dashboard greeting calls service accounts "operator"
- **Where:** `routes/_authenticated.index.tsx:24-25, 133-141`
- **Proposed:** For service accounts, drop time-of-day greeting; render "Authenticated as `<sa name>`".
- **Effort:** S · **Priority:** P2

### DSGN-023 — Sidebar has no mobile / narrow-viewport fallback
- **Where:** `components/shell/sidebar.tsx:101-107` (`hidden ... lg:flex`)
- **Proposed:** `<MenuButton />` in Topbar (visible below `lg`) opening Sheet-style drawer reusing the same `SECTIONS` array.
- **Effort:** M · **Priority:** P1

### DSGN-024 — Page-header pattern varies across routes
- **Where:** 8+ distinct shapes (`_authenticated.repositories.index.tsx`, `_authenticated.security.tsx`, `_authenticated.activity.tsx`, `_authenticated.workspace.domains.tsx`, `_authenticated.workspace.audit-export.tsx`, `_authenticated.webhooks.index.tsx`, `_authenticated.api-keys.index.tsx`, `_authenticated.api-keys.service-accounts.tsx`)
- **Proposed:** Extract `<PageHeader>` primitive accepting `eyebrow`, `title`, `icon?`, `description?`, `action?`. Migrate all 13 routes.
- **Effort:** M · **Priority:** P2

---

## Summary

24 findings spanning navigation IA, role gating, primitive gaps, and operator clarity. Three dominant themes:

1. **RBAC is half-implemented** — `isPlatformAdmin` misused as `isWorkspaceAdmin` in 3 places, fragmenting workspace-admin nav across Access/Integrations.
2. **Inconsistent primitives** — confirmation dialogs, page headers, secret inputs, date helpers reinvented per surface. Design system stopped at low-level Card/Button/Dialog and never grew the next layer.
3. **Error / empty / first-run states are user-hostile** — generic "check BFF logs" prose, hidden HTTP codes, no first-tenant onboarding, repeated "What you can do here" scaffolding.

Files referenced most: `components/shell/sidebar.tsx`, `components/access/AccessSubNav.tsx`, `routes/_authenticated.workspace.audit-export.tsx`, `routes/_authenticated.repositories.$org.$repo.tsx`, plus primitives `empty-state.tsx` / `error-state.tsx` / `dialog.tsx`.

**Top P0s to ship first:** DSGN-001 (workspace-admin helper), DSGN-003 (unified ConfirmDestructive + kill `window.confirm`), DSGN-004 (surface HTTP status in `ErrorState`).
