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
- **What:**
  - TOTP enrolment with QR code + 8 backup codes (services/auth migration
    + new gRPC + `/users/me/mfa` BFF routes + enrolment dialog on `/profile`).
  - Optional WebAuthn / hardware key support (deferrable; TOTP unblocks
    most enterprise procurement).
  - Active session list on `/profile` — device label, IP, last active —
    with per-row revoke button. Backs onto `auth_login_sessions`
    (REM-002 already tracks the table).
  - Workspace policy toggle: "require MFA for all members" — gates token
    issuance at the auth service.
- **Affects:** `services/auth`, `services/management`, `frontend`.

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

### 5. SCIM provisioning
- **Why:** Manual user lifecycle doesn't scale past ~50-user customers.
  Okta / Azure AD admins expect to add a user to "engineering" and have
  the registry give them the right tenant + role automatically.
- **What:**
  - SCIM v2 endpoints on services/auth (`/scim/v2/Users`,
    `/scim/v2/Groups`).
  - Mapping: IdP group → tenant + role (e.g. `eng-admin@acme.okta` →
    `tenant=acme, role=admin`).
  - Workspace admin UI: SCIM token issuance + mapping editor.
- **Affects:** `services/auth`, `services/management`, `frontend`.
- **Depends on:** `FUT-012` (tenant-user lifecycle management) — SCIM
  is the automated source-of-truth layered on top of the same
  invite / disable / list machinery. Build the manual surface first.

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

### 4. Image lineage / provenance surface
- **Why:** A real deploy traces back to "this manifest was built from
  this commit by this CI run." OCI annotations carry it; we don't
  surface it.
- **What:** Parse `org.opencontainers.image.*` annotations on push (we
  already store the manifest JSON) and surface as a "Provenance" panel
  on the tag detail page: git commit, source URL, build URL, vendor.

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

## Tier 2 — Access: machine identity & policy

> All four items below have preview UI surfaces already shipped (FE-API-048 T24+)
> with dummy data. Backend work is what remains.

### FUT-001: Federated workload identity (OIDC trust) — Sprint 11
- **Why:** GitHub Actions, GKE Workload Identity, and similar OIDC-capable
  CI systems can authenticate without a stored secret at all. A trust
  relationship removes the "rotation reminder" problem entirely.
- **What:** New `oidc_trust_configs` table on services/auth; admin UI on the
  `/api-keys` Trust tab (preview surface already shipped). services/auth adds
  a `POST /auth/token/workload` exchange endpoint: validates the OIDC
  assertion against the configured JWKS URL + audience, issues a short-lived
  JWT mapped to a service account.

### FUT-002: Credential helpers (docker login / k8s YAML / terraform / GHA snippets) — Sprint 11
- **Why:** Operators copy-paste credentials into CI configs and get them wrong.
  Auto-generated, copy-ready snippets reduce support burden.
- **What:** `/api-keys` Helpers tab (preview surface already shipped) renders
  per-format snippets: `docker login` command, Kubernetes imagePullSecret YAML,
  Terraform `docker_registry_image` block, GitHub Actions step. All snippets
  reference the workspace's actual registry hostname and the selected service
  account. No new backend RPCs needed — purely frontend rendering against
  existing `/api/v1/workspace/me` data.

### FUT-003: Token policies (max-TTL, force-rotation, idle-revoke) — Sprint 12
- **Why:** Long-lived keys with no rotation policy are the #1 lateral-movement
  vector after a breach. Operators want guardrails at the workspace level.
- **What:** New `token_policies` table on services/auth keyed by tenant + scope
  (service account or all accounts). Fields: `max_ttl_days`, `rotation_interval_days`,
  `idle_revoke_days`. `/api-keys` Policies tab (preview surface already shipped).
  Enforcement: key creation rejects TTL beyond `max_ttl_days`; a background job
  (pattern: `FOR UPDATE SKIP LOCKED`) revokes keys exceeding rotation or idle
  thresholds and publishes `auth.key_revoked` audit events.

### FUT-004: Access review (quarterly stale-key nudge) — Sprint 12
- **Why:** Without a periodic review prompt, stale keys accumulate silently.
  Security auditors expect to see evidence that access is re-certified.
