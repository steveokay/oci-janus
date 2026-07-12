# futures.md — Prioritized Backlog

> Items that are **not yet started** and **not yet bucketed into a sprint or
> FE-API number**. As an item gets picked up it moves out of this file and
> into [`status-tracker.md`](status-tracker.md) (backend, while in flight)
> or [`FE-STATUS.md`](FE-STATUS.md) (frontend) with an appropriate FE-API
> or REM identifier. When work ships, the tracker entry is replaced with
> a resolution note in [`status.md`](status.md).
>
> Convention: items are listed in rough priority within each tier. The
> tiering is opinionated — see the "How to use this file" section at the
> bottom for the criteria. Surfaced 2026-06-21 during a gap audit after
> S9.5 + REM-011 wrapped.

---

## Tier 1 — Table-stakes for any production registry

These are not enhancements; they are gates a customer running real
workloads will refuse to deploy without. Estimated as 1-2 sprints each.

### 1. MFA + session management
- **Why:** No two-factor enforcement today. A stolen password or JWT is a
  full takeover of every push/pull credential the user holds.
- **✅ SHIPPED 2026-07-05 (core):** TOTP step-up — enrolment with QR +
  8 single-use backup codes, `/users/me/mfa*` BFF routes + `/login/mfa`
  challenge, AES-256-GCM secret under a dedicated `MFA_SECRET_KEY_HEX`
  KEK, and the "require MFA for all members" policy toggle (deployment-wide
  in single mode) via `token_policies.require_mfa`. PR #267 (+ SEC-078/079/080
  hardening in #267/#268). Design: `docs/superpowers/specs/2026-07-05-mfa-totp-design.md`.
  Resolution rows in [`status.md`](status.md).
- **✅ SHIPPED 2026-07-05 (active-session list):** self-service session
  management on `/settings/account` — device label, IP, last-active, with
  per-row revoke + "sign out all others". New `user_sessions` table +
  stable `sid` JWT claim (preserved across refresh) + fail-closed
  `revoke:sid` gate + fail-open debounced `last_active` + hourly expiry
  sweep. PR #270 (squash `91f42f4`). Design/plan under
  `docs/superpowers/{specs,plans}/2026-07-05-active-session-list*`.
  Resolution row in [`status.md`](status.md). (Note: built its own
  `user_sessions` source-of-truth rather than backing onto the SSO-CSRF
  `auth_login_sessions` table, which is not a session store.)
- **Still open (deferred sub-items):**
  - Optional WebAuthn / hardware key support (deferrable; TOTP already
    unblocks most enterprise procurement).
- **Affects:** `services/auth`, `services/management`, `frontend`.
- **REDESIGN-001 note (2026-06-28):** sessions are deployment-wide (single
  mode). The shipped "require MFA" toggle is therefore deployment-wide, as
  designed; the new `user_sessions` rows still carry `tenant_id` (populated
  with the bootstrap tenant id) per the mode-agnostic schema rule.

### 2. Tag immutability + image promotion workflow
- **Why:** Without an immutability flag, an attacker (or a sleepy
  engineer) can re-push `myapp:1.0` and silently change what every
  consumer pulls. Image promotion (dev → staging → prod) is also
  unsafe without it.
- **What:**
  - `repositories.immutable_tags BOOLEAN` opt-in flag (per-repo, defaults
    false). When true, services/core's PutManifest rejects re-pushes to
    an existing tag with `MANIFEST_INVALID`.
  - "Pin tag" affordance on the tags table: marks a single tag
    immutable without flipping the whole repo.
  - Promotion UI: copy `dev/myapp:abc` to `staging/myapp:1.0` — records
    a `tag.promoted` audit event, optionally re-signs the manifest.
- **Affects:** `services/core`, `services/metadata`, `frontend`.

### 3. Admission policy — signed-image enforcement — DONE (Phase 1 + Phase 2, 2026-06-23)
- **Why:** Signing exists (REM-011 + FE-API-003/025/026) but nothing
  gates a pull on signature presence. A repo can be "signing required"
  in policy and still serve unsigned images.
