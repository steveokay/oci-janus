# futures.md — Prioritized Backlog

> Items that are **not yet started** and **not yet bucketed into a sprint or
> FE-API number**. As an item gets picked up it moves out of this file and
> into `status.md` (backend) or `FE-STATUS.md` (frontend) with an
> appropriate FE-API or REM identifier.
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
- **Phase 3 (deferred):** multi-key quorum ("require ≥N distinct
  approved key_ids"), automated rotation/expiry, Cosign keyless
  Fulcio identity binding. Not on a sprint yet.
- **Affects (shipped):** `services/metadata`, `services/core`,
  `services/management`, `frontend`.

### 4. Audit log streaming to SIEM
- **Why:** Enterprise procurement asks for syslog/CEF export on day one.
  Customers want every push, pull, role grant, signed scan in Splunk /
  Datadog / Elastic for their own retention + correlation.
- **What:**
  - services/audit grows an outbound exporter: syslog (RFC5424), CEF,
    or generic webhook with HMAC.
  - Per-tenant config: target URL, format, optional filters (event
    types to include).
  - Workspace settings page surfaces the config + a "Send test event"
    button.
- **Affects:** `services/audit`, `services/management`, `frontend`.

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
   (whichever fits) and move the entry into `status.md` (backend) or
   `FE-STATUS.md` (UI).
2. Strike it from this file (or move to a "Done" section if you want
   the audit trail — your call).
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
- Items already tracked elsewhere — link to `status.md` or
  `FE-STATUS.md` instead.
- Speculation without a clear user need — leave a comment in
  conversation, don't pollute the backlog.