- **What:** Scheduled job emits `auth.access_review_due` audit events (and
  webhook deliveries) once per configured interval (default 90 days) listing
  keys not used in that window. `/api-keys` Review tab (preview surface already
  shipped) surfaces the list with bulk-revoke action. Platform admin can
  configure the interval per tenant via a new settings field.

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

### FUT-007-FE: Domain re-poll reset action — ~1h
- **Why:** After 48h of failed DNS TXT verification the worker gives
  up on a `tenant_domains` row. Operator's only recourse today is
  delete + re-register. A small "Re-arm polling" button on the row
  (or auto-reset on Verify Now success) closes that cliff without
  forcing a re-register cycle.
- **Scope:** repo method to clear the 48h notify timestamps + reset
  `next_poll_after`, BFF route, FE button on the domain row.
- **Affects:** `services/tenant`, `services/management`, `frontend`.
- **Surfaced:** 2026-06-23 custom-domain documentation pass.

### FUT-008: Sign dialog "Recent signer_ids" dropdown — ~1h
- **Why:** Today the Sign dialog asks for `signer_id` as a free-form
  string — operators type the label from memory. Mirror PR #36's
  trusted-key picker pattern: surface the distinct `signer_id` values
  this tenant has used recently in a dropdown alongside the existing
  input. Cheap UX patch.
- **Note:** Only worth doing if FUT-009 is going to wait more than
  one sprint. FUT-009 supersedes this entirely by replacing free-form
  `signer_id` strings with service-account principals.
- **Affects:** `services/management`, `frontend`.

### FUT-009: Service-account-as-signing-identity — ~5h
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

### DEPLOY-001: Self-hosted vs SaaS deployment-model docs — discussion + ~1 day
- **Why:** The platform is multi-tenant by design (every row has
  `tenant_id`; custom domains let a tenant white-label;
  platform-admin marker `(admin, org, *)` separates super-admin
  surfaces). But the operator-facing story isn't explicit. Same
  binary serves both:
  - **SaaS mode:** one provider runs the stack, many tenants
    subscribe. Provider holds platform-admin marker;
    `/admin/tenants` is an active surface.
  - **Self-hosted mode:** one company runs the stack for
    themselves. They're both platform admin AND the only tenant.
    Same code, degenerate multi-tenant case.
- **Surfaced gaps to address:**
  - The dev `admin` user holds BOTH tenant-admin role AND the
    platform-admin marker — testing UI conflates the two views.
  - No documented "tenant persona" testing path (create a
    non-admin user, log in, confirm `/admin/*` routes are 404).
  - No `docs/DEPLOYMENT-MODELS.md` covering the SaaS-vs-self-hosted
    distinction + which knobs differ + how to onboard a fresh
    tenant.
  - Likely follow-ups once tested: tenant self-signup flow (Tier 3
    already lists this), team-invite flow, per-tenant theming,
    plan-tier feature gating (`tenants.plan` column exists but
    nothing reads it).
- **Scope:** ~30min to seed a tenant-only user (no platform-admin
  marker) for testing. ~half-day to write `docs/DEPLOYMENT-MODELS.md`
  covering both modes + persona mapping + onboarding paths.
  Recommended before any external user trial.
- **Affects:** `services/auth` (seed migration for tenant-only user),
  new `docs/DEPLOYMENT-MODELS.md`, possibly small README updates.

### FUT-010: RBAC + FE-RBAC polish pass — ~1 sprint
- **Why:** DEPLOY-001's tenant-persona testing will surface a class of
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
    - `/workspace/domains` + `/workspace/audit-export` settings
      groups hidden from sidebar for non-workspace-admin
    - `/admin/*` groups hidden from sidebar for non-platform-admin
    - Settings tab toggles (immutability, signed-image, trusted keys,
      scan policy, retention) read-only for writer/reader
    - Sign / Verify-now / Approve-key buttons disabled for
      writer/reader
    - Tag delete button disabled for reader; visible+enabled for
      writer/admin/owner
    - Webhook create/edit/delete/rotate gated on admin+
    - Member invite + role-grant gated on admin+
  - **Direct URL access** — when a non-admin types `/admin/tenants`
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
- **Surfaced:** 2026-06-23 tenant-persona testing (DEPLOY-001 setup +
  `tenant_only` writer-role test).

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
  insecure defaults are a footgun. **Effort:** S.
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
- **DSGN-021** — Custom-domain row-expand revealing TXT name + value + copy +
  "Check DNS now" + `next_poll_after` countdown. Today TXT challenge is only
  shown at registration; you can't re-display it for verification debugging.
  **Effort:** M.