- **What shipped (Phase 1, branch `feat/signed-image-admission`):**
  - `repositories.require_signature BOOLEAN DEFAULT FALSE` (migration
    `00015`) + `Repository.require_signature` proto field +
    `UpdateRepositorySignaturePolicy` metadata RPC.
  - services/core's `GetManifest` + `HeadManifest` consult
    services/signer's `ListSignatures` when the repo flag is on; zero
    signatures → `403 DENIED` with body
    `repository requires a signed manifest; sign the image or turn require_signature off`.
  - Fail-OPEN posture on metadata / signer reachability blips (warn +
    continue) so a transient outage doesn't break every pull.
    `SIGNER_GRPC_ADDR` unset → registry-core warns at startup and
    allows all pulls (dev-stack convenience).
  - BFF: `PATCH /api/v1/repositories/{org}/{repo}` accepts
    `require_signature: bool` (optional `*bool` so unrelated PATCHes
    don't reset it); separate RPC so audit log shows the
    security-relevant flip explicitly.
  - Frontend: `RepoSignaturePolicySection` card on the repo Settings
    tab next to `RepoImmutabilitySection` (both are repo-wide security
    flips with the same shape; they compose independently).
  - Docs: README capability matrix, docs/SERVICES.md core+management,
    docs/SIGNING.md §8 admission walkthrough.
- **Phase 2 (DONE 2026-06-23):** per-repo trusted-signer-key allowlist.
  New `repository_trusted_keys` table (migration 00016) keyed by
  `(repo_id, key_id)` + 3 metadata RPCs (List/Add/Remove) + 3 BFF
  routes + `RepoTrustedKeysSection` card on the Settings tab.
  services/core's `checkSignatureAdmission` intersects recorded
  signature `key_id`s with the allowlist. Empty allowlist falls back
  to Phase 1 "ANY signature passes" by design so the policy flip
  doesn't break pulls in the gap. ListRepositoryTrustedKeys cached
  for 30s via REM-007; Add/Remove bust the cache via a new
  `bustTrustedKeysCache` helper so flips take effect on the next
  pull.
- **Recent-signers picker (2026-06-23):** Approve-Trusted-Key dialog
  now has a "Pick from recent signers" mode that surfaces every
  `key_id` that recently signed in this repo so the operator no
  longer has to copy-paste from the tag-detail Signing panel.
  BFF-orchestrated only — new
  `GET /api/v1/repositories/{org}/{repo}/recent-signers` route on
  services/management walks the most recent N tags, fans out
  `signer.ListSignatures(manifest_digest)` per tag, dedupes by
  key_id, and returns the top N by recency. No proto change. Empty
  result falls back to Manual entry transparently; SIGNER_GRPC_ADDR
  unset returns 200 + empty list so the picker degrades gracefully
  without an error toast.
- **Phase 3 (deferred):** multi-key quorum ("require ≥N distinct
  approved key_ids"), automated rotation/expiry, Cosign keyless
  Fulcio identity binding. Not on a sprint yet.
- **Affects (shipped):** `services/metadata`, `services/core`,
  `services/management`, `frontend`.

### 4. Audit log streaming to SIEM — DONE (Phase 1 + Phase 2, 2026-06-23)
- **Why:** Enterprise procurement asks for syslog/CEF export on day one.
  Customers want every push, pull, role grant, signed scan in Splunk /
  Datadog / Elastic for their own retention + correlation.
- **What shipped (Phase 1, branch `feat/audit-siem-streaming`):**
  - New `audit_export_configs` table (1:1 with tenant, AES-256-GCM-encrypted
    `hmac_secret` + `bearer_token`, format enum, JSON event_filters,
    observability counters).
  - 4 gRPC RPCs on AuditService: Get / Put / Delete / Test. Secret
    material never returned over the wire — only `*_set` booleans.
  - 3 wire formats in `services/audit/internal/export/`:
    - **syslog_rfc5424** — RFC 5424 line over TCP / TLS with SD block
      keyed by PEN 53430.
    - **cef** — ArcSight Common Event Format body, transported over
      syslog framing.
    - **webhook** — JSON POST over HTTPS with `X-Signature: sha256=<hex>`
      HMAC or `Authorization: Bearer …`.
  - SSRF guard runs at both write time + every delivery (DNS can shift):
    blocks RFC 1918 / loopback / link-local / CGNAT.
  - Dispatcher wired into the eventconsumer's INSERT path — after each
    successful audit_events row, a goroutine renders + ships. v1 ships
    in-process retry (3 attempts, exponential backoff capped at 5s);
    exhausted attempts bump `dlx_depth` for FE visibility.
  - BFF: 4 HTTP routes at `/api/v1/workspace/me/audit-export[/test]`
    workspace-admin-gated.
  - Frontend: `/workspace/audit-export` settings page with format
    selector / URL + secret form / filter editor / "Send test event"
    button / observability pills + last-success / dlx_depth banner.
  - Live verified: `image.signed` event flowed audit DB → exporter
    → HMAC-signed POST → receiver with sig-ok=true in ~3s end-to-end.
- **What shipped (Phase 2, same branch follow-on 2026-06-23):**
  - New `services/audit/internal/exportworker/` package — Publisher
    (eventconsumer enqueue path), Consumer (drains audit.export.tasks
    + runs `export.Deliver` + ACK on success / NACK→DLX on exhaustion),
    Drain (one-shot republish from DLX → tasks queue), MgmtClient
    (RabbitMQ Management HTTP API for live queue depth).
  - `audit.export` topic exchange + `audit.export.tasks` quorum
    queue with `x-dead-letter-exchange = dlx.audit-export` + paired
    `audit.export.dlx` quorum queue bound on `#`.
  - 5th gRPC RPC: `DrainAuditExportDLX(tenant_id) → republished`.
    BFF route: `POST /api/v1/workspace/me/audit-export/drain`. FE:
    "Drain DLX → retry" button appears when `dlx_queue_depth > 0`.
  - Proto + repo + handler extended with `dlx_queue_depth` (live)
    distinct from `dlx_depth` (lifetime monotonic). -1 surfaces as
    "depth unknown" when Mgmt API is unreachable so the FE
    distinguishes that from "empty."
  - Producer side becomes near-instant (publish-confirm only) so a
    slow SIEM never back-pressures the audit DB INSERT path.
  - Phase 1 inline dispatcher kept as a safety net for environments
    without RabbitMQ — operator can revert by unsetting RABBITMQ_URL.
- **Live verified:** kill receiver → fire 3 sign events → DLX fills
  to 3 (live depth matches Mgmt API) → restart receiver → POST
  /drain returns `republished: 3` → consumer re-ships → receiver
  gets all 3 with `sig-ok=true` → DLX back to 0 →
  `last_success_at` advances.
- **Phase 3 (deferred):** delayed-retry queues (per-tenant
  exponential backoff via TTL chains), per-format throughput
  shaping, Mgmt API auto-rotation for the RabbitMQ user the audit
  service uses to query the Mgmt API.
- **Affects (shipped):** `services/audit`, `services/management`,
  `frontend`, `infra/docker-compose`.
- **Docs:** `docs/SIEM-EXPORT.md` §7 (full retry + DLX walkthrough).

### 5. SCIM provisioning — DONE (Phases 1–2, 2026-07-11; branch `feat/scim-provisioning`)
- **Status:** **Phases 1–2 shipped** — the SCIM Users-only v1 backend core
  (`/scim/v2/*` on `registry-auth`: single global Argon2-hashed bearer token +
  fail-closed `requireSCIMAuth`, `users.external_id` + `scim_config` schema,
  discovery + Users CRUD, provision/link/`409`-takeover-guard/`reader@*`-grant/
  disable-not-delete). See `status.md`. **Phase 3 (admin token-management UI:
  generate/rotate/disable + BFF passthrough + Settings panel) and Groups /
  IdP-group→role mapping remain deferred** — the items below.
- **Why:** Manual user lifecycle doesn't scale past ~50-user customers.
  Okta / Azure AD admins expect to add a user to "engineering" and have
  the registry give them the right tenant + role automatically.
- **What (remaining):**
  - ~~SCIM v2 `/scim/v2/Users` endpoints on services/auth~~ — **DONE**.
  - SCIM `/scim/v2/Groups` endpoints (deferred).
  - Mapping: IdP group → role (e.g. `eng-admin@acme.okta` → `role=admin`)
    (deferred).
  - Admin UI: SCIM token issuance + mapping editor (Phase 3, deferred).
- **Affects:** `services/auth` (done), `services/management`, `frontend`.
- **Depends on:** `FUT-012` (tenant-user lifecycle management) — SCIM
  is the automated source-of-truth layered on top of the same
  invite / disable / list machinery. Build the manual surface first.
- **REDESIGN-001 note (2026-06-28):** per-tenant SSO was collapsed to a
  global `global_sso_config` table per RM-003. SCIM mapping now hangs off
  the global IdP. Single mode = natural shape.

---

## Tier 2 — Operationally high-value

Lifts UX from "works" to "delightful," or fills gaps that surface
quickly in real operator workflows.

### 1. Workspace-wide search
- **Why:** Today there is no way to find a repo / tag / digest without
  knowing the org name first. Cmd+K palette exists but only knows about
  routes, not content.
- **What:** Search bar in the topbar that hits a new `/api/v1/search`
  BFF route. Returns repos, tags, manifests, recent scans matching the
  query. Postgres `pg_trgm` or full-text index on the relevant columns.

### 2. Repo settings page
- **Why:** `/repositories/$org/$repo` Settings tab currently shows an
  EmptyState placeholder ("arrives in a later sprint"). Quota override,
  description, visibility toggle, tag-immutability flag, signature
  policy, and webhook subscriptions all belong here.
- **What:** Wire the existing repo-update RPC, add the immutability +
  signature flags from Tier 1 #2 + #3 when they land.

### 3. Image diff between two tags
- **Why:** "What shipped this week?" is a common review question. Today
  the only answer is reading the dockerfile + git log out of band.
- **What:** UI: pick tag A + tag B → table of layers added/removed +
  package version deltas (parsed from SBOMs if available) + size delta.
  Backend: new comparison RPC on services/metadata that joins two
  manifests.

### 4. Image lineage / provenance surface — DONE (PR #317, FE-API-060, 2026-07-11)
- **Why:** A real deploy traces back to "this manifest was built from
  this commit by this CI run." OCI annotations carry it; we don't
  surface it.
- **What:** Parse `org.opencontainers.image.*` annotations on push (we
  already store the manifest JSON) and surface as a "Provenance" panel
  on the tag detail page: git commit, source URL, build URL, vendor.
- **Resolution:** Shipped as a new **Provenance** tab on the tag-detail
  page. Rode entirely on existing plumbing — the BFF already fetched +
  parsed `manifests.raw_json`, so **no new migration, proto, gRPC call,
  or BFF route**: `handleGetManifest` gained `rawManifest.Annotations`
  (top-level OCI annotations, present on both image manifests and
  indexes) + `ManifestResponse.Provenance`, built by `buildProvenance`
  (`services/management/internal/handler/provenance.go`). Annotations are
  attacker-controlled, so URL fields pass through `safeExternalURL`
  (http(s)-only, drops `javascript:`/`data:`), values are capped at 1 KiB,
  and the raw passthrough map at 64 entries; the FE re-guards every
  constructed href with a client-side `safeHref` (defense in depth). New
  `provenance-panel.tsx` (linkified commit when source+revision present,
  source/docs links, base image, collapsible raw annotations, empty
  state). 3-agent review batch (security/qa/code-review) all PASS/APPROVE.

### 5. Pull bandwidth quota + per-tag pull stats — DONE (FE-API-042)
- **Resolution:** Closed 2026-06-21. `pull.image` event published from
  services/core on every successful manifest GET; services/audit
  consumes + writes `audit_events` rows. `GetAnalytics(metric=pulls)`
  returns real bucket counts (was flat-zero before). Two-track design:
  full-fidelity audit/analytics + debounced `last_pulled_at` on
  `manifests` for retention. See status.md FE-API-042 row.
- **Follow-up still open:** services/proxy doesn't publish `pull.image`
  yet (only services/core does). Anonymous IPs / public-pull
  attribution not captured. Both tracked as their own items.

### 6. Service-account API keys — DONE (FE-API-048)
- **Why:** Today every API key is tied to a human (issued from their
  `/profile`). When the human leaves, the key still works until
  someone notices. Real CI bots want a workspace-owned identity.
- **What:**
  - `service_accounts` table on services/auth — tenant-scoped, with a
    name, plan-allowed scopes, last-used-at.
  - API keys can be issued against a service account OR a user.
  - Workspace admin UI on `/api-keys` (the new route this commit
    shipped) — list of accounts, issue/revoke keys per account.
- **Note:** FE-API-048 shipped the core implementation (shadow-user
  principal pattern + `/api-keys` hub). Items FUT-001..004 below
  capture the next-level machine-identity features; their preview UI
  surfaces are already in place with dummy data.

### 7. API key scopes — DONE (FE-API-048)
- **Resolution:** Closed 2026-06-22 with the service-account work.
  `services/auth` accepts a `scopes []string` on key creation; the
  scope intersection logic clamps a bot's effective scope to its
  service account's allowed list at `/auth/token` exchange time. The
  `/api-keys` create dialog renders permission chips; ValidateAPIKey
  emits a `pentest.cross_tenant_attempt` audit row when a key tries to
  use scopes outside its principal's allowance. See status.md
  FE-API-048 row + docs/EVENTS.md "Service account lifecycle".

---

### FUT-019 — Scheduled notifications + `/settings` hub

**Surfaced:** 2026-06-25 during the FUT-012 Phase C build. Today the
dashboard surfaces *event-driven* notifications via the topbar bell
(push.image, scan.completed, retention.evaluated, etc. via the
existing FE-API-008 feed). There is no shape for **policy-driven or
calendar-driven** messages — "your Trivy adapter is N months old",
"3 invite tokens expire tomorrow", "90-day password rotation
reminder", "mTLS cert expires in 14 days". Operators routinely get
burnt by exactly this class of missed nudge in real registries. The
`/admin/scanner` page surfaces the adapter version *if you look*; a
periodic notification tells you *when you should look*.

There is also no `/settings` route. Profile lives at `/profile`;
notification preferences, display preferences, security settings
(MFA enrolment when Tier-1 #1 lands) all want a single home. The
natural pattern (GitHub, Linear, Notion all do this) is a sticky-
bottom cog icon in the sidebar opening a tabbed `/settings` page.

**Why this matters:** keeps operators ahead of preventable incidents
without forcing them to subscribe to every audit event. Per-category
opt-in means an operator can mute "retention summary" but keep
"certificate expiry" — same posture as GitHub's email notification
preferences, which is the bar customers compare against.

**Locked design:**

1. **`/settings` hub** (sidebar cog, sticky-bottom)
   - Tabs: Notifications, Profile (move existing /profile content
     here), Display (dark/light/system — currently scattered), Security
     (MFA enrolment hook for Tier-1 #1).
   - Single route + tabs over multiple routes because the tabs share a
     `Save preferences` posture and the URL is the entry point for
     deep-links from notification messages ("Update your preferences
     here →").

2. **Scheduled notifications backend** — extend `services/audit`
   rather than spinning up `services/notifications`. Audit already
   owns the eventconsumer + `NotificationEvent` proto + the bell-feed
   handler — adding a scheduled emitter is ~200 LOC + 1 cron-style
   loop + 1 new table:
   - `scheduled_notifications(id, category, run_at, payload_json,
     state)` — the worker drains this with `FOR UPDATE SKIP LOCKED`,
     emits one `notification_events` row per recipient, marks the
     scheduled row delivered.
   - `user_notification_preferences(user_id, category, enabled,
     channel)` — per-user opt-out. Defaults to "bell on, email off,
     webhook off" for everything.

3. **Category catalogue** (per `/settings → Notifications` checkbox):
   - `scanner_freshness` — Trivy/Grype adapter version vs. latest
     release. Emits monthly + when a new minor release lands.
   - `invite_expiry_warning` — N days before an invite token expires,
     ping the inviter so they can resend.
   - `cert_expiry_warning` — mTLS / TLS cert within 14 days of
     expiry. Critical operator-facing event.
   - `password_rotation_reminder` — 90-day cadence per user.
   - `retention_dry_run_summary` — weekly digest of what retention
     *would* delete if grace fired now. Lets operators tune rules
     before they hard-delete.
   - `failed_login_burst` — N failed logins inside M minutes from a
     single IP / user. Already an audit event but a notification
     elevates it.
   - `plan_quota_threshold` — at 80% storage / pull quota. Currently
     surfaced as a dashboard chip; adding to notifications means the
     operator sees it without opening the dashboard.

4. **Frontend** — new `/settings` route + `<NotificationsTab>` table
   (category | description | bell toggle | email toggle | webhook
   toggle) + existing `/profile` content moved here. Sidebar cog
   anchored at the bottom of the sidebar (sticky `mt-auto`) so it's
   always reachable regardless of where the operator scrolled.

5. **Tone discipline (locked):** "Trivy adapter at v0.52.0 (released
   2026-04-15). 4 newer minor releases available with X new CVE
   families covered. Update via `/admin/scanner`." First sentence
   carries the actionable noun + verb. Operators ignore vague
   "you have a notification" nudges; the schedule worker MUST emit
   actionable bodies or it gets muted.

**Affects:** `services/audit` (proto + migration + worker), the
existing `services/management` notifications wrapper, `frontend/`
(new route + sidebar cog + 2 new components). No new services.

**Dependencies:** Tier-1 #1 (MFA) shares the `/settings → Security`
tab — coordinate so MFA enrolment plugs in cleanly when it lands.
The sidebar cog refactor (small first PR, see below) can land before
the rest.

**Phasing recommended (~half day skeleton, then iterative):**

1. **Sidebar cog refactor** (~half day, no backend). Sticky-bottom
   Settings cog in the sidebar. New `/settings` skeleton with
   Profile + Display tabs (move existing /profile content over). No
   backend changes — purely structural. Lands the visible
   scaffolding.
2. **scheduled_notifications worker + first category** (~1-2 days
   backend, ~half day FE). Build the table + worker + `scanner_
   freshness` category end-to-end. Proves the architecture against a
   concrete known-want category.
3. **Fan-out** — add the other 6 categories one at a time as
   separate small PRs (~half day each). Each one is a new payload
   shape + cron entry + checkbox.

**Effort:** Phase 1 ~half day; Phase 2 ~2 days; Phase 3 ~half day
per category. Full feature lands across ~1 sprint if dispatched
agent-style.

**Current state (2026-07-05):** the `/settings` hub + the **Notification
categories** preference matrix have shipped. As of the 2026-07-05 UI
cleanup the matrix lives on its own tab — **Settings › Notifications**
(`routes/_authenticated.settings.notifications.tsx`, `NotificationsSection`
+ `ChannelToggleCell`) — moved out of the old Settings › Account tab.
**Update (2026-07-07):** the **Email** channel has **SHIPPED** (branch
`feat/fut-019-email-channel`) — Resend default + pluggable SMTP/Gmail
transport, a per-tenant transport config panel on Settings › Notifications
(AES-256-GCM secrets under `NOTIFY_EMAIL_KEY_HEX`), a per-user delivery send
loop that resolves recipients through the new `registry-auth.ResolveUserEmails`
RPC, and a topbar ✉️ delivery-log dropdown. The **Email** matrix column is now
unlocked and live.

**Update (2026-07-08): the Webhook channel has SHIPPED** (branch
`feat/fut-019-webhook-channel`; see `status.md`) — a shared per-tenant org
webhook receives one HMAC-signed POST per scheduled notification for the
selected categories. `services/audit` owns it (`notification_webhook_config`
+ `notification_webhook_deliveries`, AES-256-GCM secret under
`NOTIFY_WEBHOOK_KEY_HEX`, send loop reusing `services/webhook` retry/HMAC/SSRF
patterns; no new mTLS peer edge). FE: admin `NotificationWebhookPanel` + the
**Webhook** matrix column unlocked (FE-API-058). **Bell, Email, and Webhook are
now all live — the "Notification categories" surface is complete.**

> **Accepted follow-ups from the FUT-019 webhook review batch (2026-07-08), non-blocking:**
> - ~~**rotate-kek sweep gap (RED-FU-015 territory):**~~ **RESOLVED 2026-07-11 (PR #316).** `registry-audit rotate-kek` now sweeps both channel secrets via new `--notify-webhook` (`notification_webhook_config.secret_enc`, `NOTIFY_WEBHOOK_KEY_HEX`) and `--notify-email` (`email_transport_config`, `NOTIFY_EMAIL_KEY_HEX`) domains, mirroring auth's `--mfa` multi-domain pattern. `NOTIFY_WEBHOOK_KEY_HEX` / `NOTIFY_EMAIL_KEY_HEX` are no longer fixed.
> - **Integration-test grant guard:** `webhook_notify_test.go` (and the shared `newEmailTestRepo`) connects as the container superuser, which bypasses GRANTs — so it doesn't independently prove the `registry_audit_app` grant (the #290-class guard the spec §10 promised). Add a second pool authenticated as `registry_audit_app`. (Runtime already enforces the role via `checkRole`; the grant is present in the migration.)
> - **Consistency/coverage nits:** clamp the send-loop `fail()` error with a truncate for parity with the SendTest path (SEC-085, INFO — column is TEXT, already redacted); add direct unit tests for the sender real-error/backoff path, the handler keep-existing-secret path, and the BFF 409/happy-path mapping.

---

## Tier 2 — Access: machine identity & policy

> All four items below have preview UI surfaces already shipped (FE-API-048 T24+)
> with dummy data. Backend work is what remains.

### FUT-001: Federated workload identity (OIDC trust) — Sprint 11

**DONE — see `status.md` (REM-023).** Design history preserved below.

<details>
<summary>Original FUT-001 design (pre-implementation)</summary>

- **Why:** GitHub Actions, GKE Workload Identity, and similar OIDC-capable
  CI systems can authenticate without a stored secret at all. A trust
  relationship removes the "rotation reminder" problem entirely.
- **What:** New `oidc_trust_configs` table on services/auth; admin UI on the
  `/api-keys` Trust tab (preview surface already shipped). services/auth adds
  a `POST /auth/token/workload` exchange endpoint: validates the OIDC
  assertion against the configured JWKS URL + audience, issues a short-lived
  JWT mapped to a service account.

</details>

### FUT-002: Credential helpers (docker login / k8s YAML / terraform / GHA snippets) — Sprint 11

**DONE — see `status.md` (REM-021).** Design history preserved below for context.

<details>
<summary>Original FUT-002 design (pre-implementation)</summary>

- **Why:** Operators copy-paste credentials into CI configs and get them wrong.
  Auto-generated, copy-ready snippets reduce support burden.
- **What:** `/api-keys` Helpers tab (preview surface already shipped) renders
  per-format snippets: `docker login` command, Kubernetes imagePullSecret YAML,
  Terraform `docker_registry_image` block, GitHub Actions step. All snippets
  reference the workspace's actual registry hostname and the selected service
  account. No new backend RPCs needed — purely frontend rendering against
  existing `/api/v1/workspace/me` data.

</details>

### FUT-003: Token policies (max-TTL, force-rotation, idle-revoke) — Sprint 12

**DONE — see `status.md` (REM-025).** Design history preserved below.

<details>
<summary>Original FUT-003 design (pre-implementation)</summary>

- **Why:** Long-lived keys with no rotation policy are the #1 lateral-movement
  vector after a breach. Operators want guardrails at the workspace level.
- **What:** New `token_policies` table on services/auth keyed by tenant + scope
  (service account or all accounts). Fields: `max_ttl_days`, `rotation_interval_days`,
  `idle_revoke_days`. `/api-keys` Policies tab (preview surface already shipped).
  Enforcement: key creation rejects TTL beyond `max_ttl_days`; a background job
  (pattern: `FOR UPDATE SKIP LOCKED`) revokes keys exceeding rotation or idle
  thresholds and publishes `auth.key_revoked` audit events.

</details>

### FUT-004: Access review (quarterly stale-key nudge) — Sprint 12

**DONE — see `status.md` (REM-026).** Design history preserved below.

<details>
<summary>Original FUT-004 design (pre-implementation)</summary>

- **Why:** Without a periodic review prompt, stale keys accumulate silently.
  Security auditors expect to see evidence that access is re-certified.
- **What:** Scheduled job emits `auth.access_review_due` audit events (and
  webhook deliveries) once per configured interval (default 90 days) listing
  keys not used in that window. `/api-keys` Review tab (preview surface already
  shipped) surfaces the list with bulk-revoke action. Platform admin can
  configure the interval per tenant via a new settings field.

</details>

### FUT-005: Wire ActivityService audit gRPC client — DONE (sprint-11 maint batch 1)
- **Resolution:** Closed 2026-06-22. Added `AUDIT_GRPC_ADDR` to
  `services/auth/internal/config` and a `buildClientCreds` helper mirroring
  the existing server-side mTLS pattern (reuses `MTLS_CA_CERT_PATH` /
  `MTLS_CERT_PATH` / `MTLS_KEY_PATH`). `services/auth/internal/server/server.go`
  now dials registry-audit when the address is set, constructs
  `service.NewActivityService(users, auditClient)`, and registers it via
  `httpH.WithActivityService(...)`. Falls back to a `slog.Warn` (route stays
  501) when the address is empty so dev stacks without audit still boot.
  Snake-case JSON tags added to `service.PrincipalActivity` so the
  Beacon-themed activity table consumes the response without renaming.
  `infra/docker-compose/docker-compose.yml` sets
  `AUDIT_GRPC_ADDR: registry-audit:50051` on `registry-auth`.
  **Live verification:** admin `GET /api/v1/access/activity?limit=5` returns
  200 with real audit data (`push.image` event for `dev/nginx` already
  visible in the dev stack).

### FUT-006: `/users/me` SA-key authentication — DONE (2026-06-23)
- **Resolution:** Picked option (a) — `requireAuth` now accepts a Bearer
  token of shape `key.<uuid>.<secret>` in addition to JWTs. The `key.`
  prefix is the discriminator; anything without it falls through to
  `ValidateToken`. On successful `ValidateAPIKey`, we synthesise a
  `*service.Claims` with `Subject = vk.UserID.String()` (the shadow user
  id for SA-owned keys, the user id for human keys), `TenantID`, and
  `Access` derived from the SA's intersected `EffectiveScopes`. `Roles`
  is intentionally empty — raw API keys don't carry RBAC roles (those
  are resolved at JWT issuance time); handlers gating on roles must
  still require a JWT (those would return a clean 403 against the empty
  roles list rather than a confusing 401).
- **Parser is strict.** `parseAPIKeyBearer` rejects empty input, missing
  prefix, prefix-only tokens, JWT-shaped three-segment values, unparseable
  UUIDs, empty secrets, and case-mismatched prefixes ("KEY." stays a
  JWT). 8 unit cases cover each shape.
- **Live verification:** `GET /api/v1/users/me` with `Authorization:
  Bearer key.<id>.<secret>` returns the same human-caller envelope a
  JWT does, including roles + memberships hydrated from the user's
  actual role_assignments. SA-owned keys flow through the same
  synthClaimsFromAPIKey path and exercise the existing T16
  `type:"service_account"` branch.
- **Wire-format docs:** `docs/SERVICES.md` registry-auth section
  documents the `key.<id>.<secret>` Bearer shape next to the JWT entry.

### FUT-007: Durable audit emission for SA lifecycle — DONE (sprint-11 maint batch 5)
- **Resolution:** Closed 2026-06-22 on `feat/sprint-11-maint-batch-5`. Chose
  option (a) RabbitMQ publish to match the existing `rbac.role_granted`
  pattern. New `events.RoutingServiceAccountLifecycle` routing key
  (`service_account.lifecycle`) + `ServiceAccountLifecyclePayload`
  (`Action`, `ActorID`, `Resource`, `Fields`) — single key carries the
  full §5.7 vocabulary via the embedded action field rather than fanning
  out per-event-type. `services/auth/internal/server/server.go` defines a
  `rabbitMQAuditEmitter` that wraps the existing `pub *publisher.Publisher`
  (already constructed for RBAC events); when `pub == nil` falls back to
  `slogAuditEmitter` so dev stacks without a broker stay correct. Every
  `s.audit.Emit` call site in `service_account.go` got a `TenantID` field
  populated so the outer `events.Event` envelope can route per-tenant
  without unmarshalling. `services/audit/internal/eventconsumer/consumer.go`
  adds the `RoutingServiceAccountLifecycle` case → propagates
  `payload.Action` verbatim into `audit_events.action` so spec §5.7's
  vocabulary becomes queryable. The eight SA lifecycle action codes were
  added to both `defaultNotificationEventTypes` and
  `allowedNotificationEventTypes` so the activity feed (once FUT-005
  merges) surfaces them alongside push/pull. `infra/docker-compose/docker-compose.yml`
  sets `RABBITMQ_URL` on `registry-auth` + `depends_on: rabbitmq healthy`.
  **Live verification:** SA create → disable → delete produced four
  rows in `audit_events` (`service_account.created`, `.disabled`,
  `.updated`, `.deleted`). One nit-cleanup follow-up: the PATCH handler
  emits both `.updated` and `.disabled` on a `{disabled:true}` body —
  cosmetic over-emission, can be deduped in a small future PR.

### FUT-008: Sign dialog "Recent signer_ids" dropdown — ⛔ SUPERSEDED by FUT-009 (shipped 2026-07-12, PR #330)
> The Sign dialog now offers a service-account `<Select>` (FUT-009) instead of
> free-form `signer_id` entry, which is the stronger version of this idea. The
> free-form path also now rejects UUID-shaped values (SEC-088). Kept for history.

- **Why:** Today the Sign dialog asks for `signer_id` as a free-form
  string — operators type the label from memory. Mirror PR #36's
  trusted-key picker pattern: surface the distinct `signer_id` values
  this tenant has used recently in a dropdown alongside the existing
  input. Cheap UX patch.
- **Note:** Only worth doing if FUT-009 is going to wait more than
  one sprint. FUT-009 supersedes this entirely by replacing free-form
  `signer_id` strings with service-account principals.
- **Affects:** `services/management`, `frontend`.

### FUT-009: Service-account-as-signing-identity — ✅ SHIPPED 2026-07-12 (PR #330)
> **DONE** — BFF `service_account_id` on the sign route (tenant-scoped SA
> resolution → shadow user_id recorded as `signer_id`), FE `<Select>` from
> `useServiceAccounts()` + custom-signer fallback, tag-detail SA-name render,
> `docs/SIGNING.md`. No proto change, no migration. Security review found +
> fixed **SEC-088** inline (UUID-shaped free-form `signer_id` rejected so it
> can't forge SA provenance). Resolution row in [`status.md`](status.md).
>
> **Deferred should-fixes (non-blocking follow-ups from the review batch):**
> - a genuine two-page `ListTenantUsers` pagination test-fake (the current
>   fake is single-page; the resolver's paging loop isn't black-box exercised);
> - an FE test that switches the Sign dialog to "Custom signer ID" mode and
>   asserts the `{signer_id: …}` payload (the zod allowlist is untested in-component);
> - if a `GetServiceAccount`/`LookupUser(shadow_id)` gRPC RPC ever lands, swap
>   the O(tenant-users) scan in `resolveServiceAccountSigner` for an O(1) lookup.
>
> Original spec retained below for reference.

- **Why:** Today `POST /repositories/{org}/{repo}/tags/{tag}/sign`
  takes a free-form `signer_id` string. No validation, no lifecycle,
  no link to a real principal. The audit trail records the string
  but can't tie it back to a tenant resource. Service accounts
  (FE-API-048) already model "non-human principal" with full
  lifecycle; reusing them as signing identities lines up cleanly.
- **What:**
  - BFF Sign route accepts `service_account_id`; resolves the SA
    (must exist + be enabled + belong to caller's tenant) → records
    the SA's shadow user_id as `signer_id` in the signatures table.
  - Sign dialog: replace free-form Input with a `<Select>` populated
    from the existing `useServiceAccounts()` hook.
  - Tag detail Signing panel: render SA display name when
    `signer_id` is a UUID; preserve free-form display for historical
    rows.
  - Recommendation: dashboard-only restriction (cosign CLI path
    stays untouched — it doesn't hit this BFF endpoint).
- **Scope:** no proto change, no migration. ~2h backend, ~2h FE,
  ~30min docs, ~30min smoke.
- **Affects:** `services/management`, `frontend`, `docs/SIGNING.md`.

### FUT-010: RBAC + FE-RBAC polish pass — ~1 sprint

> **PARTIALLY SUBSUMED 2026-06-28** by REDESIGN-001. The BFF half has shipped:
> - **Phase 5.2** (PR #131) — every `require*Admin` helper (`requireScanPolicyAdmin`,
>   `requireWebhookAdmin`, `requireDomainAdmin`) tightened to scope-aware
>   tenant-admin via `effectiveTenantAdmin`. Closes the "any-org-admin =
>   tenant-admin" gate flaw.
> - **Phase 5.1** (PR #134) — typed `users.is_global_admin` column +
>   `effectiveGlobalAdmin` helper replaces the `(admin, org, '*')` marker.
>   `GrantRole` rejects the legacy marker going forward.
>
> What REMAINS open from the original scope:
> - **FE affordance hiding** — sidebar / button / form-field gating. Now
>   mapped to REDESIGN-001 **Phase 4.4** (`useAbility()` hook + `/me/abilities`
>   BFF endpoint).
> - **`useHasRole(role, scope)` hook** — replaced by Phase 4.4's
>   `useAbility(action, scope)` (action-centric, not role-centric — cleaner
>   per Review §C2).
> - **`docs/RBAC.md`** — defer to REDESIGN-001 Phase 7.1 (doc rewrite).
>
> Once Phase 4.4 ships, close this item. Don't pick up FUT-010 standalone —
> it'll collide with Phase 4.

- **Why:** Role-based UI testing will surface a class of
  gaps where the FE renders affordances that the BFF will then reject
  with 403, or where the sidebar leaks admin-only groups to roles that
  can't use them. Today the testing user (`admin@dev.local`) holds
  both tenant-admin and the platform-admin marker, so these gaps
  don't show up day-to-day.
- **Scope:**
  - **BFF audit** — sweep every `/api/v1/*` route, confirm the role
    gate matches the route's destructiveness. Today most routes use
    `hasScopedRole(_, "org", _, "admin")` or the `requireDomainAdmin`
    helper; a small but real subset relies on the platform-admin
    marker. Build a test matrix (one row per route × role × scope)
    so regressions surface in CI.
  - **FE affordance hiding** — every button / settings card / form
    field that would 403 on save should be disabled (or hidden) for
    the calling role. Tooltip explains: "Role required: admin".
    Specifically:
    - `/workspace/audit-export` settings group hidden from sidebar
      for non-workspace-admin
    - `/admin/*` groups hidden from sidebar for non-platform-admin
    - Settings tab toggles (immutability, signed-image, trusted keys,
      scan policy, retention) read-only for writer/reader
    - Sign / Verify-now / Approve-key buttons disabled for
      writer/reader
    - Tag delete button disabled for reader; visible+enabled for
      writer/admin/owner
    - Webhook create/edit/delete/rotate gated on admin+
    - Member invite + role-grant gated on admin+
  - **Direct URL access** — when a non-admin types an `/admin/*` route
    in the URL bar, the route should redirect to a "not authorised"
    page rather than briefly rendering the admin shell before the
    BFF 404s come back. Today the route loader probably just renders
    and lets the data-fetch fail.
  - **Role-gating helper** — introduce a `useHasRole(role, scope)`
    hook that components can call, backed by the same JWT claims +
    role_assignments shape the BFF uses. Avoids the current pattern
    of hardcoding role checks per surface.
  - **Documentation** — `docs/RBAC.md` covering the role matrix +
    which surfaces each role can touch + how the platform-admin
    marker fits in. Cross-link from README.
- **Affects:** `services/management` (audit + helpers), `frontend/`
  (helper hook + every settings/admin surface), new `docs/RBAC.md`.
- **Estimated:** ~1 sprint. Sized larger than it sounds because the
  FE has ~30-40 distinct admin-gated affordances and each needs a
  permission check + disabled state + tooltip.
- **Surfaced:** 2026-06-23 role-persona testing (`tenant_only`
  writer-role test).

---

## Review batch — 2026-06-23

Three review agents (design / quality / architecture) did a deep cross-cutting
review on 2026-06-23, surfacing 74 findings (24 `DSGN-*` + 28 `QA-*` + 22 `ARCH-*`).
Full per-finding detail (file paths, line numbers, proposed fixes) lives in
[`.claude/reviews/`](.claude/reviews/). Listed below are the curated items
prioritised for backlog uptake.

### Tier 1 — must-fix correctness/security from the review batch

- **ARCH-001** — Implement PostgreSQL RLS on metadata / auth / webhook / proxy /
  tenant schemas. Documented as a second defence layer in CLAUDE.md §9; only
  `audit_events` actually has it today. Per-service migration + `SET LOCAL
  app.tenant_id` middleware + low-privilege role per service. **Effort:** M.
- **ARCH-002** — Add a transactional outbox for RabbitMQ event publication.
  `push.completed` / `scan.completed` / `image.signed` / `rbac.*` /
  `service_account.lifecycle` are all "DB commit → publish" — a broker outage
  or crash between the two silently drops the event. Per-service
  `outbox_events` table + background drainer using `FOR UPDATE SKIP LOCKED`.
  **Effort:** M.
- **ARCH-005** — Enforce production-mode config invariants in `libs/config/loader`.
  CLAUDE.md §7 says "reject empty cert paths when `OTEL_ENVIRONMENT=production`" —
  no service does this. Apache 2.0 release means anyone can stand this up; silent
  insecure defaults are a footgun. **Effort:** S. Fold in (Fable sec review
  2026-07-01): refuse to boot when a known-default dev credential fingerprint
  (`registry/registry`, `minioadmin`, `dev-root-token`) is detected in
  production mode — same posture as the `sslmode=disable` rejection (SEC-022).
- ~~**QA-001**~~ — DONE 2026-06-24 (PR #64). Migration `000002` adds
  `tenant_id` + composite UNIQUE `(tenant_id, manifest_digest, signer_id)`;
  `services/signer` propagates `tenant_id` through store / repo / handler;
  Cosign payload's `optional.tenant` field cryptographically binds each
  signature to its tenant so cross-tenant replay fails verification.
  External callers (`services/core`, `services/management`) already
  passed `TenantId` in `ListSignaturesRequest` — the field was on the
  proto but the server ignored it. **QA-015** subsumed (tenant_id is
  now in the Cosign payload, no longer "unused parameter").
- ~~**QA-002**~~ — DONE 2026-06-23 (PR #46 main fix + PR #47 deterministic
  regression tests). `libs/rabbitmq/publisher.Publish` now serialises the
  publish-then-read-confirmation sequence with `sync.Mutex` + synchronously
  drains stale confirmations on ctx-cancel/timeout. Two follow-up nits
  (Close() lock + configurable timeout) tracked at the bottom of this
  batch. See [`status.md`](status.md) for the full resolution note.
- ~~**QA-003**~~ — DONE 2026-06-24 (PR #62). `PollDueDeliveries` now
  wraps SELECT in `pool.Begin` + leases picked rows by pushing
  `next_attempt_at` 5 min into the future, then COMMIT before
  returning. No new status column needed.
- **ARCH-016** — Ship `make bootstrap` / `tools/bootstrap` for self-hosters.
  `SELF-HOSTING.md` §3 is 9 manual openssl+base64 steps before first push;
  the "10 min to first push" claim is aspirational without this. **Effort:** S.

### Tier 2 — robustness, security & UX polish from the review batch

- ~~**DSGN-001**~~ — DONE 2026-06-24 (PR #53). `isWorkspaceAdmin` helper +
  3 call-site swaps + AccessSubNav tests.
- ~~**DSGN-003**~~ — DONE 2026-06-24 (PR #50). `ConfirmDestructiveDialog`
  primitive with 3 severity levels + 6 `window.confirm` migrations + DSGN-012
  Phase-1 fallback warning folded in.
- ~~**DSGN-004**~~ — DONE 2026-06-24 (PR #52). `ErrorState` w/ HTTP code +
  detail + request-id expander. New `lib/api/error.ts` helper.
- ~~**DSGN-023**~~ — STALE (verified 2026-07-04): the mobile shell shipped with
  REDESIGN-001 Phase 4.5 (`frontend/src/components/shell/mobile-nav.tsx` +
  tests); below-1024px nav is a drawer with full route parity.
- ~~**QA-004**~~ — DONE 2026-06-24 (PR #59). JWT cache key in
  `services/core` + `services/proxy` now uses `jti` (extracted via a
  small inline `parseJTI` helper) instead of the raw token. Malformed
  tokens skip the cache and fall through to `grpc.ValidateToken`.
- ~~**QA-005**~~ — DONE 2026-06-24 (PR #59). `services/scanner.Store`
  gained `Sweep(maxAge)` + `StartSweeper(ctx, interval, maxAge)`;
  server.go runs `go scanStore.StartSweeper(ctx, time.Hour, 24*time.Hour)`
  so terminal-status rows older than 24h are dropped each hour.
- ~~**QA-007**~~ — DONE 2026-06-24 (PR #62). Webhook SSRF dialer now
  picks the first validated resolved IP and dials by IP literal so the
  underlying dialer doesn't re-resolve the hostname. HTTPS SNI
  unaffected (`http.Transport` derives SNI from the request URL).
- **QA-013** — Add `tenant_id` to upload Redis keys + constant-time tenant
  check in `services/core` upload handler. **Effort:** S.
- ~~**QA-015**~~ — SUBSUMED by QA-001 (PR #64). `tenant_id` is now baked
  into the Cosign payload's `optional.tenant` field; no longer an
  "unused parameter".
- **QA-019** — Top-level React `ErrorBoundary` in `__root.tsx`. Render-time
  exceptions currently show a blank page with no diagnostic. **Effort:** S.
- **QA-020** — Frontend test coverage pass: 3 test files for ~140 components.
  Prioritise `lib/api/client.ts` (refresh+retry stampede), `lib/auth/store.ts`,
  `lib/auth/jwt.ts`, plus auth route + role-gate snapshots. **Effort:** L.
- **ARCH-003** — Helm migration `Job` + `initContainer` for multi-replica
  rollouts. Today every replica races on goose's advisory lock at boot.
  **Effort:** M.
- **ARCH-004** — Helm graceful shutdown: `terminationGracePeriodSeconds: 120` +
  `preStop` sleep across every deployment chart. **Effort:** S.
- **ARCH-006** — `libs/rabbitmq/publisher` reconnection + channel recovery on
  `NotifyClose`. ~50 lines. **Effort:** S.
- **ARCH-009** — Circuit breaker + singleflight on `auth.ValidateToken` client.
  Today the documented 3× retry amplifies the thundering herd on JWT key
  rotation / cache flush. **Effort:** S.
- **ARCH-010** — Nightly orphan-row reconciliation across services that hold
  `tenant_id` columns (auth/webhook/audit/proxy/scanner): sweep rows whose
  owning entity no longer exists. (Dropped the multi-mode `tenant.deleted`
  cascade framing — single mode never deletes the bootstrap tenant.)
  **Effort:** M.
- **ARCH-012** — Helm `ServiceMonitor` + starter `PrometheusRule` + Grafana
  dashboard JSON. Self-hosters install the chart and see nothing in Grafana
  today. Biggest "self-hoster smiles" lever. **Effort:** M.
- **ARCH-021** — Local JWKS verifier `libs/auth/jwt-verify` so services
  verify signatures locally + only hit `services/auth` for the revocation
  check. Today auth-down wedges every service (fail-closed). **Effort:** M.

### Tier 3 — hygiene & polish from the review batch

Lower priority — pick when picking up neighbouring work in the same file:

- ~~DSGN-006~~ DONE PR #55 (repo Settings sub-sections + sticky ToC).
  ~~DSGN-010~~ DONE PR #57 (scanner adapter sort + active-name button).
  ~~DSGN-017~~ DONE PR #50 (focus rings on Button/Dialog/Switch/Tabs).
  ~~DSGN-002~~ STALE (verified 2026-07-04 — absorbed by the REDESIGN-001
  Phase 4.2 sidebar IA rework).
  **Still open:** **DSGN-008** (topbar `<Breadcrumbs/>` from `useMatches`),
  **DSGN-009** (audit-export observability tiles redesign),
  **DSGN-018** (extract `<SecretInput>` primitive from audit-export pattern),
  **DSGN-024** (extract `<PageHeader>` primitive — 8+ header shapes today).
- ~~DSGN-007 / -011 / -012 / -013 / -014 / -015 / -016 / -019 / -020 / -022~~ —
  all DONE (PR #50 shipped 007/012/013/014/017/022 + folded 012 into 003;
  PR #57 shipped 011/015/016/019/020).
- ~~**QA-006**~~ DONE PR #59 (auth `init()` no longer reads
  `TRUSTED_PROXY_CIDRS` from `os.Getenv`; env access moved to config
  layer + `SetTrustedProxies` setter called from `server.go`).
- **QA-008 / -009 / -010 / -011 / -012 / -014 / -016 / -017 / -018 /
  -019 / -021 / -022 / -023 / -025 / -026 / -027 / -028** — config-loader
  hygiene, bounded webhook dispatch, retry-interceptor cleanup, ctx-aware DNS,
  stream-interceptor request_id, scanner queue-full surfacing, gateway stub
  doc, signer ctx propagation, repository.storage_used denorm, ListRepositories
  pagination, frontend ErrorBoundary, axios exact-match exempt list,
  `time.Sleep` removal in integration tests, RequireAuth caching, scanner
  policy-resolver fail-closed, handler.go split, scanner publish-after-write
  ordering, `DetachContext` helper.
- **ARCH-007 / -008 / -011 / -013 / -014 / -015 / -017 / -018 / -019 / -020 /
  -022** — BFF/RBAC ownership cleanup, TenantPolicyService read facade,
  tenant-export tooling, read-replica adoption in audit + auth, Compose
  per-service-db profile, storage backend smoke profiles, GC CronJob + Deployment
  split, schema-evolution docs, `libs/delivery` reuse, in-process Cosign
  verification, multipart storage driver interface.

### UI-REVIEW-2026-07 — deferred nits from the 2026-07-04 four-agent UI sweep

The 2026-07-04 UI review produced 47 findings; batches 1–3 shipped 30 of them
(PRs #257/#258/#259 — see `FE-STATUS.md` → "UI polish review").

**UIR-1..10 — SHIPPED 2026-07-05 (PR #279, FE-API-054).** The deferred
remainder landed as one polish batch: GC "best-effort" caption gated on a real
timestamp (UIR-1); HealthCard pulse restricted to non-success tones (UIR-2);
retention 24h/7d "based on the latest N runs" caveat + hoisted scan-limit
constant (UIR-3); per-cell pending on the notification matrix so one write no
longer freezes all 12 checkboxes (UIR-4); login SSO buttons rendered visibly
disabled with a "coming soon" caption (UIR-5); access-review owner UUID
shortened + tooltip + copy (UIR-6, *partial* — see residual below); dead
`sticky`/`backdrop-blur` dropped from the topbar (UIR-7); notifications unread
badge paired with a new `--color-highlight-fg` token (UIR-8); PoliciesPanel
save success/error moved to sonner toasts (UIR-9); labelled "Copy secret"
button on SecretRevealDialog (UIR-10).

**Residual OPEN — UIR-6 owner name resolution (backend).** The access-review
row (`StaleKey`) only carries `owner_user_id` on the wire, so the FE can only
shorten/tooltip the UUID — it can't render `@username` / display name via
`UserCell` (which needs those fields, and the owner may be a service-account
shadow user absent from any org-members list). Proper fix: a BFF join adding
`owner_username` + `owner_display_name` to the `ListStaleKeys` response, then
swap the owner cell to `<UserCell variant="inline">`. **Effort:** ~half day
(proto field + audit/auth join + FE swap).

### REM-017 — Platform-admin "claim a new org" route (chicken-egg)

**Surfaced:** 2026-06-24 during new-user-onboarding smoke testing on the
local stack. Platform admins can't bootstrap a new org from the FE: org
creation is side-effected by repo creation, which requires `hasScopedRole(
"org", body.Org, "admin")` — and the platform-admin marker
(`(admin, org, *)`) is treated as a literal scope_value, not a wildcard
(deliberate per PENTEST-024). Result: admins can only create repos under
the seeded `dev` org. To get a different org name, an operator must run
`INSERT INTO role_assignments...` via SQL — not viable for self-hosters
following SELF-HOSTING.md.

**Scope:**

- New BFF route `POST /api/v1/admin/orgs/{org}/claim` gated on the
  platform-admin marker. Validates org name, calls
  `metadata.GetOrCreateOrganization`, then `auth.GrantRole(admin, org,
  <org>)` to the caller. Atomic — rollback if either step fails.
- FE: small affordance on the Create Repository dialog. When the typed
  org doesn't match any existing org and the caller has the platform-
  admin marker, show "This org doesn't exist yet — claim it" inline
  call-to-action that calls the new route then proceeds with the
  original create-repo. Non-platform-admin sees the existing
  "insufficient permissions" message.
- Optional: dedicated `/admin/orgs` page listing all orgs with member
  counts + a "Create org" CTA.

**Effort:** half-day backend + half-day FE. ~1 day total.

**Affects:** `services/management` (new route), `services/auth`
(GrantRole already exists, no change needed), `frontend` (Create
Repository dialog + optional `/admin/orgs` route).

### REM-018-followup — Activity feed + notifications display_name surfacing

**Surfaced:** 2026-06-25 while shipping REM-018 Phase B (PR #102).
Members tables + the remove-member dialog now render the principal's
`display_name (@username)`, but two surfaces still fall back to
`actor_username || actor_id`:

- `/activity` page (`_authenticated.activity.tsx:428`)
- Topbar notifications bell (`notifications-bell.tsx:203`)

Both read from `services/audit`'s `NotificationEvent` proto, which today
carries `actor_username` (best-effort from the upstream event payload)
but no `actor_display_name`. Surfacing display_name here requires
audit-side enrichment — either a join against `services/auth` at read
time (cross-service round-trip, fast but tightly coupled) or carrying
display_name in the audit payload at write time (write-amplified, but
keeps the read path local).

**Scope:** small. New `actor_display_name` field on
`audit.v1.NotificationEvent`; consumer in `services/audit` resolves it
via the join; both FE surfaces swap their text render for the existing
`<UserCell variant="inline">` primitive shipped in PR #102.

**Effort:** ~half day.

### FUT-011 — Production new-user onboarding smoke test

> **SUBSUMED 2026-06-26** by REDESIGN-001 (`.claude/plans/2026-06-26-single-tenant-redesign.md`). The redesign's Phase 3.1 (`registry-auth bootstrap` CLI) replaces the SQL-seeded admin path; Phase 4.3 (first-run onboarding wizard) walks the new admin through org creation, repo creation, first push, and API key creation. Do not pick up FUT-011 independently; pick up the redesign instead.

**Surfaced:** 2026-06-24 (same testing session as REM-017 above).
Walked through the FE/API path: admin creates user via
`POST /api/v1/users`, grants writer via
`POST /api/v1/orgs/dev/members`, newcomer logs in + pushes. The
backend half works (verified via curl + SQL); the FE flow was
deferred ("we need to test this later").

**Scope:** drive the same flow end-to-end via the FE only. Validate:

1. Admin can create a new user from the dashboard (find or build the
   right surface — `/members`? a future `/admin/users` page?).
2. Admin grants the new user a role on `dev` from `/orgs/dev/members`.
3. New user logs in to the FE and sees the workspace surfaces a
   `writer` should see (no `/admin/*`, no Service-accounts admin).
4. New user creates an API key from `/profile` or `/api-keys`.
5. `docker login` with the API key + `docker push` to `dev/<repo>` —
   verify the push succeeds AND the manifest is attributed to the
   newcomer in the audit log + on the repo activity tab.
6. Document the flow in `docs/SELF-HOSTING.md` or a new `docs/ONBOARDING.md`.

Self-hosters following the docs should be able to bootstrap a
real multi-user setup without SQL.

### FUT-015 — Pull-command + tag/digest row expander on `/workspace/proxy-cache`

**Surfaced:** 2026-06-24 by the operator testing FUT-013 Phase C.
Each table row shows the cached image but doesn't tell the
operator HOW to pull it. They have to construct the
`localhost:8084/cache/<upstream>/<image>:<tag>` URI by hand.

**Scope:** add a chevron-expand to each row (row-expander pattern).
When expanded:

- Copy-button on the full `docker pull` command, using the
  workspace's actual host (from `useWorkspace()` if a custom
  domain is set, else `localhost:8084` in dev). Example:
  `docker pull registry.acme.com/cache/dockerhub/library/alpine:3.20`
- The digest (so an operator can pin via
  `docker pull <host>/cache/.../<image>@sha256:...`).
- The MediaType (helpful for "is this OCI v1 or Docker v2?").
- "Last pulled" + "Cached at" absolute timestamps (the row
  shows relative; the expander shows the full ISO).

**Affects:** `frontend/src/routes/_authenticated.workspace.proxy-cache.tsx`
only. No backend change. Reuses `CopyButton`. No new vitest
coverage required (the data is already in the row payload).

**Effort:** ~half day.

### FUT-016 — Click-through detail page: layers + manifest tab for cached images

**Surfaced:** same testing session as FUT-015. Operator wants
the same "click image → see layers" flow that `/repositories`
provides for cached entries — but the proxy stores manifests in
its own schema, untouched by `services/metadata`. So the layers
tab can't reuse the per-repo tag detail.

**Scope:**

- None blocking. Proxy already persists everything we need; this
  is read-side surfacing + an evict action.
- Pairs naturally with a future "Cached blob GC" expansion of
  `services/gc` (today's GC handles metadata-backed blobs; the
  cached-blob set has different lifecycle semantics — LRU
  eviction not orphan sweep). Filed as a known follow-up below.

**Auth (locked 2026-06-24):** all three routes — list, stats,
evict — gated on **workspace-admin**, matching the pattern set
by `domains` / `audit-export` / `quota`. Platform-admin retains
implicit access via the `(admin, org, '*')` marker (it trumps
workspace-admin everywhere). Rationale: the cache is a
workspace-level concern (sized + shaped by the workspace's
pull patterns), not shared infrastructure; treating evict as
a workspace-admin operation keeps the surface consistent with
every other workspace-owned write route.

**Affects:** `services/proxy` (3 new RPCs + migration),
`services/management` (3 new REST routes + sidebar visibility
gate), `frontend/` (new route + nav entry + page + evict dialog).

**Effort:** ~1 sprint. Backend ~2-3 days (migration + 3 RPCs +
debounced pull counter + tests); BFF ~half day (route wrappers);
FE ~2 days (route + stats card + filterable table + evict dialog
+ nav entry); docs ~half day. Open follow-up: cached-blob LRU
eviction in `services/gc` — separate item, this PR is the
visibility prerequisite.
- New route `/workspace/proxy-cache/{id}` showing:
  - Summary header (upstream / image / reference / digest /
    size / cached / last pulled / pulls).
  - Layers tab — parse the manifest body (already in
    `proxy_manifests.body BYTEA`) into a layer table with
    digest + size + media type. For manifest indexes (multi-
    arch), show the platform list with click-through to the
    per-arch manifest.
  - Manifest tab — raw JSON viewer (`<CodeBlock>` already
    exists). Operator can confirm exactly what bytes the proxy
    is serving without leaving the dashboard.
- New BFF route `GET /api/v1/proxy/cache/{id}` (or extend the
  existing list response with a `body_base64` field on the
  single-row read path — cleaner as a separate route since the
  list call deliberately omits body for size). Calls a new
  `services/proxy.GetCachedManifest(tenant_id, id)` RPC that
  returns the full row including body bytes.

**Out of scope** (defer to FUT-017): scans + signing tabs.
Layers + manifest are the v1 detail surfaces.

**Affects:** `proto/proxy/v1/proxy.proto` (one new RPC),
`services/proxy` (one new RPC + repo method), `services/management`
(one new REST route), `frontend/` (new route + page + 2 tabs).

**Effort:** ~1-2 days. Layer parsing is the bulk; OCI v1 +
Docker v2 manifest list shapes are well-defined.

### QA-002 follow-ups (small)

- **QA-002a** — `Publisher.Close()` doesn't take `p.mu`; concurrent shutdown
  returns ambiguous errors. Add `ErrPublisherClosed` sentinel + lock in
  Close, with a test that distinguishes shutdown from broker failure.
  Surfaced by qa-agent review during PR #46. **Effort:** S.
- **QA-002b** — 10s publish timeout is hardcoded; make it a `PublishTimeout`
  field on the struct (configurable). Surfaced by qa-agent review during
  PR #46. **Effort:** S.

(Each item has the full where/why/fix breakdown in [`.claude/reviews/`](.claude/reviews/).)

---

## REDESIGN-001 — small follow-ups from shipped PRs

> Carve-outs from PRs that shipped in the redesign batch. Each is a
> documented should-fix kept out of the original PR to keep that PR
> reviewable. Sized in minutes / hours, not days.

### RED-FU-001 — `services/auth/internal/bootstrap/bootstrap_test.go` schema refresh
- **Why:** PR #132 (Phase 2.1) dropped the `tenant_domains` schema. The
  bootstrap integration test still inlines a snapshot of tenant migrations
  including the dropped table. Doesn't break the default suite (build tag
  `integration`) but rots quietly.
- **Scope:** refresh the inlined migrations snapshot to match current
  `services/tenant/migrations/`. ~15 min.
- **Affects:** `services/auth/internal/bootstrap/bootstrap_test.go`.

### RED-FU-002 — `services/auth/internal/repository/auth_providers.go` cleanup
- **Why:** PR #133 (Phase 2.2) replaced the per-tenant `auth_providers`
  table with global config. The Go file still hosts shared helpers
  (`AuthProviderType` constants, `nullIfEmpty`, `nullableBytes`,
  `scanAuthProvider`, `rowScanner`) used by `global_sso_config.go`. The
  legacy `AuthProviderRepository` methods have zero callers.
- **Scope:** rename to `sso_helpers.go` and trim the legacy repo methods,
  OR move helpers into a new `pgutil.go` and delete the legacy file. ~30 min.
- **Affects:** `services/auth/internal/repository/`.

### RED-FU-003 — `digest_keyed.go:295` `hasAnyWriterRole` writer-tier scoping (Phase 5.4)
- **Why:** PR #131 (Phase 5.2) tightened the admin-tier gates but
  deferred the writer-tier `hasAnyWriterRole` helper. Today any
  writer/admin/owner anywhere in the tenant can sign manifests by digest
  even for repos they don't have write access to (Review §A1 #4).
  Requires digest → repo resolution to do correctly.
- **Scope:** thread `manifest_digest` → owning repo lookup; gate via
  `hasScopedRole(assignments, "repo", repo, "writer")`. ~2-3h.
- **Affects:** `services/management/internal/handler/digest_keyed.go`,
  `services/metadata` (digest → repo lookup).

### RED-FU-004 — OCI conformance user → CI-time bootstrap
- **Why:** PR #129 (Phase 2.6 / Top-5 #5) removed the dev admin from the
  migration but kept the conformance user (`00000000-…-003`) since CI
  workflows depend on it. The conformance user has zero role assignments
  so the security risk is bounded, but a known argon2 hash is still
  baked in the production Docker image.
- **Scope:** CI workflow seeds the conformance user once via SQL,
  scrubbed from the production image. ~1h.
- **Affects:** `services/auth/migrations/20260610000001_seed_dev_tenant.sql`
  (strip the second INSERT), `services/core/Makefile`, new GH Action step.

### RED-FU-006 — `tenant.GetDeploymentMetadata` RPC hardening + test symmetry
- **Why:** Surfaced by the 3-agent review on PR #160 (Phase 3.4 prep). The
  RPC ships clean but two should-fixes round out the surface before more
  keys land in `deployment_metadata`.
- **Scope (≤1h total):**
  - Add server-side key allowlist (`var allowedDeploymentMetadataKeys = map[string]bool{"bootstrap_tenant_id": true}`); reject unknown keys with `PermissionDenied`. Future-proofs against accidentally exposing a KEK/secret key via the same generic RPC.
  - Cap `req.GetKey()` length (≤128) and restrict charset (`^[a-z0-9_]+$`) per CLAUDE.md §7. One-liner.
  - Add `TestGetDeploymentMetadata_WrappedErrNotFound_ReturnsNotFound` —
    symmetry with `TestIsDuplicateKeyError_WrappedError_ReturnsTrue`. Pins
    the `errors.Is` chain through wrappers.
  - Add `TestGetDeploymentMetadata_EmptyJSONValue_RoundTrips` — pins the
    "raw verbatim, no whitespace stripping" contract documented in
    `proto/tenant/v1/tenant.proto`.
  - Optional NIT: fold the now-6 GetDeploymentMetadata tests into a single
    `t.Run` table.
- **Affects:** `services/tenant/internal/handler/grpc.go`,
  `services/tenant/internal/handler/grpc_test.go`.

### RED-FU-007 — Conformance compose-stack bootstrap fix — **✅ DONE (PR #184, 2026-06-29)**
> Shipped via approach (b) variant: postgres:16-alpine one-shot container seeds the tenant-side rows (tenants + tenant_policies + deployment_metadata.bootstrap_tenant_id) before Phase 3.4 services start. 10 services gain `depends_on: registry-bootstrap`. Admin user creation deferred to `make dev-bootstrap` since the auth bootstrap CLI's argon2 + cross-module migration requirements were a larger scope.

#### Original notes (kept for context)
- **Why:** REM-020 #10 conformance failures since 2026-06-25 traced to
  the Phase 3.4 fail-loud bootstrap lookup tripping in compose (auth +
  metadata + core + storage call `tenant.GetDeploymentMetadata` at
  startup; deployment_metadata is empty because the bootstrap CLI never
  runs in the dev compose stack). PRs #170 + #171 wired
  `TENANT_GRPC_ADDR` + `depends_on: registry-tenant` so services can
  *reach* tenant, but tenant returns NotFound and services exit
  fail-loud per design.
- **Scope (~half day):** pick one of three and ship:
  - (a) Set `DEPLOYMENT_MODE=multi` on the compose stack — multi mode
    skips the lookup. Cleanest for conformance + dev; production stays
    single mode by default.
  - (b) Add a `bootstrap` init container to compose that runs
    `registry-auth bootstrap --admin-email ... --tenant-name ...` once
    after postgres + registry-tenant are healthy. Auth/metadata/core
    wait on it completing. Correctly exercises single mode end-to-end
    in dev too.
  - (c) Add a goose migration to services/tenant that inserts a known
    UUID into deployment_metadata at startup. Compose works in single
    mode without the CLI. Tradeoff: dev tenant has a fixed UUID.
- **Affects:** `infra/docker-compose/docker-compose.yml`, possibly
  `services/tenant/migrations/`, possibly Makefile + runbook.

### RED-FU-008 — Defensive 5s timeout at `fetchBootstrapTenantID` call sites
- **Why:** Code-review on PR #170 (services/core) suggested a
  defensive `context.WithTimeout(ctx, 5*time.Second)` at the call site
  in each service. `libs/tenant/bootstrap.FetchTenantID` already wraps
  internally with `LookupTimeout`, but per CLAUDE.md §6 ("Always set
  deadlines on outgoing gRPC calls") a call-site deadline is the
  belt-and-braces invariant. Worth applying uniformly across the 11
  service rollouts.
- **Scope (~1h):** one cross-cutting commit that updates each
  service's `fetchBootstrapTenantID` to wrap its call to
  `tenantbootstrap.FetchTenantID` in `context.WithTimeout`. Net ~3
  lines per service × 11 = ~33 lines + 11 services touched.
- **Affects:** every `services/<svc>/internal/server/server.go` that
  has the Phase 3.4 helper.

### RED-FU-009 — Scanner Debian-slim → distroless audit
- **Why:** REM-020 #10 root-cause companion. Scanner image base layer
  ships perl-base + zlib1g + other Debian transitive deps that
  generate CVEs we have to `.trivyignore` because no upstream fix is
  available. A leaner base image (distroless or scratch + scratch
  scanner adapter binary) would eliminate the entire allowlist.
- **Scope:** audit which Debian-base deps the scanner adapter actually
  needs at runtime, then design a multi-stage Dockerfile that ships
  only those. Distroless `cc` or `static` base candidate. Sized at a
  day; not urgent because skip-files on the bundled trivy/grype
  already cleared the build gate.
- **Affects:** `services/scanner/Dockerfile`,
  `services/scanner/.trivyignore` (slim down once the base is leaner).

### RED-FU-010 — scanner / core Docker build go.sum drift after #167 libs lift — **✅ DONE (PR #183, 2026-06-29)**
> Shipped: scanner gained 3 transitive deps (go-redis/v9, otelgrpc, atomic). Sweep across other modules confirmed scanner was the only one affected. ci-tidy-check workflow now runs `GOWORK=off` to match the Docker invariant so future drift is caught before reaching main.

#### Original notes (kept for context)
- **Why:** `CI — scanner Docker build` has been red on every push since
  #163 (and `CI — core` likely shares the same shape). Failure:
  `missing go.sum entry for ... libs/middleware/grpc` for `go-redis/v9`
  and `otelgrpc` when the Dockerfile builds with `GOWORK=off`. Local
  `go vet`/`go build`/`go test -short` are all clean because the
  workspace pulls in the `libs/` module's go.sum directly — the drift
  only bites the Docker stage. Fallout from the #167 middleware
  extraction that pulled new transitive deps into `libs/middleware/grpc`.
  Surfaced by qa-agent batch on 2026-06-29 (Phase 3.4 close-out review).
- **Scope:** per-service `go mod tidy` sweep in services/scanner +
  services/core covering the new libs/middleware/grpc transitive deps,
  verify Docker build succeeds, sweep any other services if needed.
  Half-day with the ci-tidy-check matrix workflow to catch future
  drift.
- **Affects:** `services/scanner/go.mod`, `services/scanner/go.sum`,
  `services/core/go.mod`, `services/core/go.sum`, possibly others
  flagged by `ci-tidy-check.yml`.

### RED-FU-011 — Phase 3.4 helper unit-test coverage — **✅ DONE (PR #185, 2026-06-29)**
> Shipped: 10 services × 3 tests each (nil extraUnary / non-nil / bad mTLS). Scanner deferred — chain still inlined into `Run()` rather than threaded through `buildGRPCOptions`. Tracked as RED-FU-013 below.

#### Original notes (kept for context)
- **Why:** The 9 Phase 3.4 rollout PRs added a `fetchBootstrapTenantID`
  helper to 7 services + a `readBootstrapTenantID` self-read variant
  to services/tenant + reused-conn variant in services/gc, plus the
  new `buildGRPCOptions(cfg, extraUnary)` chain — and zero unit tests.
  `libs/tenant/bootstrap.FetchTenantID` already has bufconn coverage
  from #167 so the RPC path itself is tested, but the per-service
  wrappers + the interceptor-chain ordering aren't. Surfaced by
  qa-agent batch on 2026-06-29.
- **Scope:** P2 coverage backlog. Add 2 tests per service: (a) single
  mode with bootstrap_tenant_id set wires injector; (b) multi mode
  leaves chain unchanged. tenant gets a third for the pre-bootstrap
  skip-with-warn branch. Half-day.
- **Affects:** `services/{auth,metadata,core,storage,signer,webhook,scanner,audit,gc,proxy,tenant}/internal/server/server_test.go`.

### RED-FU-012 — Lift mTLS ClientCreds wrapper into loader.BaseConfig — **✅ DONE (PR #186, 2026-06-29)**
> Shipped: `(*BaseConfig) MTLSClientCreds(serverName)` added to `libs/config/loader/loader.go`. 5 services that already embed BaseConfig (auth, metadata, storage, proxy, management) dropped their local helper (`clientCreds` / `buildClientCreds` / `buildGRPCCreds`) and call the lifted method directly. 16 call sites unified. Remaining 7 services without BaseConfig embed filed as RED-FU-014 below.

### RED-FU-013 — Extract scanner buildGRPCOptions + add helper tests — **✅ DONE (PR #188, 2026-06-29)**
> Shipped: extracted helper + the 3-test smoke suite. Two cosmetic wording fixes (matching the 9-service majority) folded inline per code-review-agent.

#### Original notes (kept for context)
- **Why:** Scanner's interceptor chain is built inline in `Run()`
  rather than threaded through a `buildGRPCOptions(cfg, extraUnary)`
  helper like every other Phase 3.4 service. Surfaced twice: by
  code-review-agent on PR #175 (scanner Phase 3.4 wiring) as a
  should-fix, and by RED-FU-011 (PR #185) which had to skip scanner
  from the unit-test sweep because there was no helper to test.
- **Scope (~1h):** extract `buildGRPCOptions(cfg, extraUnary
  grpc.UnaryServerInterceptor) ([]grpc.ServerOption, error)` from
  `services/scanner/internal/server/server.go` `Run()`; add the
  3-test smoke suite (`build_grpc_options_test.go`) matching the
  template now used by the other 10 services.
- **Affects:** `services/scanner/internal/server/server.go`,
  `services/scanner/internal/server/build_grpc_options_test.go`
  (new).

### RED-FU-014 — Migrate the remaining 7 services to embed loader.BaseConfig — **✅ DONE (PR #189, 2026-06-29)**
> Shipped: core/scanner/signer/webhook/audit/gc/tenant all embed BaseConfig now. core+scanner dropped their local `clientCreds` helpers; signer/webhook/audit/gc swapped inline `mtls.ClientCreds` calls to `cfg.MTLSClientCreds`. PR also fixed a latent bootstrap container bug (missing `slug` column in `INSERT INTO tenants`).

#### Original notes (kept for context)
- **Why:** RED-FU-012 only refactored the 5 services that already
  embed `loader.BaseConfig` (auth, metadata, storage, proxy,
  management). core, scanner, signer, webhook, audit, gc, and tenant
  still declare `LogLevel` / `LogFormat` / `GRPCAddr` / `HTTPAddr` /
  `MetricsAddr` / `MTLS_*` / `OTEL_*` fields directly on their Config
  struct — ~13 inherited fields per service. Migrating them to embed
  BaseConfig delivers (a) automatic access to the lifted
  `MTLSClientCreds` method and (b) one canonical home for the shared
  fields. Code-review-agent on RED-FU-012 (PR #186) recommended adding
  a `// TODO RED-FU-014` sentinel comment next to each surviving
  helper in those services to signal intent to the next reader.
- **Scope (~1 day):** per-service edit: replace ~13 standalone fields
  with `loader.BaseConfig \`mapstructure:",squash"\``; sweep callers
  for the embedded fields (already promoted via Go's field promotion
  so no rename needed); drop the local clientCreds-style helper if
  the service has one. Verify per-service local CI gate. Touches 7
  service config + server files.
- **Affects:** `services/{core,scanner,signer,webhook,audit,gc,tenant}/internal/config/config.go`
  + `internal/server/server.go`.

### RED-FU-005 — Phase 7.1 CLAUDE.md / docs/SERVICES.md rewrite
- **Why:** REDESIGN-001 Phase 7.1 is the catch-all "make CLAUDE.md and
  docs/SERVICES.md match the new reality." Once enough phases ship, the
  aspirational-section banner at the top of CLAUDE.md gets replaced with
  a real rewrite covering: custom-domain removal, SSO global config,
  `is_global_admin`, bootstrap CLI, single-mode tenant behaviour, audit
  catalogue completeness.
- **Scope:** doc-only sweep across `CLAUDE.md`, `docs/SERVICES.md` §2
  (auth) + §12 (tenant), `docs/SAML.md`, `docs/EVENTS.md`. ~half-day.
- **Affects:** docs only. **✅ DONE** — PR #210 + #211 (ADRs) + #212 (spec-lint) + #213 (tracker trim).

### RED-FU-015 — KEK rotation tool (REDESIGN-001 Phase 6.4 follow-up) — ✅ SHIPPED 2026-07-03
- **Shipped:** per-service `rotate-kek` subcommand across `auth`/`proxy`/`webhook`/`audit`
  (mirrors `bootstrap`), backed by the shared `libs/crypto/rekey` package (crypto core +
  declarative sweep engine + CLI runner) and a nullable `kek_version SMALLINT` tracking
  column per affected table. Modes: `--dry-run`, `--verify` (exit 3 if rows remain),
  `--generate`, `--to-version`. Operator runbook: `infra/runbooks/kek-rotation.md`.
  Design: `docs/superpowers/specs/2026-07-03-kek-rotation-design.md`;
  plan: `docs/superpowers/plans/2026-07-03-kek-rotation.md`. See `status.md` for the row.
- **Corrected scope (three backlog errors caught during scoping — the bullets below are wrong,
  kept for history):** (1) the ciphertext `0x01` byte is a *layout* marker, not a KEK id, so
  completion detection uses the `kek_version` column + trial-decryption, not the version byte;
  (2) there is **no single master KEK** — four independent KEKs across four services/databases,
  so rotation is per-service (four invocations), not one CLI with `--from-key/--to-key`;
  (3) `signatures.private_key_enc` **does not exist** — signer key material lives in Vault
  Transit / cloud KMS and is out of scope.
- **Why:** Phase 6.4 (PR #203) shipped the AES `Version = 0x01` byte
  prefix on every ciphertext. The version byte is the prerequisite; the
  rotation tool is the deliverable that makes the prerequisite useful.
  Without it, anyone running a long-lived deployment who needs to
  rotate the master KEK (suspected compromise, scheduled rotation,
  regulator requirement) has no shippable path. The deferred plan
  checkboxes are documented in
  `.claude/plans/2026-06-26-single-tenant-redesign.md` Task 6.4 as
  "DEFERRED — version byte shipped" with explicit follow-up anchors.
- **Scope:** small design doc → CLI subcommand (`registry-auth
  rotate-kek --from-key OLD --to-key NEW`) → re-encrypt iterator
  with a `kek_id` column added to the encrypted-secret tables
  (`global_sso_config.oauth_client_secret_enc`,
  `signatures.private_key_enc`, etc.). Estimate: 3-5 days.
- **Recommendation:** next pickup after REDESIGN-001 v2.0.0 ships.

### RED-FU-016 — SAML library upgrade to v0.5.x (REDESIGN-001 Phase 6.8 descoped) — **LOW PRIORITY**

> **2026-07-04 update:** the *security* half of this item is retired — the
> REM-016 sweep (PR #256) bumped `russellhaering/goxmldsig` v1.3.0 → v1.6.0,
> fixing **GO-2026-4753** (XML-dsig signature bypass; logged as SEC-076,
> RESOLVED) while staying on `crewjam/saml` v0.4.14. Trigger (a) below has
> therefore already fired and been handled at the dependency layer; what
> remains is purely the v0.5.x API-ergonomics upgrade.

- **Why:** Originally scoped as REDESIGN-001 Phase 6.8 (semver-breaking
  upgrade from `crewjam/saml` v0.4 → v0.5). Descoped 2026-06-30 — no
  forcing function on v0.4. Enterprise SAML self-hosters are a thin
  slice of the OSS audience and v0.5's headline changes are API ergonomics,
  not security. Re-evaluate only when one of: (a) a security advisory
  drops on v0.4-line; (b) a self-hoster files a v0.5-only feature
  request; (c) we want to drop our hand-rolled `samlsp.Middleware`
  bypass (a v0.5 cleanup).
- **Scope when picked up:** `cd services/auth && go get
  github.com/crewjam/saml@v0.5.x && go mod tidy`; run SAML tests; fix
  API churn; add `samlsp.ParseMetadata` cache per `(provider_id)` to
  avoid per-request parse. Estimate: 1-2 days assuming clean upgrade.
- **Recommendation:** park here; revisit only on a triggering event
  per above.

### RED-FU-017 — Audit hash-chain checkpoint signing (REDESIGN-001 Phase 6.12 follow-up) — **LOW PRIORITY**
- **Why:** Originally part of REDESIGN-001 Phase 6.12 plan but explicitly
  scoped out of the 6.12 PR (`#208`) as "checkpoint signing is OUT OF
  SCOPE for this PR; this PR just lays the per-row primitive." The
  in-DB hash chain catches internal tampering (the SEC-050 scenario);
  checkpoint signing catches a different and rarer threat: an attacker
  with **full DB superuser** who bypasses `FORCE RLS` + the
  `registry_audit_app` role and rewrites the entire chain from genesis
  (including the genesis sentinel). At that privilege level the right
  defence is offline verification + tamper-evident checkpoints
  periodically published to an immutable external store.
- **Scope when picked up:** cron-driven publisher that signs `(tenant_id,
  chain_seq, row_hash, occurred_at)` tuples with a long-lived KMS key
  and writes them to S3 (or equivalent immutable object store) with
  object-lock + WORM. Verifier walks the in-DB chain AND cross-checks
  against the latest published checkpoint. Estimate: 1 week (S3 plumbing,
  KMS integration, verifier CLI).
- **Recommendation:** park here. Revisit only when a regulated customer
  arrives, an incident-response runbook requires it, or audit forensics
  becomes a stated use case.

### RED-FU-018 — Scanner plugin in-process sandbox (REDESIGN-001 Phase 6.11 descoped) — **PARKED**
- **Why:** Originally REDESIGN-001 Phase 6.11. Descoped 2026-06-30 in
  favour of the operator-facing
  [`infra/runbooks/scanner-isolation.md`](infra/runbooks/scanner-isolation.md)
  runbook (read-only root, cap-drop, NetworkPolicy egress restriction,
  cgroup CPU/RAM limits, seccomp profile via K8s `RuntimeDefault`). The
  runbook neutralises ~80% of the scanner-RCE-via-CVE threat at the
  container boundary — no Linux-only Go primitives required, ports to
  dev/test cleanly. The remaining 20% is "attacker compromises scanner
  process AND escapes the container runtime AND reaches the host" — a
  scenario the original 6.11 plan addressed in-process via seccomp /
  landlock / netns drops.
- **Scope when picked up:** the original 6.11 task body. Re-read the
  `Plan task 6.11` section before starting; the design has not changed.
- **Recommendation:** park here unless a container-runtime CVE drops or
  a multi-tenant SaaS deployment specifically requires per-process
  isolation beyond NetworkPolicy enforcement.

---

## Platform expansion (self-hosted-first — 2026-07-01)

> **Framing:** these items were surfaced during a "what's amazing but
> missing?" review after the FUT-001..FUT-004 access-surface batch
> shipped. Re-scoped through the **`DEPLOYMENT_MODE=single`** lens
> (one organisation running the whole stack for themselves, competing
> with Harbor / Nexus / plain Docker Registry — NOT ECR/GCR/ACR-style
> SaaS). SaaS-flavoured items (cross-region replication, marketplaces,
> billing surfaces) are intentionally absent. `DEPLOYMENT_MODE=multi`
> stays a supported posture but is not the design driver here.
>
> Everything below is forward-looking. None of it is on a sprint yet
> — pick, spec, and pull into `status-tracker.md` when picked up.

### Tier 1 — I'd sit down and build these tomorrow

#### FUT-020 — Image promotion workflow (`dev → staging → prod`) — ✅ SHIPPED 2026-06-25
- **SHIPPED** in **PR #231** (`feat: FUT-020 Image promotion workflow`) + follow-up **PR #234** (REM-030 — dst-org dropdown + `create_if_missing`). Live on `main`: metadata `PromoteTag`/`ListPromotions` RPCs + `00018_promotions.sql`, BFF `POST .../tags/{tag}/promote` + `GET .../promotions` (`promote_tag.go`), `image.promoted` event + audit mapping, MCP promotion tools, FE `PromoteTagDialog` + `PromotionsTab` + `usePromoteTag`. **Cross-org already works** — the BFF gates on writer-or-above on BOTH `srcScope` and `dstScope` independently (`promote_tag.go:145-163`), no same-org restriction, so `dev/* → prod/*` is supported today.
- **Only unbuilt slice of this sketch:** the *optional prod approval gate* ("workspace-admin required for `→ prod/*`", two-step approve) was never built — current gate is writer-on-both-sides. Marginal under single-tenant; revisit only if a protected-repo approval flow is wanted.
- **Why:** Every team hand-rolls this in CI today. A first-class
  registry primitive removes 40+ lines of `docker tag && docker push`
  glue per pipeline + captures provenance in the audit trail.
- **What:** `POST /repositories/{org}/{src}/tags/{tag}/promote?to={org}/{dst}:{tag}`
  — atomic tag copy with digest verification. Optional approval gate
  (workspace-admin required for `→ prod/*`). Emits `image.promoted`
  audit event carrying the origin digest so provenance survives the
  copy. FE: "Promote this tag" button on the tag detail page +
  Promotions history tab per repo. Reuses existing tag proto + RBAC.
- **Cost estimate:** ~2 days. Every ingredient exists.
- **Affects:** `services/core` (new endpoint), `services/metadata`
  (record promotions), `services/management` (BFF), `frontend`.

#### FUT-021 — CVSS-gated admission policy (finish the scanner loop) — ✅ SHIPPED 2026-06-25
- **SHIPPED** in **PR #233** (`feat: FUT-021 CVSS-gated admission policy`). Live on `main`: per-repo CVSS threshold column + metadata RPC, `services/core` pull-time admission gate (`cvss_admission_test.go`, `IMAGE_BLOCKED_BY_POLICY`), BFF `repo_cvss_policy` handler, FE `repo-cvss-policy-section.tsx`. Fail-open-before-first-scan / fail-closed-after posture as sketched.
- **Why:** The scanner produces reports but nothing blocks on them.
  You paid for Trivy + SBOM generation; the value multiplies when
  the gate can act on the finding at pull time.
- **What:** New `repositories.max_cvss_score INT` column (nullable =
  no gate). `services/core.checkAccess` extended: pull → look up scan
  result → reject with `403 IMAGE_BLOCKED_BY_POLICY` if
  `max_cvss > threshold`. Fail-OPEN when the scan hasn't run yet
  (don't block first pulls) + fail-CLOSED once the row exists.
  Operator can flip fail-OPEN posture per repo. FE: toggle next to
  "require signature" on the Settings tab.
- **Cost estimate:** ~1 sprint including migration + tests.
- **Affects:** `services/core`, `services/metadata`, `services/scanner`
  (may need to publish scan-complete events reliably), `frontend`.

#### FUT-022 — OCI artifacts as first-class citizens
- **Helm-detail scope SHIPPED 2026-07-06** (branch `feat/fut-022-helm-chart-detail`) — the tag-detail **Chart tab** (gated on `artifact_type === "helm"`) renders `Chart.yaml` metadata + `values.yaml` inline via a new generic size-capped `CoreService.GetBlob` gRPC + BFF `GET .../tags/{tag}/chart`. See `status.md` / `FE-STATUS.md` (FE-API-055).
  - **Deferred remainder:** generic `/artifacts` mediaType landing page (redundant with the existing `/helm` page + artifact-type chips under the single-tenant posture); richer referrer rendering (SBOM package tables, inline signature verification); `helm template` dry-run / provenance verification. ~~streaming `GetBlobStream` for large blobs (current `GetBlob` is unary + size-capped)~~ — **SHIPPED 2026-07-06** (branch `feat/helm-chart-download`): server-streaming `CoreService.GetBlobStream` + BFF `GET .../tags/{tag}/chart/download` back a one-click byte-identical `helm pull` .tgz download on the Chart tab (see `status.md` / `FE-STATUS.md` FE-API-056). Original entry body below retained for that deferred context.
- **Why:** The registry is already OCI v1.1 compliant with referrers
  support — Helm charts, Wasm modules, SBOMs, OPA bundles, Cosign
  signatures, in-toto attestations all push cleanly today, but the
  FE renders them as opaque `application/vnd.*` blobs. That's a
  distribution-platform-shaped hole with a registry-shaped bandage.
- **What:** New `/artifacts` route with a mediaType-aware list view
  (icon per type). `helm push oci://registry/charts/mychart` renders
  a per-chart page with `helm show values` inline. Cosign
  signatures + SBOMs + attestations render on the tag detail's
  "Referrers" tab instead of hidden behind proto JSON. Optional:
  policy bundle inspection (OPA / Cedar).
- **Cost estimate:** ~2 weeks (mostly FE + a small metadata proto
  extension for mediaType discovery).
- **Affects:** `services/metadata`, `services/management` (BFF),
  `frontend`.
- **Positioning:** turns "an image registry" into "a distribution
  platform." Harbor does a subset; you'd match/exceed.

#### FUT-023 — Ephemeral PR-scoped registries
- **✅ SHIPPED — Phase 1 backend (PR A), branch `feat/fut-023-pr-registry-backend` (2026-07-09):** the namespace lifecycle backend only. `services/metadata` gained the migration `00020_pr_registry.sql` (`pr_registry_config` + `pr_namespaces`), the `internal/prregistry` package (KEK-sealed webhook-secret unseal under `PR_REGISTRY_KEY_HEX`, `X-Hub-Signature-256` HMAC verify, GitHub `pull_request` parse, `pr-<repo>-<N>` name derivation, and provision/promote-on-merge/teardown dispatch reusing FUT-020 `PromoteTag`), and 5 new RPCs (`GetPRRegistryConfig`, `PutPRRegistryConfig`, `HandlePREvent`, `ListPRNamespaces`, `DeleteOrganization`). Two new events (`pr.namespace.provisioned` / `pr.namespace.torn_down`) + `registry-audit` `mapEvent` cases. `services/management` gained the public `POST /webhooks/scm/github/pr` receiver (unauthenticated; HMAC verified downstream) + admin `GET/PUT /api/v1/pr-registry/config` + `GET /api/v1/pr-registry/namespaces` + `PUBLIC_BASE_URL` config. Design: `docs/superpowers/specs/2026-07-08-fut-023-ephemeral-pr-registries-design.md`. **Still pending:** Phase 2 keyless-OIDC push (extend `oidc_exchange.go` to derive `pr-<N>/*` scope from the signed GitHub `ref` claim — the security-critical piece deliberately deferred).
- **✅ SHIPPED — Phase 1 frontend (PR B), branch `feat/fut-023-pr-registry-frontend` (2026-07-10):** a new **Settings › Integrations** tab (`_authenticated.settings.integrations.tsx`), global-admin-gated in the settings layout. `lib/api/pr-registry.ts` hooks (`usePRRegistryConfig`, `useUpdatePRRegistryConfig`, `usePRNamespaces`). `components/settings/pr-registry-panel.tsx` — admin-gated config panel mirroring `NotificationWebhookPanel`: enable toggle, copyable webhook receiver URL (`CopyButton`), write-only signing secret (`has_secret` → "•••• configured" placeholder, blank = keep), promote-target-org `Select` sourced from the caller's visible orgs (like the promote dialog) with a "None" sentinel, and a targeted 409→"set PR_REGISTRY_KEY_HEX first" toast. `components/settings/pr-namespaces-list.tsx` — read-only active-namespace table (provider / source repo / PR # / org / created) with empty + error states. 8 new vitest cases (admin-gate null, secret write-only, enable/target round-trip, empty state, row render). All 4 CI gates green (lint 0 errors, typecheck, 334 tests, build). Manual FE teardown deliberately out of scope (teardown is webhook-driven).
- **⚠️ Known gap:** `PR_REGISTRY_KEY_HEX` is **not** yet swept by `rotate-kek` — same class as the notification-webhook (`NOTIFY_WEBHOOK_KEY_HEX`) / email (`NOTIFY_EMAIL_KEY_HEX`) KEKs, tracked under RED-FU-015. The `pr_registry_config.kek_version` column is stamped in anticipation, but no rotation spec has been added.
- **✅ RESOLVED — SEC-085 org-adoption guard (`fix/sec-085-org-adoption-guard`, 2026-07-10):** provision now runs an adoption guard before `GetOrCreateOrganization` — it looks the org up by name (`LookupOrgIDByName`, added to the `Store` interface) and, when one already exists under the derived `pr-<repo>-<N>` name, only proceeds if it's the exact org our own **active** `pr_namespaces` row already points at (a GitHub re-delivery). Any other pre-existing org is refused (logged `slog.Warn`, `OutcomeIgnored`, foreign org untouched), so a name collision can no longer be adopted and teardown can never cascade-delete an operator org. Teardown org-DELETE was already tenant-scoped (SEC-085 #3, PR #293). Regression tests cover both the foreign-collision-refused and own-re-delivery-allowed paths.
- **Why:** CI teams pollute their main tag namespaces with `pr-123`,
  `pr-456`, etc. — cleanup is manual, retention rules skip them.
  Auto-provisioned per-PR namespaces with auto-cleanup on close is
  genuinely novel; most competitors don't do this cleanly.
- **What:** New `services/management` webhook receiver for GitHub /
  GitLab PR events. On PR open → auto-provision `pr-<N>/*` namespace
  with retention `until = merge_or_close_at`. On merge → promote
  (via FUT-020) + delete namespace. On close without merge → delete
  namespace. Perfect fit for the FUT-001 OIDC federation you just
  shipped — the GHA workflow's federated identity grants scoped
  push access to its own `pr-<N>/*` namespace only.
- **Cost estimate:** ~1 sprint.
- **Affects:** `services/management` (webhook receiver), retention
  (already shipped), FUT-001 OIDC federation (already shipped),
  `frontend` (namespace visualisation).
- **Depends on:** FUT-020 (promotion) for the merge → promote step.

### Tier 2 — Amazing but bigger commitment

#### FUT-024 — Web-based layer / file inspector
- **Why:** "See what's inside this image without pulling it." Debug
  workflows on constrained / air-gapped networks; SBOM-adjacent
  investigation without local `tar` gymnastics.
- **What:** Stream a layer, `tar` it in-browser, render the file
  tree. Files viewable inline (Dockerfile, license, package.json,
  go.mod). Diff between two tags: layer add/remove, file changes,
  dep-version deltas.
- **Cost estimate:** ~2 weeks of careful FE + a streaming blob
  handler on the BFF.
- **Affects:** `services/management` (streaming), `frontend`
  (tar.js library integration).

#### FUT-025 — Storage / pull dependency graph
- **Why:** "Which of my images depend on `base/alpine:3.19`?"
  Operators can't retire base images without this. Also unlocks
  "which images are your biggest storage cost, and who's still
  pulling them?" ops analysis.
- **What:** Parse manifest → layer chain → identify shared base
  layers by digest match. Graph visualisation (roots = base images,
  edges = "layer-shared-by"). Table view: base image → downstream
  count + total downstream storage attributed. Complements the
  existing Tier 2 #4 (image lineage) but goes further.
- **Cost estimate:** ~2 weeks.
- **Affects:** `services/metadata` (new query), `services/management`,
  `frontend` (graph library).

### Tier 3 — Nice-to-haves that would delight

#### FUT-026 — Import from public registries
- **Why:** Adoption unblocker. New self-hoster wants "mirror the top
  500 pulls from Docker Hub" or "import this exact list from GHCR."
  Combined with the existing pull-through cache = zero-touch outage
  tolerance.
- **What:** `registryctl import --from docker.io/library --top 500` +
  a dashboard equivalent. Supports both bulk (top-N by pull count) and
  explicit list (from YAML). Handles digest verification, rate
  limiting, upstream cred prompts.

#### FUT-027 — Terraform provider
- **Why:** Ops teams want their registry declared as code. Moves the
  product from "self-hosted app" to "infra platform." Real
  contributors ask for this on day one.
- **What:** `terraform-provider-oci-janus` — resources for
  `oci_janus_repository`, `oci_janus_token_policy`, `oci_janus_webhook`,
  `oci_janus_oidc_trust`, `oci_janus_scan_policy`, etc.

#### FUT-028 — Kubernetes operator
- **Why:** k8s-first orgs want CRDs instead of Terraform. Same
  leverage as FUT-027, different constituency.
- **What:** CRDs for `Repository`, `TokenPolicy`, `Webhook`,
  `OIDCTrust`. Operator reconciles by calling the management BFF.

#### FUT-029 — `registryctl` CLI (one binary)
- **Why:** Operators live in the shell, not always the dashboard.
  Self-hosted ops teams especially. Bulk scripted ops beat clicking.
- **What:** Single Go binary. `registryctl repo create`,
  `registryctl policy set`, `registryctl scan trigger`,
  `registryctl backup ...`, `registryctl import ...`,
  `registryctl users invite`. Uses the same BFF as the dashboard.
  Supersedes the small `oci-janus` CLI stub in the existing Tier 3
  polish section — this is the full realisation.

#### FUT-030 — Vulnerability triage workflow
- **Why:** Scan results are noise without a workflow. "Accept this
  CVE for now with justification", "snooze until upstream fixes",
  "assign to $user". Turns scan output from a wall of red into an
  actionable queue with an audit trail.
- **What:** New `vulnerability_findings` table: `finding_id`
  (CVE + package + digest) + `status` (open / accepted / snoozed /
  fixed) + `assignee_user_id` + `justification` + timeline. FE:
  "Triage" tab on the security surface with filter chips. Audit
  events per state transition. Ties into FUT-021 (a
  currently-accepted finding SHOULDN'T block admission).
- **Depends on:** FUT-021 for the "accepted-CVE bypasses gate"
  contract.

#### FUT-031 — MCP server for the registry
- **Why:** AI coding assistants (Claude, Cursor, GitHub Copilot
  Workspace, etc.) increasingly speak MCP (Model Context Protocol).
  Exposing the registry as an MCP server lets an operator's LLM ask
  "which of our images have log4j 2.14?", "trigger a rescan of every
  tag in `prod/*`", "revoke every API key not used in 60 days" — all
  via natural language grounded in real state. Genuinely novel for a
  registry.
- **What:** New `services/mcp` binary implementing the MCP protocol.
  Read tools: `list_repositories`, `list_tags`, `search_images`,
  `get_scan_report`, `list_stale_keys`, `get_audit_events`. Write
  tools (behind explicit consent flags): `trigger_scan`,
  `snooze_review`, `revoke_key`, `promote_tag`. Authenticates via a
  service-account key; runs alongside the management BFF; docs on
  wiring up Claude Desktop / Cursor / etc.
- **Cost estimate:** ~1 sprint for read-side; +1 sprint for write
  tools with proper consent UX.

### Tier 4 — Self-hosted-specific operational tooling

#### FUT-032 — Air-gapped install bundle
- **Why:** Regulated environments (defence, healthcare, banking)
  cannot pull dependencies from the internet at install time. An
  offline-friendly bundle unlocks this whole customer segment.
- **What:** `oci-janus-airgapped.tar.gz` — all container images
  pre-populated for one command install; embedded Docker Hub / GHCR
  mirror seeded with the top 500 base images; offline docs; scripted
  cert generation. Companion `docs/AIRGAPPED-INSTALL.md`.

#### FUT-033 — Backup / restore CLI + procedure
- **Why:** Self-hosters own their DR — SaaS provides this transparently,
  self-hosters need first-class tooling or they'll roll it themselves
  badly. Real risk mitigation.
- **What:** `registryctl backup --to s3://bucket/prefix` — captures
  Postgres dumps (per-service DBs), blob storage manifest,
  `token_policies`, secrets metadata, cert bundle. `registryctl
  restore --from s3://bucket/prefix` — one command to recover on a
  fresh host. Runbook: `infra/runbooks/dr-restore.md`.
- **Depends on:** FUT-029 for the CLI surface.

#### FUT-034 — Bootstrap wizard (GUI-driven first run)
- **Why:** The existing `registry-auth bootstrap` CLI is great for
  automation but the first-run UX for a fresh self-hoster clicking
  through the dashboard is a JSON-error wall. A guided wizard —
  "point me at your S3 bucket + your OIDC IdP and I'll do the rest"
  — closes the "installed but not configured" gap.
- **What:** Detect fresh-install state (no bootstrap tenant yet)
  → serve a `/setup` wizard route with 5 steps: (1) admin creds
  (2) storage backend (3) SSO (optional) (4) certificate posture
  (5) sanity checks + finish. Wraps the existing bootstrap CLI as
  a BFF-side function; disables itself after first run.

#### FUT-035 — Actionable operator dashboard homepage
- **Why:** The current dashboard homepage is a marketing-ish landing
  page. Self-hoster IS the ops team — they need "what broke? what's
  expiring? what's spending storage?" surfaced immediately.
- **What:** Replace the homepage with a ops-focused feed: cert
  expiries in <30 days, stale keys from FUT-004, failed webhook
  deliveries from FUT-019 territory, storage growth chart, scan
  queue depth, retention pending-delete counts, unassigned CVE
  findings from FUT-030. Every card links to its detail surface.

#### FUT-036 — Config export / drift detection
- **Why:** IaC-adjacent. Self-hosters want "what changed in my
  token policies over the last month?" — snapshot state, diff
  snapshots, alert on drift.
- **What:** `registryctl config export > config.yaml` captures
  every operator-owned setting (repos, policies, RBAC, webhooks,
  OIDC trusts). `registryctl config diff` compares two exports.
  Optional: nightly export + git commit for a full audit trail.

#### FUT-037 — Command palette (Cmd+K)
- **Why:** Fast navigation. Every modern developer tool has it now.
  Half a day of FE work; big polish delta.
- **What:** Cmd+K opens a palette. Search across: repos, tags,
  digests (paste and jump), users, service accounts, settings
  routes, scan reports. Uses existing TanStack Router route table.

#### FUT-038 — Bulk multi-repo operations
- **Why:** Ops teams love bulk. "Delete every tag matching `dev-*`
  older than 30 days across every repo." Currently requires custom
  scripts against the API.
- **What:** New BFF route `POST /api/v1/bulk/tag-delete` accepting
  a JSON selector (name glob + tenant + retention). Dashboard:
  "Bulk operations" section under `/admin` with pre-flight
  preview + confirm + audit trail. Also: bulk assign RBAC, bulk
  scan trigger, bulk retention apply.

#### FUT-039 — Just-in-time (JIT) access grants
- **Why:** Internal deploy bots don't need forever-credentials.
  "Give this bot pull access to `prod/*` for 45 minutes" — auto-expire,
  full audit. Complements FUT-004 (which surfaces stale keys) with the
  opposite posture (short-lived by construction).
- **What:** New `access_grants` table: `principal_id`, `scope`,
  `expires_at`. FE: `/settings → Security → JIT grants` with a
  "grant temporary access" form. Auth service consults `access_grants`
  on ValidateAPIKey (as an OR clause alongside role_assignments).

#### FUT-040 — SLSA attestation viewer + provenance chain
- **Why:** Cosign / in-toto attestations already work through the
  referrers API, but rendering them in the FE currently is raw JSON.
  A proper viewer completes the supply-chain story.
- **What:** FE parses SLSA v1.0 provenance predicates + renders
  build metadata: source repo + commit, builder (GitHub Actions
  workflow URL), materials list, invocation params. "Provenance"
  tab on the tag detail page. Optional: chain visualisation
  (tag ← attestation ← source commit ← previous tag).

---

## Self-hosted gap batch — 2026-07-01

> Surfaced during a "what's still missing?" ideation pass after the
> Wave 1 batch (FUT-020/021/031) shipped. Same lens as the platform
> expansion section above: `DEPLOYMENT_MODE=single`, one org running
> the stack for themselves, competing with Harbor / Nexus. Deliberately
> excludes anything already tracked above. Recommended pickup order:
> FUT-044 → FUT-047 (quick wins), then FUT-042 (security gap),
> FUT-041 (differentiator), then FUT-045 / FUT-046 (sprint-sized
> Harbor-migration bets).

### FUT-041 — Base-image staleness detection ("Dependabot for images")
- **Why:** The scanner says "you have CVEs"; the fix is almost always
  "rebuild on a newer base." Nothing connects the two today. Detecting
  that an image was built on `alpine:3.19` while a newer patch of that
  base exists closes the loop — and no competitor does this well
  in-registry.
- **What:** Identify each manifest's base image via layer-chain digest
  match (same primitive FUT-025 needs) + SBOM `base-image` hints where
  present. Compare against upstream state the pull-through cache
  already tracks. Surface as a dashboard panel ("12 prod images on a
  stale base with 3 fixed CVEs") + a `base_image_staleness` FUT-019
  notification category with per-repo detail.
- **Cost estimate:** ~1-2 sprints.
- **Affects:** `services/metadata` (base-image resolution query),
  `services/proxy` (upstream tag freshness), `services/audit`
  (notification category), `services/management`, `frontend`.
- **Depends on:** FUT-019 phase 2 (scheduled-notification worker) for
  the nudge channel; complements FUT-025 (shared base-layer analysis).

### FUT-042 — Scan the pull-through cache
- **Why:** Scan policies + the FUT-021 CVSS gate cover pushed images,
  but proxy-cached upstream content flows through unscanned — the
  proxy's schema is untouched by `services/metadata`, and the proxy
  doesn't even publish `pull.image` yet (known follow-up, Tier 2 #5).
  For a self-hoster the cache is where Docker Hub content enters the
  network — often the *largest* unscanned attack surface.
- **What:** Proxy publishes a `store.completed` event on each newly
  cached manifest; `services/scanner` consumes it and scans the cached
  image like any pushed one. Extend the FUT-021 admission check to
  cached pulls (same fail-OPEN-until-first-scan posture). Scan status
  column on the `/workspace/proxy-cache` table.
- **Cost estimate:** ~1 sprint.
- **Affects:** `services/proxy` (event publish), `services/scanner`
  (consumer + cache-image resolution), `services/core` or proxy serve
  path (admission), `frontend`.
- **Depends on:** closes the Tier 2 #5 follow-up (proxy `pull.image`)
  as a natural side effect.

### FUT-043 — Storage forecast + disk-pressure alerting
- **Why:** SaaS users never think about disk; self-hosters own it.
  Storage-usage data, GC, and retention all exist, but nothing warns
  "at the current growth rate your MinIO volume fills in ~38 days."
- **What:** Time-series growth trend on the dashboard storage card +
  a `storage_pressure` FUT-019 notification category. Alert body ranks
  remediations by reclaim size: retention rules to tighten, biggest
  never-pulled repos, GC dry-run estimate.
- **Cost estimate:** ~1 week.
- **Affects:** `services/metadata` (growth query), `services/audit`
  (category), `frontend` (dashboard card).
- **Depends on:** FUT-019 phase 2 for the notification channel.

### FUT-044 — Maintenance / read-only mode
- **Why:** A self-hoster running `pg_dump` mid-push gets a torn
  snapshot. A first-class read-only toggle is what makes backups
  consistent and upgrades safe — and it should ship *before* the
  FUT-033 backup/restore CLI so that tooling has a safe window to
  operate in.
- **What:** `PUT /api/v1/admin/maintenance` (platform-admin-gated)
  flips a `maintenance_mode` key in `tenant.deployment_metadata`.
  `services/core` checks it in the interceptor chain: pulls succeed,
  pushes / deletes reject with a clear OCI `UNAVAILABLE`-class error
  body; optionally drain in-flight uploads. FE shows a persistent
  banner + a toggle on `/admin`.
- **Cost estimate:** ~2-3 days.
- **Affects:** `services/tenant` (metadata key), `services/core`
  (interceptor check), `services/management` (route), `frontend`.
- **Depends on:** nothing. FUT-033 (backup/restore) depends on *this*.

### FUT-045 — LDAP / Active Directory auth
- **Why:** SSO covers OAuth / OIDC / SAML, but a large slice of the
  self-hosted market (exactly the Harbor / Nexus crowd) runs plain
  LDAP with no OIDC bridge. A hard adoption blocker for those shops.
- **What:** LDAP bind provider alongside the existing entries in
  `global_sso_config`: server URL + bind DN + user/group search
  filters, StartTLS/LDAPS only, group→role mapping reusing the SAML
  auto-provisioning path. Admin UI panel next to the OAuth/SAML
  config; connection-test button.
- **Cost estimate:** ~1 sprint.
- **Affects:** `services/auth` (provider + config columns),
  `services/management`, `frontend`, `docs/AUTH.md`.
- **Depends on:** none; slots into the RM-003 global SSO shape.

### FUT-046 — Registry-to-registry replication policies
- **Why:** Tier 3's "geo-replication" is a storage-layer mirror; this
  is Harbor's actual killer feature — *selective, policy-driven*
  replication: "push everything matching `prod/*` to the edge-site /
  DR / air-gapped-enclave registry." The biggest remaining "why Harbor
  instead of you?" answer.
- **What:** New `replication_policies` table (name filter glob,
  destination registry + creds AES-256-GCM like proxy upstreams,
  trigger: on-push | scheduled | manual). Worker reuses the proxy's
  remote-registry client to push manifests + blobs, with digest
  verification + retry via `FOR UPDATE SKIP LOCKED`. Composes with
  FUT-020: promote to `prod/*` → auto-replicates. FE: `/workspace/
  replication` policy CRUD + per-policy run history.
- **Cost estimate:** ~2 sprints.
- **Affects:** new worker (likely inside `services/proxy` or a small
  `services/replicator`), `services/management`, `frontend`.
- **Depends on:** FUT-020 (shipped) for the promote-then-replicate
  composition; supersedes Tier 3 "Geo-replication" when picked up.

### FUT-047 — Upgrade advisor
- **Why:** Self-hosted deployments rot silently. Operators don't know
  a release with security fixes exists until something breaks.
- **What:** Daily check against the GitHub releases API (opt-out flag
  for air-gapped installs): compares running version to latest,
  surfaces "v2.1.0 available — 2 security fixes, 1 migration required"
  as a FUT-019 notification + an `/admin` banner with inline release
  notes. Follows the FUT-019 tone rule: actionable noun + verb first.
- **Cost estimate:** ~2 days.
- **Affects:** `services/audit` (scheduled category), `services/
  management` (version endpoint), `frontend` (banner).
- **Depends on:** FUT-019 phase 2 for the worker + category plumbing.

---

## Fable review absorption — 2026-07-01

> Absorbed from three review docs (`docs/{ui,sec,backend}-suggestion-fable.md`,
> deleted once absorbed here). Items already tracked elsewhere were skipped
> (Cmd+K → FUT-037, ops attention strip → FUT-035, vuln triage → FUT-030,
> MFA → Tier 1 #1, universal RLS → ARCH-001, circuit breaker → ARCH-009,
> read replicas → ARCH-013/014, CI items → REM-014/016/020, KEK rotation →
> RED-FU-015, FE test depth → QA-020). Three claims were verified false and
> dropped: `infra/runbooks/secret-rotation.md` "missing" (it exists),
> `server.exe`/`cover_*.out` "committed" (untracked + gitignored), and "no
> rate limit on login" (`CheckIPRateLimit` already returns 429 — only the
> per-username dimension is missing, see FUT-052).

### FUT-048 — Consumer idempotency + poison-message policy
- **Why:** ARCH-002 (transactional outbox) covers the *publish* side only.
  Consumers have DLX + manual ack but no documented redelivery cap and no
  idempotency convention — a crash between side-effect and ACK reprocesses
  the event.
- **What:** `message_id` on all published events (publisher is already
  typed); consumer-side dedup (Redis `SETNX` or a small dedup table) per
  service; max-redelivery via `x-delivery-count` before DLX. Document the
  convention in `docs/EVENTS.md`.
- **Cost estimate:** ~1 week.
- **Affects:** `libs/rabbitmq/{publisher,consumer}`, every consumer
  service, `docs/EVENTS.md`.

### FUT-049 — Supply-chain dogfooding: sign + SBOM our own images
- **Why:** The platform verifies signatures and generates SBOMs for
  customer images but ships its own images unsigned and SBOM-less. Strong
  credibility signal for a registry product to eat its own dog food.
- **What:** Cosign keyless (GitHub OIDC) signing in CI on push to GHCR;
  syft → SPDX SBOM per service image attached as OCI referrers (we
  implement the referrers API — use it); `cosign verify` walkthrough in
  `docs/SELF-HOSTING.md`. Add `go mod verify` + a license check
  (`go-licenses`) to the shared CI path while in there.
- **Cost estimate:** ~2-3 days of CI work.
- **Affects:** `.github/workflows`, `docs/SELF-HOSTING.md`.

### FUT-050 — Storage-driver conformance test suite
- **Why:** storage + proxy sit directly on the data path and are
  effectively untested (coverage is 80%+ on core/auth/audit/management/
  webhook). The driver interface in `libs/storage/driver` makes one shared
  contract suite natural.
- **What:** conformance tests run against all 5 backends — testcontainers
  MinIO + filesystem in CI; S3/GCS/Azure as optional live targets. Follow
  on with proxy pull-through digest-verification + `store.queued` retry
  paths, and signer Vault error paths.
- **Cost estimate:** ~1 week.
- **Affects:** `services/storage`, `services/proxy`, `libs/testutil`.

### FUT-051 — Scheduled `VerifyChain` + audit alert rules
- **Why:** `Repository.VerifyChain` (Phase 6.12 / REM-022) exists but
  nothing calls it — tamper evidence is only useful if something checks
  it. Cheaper complement to RED-FU-017 (checkpoint signing, parked).
- **What:** cron in `services/audit` runs `VerifyChain` per tenant under
  an advisory lock; failure emits a metric + notification. Ship starter
  Prometheus alert rules for metrics that already exist
  (`registry_grpc_peer_cn_denied_total`, `registry_auth_jwt_kid_fallback_total`,
  JTI-revocation Redis failures, SIEM-export `dlx_depth` growth). Document
  the audit retention posture (hot window + archive story).
- **Cost estimate:** ~2-3 days.
- **Affects:** `services/audit`, `infra/` (alert rules).

### FUT-052 — Login brute-force hardening (per-username dimension)
- **Why:** the login path already has an IP-based rate limit
  (`CheckIPRateLimit` → 429). Missing: the per-`(username, IP)` sliding
  window with exponential backoff (not hard lockout — avoids username-DoS)
  and an `auth.login_failed` audit event feeding SIEM export + the
  FUT-019 `failed_login_burst` category.
- **Cost estimate:** ~2-3 days.
- **Affects:** `services/auth`, `services/audit`.

### FUT-053 — `SSO_SAML_TRUST_EMAIL` guardrails
- **Why:** the flag treats an IdP assertion as email verification; a
  hostile IdP config can mint accounts for arbitrary email addresses.
- **What:** ensure default-false, startup warning when true, and constrain
  trust-email to a configured allowed-domain list.
- **Cost estimate:** ~1 day.
- **Affects:** `services/auth`, `docs/SAML.md`.

### FUT-054 — OpenAPI spec for the management BFF
- **Why:** the BFF conditionally mounts SSO/signer/scanner/gc routes on
  env-var presence, so the REST surface is deployment-dependent and hard
  to document; the hand-maintained Postman collection already drifted once
  (PENTEST-033).
- **What:** generate an OpenAPI spec from the router, publish it, and
  generate the Postman collection from the spec instead of by hand.
- **Cost estimate:** ~1 week.
- **Affects:** `services/management`, `docs/postman/`.

### FUT-055 — `services/mcp` positioning decision
- **Why:** FUT-031 shipped the read-side MCP server; the service now needs
  a declared status. Half-status services rot.
- **What:** decide first-class (own CI workflow, coverage targets,
  hardening-checklist row) vs experimental (marked clearly, excluded from
  release gating) — then do the ~1 day of follow-through either way.
- **Affects:** `services/mcp`, `.github/workflows`, `docs/MCP.md`,
  `docs/HARDENING-CHECKLIST.md`.

### FUT-056 — Operability small-fry batch
Each ≤1 day; pick up alongside neighbouring work:
- **GC per-run metrics** — blobs marked/swept, bytes reclaimed, duration,
  exposed on the existing `:9090` port so GC stalls alert before disks fill.
- **Restore-validation drill** — scheduled restore of the latest dump into
  a scratch DB + smoke query + `VerifyChain`. Backups never restored are
  hope, not DR. Pairs with FUT-033.
- **Auth shadow-user lookup** — the flagged inefficient lookup sits on the
  token-validation hot path; index or join rewrite before it shows in p99s.
- **Scanner report persistence** — compliance reports still write to a
  temp file; route through the storage driver so they survive restarts.
- **Root-doc fold** — move `local-setup.md` + `prod-flow.md` into `docs/`
  per the "root stays slim" philosophy.

### FUT-057 — UI polish batch (Fable review 2026-07-01)
None of these justify individual FUT numbers; pick with neighbouring FE work:
- **Copy-paste ergonomics** — `docker pull` command block on tag detail,
  `cosign verify` snippets on signed tags, one-click copy on every digest
  (JetBrains Mono). Registry users live in terminals.
- **Teaching empty states** — empty repo list shows the real
  `docker login` / `docker push` commands with the deployment's hostname.
- **Error-message mapping** — gRPC/BFF codes → human explanations at the
  axios layer (e.g. single-mode `FAILED_PRECONDITION` on tenant create →
  "this deployment is single-tenant"). Raw code strings never reach a toast.
- **Session-expiry UX** — refresh failure shows a "reconnecting…" banner
  with retry before hard logout; don't destroy form state on a blip.
- **Live-ish freshness** — React Query `refetchInterval` (or BFF SSE) on
  active surfaces + "updated Ns ago" stamp so tag lists update after push.
- **Table power features** — column sort persisted to URL search params,
  per-surface filter persistence (localStorage).
- **Route-level code splitting** — lazy modules for heavy surfaces (SBOM
  viewer, audit explorer) to keep first paint fast.
- **`frontend/DESIGN.md`** — document the Beacon OKLCH tokens, spacing,
  motion rules, font usage before OSS contributors drift them.

---

## UI feature sweep — 2026-07-05

Output of a 4-agent read-only sweep (UX inventory, backend-exists-no-FE,
OCI-domain gaps, backlog dedup) requested after the active-session-list
ship. Everything below was deduped against the existing FUT-/DSGN-/UIR-
backlog — these are net-new or only loosely adjacent. **Group A items have
working gRPC RPCs today and no UI — highest value-per-effort (mostly a BFF
route + a form).**

### Group A — backend already built, just needs BFF + FE wiring

#### FUT-058 — Proxy-cache upstream management — **Tier 2**
- Add/list/remove upstream registries (docker.io, ghcr, quay…) from the
  UI instead of editing deploy config. The `/workspace/proxy-cache` page
  currently only *reads* upstreams discovered from cached manifests.
- **Backend ready:** `proxy.v1` `RegisterUpstream` / `ListUpstreams` /
  `DeleteUpstream` (`proto/proxy/v1/proxy.proto`, impl in `services/proxy`).
  No BFF route exists yet — add to `services/management` + FE CRUD.

#### FUT-059 — Tenant-wide default security policy — **Tier 2**
- One screen for org-wide defaults (scan-on-push, block-on-severity,
  allow-unscanned, signing-required, proxy-cache-enabled, storage quota,
  exempt repos) instead of per-repo policy only. Slots into
  `/settings/workspace` (single mode) / `/settings/platform` (multi).
- **Backend ready:** `tenant.v1` `GetTenantPolicy` / `UpdateTenantPolicy`
  (`services/tenant/internal/handler/grpc.go`); management never calls them.

#### FUT-060 — Pending-deletion review view — **Tier 2**
- Aggregate "everything retention + GC will delete on the next run" list
  with cancel, so operators audit scheduled deletions before they happen.
  Today only a per-tag `retention_pending_delete_at` pill exists.
- **Backend ready:** `metadata.v1` `ListPendingDeleteManifests`
  (`services/metadata/internal/handler/retention_pending.go`); no BFF/FE.

#### FUT-061 — Platform-admin management UI — **Tier 2**
- Promote/demote global admins (`users.is_global_admin`) from the UI;
  today it needs the bootstrap CLI or a direct DB edit. Fits a new section
  on `/settings/platform`.
- **Backend ready:** `auth.v1` `SetGlobalAdmin` (referenced only in a
  comment at `services/management/internal/handler/rbac.go:99`).

#### FUT-062 — Per-repo storage quota editing — **Tier 3 (small)**
- Raise/lower a single repo's quota after creation (schema field
  `repositories.storage_quota` is set at create-time only; the PATCH repo
  handler edits description/immutable/require-signature/CVSS but not quota).
- **Backend ready:** `metadata.v1` `UpdateRepositoryQuota`; add the BFF
  PATCH field + a form control.

### Group B — net-new features (genuine whitespace, real value)

#### FUT-063 — Image config & history viewer — **Tier 2**
- Render the image *config blob* on tag detail: env / entrypoint / cmd /
  exposed ports / working dir + the `history[]` step list (effectively the
  Dockerfile). The Layers tab shows only the config *descriptor* today.
  Highest-impact pure-whitespace item — the #1 "what is this image?"
  surface, and the bytes are already stored. Docker Hub / GHCR / Quay all
  have it.

#### FUT-064 — Admission-policy decision & violation log — **Tier 2**
- Queryable feed of denied pulls/pushes and *why* (unsigned, untrusted
  key, immutable re-push, quarantined, over-quota, CVSS-gated). Operators
  turning on enforcement currently fly blind on impact / false-positives.
  `scan.policy_blocked` is already a notification event but isn't queryable.

#### FUT-065 — Markdown README / Overview per repo — **Tier 2**
- Full markdown README tab (usage, examples, copyable pull commands).
  `DescriptionCard` is plain-text only today ("no markdown per FE-SEC-011").
  Needs a sanitising markdown renderer (revisit the FE-SEC-011 XSS concern).
  Primary repo discoverability/onboarding surface.

#### FUT-066 — GC dry-run preview (mark-sweep) — **Tier 2**
- "What would this GC reclaim" preview before running destructive blob GC
  (mirrors retention's mandatory dry-run). Today the GC card only offers
  status/history/run-now — running blind on a self-hosted volume is risky.
  Harbor ships exactly this. (Related storage-reclaim reads:
  `metadata.v1` `ListUntaggedManifests` / `ListOrphanedBlobs`, GC-internal
  today.)

#### FUT-067 — In-app audit explorer + CSV/JSON export — **Tier 2**
- Forensic search on `/activity` by actor / resource / action (not just
  event-type chips + date range) with on-demand export to attach to a
  ticket. SIEM streaming (audit-export) already exists but assumes you
  *have* a SIEM. (FUT-057 mentions lazy-loading an "audit explorer" but
  nothing is specced/built.)
- **Amendment (2026-07-05 system review):** include a "Verify chain
  integrity" action surfacing `Repository.VerifyChain` — result banner
  showing intact / `FirstBadID`+`FirstBadAt` / `Unverifiable` count.
  The tamper-evident hash chain (Decision #30, CLAUDE.md §10) is the
  platform's headline audit feature and currently has **no UI at all**;
  this complements FUT-051 (scheduled backend verify + alert rules)
  with an on-demand operator surface.

#### FUT-068 — Raw manifest / config JSON inspector on tag detail — **Tier 3 (small)**
- Show the exact manifest + config bytes for a pushed tag (media-types,
  annotations, multi-arch shape). Today operators shell out to
  `crane`/`skopeo`. FUT-016 slates this for *proxy-cache* detail only —
  this is the same for regular pushed tags.

#### FUT-069 — Discovery: top-pulled leaderboard + OCI label browse/filter — **Tier 3**
- (a) Tenant-wide ranked "most-pulled / most-active images" view (pull
  counts already tracked via FE-API-042) — fastest way to see what matters
  and what's safe to retire. (b) Surface + filter by
  `org.opencontainers.image.*` annotations and arbitrary labels
  (maintainer, source, version, team) — invisible today.

### Group C — untracked UX improvements (low effort, high polish)

#### FUT-070 — Navigation & drill-down UX bundle — **Tier 3**
Pick alongside neighbouring FE work; none justifies its own number:
- **Dashboard KPI tiles as drill-downs** — the vuln / pulls / repos /
  storage tiles show numbers but link nowhere; route each to its filtered
  view (`_authenticated.index.tsx`).
- **Repo-level security rollup card** — repo detail has no "N open findings
  across tags" → filtered `/security`; today you open each tag's Security
  tab (`_authenticated.repositories.$org.$repo.tsx`).
- **Bulk actions on user-facing tables** — row checkboxes + selection bar
  for revoke-keys / disable-users / rescan-selected / bulk-delete-tags.
  (FUT-038 covers admin bulk-*repo* ops only; this is the per-table case.)
- **"Recently viewed" repos strip** — dashboard/repo-list shortcut so
  operators stop re-searching the same handful of repos.

#### FUT-079 — Auth/forms UX polish bundle — **Tier 3**
Low-effort, high-polish fixes on the auth + secret-entry surfaces. None
justifies its own number; pick alongside neighbouring FE work.
- **"Show password" reveal toggle** — ✅ **SHIPPED 2026-07-12 (PR #328).**
  Reusable `PasswordInput` wrapper (`components/ui/password-input.tsx`, Eye/EyeOff)
  wired to all 8 `type="password"` fields; a11y-correct + TDD. Resolution row
  in [`status.md`](status.md).

#### FUT-077 — Cross-environment image comparison matrix — **Tier 2/3**
Deferred sibling of the environments-overview work
(`feat/environments-overview`, spec
`docs/superpowers/specs/2026-07-11-environments-overview-design.md`). That
work delivers org-as-environment **cards → per-environment repo drill-down**
but deliberately stops at browsing *one environment at a time*.
- **Why:** when orgs are environments (`dev`/`stage`/`prod`) the same logical
  image lives in several of them (`dev/api`, `prod/api`). The common operator
  question is "**is `prod/api` behind `dev/api`?**" — which a per-environment
  drill-down can't answer on one screen.
- **What:** an image-centric matrix view — rows = logical image names,
  columns = environments (`dev`/`stage`/`prod`), cells = current tag/digest +
  promotion/drift status. Ties into the existing promotion flows
  (`PromoteTagDialog`, promotions API, PR-registry).
- **Blocked on backend data we don't expose yet:** "image `X` exists in orgs
  `[Y…]` at digests `[…]`" — a cross-org GROUP BY that the metadata service
  doesn't surface today. The environments-overview `ListOrgs` aggregate is a
  natural place to grow this.
- **Affects:** `services/metadata`, `services/management`, `frontend`.

---

## Tier 3 — Nice-to-have polish

Real value, but easy to defer.

- **Native CLI** — `oci-janus` Go binary for bulk ops (scripted
  promotions, backup, repo lifecycle). Docker CLI only goes so far.
- **Onboarding wizard** — first-push tutorial, first-scan walkthrough
  on a freshly-provisioned tenant.
- **Slack / Teams native connectors** — first-class beyond webhooks,
  so an admin can install with one click and pick channels.
- **GitOps integrations** — Argo CD / Flux image-update polling
  endpoint with weak-ETag support.
- **Storage tiering** — cold tier for old images (S3 Glacier / GCS
  Coldline), surfaced as a tenant policy.
- **Geo-replication** — secondary-region mirror of the storage layer
  for DR.
- **Backup / restore UX** — point-in-time recovery for metadata.
  Today operators have to use pg_dump out of band.
- **Migration tooling** — bulk import from Docker Hub / Nexus / ECR
  via an Operator dialog ("import all images from your Docker Hub org").
- **Print-friendly compliance report views** — auditor PDFs that
  don't require a screenshot.
- **Mobile-responsive QA pass** — the dashboard is desktop-first.
- **a11y audit** — `S8` polish covers this; carve out time.

---

## Bug-class / minor cleanups (not features, but worth tracking)

- **`/repositories/$org/$repo` Settings tab** is an EmptyState placeholder —
  see Tier 2 #2 above; ship as a unit.
- **Some sidebar entries shipped before their route existed** (the
  `/api-keys` dead-link in the sidebar was the canonical example;
  fixed in this commit). Worth a one-pass audit of every `to:` in the
  sidebar against the actual route tree.
- **Trivy adapter ignores whiteout files** when flattening layers (REM-011
  Phase 1 known limitation, documented in `docs/SCANNER.md` §6).
  Produces correct-but-stricter results; a proper overlayfs replay is
  the fix.
- **`pull.image` event never published** — DONE (FE-API-042, 2026-06-21).
  services/core now publishes `pull.image` on every successful manifest
  GET; analytics endpoint returns real numbers. Tier 2 #5 above has
  the resolution notes.
- **`signature_header` + `response_body` empty on webhook deliveries** —
  the FE-API-035 schema is in place; the dispatcher needs a migration +
  patch to actually capture them at delivery time. The UI already
  renders a "Not captured · backend follow-up tracked in status.md
  FE-API-035" muted placeholder.
---

## System review batch — 2026-07-05 (full-platform sweep)

Output of a 3-agent full-system review (trackers, backend gap analysis,
frontend UI audit) run ahead of the v2.0.0 tag. Deduped against the
whole backlog — already-tracked overlaps landed as pointers, not
duplicates: replication → FUT-046, audit explorer → FUT-067 (amended
above with the chain-verify action), CLI/Terraform → FUT-029/FUT-027,
base-image staleness → FUT-041, Grafana/alert rules → ARCH-012,
backup restore-verification → FUT-056, `libs/storage` tests → FUT-050,
WebAuthn → Tier 1 #1 residual. Below is only what was genuinely
untracked.

#### FUT-071 — Air-gap export/import bundle — **Tier 2**
- **Why:** Regulated / disconnected self-hosters need to move images
  between a connected and an air-gapped registry instance. Almost no
  OSS registry does this well — a genuine differentiator for the
  self-hosted posture, and a natural headline feature for v2.1.
- **What:** `registryctl export <repo[:tag|@digest]> --out bundle.tar`
  producing a self-contained archive (blobs + manifests + referrers +
  Cosign signatures + SBOMs + latest scan report), and
  `registryctl import bundle.tar` on the target instance with full
  digest verification before any write. Dashboard equivalent optional.
- **Depends on:** FUT-029 (`registryctl` CLI) for the transport;
  complements FUT-026 (import from public registries).

#### FUT-072 — Vulnerability diff between tags — **Tier 2**
- **Why:** "What CVEs did `:v1.2` add vs `:v1.1`?" is the
  release-gating question every operator asks before promoting. All
  the scan findings are already stored per digest in metadata — this
  is mostly a query + a panel, with outsized perceived value.
- **What:** metadata query joining scan findings for two digests →
  added / removed / unchanged CVE sets (with severity deltas); BFF
  endpoint + a "Compare with…" tag picker on the tag-detail Security
  tab. Distinct from FUT-024, which diffs *files/layers* — this diffs
  *scan findings*.

#### FUT-073 — Per-token rate limiting on the core data plane — **Tier 2**
- **Why:** the only per-principal limiter today is the in-process
  per-user token bucket in the management BFF
  (`services/management/internal/middleware/ratelimit.go`,
  PENTEST-014). `services/core` — the actual OCI push/pull path — has
  none; it relies entirely on gateway IP limits. One leaked token or a
  runaway CI loop can hammer pulls unbounded, and IP limits don't help
  when the abuser is behind shared NAT / in-cluster.
- **What:** Redis-backed limiter keyed by token/user (fall back to IP
  for anonymous pulls) on core's request path, per-tenant configurable
  rates, returning `429` + `Retry-After` with the OCI
  `TOOMANYREQUESTS` error code. Consider lifting into
  `libs/middleware` so proxy gets it for free. Fail-open on Redis
  outage (availability posture, mirrors the API-key cache).

#### FUT-074 — Quota fail-open observability + optional fail-closed mode — **Tier 3 (small)**
- **Why:** `CheckQuota` in core deliberately fails OPEN when the
  metadata call errors (`services/core/internal/handler/http.go:808`)
  — a reasonable availability choice, but today the bypass window is
  completely invisible to operators.
- **What:** (a) counter metric + WARN log whenever a push proceeds
  with the quota check skipped, plus an alert-rule entry (ties into
  ARCH-012); (b) a `QUOTA_FAIL_CLOSED=true` env opt-in for operators
  who prefer a 503 over an unmetered write during a metadata outage.

#### FUT-075 — Test-debt truth-up: `libs/scanner` + thin services + TESTING.md claims — **Tier 3**
- **Why:** `docs/TESTING.md` claims 80% minimum per service and "libs
  foundation packages all covered", but the 2026-07-05 sweep found
  `libs/scanner` has **zero** test files and several services are thin
  (gateway 2 test files, storage 3/7, tenant 3/6, signer 5/10). The
  doc overstates reality — that's worse than honest gaps because it
  suppresses the signal. (`libs/storage` zero-tests is already covered
  by FUT-050's driver conformance suite.)
- **What:** (a) unit tests for `libs/scanner` plugin-host types
  (ScanRequest/ScanResult marshalling, interface contract); (b) raise
  gateway/tenant/signer coverage toward the stated floor; (c) correct
  TESTING.md to state *measured* per-service coverage and mark the
  known exceptions instead of a blanket claim.

#### FUT-076 — Live documentation site: getting-started, UI guide, integrations & MCP — **Tier 2 (the getting-started + publish slices are Tier 1 for OSS adoption)**
- **Status (2026-07-10): ✅ SHIPPED & LIVE** at
  https://steveokay.github.io/oci-janus/ — all 6 slices done, including the
  generated OpenAPI spec + body schemas, generated Postman collection,
  consolidated env reference, Helm/Compose deep-dive, and dashboard
  screenshots + flow GIFs across the UI guide. **Only remaining (optional,
  low priority):** tighten the cross-repo `../CLAUDE.md` / `../README.md` /
  `../infra/...` links that 404 on the published site, then flip
  `strict: true` in `mkdocs.yml`.
- **Why:** The platform has deep, accurate **reference** docs in
  `docs/*.md` (SERVICES, AUTH, DATABASE, DEPLOYMENT, SELF-HOSTING, MCP,
  SIGNING, SCANNER, SAML, CREDENTIAL-HELPERS, WORKLOAD-IDENTITY, …) — but
  they are all **developer/operator-facing markdown living in the repo**.
  There is no **published, browsable docs site**, no **end-user / UI
  walkthrough**, and no single guided **"get started in 10 minutes"**
  path. For an Apache-2.0 OSS project past launch (see the HYG-* items),
  that is the single biggest *adoption* blocker: people can't evaluate or
  operate the platform if the only way in is reading source + scattered
  `.md` files. Goal: **live docs that help other people get started and
  actually use the system — document everything, including every
  integration and MCP connectivity.**
- **What (ships in slices, roughly in order):**
  1. **Docs-site scaffold + publish pipeline** *(Tier 1 for adoption)* —
     ✅ **SHIPPED (2026-07-10)**: MkDocs Material site (`mkdocs.yml`) with
     an `index` + `getting-started` landing pair, the existing `docs/*.md`
     folded in as curated nav sections (nothing duplicated), planning
     (`superpowers/`) + Postman exports excluded, and a
     `.github/workflows/docs-site.yml` that builds on every PR and
     publishes to GitHub Pages on merge to `main`. One-time repo setting:
     Settings → Pages → Source = "GitHub Actions". Follow-up: tighten the
     cross-repo `../CLAUDE.md` / `../security.md` / `../README.md` links
     that 404 on the published site, then flip `strict: true`.
  2. **Getting-started / quickstart** *(Tier 1 for adoption)* — ✅
     **SHIPPED (2026-07-10)**: `docs/getting-started.md` expanded from a
     stub into a guided narrative — prerequisites, bring-up, **verify the
     stack is healthy** (compose ps + `/v2/` + dashboard smoke checks),
     **first push/pull** with expected output, **see it in the dashboard**
     (Repositories → tag → Trigger scan → Activity), next steps, and a
     troubleshooting block (stuck service, insecure-registry TLS,
     idempotent bootstrap, tenant-id build arg). Copy-paste blocks
     throughout. **Follow-up:** ✅ screenshots/GIFs SHIPPED (2026-07-10) —
     login GIF + dashboard screenshot wired in.
  3. **UI / dashboard guide** — ✅ **SHIPPED (2026-07-10)**: a six-page
     **"Using the dashboard"** section in the docs site
     (`docs/guide/{index,repositories,security,access,settings,operations}.md`)
     covering sign-in/MFA/SSO + shell, repositories & tags (incl. the
     Chart tab), the Security section, access/RBAC + API keys/service
     accounts, all Settings tabs (incl. Settings › Integrations / PR
     registries + email & webhook channels), profile/MFA/sessions, and
     operations (activity, SIEM export, pull-through cache, webhooks).
     Written from a two-agent read of the live routes so admin-gating +
     single-vs-multi-mode surfaces are accurate. **Follow-up:** ✅
     screenshots/GIFs SHIPPED (2026-07-10) — 13 dashboard screenshots + 3
     flow GIFs (login, explore-image, security-tour) captured from the live
     stack via a committed Playwright pipeline (`tools/screenshots/`) and
     wired into every guide page.
  4. **Integrations catalog** — ✅ **SHIPPED (2026-07-10)**:
     `docs/integrations/index.md` — one discoverable catalogue with a
     consistent what/how/deep-link entry per pluggable surface (storage,
     SSO, scanners, signing, webhooks, notification channels, SCM PR
     registries, MCP), env/KEK selectors sourced from a two-agent read of
     the code. Filled the biggest gap with a dedicated
     `docs/integrations/storage.md` (there was no storage doc): honest
     driver status (**minio + filesystem implemented**; s3/gcs/azure
     recognised in config but driver pending → use the S3-compatible minio
     driver for AWS S3 today), per-driver env tables, encryption-at-rest,
     tenant key prefixing. Nav section renamed **Integrations** with the
     catalog + storage pages on top.
  5. **MCP connectivity guide** — ✅ **SHIPPED (2026-07-10)**: `docs/MCP.md`
     was **verified accurate against `services/mcp` code** (two-agent read:
     all 12 `registry_`-prefixed read-only tools, the `MCP_*` env vars, the
     `:8092` HTTP default, and the `--profile mcp` compose service all
     PASS — no drift) and promoted to a first-class connect guide: framed
     as "connect your agent to the registry," an explicit **12 read-only
     tools** count, and cross-linked from the Integrations catalog. Nav
     label renamed to **Connect an AI agent (MCP)**. Content (stdio +
     HTTP setup for Claude Desktop / Cursor, security notes, example
     prompts, troubleshooting) was already complete — no rewrite needed.
  6. **Reference completeness ("document everything")** — ✅ **SHIPPED
     (2026-07-10)**: shipped `docs/architecture.md` (three-plane overview +
     diagram + comm patterns + data ownership + security posture, adapted
     from the README into a first-class page) and `docs/api-reference.md`
     (API & automation: the two HTTP surfaces — management BFF `/api/v1/*`
     + registry-auth direct routes — the JWT-vs-API-key auth model with a
     worked `curl`, pagination/error conventions, the `docs/postman/`
     collection seed, and a CLI/credential-helper pointer). Shipped the
     **generated OpenAPI 3.0 spec** — `services/management/cmd/openapi-gen`
     parses the `mux.Handle` route table (AST) and emits
     `docs/openapi.json` (all **142 operations / 104 paths**, with path
     params + Bearer auth), rendered as an interactive **API explorer**
     (`docs/api-spec.md`, self-contained Swagger UI via
     `mkdocs-swagger-ui-tag`) and guarded by a CI drift-check
     (`make openapi` + `git diff --exit-status` in `ci-management.yml`).
     Chose route-table generation over `swaggo` per-handler annotations so
     it's complete + never-drifting from day one. Also shipped the
     **consolidated per-service env reference** — `libs/cmd/env-ref-gen`
     parses all 14 `services/*/.env.example` files (section headers +
     per-var comments) into `docs/env-reference.md` (**280 vars across 14
     services**), drift-guarded by `docs-env-ref.yml`. Also shipped the
     **Helm/Compose deployment deep-dive** (`docs/deployment-guide.md`) —
     guided Compose path (infra deps, KEK `.env`, `--profile
     scanner|clair|mcp`, bootstrap) + Kubernetes/Helm path (umbrella
     `infra/helm/registry` v0.1.0, `global`/per-service values,
     External-Secrets + cert-manager mTLS, `helm dependency build` +
     `upgrade --install`, scaling/NetworkPolicy), health/observability, and
     a runbooks index — written from the real chart + compose. Finally
     shipped the **OpenAPI response body schemas** (PR #311 — `openapi-gen`
     reflects the handlers' response structs into 19 `components.schemas`,
     so the explorer shows real shapes) and a **generated Postman
     collection** (`services/management/cmd/postman-gen` builds it from
     `docs/openapi.json`, one folder per tag + a curated `identity` folder
     for the auth routes; drift-guarded alongside the spec).
- **Notes:** absorbs the doc-hygiene HYG items (HYG-001 README
  screenshot, HYG-006 architecture-diagram PNG) and pairs with HYG-007
  (Discussions) / HYG-008 (private vuln reporting) for the community
  surface. Ship incrementally — slices 1 + 2 unblock external evaluation;
  3–6 fill in over time. Keep the site self-contained and versioned to
  releases.

---

## Artifact catalog follow-up — 2026-07-12

Surfaced while reviewing the unified artifact catalog (PR #321). The
backend already **stores** every OCI artifact class — `deriveArtifactType`
(`services/metadata/internal/repository/repository.go:1045`) classifies
unknown `config.mediaType`s as `other` rather than rejecting, and
`ListRepositories` has a working `?artifact_type=other` filter. The gap is
presentation: the UI browses only `image` + `helm`, so ML models, WASM
modules, GitOps/policy/Terraform bundles, attestations, and arbitrary
ORAS pushes are stored-but-invisible. The unified-catalog spec explicitly
deferred this ("omit non-image/helm from row badges in v1"). Design:
[`docs/superpowers/specs/2026-07-12-generic-artifacts-surface-design.md`](docs/superpowers/specs/2026-07-12-generic-artifacts-surface-design.md).

#### FUT-078 — Generic Artifacts surface for non-image/helm OCI artifacts — **Tier 2**

Two slices; **only Slice 1 is committed**, Slice 2 is documented-but-deferred.

- **Slice 1 (committed) — generic "Artifacts" viewer.** Make everything in
  the `other` bucket visible + identifiable with **no new taxonomy**. Reuses
  the existing `deriveArtifactType` `other` bucket and the already-implemented
  `?artifact_type=other` filter — zero classification changes.
  - **Backend (verify-before-add):** confirm the raw `config_media_type`
    (and OCI 1.1 top-level `artifactType`, if present) reaches the BFF
    tag/manifest JSON; the proto already carries `config_media_type`, so this
    may be nil work. No new gRPC call.
  - **FE:** an "Other" lens on the unified catalog (leaning **fourth filter
    chip** All / Images / Charts / Other over a new sidebar route — plan-time
    call) that drives `?artifact_type=other` and renders each artifact's
    **raw media-type string verbatim** (e.g.
    `application/vnd.kitops.modelkit.config.v1+json`), size, tags, and a link
    to the **FUT-068 manifest inspector**. Neutral badge tone, distinct from
    cyan image / amber helm.
  - **Value:** every ORAS/model/wasm/policy artifact becomes visible on day
    one. Read-only surface — **no OCI conformance impact**.
- **Slice 2 (deferred, demand-gated) — promote a specific kind to first-class.**
  The *mechanism* is one `case` in `deriveArtifactType` + one entry in
  `configMediaTypesFor` + a badge/chip (the code documents this as "one row +
  one switch case"). The *cost is design*, captured as open questions, **not
  built until someone is actually pushing that kind**:
  1. **Scanner-policy applicability** — should a vuln scanner run against ML
     weights / WASM / policy bundles at all? Don't silently apply the image
     default.
  2. **Referrer semantics** — do `signature`/`sbom` referrers make sense for a
     non-image subject? Includes splitting `attestation` (in-toto/SLSA DSSE)
     out of `signature`.
  3. **Retention / GC semantics** — do tag-retention + mark-sweep GC treat a
     large versioned model tag like an image tag?
  - First candidate: **`model`** (biggest 2026 trend); `wasm` and the
    `attestation`/`signature` split follow the same earn-it path — promoted
    only when demand shows, the way `/helm` did.
- **Depends on / relates to:** FUT-068 (manifest inspector — Slice 1 reuses it);
  builds directly on the unified artifact catalog (PR #321).

---

## System gap audit batch — 2026-07-12 (5-agent full-system sweep)

Output of a 5-agent gap audit (frontend UI coverage, backend
completeness, gRPC→BFF→FE API matrix, tracker sweep, testing/CI/infra)
run 2026-07-12. Deduped against the whole backlog — already-tracked
overlaps stay where they are, with new audit evidence noted here:

- **Proxy upstream CRUD → FUT-058** — audit confirmed it's worse than
  "needs BFF+FE": `proxy.RegisterUpstream/DeleteUpstream/ListUpstreams`
  have **zero callers anywhere**; upstreams are SQL-seed-only today.
- **Tenant default policy screen → FUT-059** — `tenant.GetTenantPolicy`
  / `UpdateTenantPolicy` implemented server-side, zero callers.
- **Repo-level quota → FUT-062** — `metadata.UpdateRepositoryQuota` is
  a dead RPC; only tenant quota is surfaced.
- **Platform-admin UI → FUT-061** — worse than tracked:
  `auth.SetGlobalAdmin` has **no invocable path at all** (not even the
  bootstrap CLI); global admin can only be minted via direct SQL.
- **SCIM admin surface → Tier 1 #5 residual** — `scimTokenSvc.generate`
  has no HTTP/gRPC/CLI caller surfaced; SCIM cannot be enabled without
  touching the DB. No BFF routes, no UI, no `provisioned_via` badge.
- **Audit chain-verify UI → FUT-067**; **SAML trust-email fallback →
  FUT-053** (audit confirmed: with `SSO_SAML_TRUST_EMAIL=false` both
  provisioning AND existing-user login 403 — the Phase 5.6
  email-verification fallback was never built, so SAML is unusable
  without trusting IdP emails); **test-debt truth-up → FUT-075**;
  **router error/not-found boundary → QA-019**; **webhook
  signature/response capture → bug-class tail above**; **digest-keyed
  signing repo-scoping → RED-FU-003**; **webhook test-dispatch throttle
  → PENTEST-030**; **rotate-kek sslmode bypass → RED-FU-015 tail**;
  **older mapEvent malformed-payload hardening → SEC-070 residual**;
  **s3/gcs/azure drivers missing → known** (`docs/integrations/storage.md`
  is honest; CLAUDE.md §8 still claims five — see FUT-089).
- **Tracker inconsistency to verify:** security.md shows SEC-087 OPEN
  while status.md's PR #321 row claims the tenant-scope fix shipped
  inline — verify and flip the status line.

Below is only what was genuinely untracked.

#### FUT-080 — Compliance reports fabricate data: wire the report worker to real findings — **Tier 1 (correctness)**
- **Why:** every generated SPDX/PDF compliance report shows **0
  findings regardless of actual scan results**.
  `services/scanner/internal/reportworker/worker.go:99-110` hardcodes
  `SummaryCount` to all-zeros and never populates `Findings`; the
  comment claims a metadata gRPC client "is wired in main" but the
  worker has no metadata client field at all, and
  `internal/report/report.go:31-34` admits the renderer "fabricates a
  minimal set from the tenant counts so the output isn't empty." A
  compliance artifact that is silently wrong is worse than no artifact.
- **What:** add the metadata gRPC client to the report worker, fetch
  per-digest scan findings + tenant summary counts, populate
  `SummaryCount`/`Findings` for both SPDX and PDF renderers; unit test
  asserting non-zero findings propagate end-to-end. (The separate
  local-disk / plaintext-PDF persistence deferral stays tracked via
  `docs/SERVICES.md` §624 `TODO(prod)` — object storage + signed URLs.)

#### FUT-081 — Missing event publishers: deletions + webhook outcomes never reach the audit trail — **Tier 1 (audit completeness)**
- **Why:** five routing keys documented as live in `docs/EVENTS.md`
  have **no publisher anywhere**: `manifest.deleted` / `tag.deleted`
  (core publishes only `push.completed` + `pull.image` — deletions are
  invisible to the audit trail and deletion-subscribed webhooks never
  fire), `webhook.queued` / `webhook.delivered` / `webhook.failed`
  (`services/webhook/internal` contains zero Publish calls despite an
  audit `mapEvent` case at `consumer.go:403`), and `push.failed` (a
  subscribable notification event — `notifications.go:46,85,346` — that
  can never fire).
- **What:** publish `manifest.deleted`/`tag.deleted` from core's delete
  handlers, delivery-outcome events from the webhook dispatcher, and
  `push.failed` from core's push error paths (or descope keys we decide
  not to emit and annotate `// audit: skip` + fix EVENTS.md). Also:
  remove the dead `tenant.domain.verified` consumer
  (`eventconsumer/consumer.go:851` — custom domains removed, RM-001),
  type the `auth.provider_*` events in `libs/rabbitmq/events` (EVENTS.md
  acknowledged follow-up), and verify the dashboard "pulls" analytics
  series actually populates post-FE-API-042 — `analytics-card.tsx:143`
  still says "pull events are not yet emitted by the audit consumer"
  and the sparkline renders permanently zero.

#### FUT-082 — registry-mcp repair: broken endpoints + missing CI lane — **Tier 1 (correctness)**
- **Why:** roughly **half the 12 MCP tools fail at runtime** —
  `services/mcp/internal/client/registry.go` calls
  `.../manifests/{tag}` (BFF path is `.../tags/{tag}/manifest`),
  `GET /api/v1/audit` (no such route), `.../scans/{digest}` +
  `.../signatures/{digest}` (BFF uses `.../tags/{tag}/scan|signature`
  and `/api/v1/scan-by-digest|signatures-by-digest/{digest}`), and
  tenant-wide `/api/v1/promotions` (BFF only has per-repo). Plus
  `/api/v1/service-accounts` is auth-owned, so it 404s unless
  `MCP_MANAGEMENT_URL` points at a path-splitting gateway. This shipped
  because **mcp is the only service with no CI workflow** (13 of 14
  have `ci-<svc>.yml`).
- **What:** fix the client paths; add `ci-mcp.yml`; add a contract
  test validating every MCP client path against the generated
  `docs/openapi.json` (exists since FUT-076 slice 6 — cheap and
  never-drifting) or an integration smoke against a live BFF. Feeds
  the FUT-055 positioning decision.

#### FUT-083 — Helm deploy can't serve the dashboard: management chart + `/api/v1` routing contract — **Tier 1 (for Helm deployers)**
- **Why:** two compounding deploy-blockers: (1)
  `infra/helm/registry/charts/` has no `registry-management` subchart
  despite `docs/DEPLOYMENT.md:59` listing it — a Helm install deploys
  no BFF at all; (2)
  `charts/gateway/templates/ingressroutes.yaml:47-56` routes **all**
  `/api/v1/*` to registry-auth, so every BFF-owned FE call would 404
  even with the chart present. The auth-vs-BFF path split (login,
  apikeys, users, service-accounts, access → auth :8080; everything
  else → BFF :8091, with per-path overrides) is encoded **only in
  `frontend/vite.config.ts:48-84`** — no production artifact
  implements it, and no compose label was found either.
- **What:** add the `registry-management` subchart; implement the path
  split in the gateway IngressRoutes; document the routing contract in
  one canonical place (`docs/deployment-guide.md`) and make
  compose/Helm both implement it; verify the compose path split.

#### FUT-084 — Wire SSO login in the frontend (backend is done) — **Tier 1 (dead feature)**
- **Why:** the complete OAuth PKCE + SAML dance exists server-side —
  `GET /api/v1/auth/providers`, `/auth/oauth/{id}/start` + callback
  (302 to `{next}?sso_token=<jwt>`), `/auth/saml/{id}/start` + ACS —
  but `sso-buttons.tsx` renders all four provider buttons permanently
  `disabled` ("coming soon"), grep for `sso_token` across
  `frontend/src` → **0 hits**, and the login page never fetches
  `/auth/providers`. SSO is unusable end-to-end purely because of the
  frontend; the component comment even falsely claims the backend is
  "not yet implemented." Biggest feature-for-effort win in the audit.
- **What:** fetch providers on the login page, enable/render buttons
  per configured provider, link to `/auth/oauth/{id}/start`, consume
  `sso_token` on first paint into the auth store (FE-API-034 contract),
  delete the stale comment; tests for the token handoff.

#### FUT-085 — CI enforcement gap: integration tests, coverage gate, gosec, release pipeline — **Tier 2**
- **Why:** `docs/TESTING.md` claims an 80% coverage gate "enforced in
  CI", integration tests, gosec on every PR, and weekly ZAP — none
  exist across the 22 workflow files. **53 files tagged
  `//go:build integration` never run in CI** (DB-migration regressions
  invisible); no workflow computes coverage; nothing fires on tags —
  `docs/CI-CD.md` stages 7–8 (publish image on semver tag, helm
  upgrade staging) have no workflow, so the v2.0.0 tag triggered
  nothing. Conformance is also path-filtered, not "every PR" as
  claimed. (govulncheck blocking sweep, gitleaks, proto `breaking`,
  and per-service Trivy scans are all real.)
- **What:** (a) integration-test job (testcontainers; nightly if too
  slow for PR lanes); (b) coverage computation + threshold, or correct
  TESTING.md to measured reality (pairs with FUT-075); (c) gosec job;
  (d) `release.yml` on semver tags per CI-CD.md. Candidates to absorb
  into the REM-020 reshape backlog as new numbered sub-items.

#### FUT-086 — Frontend e2e suite (Playwright) — **Tier 2**
- **Why:** 57 vitest unit/component files but **zero e2e** — no
  playwright/cypress anywhere in `frontend/`. Login/SSO and the
  push→scan→sign flows have no end-to-end coverage; QA-020 covers unit
  coverage, not this. The FUT-076 screenshot pipeline
  (`tools/screenshots/`) already drives the live stack via committed
  Playwright — reuse it as the harness seed.
- **What:** Playwright smoke pack against the compose stack: login
  (incl. SSO once FUT-084 lands), repo browse → tag detail → trigger
  scan, sign + verify badge, webhook delivery inspect. Wire into CI
  behind a nightly or compose-gated lane.

#### FUT-087 — API surface reconciliation: dead RPCs, unconsumed routes, vestigial gateway — **Tier 3**
- **Why:** the matrix audit found implemented-but-orphaned surface
  beyond the FUT-058/059/061/062 pointers above: `scanner.TriggerScan`
  / `GetScanStatus` (manual scans go via RabbitMQ `scan.queued`
  instead) and `scanner.GetRepoScanPolicy` have zero callers;
  `metadata.DeleteOrganization` has no caller (orgs can be claimed but
  never deleted via API); BFF `GET
  /api/v1/repositories/{org}/{repo}/activity` has no FE consumer
  (candidate per-repo Activity tab); the Go `services/gateway` module
  is a hollow shell (health + metrics only — Traefik is the real
  gateway) yet ships a Dockerfile + CI lane; `services/storage`
  `.env.example` documents `STORAGE_S3_*`/`STORAGE_GCS_*`/
  `STORAGE_AZURE_*` vars that no code reads.
- **What:** per item, decide wire-or-remove: give keepers a caller
  (org delete route, repo Activity tab, scan-trigger RPC vs queue —
  pick one path); mark removals deprecated in proto comments (field
  numbers stay reserved per §12); either delete the gateway Go module
  or document it as intentional scaffold; trim the dead storage env
  vars until drivers exist.

#### FUT-088 — UI paper-cuts batch (2026-07-12 audit) — **Tier 3**
- **API-key activity time chips are fake** —
  `api-keys.activity.tsx:31-40` uses `limit` as a proxy for 24h/7d/30d;
  needs real `from`/`to` params on the backend + FE swap.
- **Access-activity pagination not wired** — `ActivityTable.tsx`
  exposes `next_page_token` but never fetches page 2.
- **Hardcoded retention grace window** — `tags-panel.tsx:571-574` pins
  `GRACE_DAYS = 7` client-side; BFF should return the platform grace
  setting so the pending-delete ETA can't drift.
- **Onboarding replay link from Settings** — Phase 4.3 §3 leftover;
  the first-run redirect shipped, the Settings link never did.
- **Org-wide bulk scan trigger** — `useBulkScanOrg`
  (`lib/api/scan.ts:171`) + the BFF route exist; no UI imports it.
  (Repo-level bulk scan IS wired.)
- **MCP connect card** in Settings › Integrations — zero in-app MCP
  presence; the natural home for a connect-an-AI-agent setup snippet.
- **Comment rot + unused hooks sweep** — `sso-buttons.tsx:7-8` stale
  "backend not implemented" (dies with FUT-084);
  `api-keys.service-accounts.tsx:18-20` TODO says the T26 drawer is
  unmounted but it's mounted at lines 98-103; audit unused exports
  (`useAbility`, `useActiveAdapter`, `useReport`, single-upstream
  proxy-policy hooks, `useUpdateOIDCTrust`).

#### FUT-089 — Doc truth-up batch: docs claim X, code does Y — **Tier 3**
- **CLAUDE.md §8** claims 5 storage drivers; 2 exist (minio,
  filesystem) — `docs/integrations/storage.md` is already honest.
- **CLAUDE.md §3 diagram** shows `gc ──gc.run──► storage` via
  RabbitMQ; storage has no RabbitMQ consumer — GC deletes via gRPC.
- **CLAUDE.md §7** phrases peer-CN matching as "enforced"; it's opt-in
  and an empty allowlist only warns in production (SEC-044 deliberate)
  — align wording or flip to fail-closed.
- **README / ADR-0006** "Cosign + Notary v2" — Notary v2 has zero code
  (folds the existing doc-inventory P3 item).
- **DEPLOYMENT.md** lists `docker-compose.test.yml` (doesn't exist)
  and the `registry-management` chart (see FUT-083).
- **TESTING.md** claims covered by FUT-075/FUT-085; also claims
  `.mockery.yaml` config that exists nowhere.
- **HARDENING-CHECKLIST.md** — all 25 boxes unchecked, no per-service
  compliance record; also its "Dependabot or Renovate configured" box
  is unmet (no config in `.github/`).
- **security.md SEC-087** status line vs status.md PR #321 — verify +
  flip (see preamble).

---

## How to use this file

**Tiering criteria:**

| Tier | Criterion |
|---|---|
| **1** | A customer would not deploy this to production without it. |
| **2** | An operator's day gets noticeably harder without it once they're past first-push. |
| **3** | "Wouldn't it be cool if…" — real value, easy to defer. |

**Promotion workflow:**

1. When someone picks up an item, assign it an FE-API or REM number
   (whichever fits) and move the entry into
   [`status-tracker.md`](status-tracker.md) (backend, while in flight)
   or [`FE-STATUS.md`](FE-STATUS.md) (UI).
2. Strike it from this file. The audit trail lives in `status.md`
   once the work completes (see [`status-tracker.md`](status-tracker.md)
   for the tracker → done workflow).
3. Keep this file under ~400 lines. When it grows past that, the
   tiering has lost its meaning; split or prune.

**When to add an item here:**

- During a gap audit or post-mortem, where it's obvious something is
  missing but nobody has time today.
- After a customer interview surfaces a hard "we won't buy without it"
  requirement.
- When you find a sidebar / route / surface that promises something
  the backend doesn't yet support (so the dead-link doesn't get
  forgotten).

**Don't add items here:**

- Bug fixes — those are commits, not futures.
- Items already tracked elsewhere — link to
  [`status-tracker.md`](status-tracker.md) (in flight),
  [`status.md`](status.md) (completed), or
  [`FE-STATUS.md`](FE-STATUS.md) (UI) instead.
- Speculation without a clear user need — leave a comment in
  conversation, don't pollute the backlog.
