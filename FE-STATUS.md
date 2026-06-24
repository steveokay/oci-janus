# Frontend — Build Tracker

> Living tracker for the frontend rebuild on branch `feat/frontend-rebuild`.
> Started 2026-06-19. Owner: AI-assisted build. Aesthetic codename: **Beacon**.
>
> Related: [`status.md`](status.md) (backend), [`futures.md`](futures.md)
> (prioritized backlog of unsprinted items — MFA, tag immutability,
> admission policy, SCIM, etc. — that don't yet have FE-API numbers).

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
| S4 | RBAC & Members | DONE ✅ | `/members`, `/orgs/:org/members`, repo members tab |
| S5 | Webhooks | DONE ✅ | `/webhooks` list + `/webhooks/$id` detail, create/edit/delete, delivery log, test, rotate-secret |
| S6 | Platform Admin | DONE ✅ | `/admin/tenants`, tenant CRUD + quota + page footer |
| S7A | Profile & API keys | DONE ✅ | `/profile` real wiring (identity, password change, API keys CRUD) — backend FE-API-011/012/013 ready |
| S7B | Image detail enhancement | DONE ✅ | Layers + Signing tabs on tag-detail — FE-API-002 (extended for index manifests) + FE-API-003 (signature route) shipped backend-side |
| S8 | Polish pass | NOT STARTED | dark-mode QA, a11y audit, responsive QA, motion review |
| S9.1 | Tag-detail signing + supply chain | DONE ✅ (`8a7271f`) | FE-API-025 verify-on-demand, FE-API-026 sign-from-UI dialog, FE-API-033 SBOM download |
| S9.2 | Workspace metadata + notifications + custom domains | DONE ✅ (`52178b1`) | FE-API-007/009 workspace identity, FE-API-008 notifications topbar bell + `/activity` live feed, FE-API-027 `/workspace/domains` CRUD |
| S9.3 | Workspace-wide security center | DONE ✅ (`5968bf0`) | FE-API-014 vulnerabilities table, FE-API-015 scan history timeline |
| S9.4 | Analytics + storage + admin tenant detail + bulk delete | DONE ✅ (`2e983fc`) | FE-API-028 admin tenant drawer, FE-API-029 rename/plan edit, FE-API-030 analytics sparkline (dashboard + repo), FE-API-031 storage breakdown card, FE-API-036 bulk tag delete |
| S9.5 | Remaining stubs (mop-up) | DONE ✅ (this commit) | FE-API-017 remediation table, FE-API-018 scan policy editor (Switch + radio + chip CVEs), FE-API-019 compliance reports tab with generate + PDF/SBOM download + smart polling, FE-API-020 scan coverage + freshness card, FE-API-032 admin GC card (status + history + type-to-confirm run-now), FE-API-035 webhook delivery payload reveal dialog. Every ComingSoon panel for a backend-DONE surface is gone. |
| REM-011 P1 FE | Stuck-scan graceful degradation on tag detail | DONE ✅ (`8debd29`) | `ScanPanel` flips to "Scanner isn't producing results" after 90s of `pending` with no row; surfaces the `docker compose --profile scanner up` command inline. Client-side heuristic only — replaced by FE-API-047 liveness in P2. Backend tracked in `status.md` → REM-011 Phase 1. |
| REM-011 P2 FE | Platform-admin scanner adapter page | DONE ✅ (this commit) | New `/admin/scanner` route — health card up top (worker pool status, queue depth, in-flight, last-success), adapter grid below with `accentBar="success"` + "Active" badge on the chosen one, "Make active" type-to-confirm dialog on the rest, "Run test scan" button on the active card with inline result panel (SeverityBar + duration + scanner version), sidebar entry gated on platform-admin marker grant. `ScanPanel` upgraded — `InFlightCard` now reads `useScannerHealth({ refetchInterval: 15s })` and flips to "Scanner isn't producing results" immediately on `healthy=false`, falling back to the old 90s heuristic for non-admins / 404 BFFs. |
| S10 | Documentation surface | NOT STARTED | author `/docs/*` content + Topbar docs link + Footer link points at real docs |
| S11.1 | Retention — per-repo tab (read) | DONE ✅ (this session) | `useRepoRetention` + `useRepoRetentionPreview` hooks against `GET .../policies/retention` + `.../preview`; new "Retention" tab between Members and Settings on `/repositories/$org/$repo`; four states (skeleton / error / no-policy empty / loaded); preview-window banner with countdown driven by server `in_preview_window` (clock-skew safe). FE-API-037 + FE-API-038 read-paths. |
| S11.2 | Retention — rule editor + dry-run + save | DONE ✅ (this session) | `RetentionEditor` (enabled switch + 5-rule chips with kind selector + value input + protected-pattern chip input) → `RetentionDryRunDialog` (totals strip + would-delete table + protected-skipped table + truncation banner) → PUT. Save gated on a successful dry-run. Per-repo override "Remove" button reverts to inherited. FE-API-037 PUT/DELETE + FE-API-038 POST. |
| S11.3 | Retention — executor trigger + run polling | PARTIAL ✅ (this session) | `RetentionRunCard` below summary — "Run now" POST + 5s status polling (queued/running/completed/failed) + result strip (marked / bytes-grace / completed-at). Hidden on inherited policies. **Pending-delete pills on Tags tab + per-repo Run history panel deferred** — blocked by REM-013 gaps 1 + 2; both have fix sketches in `status.md`. |
| S11.4 | Retention — org default + storage column | PARTIAL ✅ (this session) | `useOrgRetention` / `useUpdate`/`useDelete` hooks + new `OrgRetentionPanel` (summary + editor + remove-default) on new route `/orgs/$org/settings`; cross-link from inherited per-repo policies. FE-API-039 wired. **Storage breakdown "Retention" column deferred** — blocked by REM-013 gap 3. |
| S11.5 | Retention — admin tile + notifications + activity | DONE ✅ (this session) | `RetentionCard` below `GCCard` on `/admin/tenants` — 24h/7d counts strip + last-10 retention runs table (mode pill + status + manifests + bytes + triggered-by). `retention.evaluated` / `retention.applied` / `retention.grace_completed` added to BFF + audit allowlists + audit `renderNotification` switch → topbar bell + `/activity` chips. Webhook routing-key chips were already in place from FE-API-041. |
| S11 | Retention policies | DONE ✅ (this session — slices 1+2+5) / PARTIAL on slice 3+4 (blocked by REM-013) | See per-slice rows above. FE-API-037/038/039/041/043 fully FE-DONE. FE-API-040 FE-PARTIAL — executor trigger live, badges + per-repo run history blocked by REM-013 gap 1+2 (proto extensions, fix sketches logged). |
| FE-API-048 | Service accounts + activity hub | DONE ✅ | `/api-keys` hub with sub-routes for personal keys, service accounts, activity, plus four preview surfaces (trust/helpers/policies/review) carrying dummy data + a11y-compliant PreviewBanner. |
| FE-API-049 | Org-default + per-repo scan policy | DONE ✅ | `OrgScanPolicySection` on `/orgs/$org/settings`, per-repo override on `/repositories/$org/$repo/settings`. Inheritance chip ("inherited from org" / "per-repo override"). Mirrors retention shape. |
| FE-API-050 | Pull-time manifest quarantine | DONE ✅ | 🔒 pill on quarantined tag rows. Quarantine banner on tag detail Security tab + Lift dialog (type-to-confirm) on repo-admin path. `useLiftQuarantine` mutation invalidates tags + manifest queries. |
| S-MAINT-1 B1+B3 | Data-integrity bug fixes (vuln dedup + storage meter) | DONE ✅ | Per-repo storage strip + dashboard storage breakdown now show real bytes (was 0 for every repo). Vulnerability counts no longer 3× inflated by re-scans. Backend SQL fix; FE rendering unchanged. |
| S-MAINT-1 B2 | API key creation contract alignment | DONE ✅ | `/apikeys` create dialog body shape aligned to BFF (`name`, optional `expires_in_days`, optional `service_account_id`); `last_used_at` surfaced on the list. |
| S-MAINT-1 B4 | Dialog label spacing | DONE ✅ | `Label` primitive defaults to `block mb-1.5`, fixing label-touches-input across every dialog that omits a `space-y-*` wrapper. Vertical block margins collapse so well-structured dialogs are unaffected. |
| S-MAINT-1 P1 | Dashboard storage capacity rendering | DONE ✅ | Card renders "used / total" when `tenant_storage_quota_bytes` is set; top-N reduced 8 → 6. |
| S-MAINT-1 P3+P4 | Admin GC + Retention tile heading + Time-run column + last-5 | DONE ✅ | Both tiles cap recent-runs table at 5 with "Time run" column. Heading prefixed (`Garbage collection: Recent runs` / `Retention: Recent runs`) so the table is self-describing when scrolled. |
| S-MAINT-1 P5 | Page-size selector on data tables | DONE ✅ | New `<PageSizeSelector>` + `usePageSize(key)` hook (20/50/100, default 20, per-surface localStorage). Wired into Vulnerabilities, Scans, Remediation, /activity. |
| S-MAINT-1 P6+F4 | Helm artifact taxonomy (skip non-image scan + filter chips + per-tag pill) | DONE ✅ | Artifact-type chip filter row on repo Tags tab. Non-image artifacts render a `helm` / `sig` / `sbom` / `other` pill alongside the tag badge. Container repos still scan as before; Helm/sig/sbom rows skip the scanner via P6's backend gate. |
| S-MAINT-1 F3 | /activity date-range chips | DONE ✅ | "Last 24h / 7d / 30d / All" chips above the event-type chips. URL search-param state (`?range=24h`). `useMemo`'d `since` so the queryKey stays stable. Activity icon + page-header icons on /repositories + /security + /helm. |
| S-MAINT-1 F2 | GC + Retention run search bar | DONE ✅ | `triggered_by` substring + date_from/date_to inputs above both admin tiles. Debounced 250 ms. Empty-state copy widens to "no runs match the filter". |
| S-MAINT-1 F1 | Bulk scan an org or repo | DONE ✅ | "Scan all tags" button on repo Tags panel → type-to-confirm dialog → POST `/repositories/{org}/{repo}/scan`. Toast surfaces `{scans_queued, tags_count, capped}`. Org-level POST exists too; UI for it deferred to org settings page. |
| REM-014 Clair | Clair v4 as third scanner option | DONE ✅ | `SCANNER_PLUGIN_CHOICES` + zod enum include `clair`. `/admin/scanner` lists all four adapters once `--profile clair` is up. Backend embedded HTTP layer-server bridges Clair's pull-style API to the platform's stage-then-scan contract. |
| Futures Tier 1 #2 | Tag immutability (repo flag + per-tag pin) | DONE ✅ | `RepoImmutabilitySection` toggle on the repo Settings tab + 📌 pin pill / Pin-Unpin button on the Tags table & tag detail page. Backend rejects re-pushes with `400 MANIFEST_INVALID`; per-tag pin overrides repo flag; idempotent same-digest re-pushes always succeed. |
| Futures Tier 1 #3 | Signed-image admission (`require_signature` + trusted-key allowlist) | DONE ✅ (Phase 1 + Phase 2 + recent-signers picker) | `RepoSignaturePolicySection` toggle + `RepoTrustedKeysSection` allowlist editor on the Settings tab. Phase 2 narrows the gate to approved `key_id`s when the allowlist is non-empty; empty list falls back to "any signature passes" (Phase 1). Approve dialog now offers a **Recent signer** picker (BFF-orchestrated `/recent-signers` route over `signer.ListSignatures`) alongside **Manual entry** so operators stop copy-pasting key_ids from the Signing panel; picker auto-fills the display name from the signer_id. Per-key Revoke with confirmation. Phase-1-fallback warning pill when policy is on but allowlist is empty. Docs: `docs/SIGNING.md` §8. |
| Futures Tier 1 #4 | Audit log streaming to SIEM | DONE ✅ (Phase 1 + Phase 2) | New `/workspace/audit-export` settings page (sidebar Integrations group): format selector (syslog RFC 5424 / CEF / webhook), target URL input with format-specific validation, HMAC secret + bearer token write-only inputs, JSON filter editor, Send-test-event with rendered preview. **Phase 2:** durable `audit.export` queue + `dlx.audit-export` DLX with operator-controlled drain. Live `dlx_queue_depth` via RabbitMQ Mgmt API; "Drain DLX → retry" button appears when depth > 0. Both monotonic counter + live queue depth surfaced (distinct semantics). Docs: `docs/SIEM-EXPORT.md`. |

---

## Snapshot (as of 2026-06-23)

> Sprint 9 sub-passes 9.1/9.2/9.3/9.4/9.5 all landed. **REM-011 fully shipped** — Phase 1 + Phase 2 backend + Phase 2 FE `/admin/scanner` route. **S11 retention shipped** this session — slices 1, 2, 5 fully DONE (FE-API-037/038/039/041/043); slices 3 and 4 PARTIAL because three retention surfaces (pending-delete pills on Tags tab, per-repo Run history panel, dashboard storage-breakdown "Retention" column) are blocked by backend gaps tracked as **REM-013** in `status.md` — proto / SQL / BFF extensions sketched there. Next FE work: REM-013 backend follow-up to unblock the three deferred S11 surfaces; **FE-API-004** per-repo activity tab (small, backend-ready); **FE-API-034** SSO admin UI (large); **S8** polish; **S10** docs.

**Routes shipped & wired against real backend (no stubs):**

| Route | Backing endpoints | Notes |
|---|---|---|
| `/login` | `POST /api/v1/login` + SSO buttons (stubbed) | Vague-error UX; tenant from `VITE_DEFAULT_TENANT_ID` |
| `/` (dashboard) | `GET /api/v1/stats` | KPI grid, storage quota progress, system health, mini severity bar, quick actions |
| `/repositories` | `GET /api/v1/repositories` + create/delete | Cursor pagination, search, visibility filter, create dialog (with description), type-to-confirm delete |
| `/repositories/:org/:repo` | `GET /api/v1/repositories/{org}/{repo}` + tags + members | Header card, pull-command, DescriptionCard (FE-API-006), Tabs: Tags / Members / Settings |
| `/repositories/:org/:repo/tags/:tag` | manifest + scan + builds + signature + delete | Tabs: Security / Push history / Layers (FE-API-002) / Signing (FE-API-003) — all wired |
| `/security` | `GET /api/v1/stats` for severity (FE-API-016) | 5-tab inner surface; Overview shipped real, others honest ComingSoon panels keyed to FE-API ids |
| `/activity` | (none yet — FE-API-008 stub) | Sketched preview rows showing the intended event shape |
| `/members` | derived from `GET /api/v1/repositories` | Workspace org-selector card grid |
| `/orgs/:org/members` | `GET/POST/DELETE /api/v1/orgs/{org}/members` | Add member dialog (UUID input, radio-card role picker), revoke confirmation |
| `/webhooks` | `GET /api/v1/webhooks` | Table with URL + events chips + Active/Paused pill + relative date |
| `/webhooks/:id` | full webhook surface | Test dispatch, deliveries timeline, rotate-secret, edit, delete |
| `/admin/tenants` | `GET/POST/DELETE /api/v1/admin/tenants` + quota + `/admin/gc/*` + retention runs filter (client-side) | `beforeLoad` gate redirects non-admins; platform-admin banner; plan breakdown tiles; quota in GB/TB; `GCCard` + `RetentionCard` (S11.5) |
| `/profile` | `GET/PATCH /api/v1/users/me` + apikeys CRUD + password | Inline-edit identity, live policy checklist, API keys with show-once secret |
| `/repositories/:org/:repo` (Retention tab) | `GET/PUT/DELETE .../policies/retention` + `/dry-run` + `/preview` + `/run` + `/runs/{id}` | S11.1+S11.2+S11.3 — read summary + editor + dry-run dialog + executor "Run now" button; pending-delete pills on Tags tab and per-repo run history deferred (REM-013) |
| `/orgs/:org/settings` | `GET/PUT/DELETE /api/v1/orgs/{org}/policies/retention` | S11.4 — org default editor; cross-linked from inherited per-repo policies |

**Cross-cutting primitives** delivered across the sprints:

- **Beacon design system** — light + dark OKLCH tokens, teal accent (`#0D9488`), amber highlight, severity scale; Fraunces serif heros, Inter UI, JetBrains Mono code
- **State coverage** — every list / detail surface ships skeleton + empty + error + loaded states (no `—` fallbacks anywhere)
- **Motion** — `AnimatedNumber` (framer-motion count-up), scan-pulse, quota bar fill, card stagger-fade
- **Page footer** — persistent status bar (brand + live `/healthz` poll + docs/GitHub links)
- **Theme toggle** — light / dark / system tri-state, persisted in localStorage
- **Single-flight refresh** in axios interceptor — silent JWT refresh 60s before expiry, concurrent 401s share one round-trip

**Reusable secret-handling primitive** — `SecretRevealDialog` (Sprint 5): masked-by-default, reveal toggle, copy works either way, locked escape/outside-click so secret can't be dismissed unread. Reused for webhook create + rotate AND API key create.

**Reusable destructive flow** — type-to-confirm dialogs across repo delete, tag delete, webhook delete, tenant delete (cascade soft-delete). API key revoke uses a lighter single-click confirm since revocation is reversible.

## Backend wave landed on the frontend's behalf

| FE-API | Description | Status |
|---|---|---|
| 001 | Tag `size_bytes` on `ListTags` | DONE — surfaced in repo detail Tags table |
| 002 | Per-tag manifest detail | DONE (Sprint 7B) — extended for index manifests |
| 003 | Per-tag signing status | DONE (Sprint 7B) — `signer.ListSignatures` wrapped, signer gRPC client wired in management |
| 004 | Repo-scoped activity feed | DONE — handler `repo_activity.go` |
| 006 | Repository description | DONE — rendered on detail + accepted on create |
| 010 | Org name on `RepoResponse` | DONE — empty-org rendering fix shipped client-side |
| 011/012/013 | `/users/me` GET / PATCH / password | DONE (Sprint 7A) — profile fully wired |
| 016 | Severity counts in `/stats` | DONE — dashboard mini bar + `/security` overview |
| 020 | Tenant security overview snapshot | DONE — handler `security.go` |
| 021..024 | Webhook CRUD + deliveries + test + rotate | DONE — full Sprint 5 wiring |
| 037 | Per-repo retention CRUD | DONE — Retention tab summary + editor (S11.1+S11.2) |
| 038 | Retention dry-run + preview | DONE — mandatory pre-save dialog + 24h preview banner (S11.1+S11.2) |
| 039 | Per-org default retention | DONE — `/orgs/$org/settings` editor (S11.4) |
| 040 | Retention executor (gc modes) | PARTIAL — "Run now" + admin tile shipped (S11.3+S11.5); per-tag pending-delete pills + per-repo run history blocked by REM-013 gaps 1+2 |
| 041 | Retention events (audit + webhook) | DONE — notifications bell + activity chips + webhook routing-key chips (S11.5) |
| 042 | Pull-activity tracking | DONE — closes FE-API-030 caveat (pulls analytics now live) |
| 043 | `max_idle_days` retention rule | DONE — rule kind present in both editors (S11.2+S11.4) |
| 044..047 | Scanner adapter admin | DONE — `/admin/scanner` (REM-011 P2 FE) |

**Still NOT STARTED on the frontend (backends already DONE):**

- **FE-API-004** — per-repo activity tab on `/repositories/$org/$repo`. Backend handler `repo_activity.go` exists; FE never wired (workspace-wide `/activity` covers the bigger surface).
- **FE-API-034** — per-tenant SSO admin UI (`/workspace/sso`). Backend wraps OAuth PKCE + SAML SP; FE deferred to a focused sprint.

**Blocked on REM-013 backend gaps** (status.md tracks the three gaps with fix sketches):

- Pending-delete pills on Tags tab — needs `retention_pending_delete_at` on `Tag` proto + metadata SQL JOIN + management `TagResponse` surface.
- Per-repo Run history panel on Retention tab — needs `repo_id` + `mode` filters on `gcv1.ListRunsRequest` + new BFF list route.
- Dashboard storage-breakdown "Retention" column — needs `retention_summary` + `retention_source` on `RepositoryStorageEntry` + per-row effective-policy fan-out.

## Review batch — 2026-06-23 (FE-facing items)

A cross-cutting review by three subagents on 2026-06-23 surfaced 74 findings across
the whole platform. The full report + per-finding detail (file paths, line numbers,
proposed fixes) lives in [`.claude/reviews/`](.claude/reviews/); the curated
Tier 1 / 2 / 3 backlog is in [`futures.md`](futures.md) under the
"Review batch — 2026-06-23" section.

**Status as of 2026-06-24: 16 of 24 DSGN items DONE.** Sweep across PRs #49, #50, #52, #53, #55, #56, #57.

**FE-facing slices** — what's still open:

| Tier | Status |
|---|---|
| **1 — P0** | ~~DSGN-001~~ (#53) · ~~DSGN-003~~ (#50) · ~~DSGN-004~~ (#52) — **all done** |
| **2 — P1** | ~~DSGN-005~~ (#54 / #56) · ~~DSGN-006~~ (#55) · ~~DSGN-010~~ (#57) · ~~DSGN-012~~ folded into #50 · ~~DSGN-017~~ (#50) — **5 done**; still open: **DSGN-021** (custom-domain TXT row-expand), **DSGN-023** (mobile sidebar), **QA-019** (top-level ErrorBoundary), **QA-020** (FE test coverage), **FUT-007-FE** (domain re-poll reset) |
| **3 — P2** | ~~DSGN-007 / -011 / -013 / -014 / -015 / -016 / -019 / -020 / -022~~ all done (#50 + #57) · ~~QA-021~~ done (#50) — **10 done**; still open: **DSGN-002** (sidebar IA), **DSGN-008** (topbar breadcrumbs), **DSGN-009** (audit-export tiles redesign), **DSGN-018** (`<SecretInput>` primitive), **DSGN-024** (`<PageHeader>` primitive), **FUT-008** (Sign dialog recent signer_ids) |

Other open backlog items live alongside them in `futures.md`:

- **REM-017** — Platform-admin "claim a new org" route (chicken-egg gap surfaced 2026-06-24)
- **REM-018** — UI user-ID → username + enforce display_name on user creation (filed 2026-06-24)
- **FUT-013** — Pull-through cache visibility: Phase A + B shipped (PRs #73, #74); Phase C (FE) in flight on `feat/fut-013-c-proxy-cache-frontend` — `/workspace/proxy-cache` route + sidebar entry probe-and-hide
- **FUT-012** — Tenant-user lifecycle management (filed 2026-06-24): new `/tenant/users` route shared between tenant-admin + platform-admin; invite / list / disable. Pairs with REM-018 + precedes SCIM (Tier 1 #5)
- **FUT-014** — Proxy publishes `pull.image` events (filed 2026-06-24, expanded same day): one design change covers both the cache page's `pull_count` column AND the dashboard 24h pulls card not including cache traffic. Pure backend — both FE surfaces start showing real numbers with zero FE wiring once it lands
- **FUT-014** — Pull-through cache `pull_count` undercount (filed 2026-06-24): `docker pull` HEAD-only flow doesn't bump the counter; FE column shows 0 on rows that were demonstrably pulled. Pure backend fix (no FE change), but the visible artefact lives on `/workspace/proxy-cache`
- **FUT-015** — `/workspace/proxy-cache` row expander + `docker pull` copy command (filed 2026-06-24). FE-only — operator-surfaced UX gap from testing FUT-013 Phase C
- **FUT-016** — Click-through detail page on `/workspace/proxy-cache` — new `/workspace/proxy-cache/{id}` route with Layers + Manifest tabs (filed 2026-06-24)
- **FUT-017** — Scan-on-cached-images: new "Scans" tab on the FUT-016 detail page + severity column on the cache table + per-upstream auto-scan policy editor on the cache page header (filed 2026-06-24)
- **FUT-009** — service-account-as-signing-identity (~5h, supersedes `FUT-008`)
- **FUT-010** — RBAC + FE-RBAC polish pass (~1 sprint, full audit; pairs with `DSGN-001`)
- **FUT-011** — New-user onboarding flow end-to-end via FE (paired with DEPLOY-001)

## Sprint 8 — Polish pass (remaining)

The S8 checklist below is the catch-all polish bucket. Specific items from the
review batch above absorb most of "A11y audit" and "Responsive QA"; the
remaining S8 sub-items are bundled here so nothing slips:

- [ ] Dark-mode parity sweep — toggle every route, log any contrast / token gaps
- [ ] Responsive QA — sub-`lg` sidebar behaviour (`DSGN-023`), table horizontal scroll, dialog widths on mobile
- [ ] A11y audit — keyboard nav across every interactive surface (`DSGN-017` focus rings), aria-labels on icon-only buttons, color contrast vs WCAG AA
- [ ] Motion review — confirm count-up timing, severity-pulse cadence, route transitions feel intentional not fidgety
- [ ] Loading-state geometry parity — skeleton tiles should match real card heights to remove layout shift
- [ ] Empty-state copy review — every empty pane should name a concrete next action (`DSGN-007` EmptyState gains `secondaryAction` for docs link)
- [ ] Network-error UX — verify retry recoveries across every query (`DSGN-004` ErrorState surfaces HTTP code + detail)

## Known UI bugs fixed in flight (this branch)

- **Tag row click did nothing** (this turn) — the `<Link>` + `stopPropagation()` pattern was eating clicks in some browsers. Replaced with whole-row `onClick` + `tabIndex=0` + Enter/Space keyboard handler; copy button stops propagation locally.
- **Table column alignment broken across every table** — `position:relative` + `::before` on `<tr>` collapsed table layout in some browsers. Replaced with inset box-shadow; fix landed at the primitive level.
- **Empty `org` rendering** — older dev rows render as `alpine`, not `/alpine`.
- **User-menu literal "User"** — falls back to `sub` initial + truncated UUID when JWT carries no username.
- **Tenants-table name pushed to top border** — copy button was sharing the line with the UUID; moved to its own centerline + added `py-3`.

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

### S7A — Profile & API keys

> Backend FE-API-011/012/013 (`GET/PATCH /api/v1/users/me`, `POST /api/v1/users/me/password`)
> landed in merge `22fa246`. Existing `/api/v1/apikeys` GET/POST/DELETE already live.

- [x] `useMe`, `useUpdateMe`, `useChangePassword` hooks + `useApiKeys`, `useCreateApiKey`, `useDeleteApiKey`
- [x] `IdentityCard` — hero (avatar + display_name + role chip + username + truncated tenant) + inline-edit rows for display_name + email + read-only last_login / created / memberships
- [x] Inline-edit pattern: click Pencil → toggles to Input → Enter / Check saves, Esc / X cancels; live email validation; cache updated optimistically
- [x] `ChangePasswordDialog` — current + new + confirm fields; **live 5-rule policy checklist** ticking off lowercase / uppercase / digit / non-alphanumeric / 12+ chars as you type; vague error mapping (401/403 → "incorrect")
- [x] `ApiKeysSection` with Issue + Revoke flows; `CreateApiKeyDialog` chains into the Sprint 5 `SecretRevealDialog` for the once-shown secret
- [x] `DeleteApiKeyDialog` — single-click revoke confirmation (key cards are revocable, not destructive)
- [x] `/profile` route replaces the Sprint 0 placeholder
- [x] Build / typecheck / lint pass

### S7B — Image detail enhancement (Layers + Signing)

> Both backends (`FE-API-002`, `FE-API-003`) are NOT STARTED. Sprint scope therefore
> includes the backend work, not just frontend wiring.

**Backend FE-API-002 — manifest detail**
- [x] `GetManifest` RPC on `services/metadata` (already existed)
- [x] `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/manifest` HTTP route on `services/management` (route already registered; **extended to also parse OCI image indexes / Docker manifest lists** so multi-arch images render per-platform entries)
- [x] Response shape adds `is_index: bool` + `manifests[]: {digest, size, media_type, architecture, os, variant, os_version}`

**Backend FE-API-003 — signing status**
- [x] `GET /api/v1/repositories/{org}/{repo}/tags/{tag}/signature` HTTP route on `services/management` (`signature.go`); wraps `signer.ListSignatures` over gRPC
- [x] Response shape: `{ manifest_digest, signed, signatures[]: {signer_id, key_id, signature_digest, signed_at} }`
- [x] Signer gRPC client wired into management (opt-in via `SIGNER_GRPC_ADDR`); 404 "route disabled" when unset → frontend renders Disabled state
- [x] NotFound from signer collapsed into `signed: false` — that's the unsigned state, not an error

**Frontend wiring**
- [x] `useManifest`, `useSignature` hooks (forgiving 404 → null / SIGNING_DISABLED)
- [x] `LayersPanel` — image-manifest view (config + manifest digest rows + layers table with `#` / digest / media-type / size) **or** image-index view (Multi-platform banner + per-platform rows with arch/os/variant chips)
- [x] `SigningPanel` — three states: **Disabled** (signer not wired on BFF), **Unsigned** (warning tone with `cosign sign` hint), **Signed** (success tone with one card per signer showing signer_id + key_id + signature_digest + signed_at)
- [x] Wired into the tag-detail tabs replacing the Sprint 2 ComingSoon stubs
- [x] Build / typecheck / lint pass

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

### S4 — RBAC & Members

- [x] API hooks: `useOrgMembers`, `useGrantOrgRole`, `useRevokeOrgRole`, `useRepoMembers`, `useGrantRepoRole`, `useRevokeRepoRole` + `UUID_REGEX` validator
- [x] `RoleBadge` primitive — owner (Crown / warning), admin (Shield / accent), writer (Pencil / success), reader (Eye / neutral)
- [x] `MembersTable` — reusable across org + repo, avatar tile from first UUID char, copy-able user_id
- [x] `AddMemberDialog` — UUID input with regex validation, radio-style role picker with descriptions per role
- [x] `RemoveMemberDialog` — single-click confirmation (lighter touch than type-to-confirm because revoking doesn't drop data)
- [x] `/members` — workspace org selector, auto-fetches all pages so derived org list is complete
- [x] `/orgs/:org/members` — breadcrumb + member count + table + add/remove
- [x] Repo detail "Members" tab wired via new `RepoMembersPanel` (same primitives, repo-scoped hooks)
- [x] FE-API-006 — `DescriptionCard` renders Repository.description on the repo detail page (paragraph-split, no markdown parsing yet per FE-SEC-011); `CreateRepositoryDialog` gains an optional description textarea
- [x] Build + typecheck + lint pass

### S5 — Webhooks

- [x] API hooks: `useWebhooks` / `useWebhook` / `useCreateWebhook` / `useUpdateWebhook` / `useDeleteWebhook` / `useDeliveries` / `useTestWebhook` / `useRotateSecret`
- [x] `WEBHOOK_EVENT_CATALOG` constant — curated operator-facing routing keys with label + description per event
- [x] `WebhookFormFields` — shared URL input + event multi-select + active toggle, used by both Create and Edit dialogs
- [x] `/webhooks` list — table with URL / events / status / created date, click-through to detail
- [x] `CreateWebhookDialog` — URL + events multi-select + submit → secret revealed via SecretRevealDialog
- [x] `SecretRevealDialog` — show-once, masked by default, copy button, escape/outside-click gated so operator must acknowledge
- [x] `/webhooks/$id` detail — breadcrumb, URL header, events card, action ribbon (Edit / Rotate secret / Delete), TestDispatchPanel, DeliveriesPanel
- [x] `EditWebhookDialog` — PATCH URL + events + active toggle
- [x] `TestDispatchPanel` — synchronous fire-and-show: status_code, duration_ms, error; persists last result until next dispatch
- [x] `DeliveriesPanel` — vertical timeline (delivered=success / failed=warning / dead=danger) with attempts, last_error, next_attempt_at
- [x] `DeleteWebhookDialog` — type URL to confirm; navigates to `/webhooks` on success
- [x] Bug fix: `ListResponse.webhooks` → `ListResponse.endpoints` to match BFF JSON key `"endpoints"`
- [x] Build + typecheck + lint pass

### S6..S8 — checklist deferred until each sprint kicks off

### S9 — Wire backend-DONE-but-UI-stubbed surfaces

> Several backends shipped per `status.md` that the frontend still renders
> as ComingSoon panels. This sprint turns stubs into live surfaces — no new
> backend work needed, just a swap from the placeholder to a real hooks +
> component pass for each ID. Runs after S8 polish so the live surfaces
> inherit the polish work straight away rather than needing a second pass.

**FE-API-007 — Custom domains** (today: full ComingSoon panel at `/workspace/domains`)
- [ ] `useDomains` / `useRegisterDomain` / `useVerifyDomain` / `usePromotePrimary` / `useDeleteDomain` hooks against `GET/POST/DELETE /api/v1/workspace/me/domains` + `POST .../verify` + `PATCH .../{domain}`
- [ ] `DomainsTable` — domain, primary chip, verified chip, TXT challenge, registered-at
- [ ] `RegisterDomainDialog` — URL input + display of the returned TXT challenge with copy
- [ ] `VerifyDomainDialog` — force-poll button, surfaces the verification worker outcome
- [ ] Set-primary affordance — confirmation dialog (the primary change is what flips `host` for every pull / push)
- [ ] Replace the Sprint 7B-era ComingSoon panel on `/workspace/domains` with the live surface

**FE-API-008 — Notifications** (today: sketched-preview rows on `/activity`)
- [ ] `useNotifications` hook — `GET /api/v1/notifications?since&limit&event_types&unread_only`, with `last_seen_at` persisted in `localStorage` so cross-tab unread count stays consistent
- [ ] **Topbar notifications bell** — badge with unread count, dropdown listing recent events with the synthesized `title` + `summary` + `link`
- [ ] `/activity` route — replace the sketched preview with a live feed; filter chips for the 8 event types (push.image / push.failed / delete.manifest / delete.tag / scan.completed / scan.policy_blocked / image.signed / webhook.delivery_failed)
- [ ] Click-through — each event's `link` lands on the right detail page (tag detail / webhook delivery / etc.)
- [ ] Empty state — "No new events since {last_seen_at}"

**FE-API-009 — Workspace metadata** (today: not surfaced anywhere)
- [ ] `useWorkspace` hook — `GET /api/v1/workspace/me` returning `{ tenant_id, name, slug, plan, host, host_is_custom, domains[], created_at }`
- [ ] **Sidebar header swap** — replace the hardcoded "Janus / Registry control" label with the workspace name + plan badge; tenant id stays in the dropdown
- [ ] **Pull-command card** — drop the hardcoded `registry.localhost` and use `workspace.host` (custom-domain users see their own host immediately once FE-API-007 lands)
- [ ] **Profile identity card** — surface the tenant name + plan alongside the existing tenant_id chip
- [ ] **Login footer chip** — append the resolved tenant name when the JWT identifies it (still no leak of full identity)

**FE-API-014 — Workspace vulnerabilities** (today: full ComingSoon at `/security/vulnerabilities` tab)
- [ ] `useVulnerabilities` infinite query — `GET /api/v1/security/vulnerabilities?severity=&page_token=&limit=`; severity chip row drives the param
- [ ] **CVE rollup table** — one row per CVE with severity badge, CVE id, title, primary URL, affected-images count
- [ ] **Affected-images expansion** — click row → expand shows `(repo, tag, digest)` triples each linking to its tag detail page
- [ ] Severity filter chip row (CRITICAL / HIGH / MEDIUM / LOW) with multi-select; URL search param syncs
- [ ] Replace the Sprint 3 ComingSoon panel on the `/security/vulnerabilities` tab with the live table

**FE-API-015 — Scan history** (today: full ComingSoon at `/security/scans` tab)
- [ ] `useScanHistory` infinite query — `GET /api/v1/security/scans?since&limit&page_token`; keyset cursor over `(completed_at, scan_id)`
- [ ] **Scan timeline** — vertical timeline of recent scans across the workspace; status pill + severity bar + scanner version + duration + `triggered_by` per entry
- [ ] **Trigger filter** — chip row for `push / manual / scheduled` (FE-API-015 already plumbs the field; rows populated as scanner updates land)
- [ ] **Status filter** — chip row for `complete / running / failed`
- [ ] Click-through to the tag-detail Security tab for the underlying scan
- [ ] Replace the Sprint 3 ComingSoon panel on the `/security/scans` tab

**S9.1 — Tag-detail signing + supply chain** (DONE ✅ — first S9 sub-pass)

**FE-API-025 — Verify-on-demand for signing**
- [x] Enable the disabled "Verify now" button on `SigningPanel`
- [x] On click: refetch the signature endpoint with `?verify=true` via `useSignature(_, _, _, { verify: true })`; separate query key so the cheap default path stays shared across tabs
- [x] Per-signature `Verified` / `Failed` badge on the SignatureCard (tri-state on the wire: `undefined` / `true` / `false`)
- [x] Failed-with-reason error block on each signature card when verification returned `verified: false`
- [x] Roll-up badge in the SignedCard header ("Verified (3/3)" / "Verify failed (1/3)") + accentBar shifts danger on any failure
- [x] Per-signature accentBar (success / danger / neutral) when verify completed
- [x] PendingCapabilities ComingSoon copy removed (replaced by live ActionRibbon)

**FE-API-026 — Sign manifest from UI**
- [x] `useSignManifest` mutation hook
- [x] `SignManifestDialog` — single-field `signer_id` form, zod regex matching backend's ASCII-printable rule, default `registry-signer` (dev Vault key)
- [x] Action ribbon on `SigningPanel` exposes Sign / Add-signature button
- [x] Distinct toast mapping per status: 403 (admin required), 409 (already signed by this signer), 404 (route disabled — SIGNER_GRPC_ADDR), 400 (signer rejected)
- [x] Mutation `onSuccess` invalidates the signature query — both verify + non-verify cache entries refresh

**FE-API-033 — Per-tag SBOM download**
- [x] `useDownloadSbom` mutation hook (binary blob → object URL → transient `<a download>` click → revoke after 1s)
- [x] Live `SbomPanel` on `LayersPanel`; format chooser pill row (SPDX active, CycloneDX disabled with "coming soon" tooltip)
- [x] Distinct error mapping: 404 → "no SBOM recorded — run a scan first"; 400 → "format not supported yet"; default → generic
- [x] Filename auto-derived: `{repo}-{tag}.spdx.json`
- [x] ComingSoonHint footer copy removed (replaced by live download flow)

**Verification**
- [x] Build / typecheck / lint pass
- [ ] Backend connectivity verified end-to-end against the docker-compose stack
- [ ] S9.1 commit pushed; remaining S9 sub-passes (workspace identity, security center, admin niceties) queued

### S10 — Documentation surface

> Today `status.md` and `FE-STATUS.md` are internal trackers; the Footer
> docs link points at the placeholder `docs.example.com`. This sprint
> authors a polished `/docs/*` set drawn from those trackers + CLAUDE.md
> + `frontend/Dockerfile`/`nginx.conf`, then surfaces it through a Topbar
> Docs button and the Footer link.

**Authoring**
- [ ] Decide hosting — markdown files under `/docs/` rendered by the SPA, OR an out-of-app static site (Mintlify / Docusaurus / GitBook). Trade-off: in-app keeps a single deploy + reuses the Beacon theme; out-of-app makes versioning + search easier.
- [ ] `docs/getting-started.md` — install Docker stack, log in with dev creds, push first image, view in dashboard
- [ ] `docs/architecture.md` — the 13-service map (drawn from `status.md` + CLAUDE.md §1-3), event flow, multi-tenant model
- [ ] `docs/api-reference.md` — every `/api/v1/*` route the BFF exposes, mirrored from the management handler files; pair with the existing Postman collection
- [ ] `docs/fe-api-catalog.md` — the FE-API-001..036 tracker rolled up as a public-facing changelog ("what each ID actually shipped, and where it surfaces in the UI")
- [ ] `docs/operations.md` — runbooks: rebuilding images, recreating containers, DR (`infra/runbooks/disaster-recovery.md` mirror), GC trigger, custom-domain verification
- [ ] `docs/security.md` — security model (RLS, mTLS, JWT, RBAC scope grammar), reporting a vulnerability
- [ ] `docs/troubleshooting.md` — common errors (the table-layout / route-nesting / dot-tag-name traps we hit + every "route disabled" 404 path)
- [ ] `docs/changelog.md` — per-sprint summary auto-generatable from git log + tracker

**UI surfacing**
- [ ] Update `frontend/src/components/shell/footer.tsx` — replace the `docs.example.com` placeholder with the real docs URL (config-driven via `VITE_DOCS_URL` so prod can override)
- [ ] Add a **Docs button** to `frontend/src/components/shell/topbar.tsx` — sits left of the theme toggle, opens in a new tab; small `BookOpen` icon + "Docs" label (hide label below `md`)
- [ ] Inline help — every `ComingSoon` / `ComingSoonHint` chip becomes a real link into the relevant FE-API docs section once that section ships
- [ ] FE-STATUS snapshot section gains a "See published docs at …" line so contributors know the canonical reference

**Verification**
- [ ] Every link in `/docs/*` resolves; no `TODO` placeholders left
- [ ] Topbar Docs button opens the right URL in dev (`VITE_DOCS_URL` set) and prod
- [ ] Mobile (sub-`md`) — Docs button collapses to icon-only without overflowing the topbar
- [ ] Build / typecheck / lint pass

### S11 — Retention policies

> Per-repo image lifecycle policies (delete after X days / X total / X
> GB / N days no activity). Lives on the **repo detail** page next to
> Tags / Members / Settings — NOT under `/admin/*`. RBAC-gated: repo
> `admin` or `owner` writes per-repo; org `admin` or `owner` writes
> org default; readers see inherited values labelled
> "(inherited from org default)". Mirrors the Members + Webhooks
> ownership model — never platform-admin tier.

**Backend dependencies** (in order)
- FE-API-037: per-repo retention CRUD (`GET/PUT/DELETE /api/v1/repositories/{org}/{repo}/policies/retention`)
- FE-API-038: dry-run + 24h preview window
- FE-API-039: per-org default + inheritance (`GET/PUT /api/v1/orgs/{org}/policies/retention`)
- FE-API-040: executor (gc mode `retention` + soft-delete + 7-day grace)
- FE-API-041: `retention.evaluated` / `retention.applied` / `retention.grace_completed` audit + webhook events
- FE-API-042: pull-activity tracking (also closes the FE-API-030 caveat)
- FE-API-043: activity-based rule (depends on 042)

**Repo detail — new "Retention" tab**
- [ ] Tab added next to Tags / Members / Settings; visible to everyone with repo read access; CTAs disabled-with-tooltip for sub-admin roles
- [ ] **Rule editor** — chip-based UI for the rule kinds: `max_age_days` / `max_count` / `max_size_bytes` / `dangling_grace_days` (and `max_idle_days` once FE-API-043 lands). Each chip carries the numeric input + a remove button
- [ ] **Protected tag patterns** — chip input pre-seeded with `latest`, `stable`, `^v?\d+(\.\d+){0,2}$`; operators can add/remove
- [ ] **"Inherited from org default"** read-only view when no per-repo policy exists; CTA "Override default for this repo" promotes to editor
- [ ] **Dry-run dialog** — clicking "Preview impact" before Save POSTs to FE-API-038 and renders the would-delete table (tag, digest, pushed_at, size, reason) with a total at the bottom; explicit "Cancel" / "Save policy" buttons; preview is mandatory before first save
- [ ] **Preview-window banner** — after Save, shows "Policy is in preview for 24h — no deletions will run yet. Showing what WILL be deleted on …" with a countdown
- [ ] **History panel** — last 10 runs from `gc_runs WHERE mode='retention'`: triggered_by + counts + bytes_freed + status
- [ ] **Pending-deletion badges** — Tags tab gains a "🗑 deletes in N days" pill on each tag that's in the soft-delete window; clicking the badge opens an "Undo" dialog (clears `retention_pending_delete_at` for that manifest)

**Org page — new "Default retention" section**
- [ ] Located on the existing `/orgs/{org}/members` page as a new sub-tab (or new route `/orgs/{org}/settings` — pick during build)
- [ ] Same rule editor + protected-pattern chips as the per-repo editor
- [ ] Dry-run preview shows aggregate impact across every repo in the org that DOESN'T have its own override
- [ ] List of repos that override the default + a quick-link to each repo's Retention tab
- [ ] Save fires `retention.evaluated` event so audit picks up who configured the default

**Dashboard — Storage breakdown enhancement**
- [ ] Reuse FE-API-031 storage breakdown card; add a column "Retention" showing the rule summary ("max 50 manifests" / "30 days" / "inherited") with a link to the repo Retention tab
- [ ] Optional: bar segment shading for "pending deletion" portion of each repo's storage so operators can see what would clear after grace

**Admin — Housekeeping card grows a Retention tile**
- [ ] FE-API-032 admin GC page (already planned) gains a "Retention" tile next to "GC" — same shape but mode-scoped to `retention`. Counts of pending-delete + grace_completed runs in the last 24h / 7d
- [ ] Recent runs table filterable by mode (`gc` / `retention` / `retention_grace`)

**Notifications + audit**
- [ ] Topbar notification bell consumes `retention.evaluated` events with summary copy "Retention policy on acme/api would delete 12 manifests in 24h"
- [ ] `/activity` route shows `retention.applied` rows with link to the affected repo
- [ ] Webhook delivery panel surfaces `retention.*` event types in the routing-key chip list

**Inline help / docs**
- [ ] First-time tab visit shows a 1-screen explainer ("How retention works on Janus") with link to `docs/retention.md`
- [ ] Rule chips have tooltip explanations ("Removes manifests pushed more than N days ago; tag-pattern protection applies first")

**Verification**
- [ ] RBAC: writer cannot save; admin can; reader sees inherited label
- [ ] Dry-run output matches a hand-computed deletion list for a seeded fixture
- [ ] Preview-window countdown clears at the right time + executor switches to real deletes
- [ ] Soft-delete badges appear / disappear correctly on the Tags tab
- [ ] Build / typecheck / lint pass

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
- **`FUT-013`** Pull-through cache visibility: `GET /api/v1/proxy/cache` (paginated list), `GET /api/v1/proxy/cache/stats`, `DELETE /api/v1/proxy/cache/{manifest_digest}` — backed by 3 new `services/proxy` RPCs (`ListCachedManifests`, `GetCacheStats`, `DeleteCachedManifest`) + `last_pulled_at` / `pull_count` migration on `proxy_manifests`. New sidebar entry + `/proxy/cache` page (see futures.md §FUT-013).
- **`FUT-012`** Tenant-user lifecycle: `GET /api/v1/tenant/users` (paginated list), `POST /api/v1/tenant/users/invite`, `PATCH /api/v1/tenant/users/{id}/disabled` — backed by 3 new `services/auth` RPCs gated on the new `'tenant'` RBAC scope. Frontend route `/tenant/users` shared between tenant-admin and platform-admin (see futures.md §FUT-012).

---

## How to resume

```bash
cd frontend && npm install
npm run dev    # → http://localhost:5173
```

Backend stack: `docker compose -f infra/docker-compose/docker-compose.yml up -d`