- **DSGN-023** — Mobile / narrow-viewport sidebar fallback. Below 1024px the
  sidebar vanishes and Topbar has no nav control. **Effort:** M.
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
- **ARCH-010** — Wire `tenant.deleted` cascade across every service that holds
  `tenant_id` columns (auth/webhook/audit/proxy/scanner). Nightly orphan-row
  reconciliation. **Effort:** M.
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
  **Still open:** **DSGN-002** (sidebar IA — Workspace cluster split from Access),
  **DSGN-008** (topbar `<Breadcrumbs/>` from `useMatches`),
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
  counts + a "Create org" CTA. Pairs naturally with DEPLOY-001 tenant-
  persona docs.

**Effort:** half-day backend + half-day FE. ~1 day total.

**Affects:** `services/management` (new route), `services/auth`
(GrantRole already exists, no change needed), `frontend` (Create
Repository dialog + optional `/admin/orgs` route).

### REM-018 — UI user-ID → username + enforce display_name on user creation

**Surfaced:** 2026-06-24 during onboarding smoke testing. The dashboard
surfaces raw UUIDs in member lists, audit-event actor cells, grant-by
columns, and tenant-detail "created by" — none of which are human-
readable. Operators routinely need to cross-reference UUIDs against a
mental table of "who is that?" which is exactly the cognitive load the
dashboard should be removing.

Also: today `POST /api/v1/users` only requires `username` + `email` +
`password` (and treats `display_name` as optional). Operators creating
real accounts skip `display_name`, the surfaces above then show
`username` as a fallback (better than UUID, still inconsistent). The
right posture is to make a human-friendly `display_name` part of the
contract.

**Scope:**

Backend (services/management):
- Extend `MemberResponse` shape with `username` + `display_name`. Add a
  `users` LEFT JOIN in the member-list handlers so the FE doesn't have
  to chase per-row hydrate calls.
- Same change for the audit activity feed (`actor_username`,
  `actor_display_name`) and any other list endpoint that today returns
  raw `user_id` (admin tenants `created_by`, role-assignment lists).
- Optional: small batch endpoint `GET /api/v1/users/by-id?ids=...` that
  the FE can call to hydrate stray UUIDs the list endpoints don't cover.

Backend (services/auth):
- `POST /api/v1/users` validates `display_name` is non-empty (1-64
  chars, allowlist). Same `validateUserName` regex as `username`.
- Update `userResponse` to include `display_name` on every shape.

Frontend:
- New primitive `<UserCell user_id={id} username={u} display_name={n}>`
  that renders `<display_name> (@<username>)` with the UUID in a tooltip
  for power-users. Falls back to `<username>` if no display_name, to
  `<short-uuid>` if neither.
- Replace UUID renders across the dashboard: `/orgs/{org}/members`,
  `/repositories/{org}/{repo}/members`, `/activity`, audit-event rows,
  tenant detail "created by", granted-by columns.
- User-create form requires non-empty `display_name`. Existing form
  has the field; make zod validation enforce.

**Affects:** `services/management`, `services/auth`, `frontend/`.

**Effort:** ~1-2 days.

### FUT-011 — Production new-user onboarding smoke test

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

Pairs with **DEPLOY-001** tenant-persona doc work. Once both are
done, self-hosters following the docs should be able to bootstrap a
real multi-user setup without SQL.

### FUT-012 — Tenant-user lifecycle management (invite / list / disable)

**Surfaced:** 2026-06-24 during the design discussion that followed
REM-017. The chicken-egg fix gave platform admins a way to claim a
new org, but there is still no first-class surface for managing the
people *inside* a tenant. Today the only routes are
`/orgs/{org}/members` (per-org workspace permissions, can grant /
revoke a role on one org) and `/admin/tenants` (platform-admin only,
tenant CRUD). The in-between layer — "tenant admin manages the
users in their tenant" — does not exist.

