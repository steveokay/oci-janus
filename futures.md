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

### 3. Admission policy — signed-image enforcement
- **Why:** Signing exists (REM-011 + FE-API-003/025/026) but nothing
  gates a pull on signature presence. A repo can be "signing required"
  in policy and still serve unsigned images.
- **What:**
  - New `repositories.require_signature BOOLEAN` flag.
  - services/core consults services/signer on every manifest GET — if
    the repo requires signatures and the manifest isn't signed by any
    trusted key, return `403 UNAUTHORIZED` with a clear error.
  - Per-repo "trusted signer key" list (PR also touches services/signer
    to surface key IDs).
  - Policy editor in the repo settings page (which also needs to ship —
    see Tier 2 #2).
- **Affects:** `services/core`, `services/signer`, `services/metadata`,
  `frontend`.

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

### 5. Pull bandwidth quota + per-tag pull stats
- **Why:** Docker Hub charges by pulls; we only meter storage. Operators
  want to see "alpine:3.20 was pulled 12k times last week" both for
  popularity and for spend forecasting.
- **What:** services/audit already consumes `push.image`; add
  `pull.image` (currently noted as a known gap in
  `docs/SCANNER.md` + `status.md`). Aggregate per-tag in a materialised
  view; expose via the existing analytics endpoints.

### 6. Service-account API keys
- **Why:** Today every API key is tied to a human (issued from their
  `/profile`). When the human leaves, the key still works until
  someone notices. Real CI bots want a workspace-owned identity.
- **What:**
  - `service_accounts` table on services/auth — tenant-scoped, with a
    name, plan-allowed scopes, last-used-at.
  - API keys can be issued against a service account OR a user.
  - Workspace admin UI on `/api-keys` (the new route this commit
    shipped) — list of accounts, issue/revoke keys per account.

### 7. API key scopes
- **Why:** Today an API key inherits the issuing user's full grants.
  A CI bot that only needs to push to `staging/*` shouldn't be a full
  account takeover risk.
- **What:** Per-key scope strings — `pull:org/*`, `push:staging/*`,
  `admin:org/myteam`. Enforced in services/auth on every
  ValidateAPIKey call. Same dialog as creation; chips for permission
  picking.

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
- **`pull.image` event never published** — see Tier 2 #5 above; the
  analytics endpoint returns flat-zero for `?metric=pulls` until this
  ships.
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