**Why this matters:** the current model leaves tenant admins
without a way to invite a new colleague, see the full member list
across all orgs, or deactivate a departing employee. The only
working paths are (a) `POST /api/v1/users` (creates a user, but no
list view to see who exists; no invite email; no role assigned),
and (b) raw SQL — exactly the surface SELF-HOSTING.md tells operators
they should never need. Real customers will expect a "Users" page
the same shape they have in GitHub / GitLab / AWS IAM.

**Recommended design** (locked after the REM-017 discussion):

1. **New RBAC scope: `'tenant'`** alongside `'org'` and `'repo'` in
   `role_assignments.scope_type`. A tenant-admin is
   `(role=admin, scope_type=tenant, scope_value=<tenant_id>)`.
   `services/auth.CheckAccess` already does scoped-role lookup, so
   this generalises cleanly. The platform-admin marker
   (`(admin, org, '*')`) remains a separate, higher-privilege scope
   and trumps tenant-admin.
   - **Open question** (resolve before building): does tenant-admin
     implicitly get `admin` on every org in the tenant, or stay
     strictly user-lifecycle? Lean toward strict (tighter blast
     radius, mirrors AWS root-vs-IAM-admin). Alternative is
     friendlier for small tenants — needs a call.

2. **Three new gRPC RPCs on `services/auth`** (gated on
   tenant-admin OR platform-admin):
   - `ListTenantUsers(tenant_id, page_token)` — paginated. Returns
     `user_id`, `username`, `display_name`, `email`, `kind` (human
     or service_account), `disabled`, `last_login_at`, and a
     role-summary chip (count of org-admin / writer / reader rows).
     Pairs with REM-018 — the same shape feeds the username-cell
     primitive.
   - `InviteUser(tenant_id, email, display_name, initial_org_role?)`
     — creates a `users` row in `invited` state. Generates a
     single-use invite token; emits an invite event for the email
     transport (Phase 1: log + copy-link affordance; Phase 2:
     real SMTP).
   - `SetUserDisabled(user_id, disabled bool)` — flips
     `users.disabled`. On disable, revoke every active JWT JTI in
     Redis + mark all API keys disabled (don't delete — preserves
     audit + reversibility).

3. **Single FE route `/tenant/users`** visible to both audiences:
   - Tenant-admin → scoped to their own tenant automatically (JWT
     carries `tenant_id`).
   - Platform-admin → same route with a tenant selector at the top.
     Reachable from `TenantDetailDrawer` via a "View users →" link
     (good attach point that drawer already has empty space for).
   - Table columns: User (uses REM-018's `<UserCell>`), kind chip
     (human / service_account), role summary across orgs, status
     (active / disabled / invited), last login, actions menu.
   - Actions: Invite user (modal — email + display_name + optional
     initial role), Deactivate / Reactivate, Resend invite (for
     pending state).

4. **Migrations:**
   - `services/auth/migrations/NNNNNNNN_add_tenant_scope.sql` —
     widen the `scope_type` CHECK constraint to include `'tenant'`,
     no data backfill (no existing tenant-scope rows).
   - `services/auth/migrations/NNNNNNNN_add_users_invite.sql` —
     `users.status` enum (`active` | `invited` | `disabled`) +
     `invite_token_hash` + `invite_expires_at`. Migrate
     `disabled BOOLEAN` → `status` (`disabled=true → status='disabled'`;
     `false → 'active'`).

**Why this shape:**

- Reuses `role_assignments` for tenant-admin instead of inventing a
  parallel `tenant_admins` table — same migration shape as adding
  `'org'` originally. Decision log entry slot: parallels #22
  (service-account principal pattern) in the "extend existing model"
  philosophy.
- One UI route serves both audiences (cuts maintenance + matches
  how `/admin/tenants` already gates behaviour on platform-admin
  detection).
- Disable rather than delete — preserves audit trail; deactivation
  is reversible. Hard-delete stays a platform-admin operation via
  the existing tenant DB cleanup path.

**Affects:** `services/auth` (migrations + 3 new RPCs + scope
generalisation), `services/management` (3 new REST routes wrapping
the RPCs), `frontend/` (new route + components + nav entry).

**Dependencies:**
- Pairs with REM-018 — the same `username` / `display_name`
  hydration this needs is what REM-018 builds. Ship REM-018 first
  (or fold its scope into this work).
- Strictly weaker than Tier 1 #5 (SCIM provisioning) — but SCIM
  needs the manual surface to exist first (it's the same data
  model + endpoints with an automated source-of-truth on top).
  Build this, then layer SCIM on the same `users.status` + invite
  machinery.

**Effort:** ~1 sprint. Backend ~2-3 days (migrations + 3 RPCs +
auth-scope generalisation + tests); BFF ~half day (route wrappers);
FE ~2-3 days (route + table + 3 dialogs + nav surface + RBAC
guards); docs ~half day.

### FUT-014 — Proxy publishes `pull.image` events on every cache pull

**Surfaced:** 2026-06-24 smoke testing FUT-013 Phase A. Two
distinct symptoms, one underlying gap:

1. `proxy_manifests.pull_count` stayed at 0 after `docker rmi`
   + `docker pull` (HEAD-only flow when the digest matches).
2. The dashboard's 24h pulls card on `/` doesn't count proxy
   traffic at all — it reads from `audit_events`, which is
   only populated by `services/core` via the existing FE-API-042
   `push.image`/`pull.image` event pipeline. The proxy publishes
   `store.queued` (retry-only) but never publishes `pull.image`.

**Root cause:** the proxy was wired for "tell me a thing got
cached" only. Operator-facing analytics + the per-row counter
both want "tell me an image got SERVED to a client" — that's
a different event and the proxy doesn't emit it.

**Locked design (revised 2026-06-24 after the operator request):**

Instead of a local-only fix in `proxy_manifests.pull_count`,
make `services/proxy` a first-class publisher in the existing
audit pipeline:

- New event `pull.image` emitted by the proxy on every
  successful client-served manifest response, regardless of
  cache hit/miss/HEAD. Same routing key, same payload shape
  as `services/core`'s existing `pull.image` event so the
  audit consumer doesn't branch on origin.
- HEAD requests count as pulls. Docker's HEAD-then-skip-GET is
  the dominant traffic shape against cached refs; pretending
  it isn't a pull means undercounting >50% of real traffic.
  Payload includes `via: "proxy"` so analytics CAN distinguish
  cache vs owned-push pulls if a future card wants to.
- The existing `proxy_manifests.pull_count` column can either:
  - (a) stay as a fast-path counter, updated alongside the
    event publish — keeps the cache page fast without a join
    against `audit_events`; OR
  - (b) be derived from `audit_events` at read time and
    drop the column entirely (one source of truth).
  - Recommend (a) — keeps the cache page snappy at scale.

**Why this matters:**

- Cache page's "Pulls" column starts showing real numbers.
- Dashboard 24h pulls card automatically includes cache pulls
  the moment this lands — no new dashboard wiring required,
  because the `audit_events` table is what `GetAnalytics` reads
  from. (The user's "change dashboard pulls/24h card" request
  becomes a zero-line FE change.)
- `services/audit`'s per-tenant activity feed picks up proxy
  pulls. `webhook` subscribers on `pull.image` start firing
  on cache pulls.

**Affects:** `services/proxy` (event publish on serve path),
`services/audit` (no change — eventconsumer already handles
`pull.image`), `services/webhook` (no change — already
allowlisted). No FE change.

**Effort:** ~1 day. Wire publisher into `handleGetManifest` +
`handleHeadManifest` cache-hit + cache-miss paths. Plumb
RABBITMQ_URL in proxy (currently optional — warns when unset).
Two regression tests + smoke verification against the
dashboard 24h card.

### FUT-015 — Pull-command + tag/digest row expander on `/workspace/proxy-cache`

**Surfaced:** 2026-06-24 by the operator testing FUT-013 Phase C.
Each table row shows the cached image but doesn't tell the
operator HOW to pull it. They have to construct the
`localhost:8084/cache/<upstream>/<image>:<tag>` URI by hand.

**Scope:** add a chevron-expand to each row (same pattern
DSGN-021 used for custom-domain TXT records). When expanded:

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

### FUT-017 — Scan + sign on cached images for the proxy cache

**Surfaced:** same testing session. The operator question:
"can I see CVEs in `library/alpine:3.20` even though I didn't
push it? And can I sign it locally so my admission policy
gates pulls of it the same way it gates pulls of my own pushes?"

**Decisions (locked 2026-06-24, both Answer A):**

1. **Cached images SHOULD scan.** The value-add of a private
   proxy IS the "private supply chain" angle. CVEs in upstream
   public images matter as much as CVEs in your own pushes.
2. **Cached images SHOULD participate in signing.** If you
   want to sign upstream content locally (so your existing
   `require_signature` admission policy gates cache pulls
   too), the cache should expose the same signing surface as
   `/repositories/$org/$repo/tags/$tag`. Upstream-signed
   images surface the upstream signature read-only;
   locally-signed cached images surface a "signed by us"
   badge.

These two were originally split between FUT-017 (scan) and a
follow-up. Bundled here because they share the detail-page
tab plumbing from FUT-016 + the same per-upstream policy
shape — splitting them would create twice the migrations and
two near-identical FE Policy editors.

**Scope:**

Backend (scan path):
- `services/proxy` publishes a new `cache.populated` event
  after a successful `cacheManifest` upsert.
- `services/scanner.eventconsumer` subscribes to the new key
  and treats cached manifests as a fourth scannable surface
  (alongside repos, tags, manifests). The scan operates on
  the layer blobs in `services/storage` exactly the same way
  it does for owned pushes — the staging dir doesn't care
  whether the blobs came from upstream.
- New scan-policy scope: `(scope_type='proxy_cache',
  scope_value=<upstream_name>)`. Per-upstream granularity is
  the v1; per-image refinement can land later if asked.
- Findings stored in the existing `scan_results` shape,
  joined on `(tenant_id, manifest_digest)` — proxy cache +
  owned repo scans land in the same table.

Backend (signing path):
- `services/signer.ListSignatures(manifest_digest)` already
  works for any digest — no signer schema change. Cache
  pulls land on the signed-image admission path through
  `services/core` when `require_signature` is on.
- Per-upstream "auto-sign on cache" policy: when set, the
  proxy emits a `sign.requested` event after `cacheManifest`
  upsert; signer consumes + signs with the workspace's
  default key. Same pattern services/core uses for push-time
  auto-sign.
- Phase 2: read-surface upstream signatures (e.g. Sigstore
  on Docker Hub) on the detail page as read-only. Optional
  follow-up — phase 1 ships local-sign + verify against the
  existing trusted-key allowlist.

Frontend (FUT-016 detail page extension):
- **Scans tab** — same component shape as the per-repo
  tag-detail ScanPanel. Reuses `useScanByDigest()`.
- **Signing tab** — same component shape as the existing
  `RepoTrustedKeysSection` + `Sign with key` dialog. Reuses
  the existing `useSignManifest()` + `useListSignatures()`
  hooks.
- **Cache page header** — per-upstream policy editor card
  with two toggles + severity selector:
  - `auto-scan cached images: yes / no` + severity threshold
  - `auto-sign cached images: yes / no` + key selector
- **Severity column** on the cache table row (badge with
  critical/high count) once a scan has landed.
- **Signed badge** on the cache table row when at least
  one signature exists.

**Affects:** `proto/proxy/v1` + `proto/scanner/v1` +
`proto/signer/v1`, `services/proxy` (publish events),
`services/scanner` (subscribe + scan + persist),
`services/signer` (subscribe + auto-sign when policy set),
`services/management` (policy CRUD + scan read + signature
read), `frontend/` (2 tabs on detail page + 2 toggles on
header + 2 columns on table).

**Effort:** ~1 sprint. The scanner + signer pipelines both
already accept arbitrary manifest digests; the wiring is
the work. Phase 1 (scan + local-sign + verify) is the bulk;
phase 2 (read-surface upstream signatures) is a follow-up.

**Dependencies:** FUT-016 must land first — the Scans tab +
Signing tab + Severity/Signed columns all live on the
detail page route + table that FUT-016 introduces. Queued
until FUT-016 ships.

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

## Tier 3 — Nice-to-have polish

Real value, but easy to defer.

- **Native CLI** — `oci-janus` Go binary for bulk ops (scripted
  promotions, backup, repo lifecycle). Docker CLI only goes so far.
- **Onboarding wizard** — first-push tutorial, first-scan walkthrough
  on a freshly-provisioned tenant.
- **Tenant self-signup flow** — public registration with email verify,
  trial plan limits, auto-expiry.
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
- **Trial / sandbox tenants** — auto-expiry, sandbox limits.

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
