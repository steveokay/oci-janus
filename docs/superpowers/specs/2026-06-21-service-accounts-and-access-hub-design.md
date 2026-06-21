# Service Accounts + `/api-keys` Hub — Design

**Status:** APPROVED for implementation
**Sprint:** 10
**Tracker IDs:** FE-API-048 (service accounts + activity), FUT-001..FUT-004 (preview surfaces)
**Author/session:** 2026-06-21 brainstorm
**Affects:** `services/auth`, `services/management`, `frontend/`, `proto/auth/v1/auth.proto`

---

## 1. Problem

Every API key today is tied to a human user via
`api_keys.user_id NOT NULL REFERENCES users(id) ON DELETE CASCADE`. When the
human offboards, their keys silently survive until someone audits. Real CI
bots want a workspace-owned identity that doesn't die with a person.

A secondary problem surfaced during scoping: the `/api-keys` route (shipped
in Sprint 9 commit `317ee32`) is currently just `ApiKeysSection` reused
from `/profile`. It has no room to grow into the natural hub for machine
identity, activity, federation, and policy — all of which are real
near-term asks.

## 2. Out of scope (deferred to FUT-001..FUT-004)

Built as **preview surfaces** in the UI this sprint (real routes, dummy
data, visible NB telling the user which sprint the feature lands), but
not implemented end-to-end:

- **FUT-001** Federated workload identity (OIDC trust — GitHub Actions /
  GitLab / Buildkite present an OIDC JWT, services/auth verifies and
  issues a short-lived registry JWT).
- **FUT-002** Credential helpers (auto-generated snippets: `docker login`,
  k8s `imagePullSecret`, terraform provider, GHA `docker/login-action`).
- **FUT-003** Token policies (max-TTL, force-rotation cadence,
  idle-revoke — mirrors retention infra from FE-API-040..043).
- **FUT-004** Access review (quarterly "these N keys haven't been used in
  M days, revoke?" nudge).

A canonical scope vocabulary (replacing `api_keys.scopes TEXT[]` free-form)
is also deferred — `service_accounts.allowed_scopes` is the forward door,
but defining the dictionary touches every handler in services/core and is
its own sprint.

## 3. Design forks resolved

| Fork | Choice | Why |
|---|---|---|
| Owner column on `api_keys` | Nullable polymorphic (`user_id` XOR `service_account_id`, CHECK + partial unique indexes) | One hot-path table for `ValidateAPIKey`, one Redis cache key, lowest read-path churn. CHECK catches the only real footgun. |
| Scope vocabulary | Reuse existing `TEXT[]` free-form; constrain per-SA via `allowed_scopes TEXT[]` | Ships this sprint without locking us in. Canonical scope dictionary is its own sprint. |
| Principal model for RBAC/audit/RLS | Shadow user per service account (`users.kind='service_account'`) | Keeps the entire downstream platform (13 services, audit table, RBAC `role_assignments.user_id`, RLS `app.user_id`, JWT `Subject`, every middleware) unchanged. All divergence is bottled up in services/auth + a handful of "filter `kind='human'`" call sites. |
| Disable vs delete | Both. `PATCH … {disabled_at: now()}` is the default soft-disable button; `DELETE` cascades. | CI keys that worked yesterday and stopped today need an audit trail of *why*. Soft-disable preserves it; hard-delete is the explicit escape hatch. |

## 4. Data model — `services/auth`

### 4.1 `users` table — add `kind`

```sql
-- migration: 20260622000001_user_kind.sql
ALTER TABLE users
  ADD COLUMN kind TEXT NOT NULL DEFAULT 'human'
    CHECK (kind IN ('human', 'service_account'));
CREATE INDEX idx_users_tenant_kind ON users (tenant_id, kind);
```

Backfill is trivial — existing rows take the `'human'` default.

**Shadow user shape** (created automatically when a service account is
created):

- `kind = 'service_account'`
- `email = 'sa+' || sa.id || '@internal.invalid'` — unique per SA UUID
  and never deliverable. Satisfies the existing `UNIQUE (tenant_id, email)`
  constraint on `users` because SA UUIDs are globally unique. The
  `.invalid` TLD is reserved by RFC 2606, so this address is guaranteed
  never to resolve.
- `password_hash = ''` (empty string — the password verify path checks
  `kind='human'` before hashing and refuses anything else)
- `sso_provider_id = NULL`
- `created_at` mirrors the SA's `created_at`

**Filter strategy — push the `kind='human'` guard to the repository
layer, not the handler.** Single-row lookups (login, password reset,
SSO email-match in `services/auth/internal/service/sso.go:450,485`) are
*not* list endpoints and the original "filter all list surfaces"
framing missed them — an IdP returning `sa+<uuid>@internal.invalid`
would otherwise authenticate as the shadow user. Code review caught it.

New repository methods (all of which carry the guard internally):

- `repository.ListHumans(ctx, tenant_id, …)` — replaces `ListUsers` at
  every call site.
- `repository.GetHumanByEmail(ctx, tenant_id, email)` — replaces
  `GetByEmail` for login, password reset, **and** SSO email-match.
- `repository.GetHumanByID(ctx, user_id)` — for JWT-`Subject`
  introspection and `/users/me`.
- `repository.CountHumans(ctx, tenant_id)` — replaces `CountTenantUsers`.

The existing kind-agnostic methods are kept (renamed `…AnyKind`) and
used only inside the service-account code path itself, where the caller
*needs* to look up a shadow user by id.

Full call-site audit (per the review):

- `services/auth` — `ListUsers`, `CountTenantUsers`, login lookup,
  password reset lookup, SSO email-match path, JWT `Subject` resolution,
  RBAC `ListMembers` in `services/auth/internal/repository/rbac.go:111`
  (the role-grant surface must distinguish shadow-user assignments —
  see §5.5).
- `services/management` — `/orgs/{org}/members` user picker,
  `/admin/tenants` headcount, the BFF user-search autocomplete.

A grep for `SELECT … FROM users WHERE` in each service is enforced as
part of the PR checklist. A CI lint (`scripts/lint-user-queries.sh`) is
added that fails the build if a new `services/auth/internal/repository`
query against `users` doesn't use one of the `…Human…` helpers.

### 4.2 `service_accounts` table

```sql
-- migration: 20260622000002_service_accounts.sql
CREATE TABLE service_accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    shadow_user_id  UUID NOT NULL UNIQUE
                       REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    allowed_scopes  TEXT[] NOT NULL DEFAULT '{}',
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at     TIMESTAMPTZ,
    UNIQUE (tenant_id, name)
);
CREATE INDEX idx_service_accounts_tenant ON service_accounts (tenant_id)
  WHERE disabled_at IS NULL;
```

`shadow_user_id` is `UNIQUE` so the relationship is strictly 1:1 in both
directions. `ON DELETE CASCADE` on `shadow_user_id` means deleting the SA
deletes the shadow user, which cascades to its `api_keys` and
`role_assignments` rows (both of which already have FK cascades on user_id).

**Why `created_by` is nullable with `ON DELETE SET NULL`** — admins
should be deletable. An admin who created a long-lived SA cannot become
permanently undeletable. To preserve the provenance trail, **SA creation
emits an audit event capturing a snapshot of the creator's identity**:

```
action: service_account.created
actor_id: <admin_user_id>
fields: {
  service_account_id, name, description, allowed_scopes,
  creator_email, creator_display_name      // snapshot for after-the-fact attribution
}
```

The UI renders "created by <name>" from the `created_by` join when
non-NULL, and falls back to the audit-row snapshot when NULL (with a
"(deactivated)" suffix).

### 4.3 `api_keys` table — polymorphic owner

```sql
-- migration: 20260622000003_api_keys_polymorphic.sql
ALTER TABLE api_keys ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE api_keys ADD COLUMN service_account_id UUID
  REFERENCES service_accounts(id) ON DELETE CASCADE;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_owner_exactly_one
  CHECK ((user_id IS NULL) <> (service_account_id IS NULL));

-- Replace UNIQUE (user_id, name) with two partial unique indexes so that
-- a human user and a service account can each have a key named "ci-prod"
-- without colliding.
ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_user_id_name_key;
CREATE UNIQUE INDEX api_keys_user_name_unique
  ON api_keys (user_id, name) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX api_keys_sa_name_unique
  ON api_keys (service_account_id, name) WHERE service_account_id IS NOT NULL;

CREATE INDEX idx_api_keys_sa ON api_keys (service_account_id)
  WHERE service_account_id IS NOT NULL;
```

No data migration needed — existing rows have `user_id` set and
`service_account_id NULL`, which already satisfies the CHECK.

### 4.4 `api_keys.last_used_at` — already wired

`repository.TouchLastUsed` exists and `service.ValidateAPIKey` already
fires it asynchronously
(`services/auth/internal/service/auth.go:352-356`).
**Self-review caught this** — an earlier draft of this spec proposed
adding the writeback path and an in-memory 60s debounce, both based on
the false premise that the column was unused. They are not.

What's actually needed for FE-API-048:
- A `ServiceAccount.last_used_at` query in the SA list/get path:
  `SELECT MAX(last_used_at) FROM api_keys WHERE service_account_id = $1`.
  Cheap, indexed via the new `idx_api_keys_sa`.
- No changes to `ValidateAPIKey` for the writeback. The existing
  fire-and-forget path is sufficient.

If multi-replica write amplification on a hot key becomes a real concern
later (it isn't today), that's a follow-up — not part of this sprint.

## 5. Wire — HTTP routes (no new proto)

The existing `/apikeys` surface on `services/auth` is HTTP-only — there
is no gRPC RPC for it, and the frontend hits `services/auth:8080` directly
through the vite proxy (`frontend/vite.config.ts` routes `/api/v1/apikeys`
and `/api/v1/users` to `:8080`, everything else under `/api/v1` to the
management BFF on `:8091`). Service accounts follow the same pattern:
all new routes live on `services/auth`'s HTTP server, in a new
`http_service_accounts.go` handler file. **No new proto RPCs are
introduced this sprint.**

### 5.1 `services/auth` HTTP routes

All routes live under `/api/v1/`:

| Method | Path | Auth | Notes |
|---|---|---|---|
| `GET`    | `/service-accounts` | workspace-admin | paged list, `?include_disabled=true` flag |
| `POST`   | `/service-accounts` | workspace-admin | creates SA + shadow user atomically |
| `GET`    | `/service-accounts/{id}` | workspace-admin | |
| `PATCH`  | `/service-accounts/{id}` | workspace-admin | name/description/allowed_scopes/disabled |
| `DELETE` | `/service-accounts/{id}` | workspace-admin | hard delete, cascades to keys + shadow user |
| `GET`    | `/service-accounts/{id}/api-keys` | workspace-admin | reuses existing key list shape |
| `POST`   | `/service-accounts/{id}/api-keys` | workspace-admin | `scopes` must be a subset of `allowed_scopes` |
| `DELETE` | `/service-accounts/{id}/api-keys/{keyId}` | workspace-admin | |
| `GET`    | `/access/activity?principal_user_id={uuid}&limit=50` | self (caller's own `user_id`) or workspace-admin (any in tenant) | proxies to `services/audit` over gRPC |

The existing `POST /apikeys` HTTP body grows an optional
`"service_account_id": "<uuid>"` field. When set, the caller must be a
workspace-admin for that SA's tenant; the human-`user_id` path remains
unchanged for the personal-key flow.

`GET /users/me` is **not** an SA-owned route, but the handler must
recognise SA-key callers and return a sanitised principal envelope (see
§5.6) instead of the underlying shadow-user row.

### 5.2 Frontend proxy update

`frontend/vite.config.ts` adds the two new auth-owned subroutes:

```ts
"/api/v1/service-accounts": { target: "http://localhost:8080", changeOrigin: true },
"/api/v1/access":           { target: "http://localhost:8080", changeOrigin: true },
```

In production the gateway already does the equivalent host-header / path-
prefix routing — no gateway config change needed because `:8080` is the
auth upstream.

### 5.3 Activity backend — no new tables, gRPC to audit

`GET /access/activity` is a thin facade on `services/audit`'s existing
`GetAuditEvents` gRPC, filtered by `(tenant_id, actor_id = user_id_string)`
and sorted by `occurred_at DESC`. The audit `audit_events.actor_id` column
is `TEXT NOT NULL` and already has an `idx_audit_events_actor` index on
`(actor_id, occurred_at DESC)` — the filter is index-aided.

`services/auth` already holds an `audit.AuditServiceClient` in dev (used
by the SSO + RBAC paths to emit events); we reuse the same client to
*read*. The trimmed `principalActivity` JSON shape excludes audit fields
that are either internal (event id, trace id) or denormalisable later
(full repo manifest digest) — the FE only wants "when, what, where from,
did it work."

Authorization: a non-admin caller may only query
`principal_user_id == caller_user_id`. Workspace-admins may query any
`user_id` that belongs to their tenant.

**Order-of-checks is fixed and timing-stable** (security agent M4):

1. Authenticate the caller (existing middleware).
2. Resolve the target `user_id` via `repository.GetUserAnyKind` —
   does not distinguish kinds, so SA shadow users are queryable.
3. Tenant match: if `target.tenant_id != caller.tenant_id`, return
   **404 with body `{"error":"NOT_FOUND"}`** — identical shape and
   identical timing (the lookup must complete even on the negative
   path) to "user genuinely doesn't exist."
4. Non-admin: if `target.user_id != caller.user_id`, return the same
   404. Never 403 — that would leak existence.
5. gRPC to audit, return result.

The gRPC filter binds `(tenant_id, actor_id)` — both. The audit table's
existing `idx_audit_events_actor` index is `(actor_id, occurred_at)`;
binding `tenant_id` in addition is defence-in-depth so a future audit
repartitioning can't silently break the guard.

### 5.4 `ValidateAPIKey` hot-path changes — cross-tenant + scope intersection

This is the security-critical change. Today `ValidateAPIKey` looks up
by `(key_id, hash)`, then returns `(user_id, tenant_id, access)`. For
polymorphic owners, the function must:

1. Look up the row by `(key_id, hash)`.
2. **If `service_account_id IS NOT NULL`:**
   a. Load the SA. If `disabled_at IS NOT NULL`, return
      `PermissionDenied`.
   b. **Authoritative tenant is `sa.tenant_id`**, not the API key row
      and not anything the request carries. If the request also
      provided `X-Tenant-ID` (gateway-injected) and it disagrees,
      return `Unauthenticated` + log a `pentest.cross_tenant_attempt`
      audit event. This is HIGH security agent finding H1.
   c. Effective scopes: `effective = key.scopes ∩ sa.allowed_scopes`.
      If the intersection is empty, return `PermissionDenied`
      ("Scope shrunk; rotate this key"). This is Q1's retroactive-
      shrink semantics in action.
   d. Return `(user_id = sa.shadow_user_id, tenant_id = sa.tenant_id,
      access = mapScopesToAccess(effective))`.
3. **If `user_id IS NOT NULL`:** existing behaviour, no change.

The cross-tenant guard test (mandatory): seed an SA in tenant B,
present its key with `X-Tenant-ID: A` in the request — must return 401,
must NOT return 200 with the SA's permissions, must write an audit row.

### 5.5 JWT revocation on SA disable

Closes security finding H2 / Open Question Q4.

When a workspace-admin disables an SA (PATCH `{disabled: true}`):

- All future `ValidateAPIKey` calls for that SA's keys return
  `PermissionDenied` immediately, via the §5.4 SA-load check. No JTI
  list, no Redis. The DB row is the source of truth.
- For any JWTs the SA shadow-user may have been issued (none today,
  but the human-user JWT path could theoretically be reached through
  some future surface), the server sets a Redis key
  `revoke:user:<shadow_user_id>` with a 25-minute TTL (longer than the
  longest reasonable JWT lifetime). `ValidateToken` checks this key on
  every call and returns `Unauthenticated` if set.

This pattern is already documented in `CLAUDE.md §7` (under
"JWT Validation") for the equivalent JTI case; we extend it to user-
scoped revocation. No new infrastructure.

### 5.6 `/users/me` for SA-key callers

When the authenticating credential is an SA-issued API key, `/users/me`
returns the **principal envelope** instead of the underlying shadow-user
row:

```json
{
  "id":               "<shadow_user_id>",
  "type":             "service_account",
  "service_account": {
    "id":             "<sa_id>",
    "name":           "ci-prod",
    "description":    "GitHub Actions deploy bot for myapp",
    "allowed_scopes": ["pull","push"]
  },
  "email":            null,
  "display_name":     "ci-prod",
  "tenant_id":        "<tenant_id>"
}
```

For human-user callers (the existing 100% of traffic), the response
keeps its current shape with `"type": "user"` added (additive, safe).

Clients (the Beacon topbar avatar especially) branch on `type` to show
a bot glyph + SA name instead of a human profile chip.

### 5.7 SA lifecycle audit vocabulary

Every SA lifecycle mutation emits an `audit_events` row. Action codes
(used in `audit_events.action TEXT`):

| Code | Emitted when | Notable fields |
|---|---|---|
| `service_account.created`        | POST /service-accounts succeeds | snapshot of creator email + display_name (for after-the-fact attribution per §4.2) |
| `service_account.updated`        | PATCH /service-accounts/{id}    | diff of changed fields |
| `service_account.disabled`       | PATCH `{disabled: true}`        | reason (free text from request body) |
| `service_account.enabled`        | PATCH `{disabled: false}`       | — |
| `service_account.deleted`        | DELETE /service-accounts/{id}   | name (denormalised so audit survives the row) |
| `service_account.key_issued`     | POST /service-accounts/{id}/api-keys | key prefix only — never the secret |
| `service_account.key_revoked`    | DELETE …/api-keys/{keyId}       | key prefix |
| `service_account.scopes_updated` | PATCH with set_allowed_scopes   | before / after lists |
| `rbac.role_granted_to_service_account` | when an SA's shadow user receives a role grant — **distinct from `rbac.role_granted`** so future "list users with role X" surfaces can render SAs separately |

These are tested as part of §8 (each mutating handler test asserts the
expected audit row landed via a fake `AuditServiceClient`).

## 6. UI — `/api-keys` becomes a hub

### 6.1 Shape

`_authenticated.api-keys.tsx` flips from "one section" to a hub with a
left vertical sub-nav (12rem wide, mono labels in `text-label-caps`).
The right pane is router-driven:

```
/api-keys                  → Personal keys (current ApiKeysSection, unchanged)
/api-keys/service-accounts → SA list + per-SA drawer (real)
/api-keys/activity         → workspace-wide auth timeline (real)
/api-keys/trust            → Federated identity (PREVIEW + dummy data)
/api-keys/helpers          → Credential helpers (PREVIEW + dummy data)
/api-keys/policies         → Token policies (PREVIEW + dummy data)
/api-keys/review           → Access review (PREVIEW + dummy data)
```

**Nav grouping** in the rail:

- **Yours** — Personal keys
- **Workspace** — Service accounts, Activity (admin-gated; non-admins
  don't see these labels at all)
- **Preview** — Federated trust, Credential helpers, Token policies,
  Access review (at lower contrast, marked with a small "Preview" pill;
  always shown to admins, not shown to non-admins)

Sub-nav is rendered by a new `AccessSubNav` component reading the current
TanStack Router route. The hub layout component is `AccessHubLayout` and
sits between `_authenticated.tsx` and the leaf routes.

### 6.2 Service accounts — real surface

**List page** (`/api-keys/service-accounts`):

- Header: title + "New service account" button (opens dialog).
- Table columns: Name, Description, Active keys, Last used, Allowed
  scopes (as small chips), Status (Active / Disabled badge).
- Empty state: illustration + one-liner + primary button.
- Row click → drawer.

**Drawer** (`ServiceAccountDetail`):

- Identity card: name (editable inline), description (editable inline),
  allowed scopes (chip editor — comma-separated, validated against a
  hardcoded "known scopes" list locally for UI hint; backend is the
  source of truth).
- **Scope-shrink warning:** when the admin removes a scope from
  `allowed_scopes`, a confirmation dialog shows: "This will narrow N
  active keys. Existing tokens with the removed scope will stop working
  immediately." (Q1 retroactive-shrink semantics surfaced in the UI.)
  The count is computed by the backend (counts keys whose `scopes` ⊄
  proposed `allowed_scopes`) and returned via a pre-flight
  `POST /service-accounts/{id}/scopes/preflight` so the dialog can
  populate without saving.
- API keys section: reuses `ApiKeysSection` shape but pointed at
  `/service-accounts/{id}/api-keys`. Key create dialog scopes-chip-picker
  is constrained to the SA's `allowed_scopes`.
- Activity preview: last 5 events from `/access/activity` for the shadow
  user, with a "View all" link to `/api-keys/activity?user={shadow_id}`.
- Danger zone: Disable / Re-enable toggle, Delete button (confirm dialog
  warns "this cascades to N keys and cannot be undone").

### 6.3 Activity — real surface

**Page** (`/api-keys/activity`):

- Filters: principal (default "all keys you can see" — for non-admins,
  their own + nothing else; for admins, a dropdown of human users +
  service accounts), action type (multi-select), time range (24h / 7d /
  30d, defaults 7d).
- Table: When, Principal, Action, Repo, IP, Key, Status.
- Pagination: keyset, "Load more" button at bottom.
- Empty state: "No activity in this window."

Drill-in from a service account drawer pre-populates the principal
filter.

### 6.4 Preview surfaces — dummy data + NB

Each of `/api-keys/trust`, `/helpers`, `/policies`, `/review` is a real
route with a fully-styled mockup so a workspace admin can *see what's
coming*. Mutating controls are visibly disabled with tooltip
"Available in Sprint NN".

A persistent `PreviewBanner` component sits at the top of each:

> **Preview.** This surface ships in **Sprint NN** ([FUT-00X]). The data
> below is illustrative. Have feedback? Drop it in `futures.md`.

**A11y requirements** for the preview surfaces:

- `PreviewBanner` renders as `role="status"` with `aria-live="polite"` so
  a screen reader announces it on route entry.
- Every disabled control is `aria-disabled="true"` **and** `disabled`
  (visual + AT consistency). A `title`/`aria-describedby` exposes the
  "Available in Sprint NN" reason so the AT user gets the same message
  as the mouse user. Pure CSS `pointer-events: none` is forbidden — the
  control must be focusable so AT can read the disabled reason.

#### 6.4.1 `/api-keys/trust` — Federated workload identity (FUT-001, Sprint 11)

- Card list of "Trust relationships" with dummy entries:
  - GitHub Actions — `steveokay/oci-janus` — env `prod` — last verified 2h ago
  - GitLab CI — `myorg/charts` — last verified yesterday
  - Buildkite — `infra-pipelines` — never verified
- "New trust relationship" wizard (disabled): picker for issuer, claims
  editor, scopes picker.
- Inline diagram: GHA runner → OIDC JWT → registry-auth → short-lived
  registry token → registry-core.

#### 6.4.2 `/api-keys/helpers` — Credential helpers (FUT-002, Sprint 11)

- Tabbed code blocks for a chosen key (selector at top):
  - `docker login` shell
  - k8s `Secret` YAML (type `kubernetes.io/dockerconfigjson`)
  - Terraform provider block
  - GitHub Actions `docker/login-action@v3` snippet
- "Copy" button on each block (works — pure clipboard, no backend).
- The selector defaults to a dummy key but is wired to real keys read-
  only — copying the dummy snippet does nothing dangerous.

This one is the closest to shippable end-to-end — the snippets are pure
client-side templating. Flagged Preview only because the workflow around
it (key selection UX, "regenerate snippet on rotation", k8s-secret naming
defaults) wasn't designed during this sprint.

#### 6.4.3 `/api-keys/policies` — Token policies (FUT-003, Sprint 12)

- Three policy cards (each with mock value):
  - Max token TTL — 90 days
  - Force rotation — every 365 days
  - Idle revoke — after 30 days unused
- Sliders + inputs all disabled.
- "Apply to all keys" / "Per-key override" toggle (disabled).

#### 6.4.4 `/api-keys/review` — Access review (FUT-004, Sprint 12)

- Banner: "5 keys haven't been used in 30 days. Review and revoke?"
- Table of dummy keys with Last used, Owner, Suggested action (Revoke /
  Keep / Snooze 30d).
- "Send review reminders to owners" button (disabled).

### 6.5 Route guards

- `/api-keys/service-accounts` and `/api-keys/activity` (when filtering
  to anyone other than self) use `beforeLoad` to require workspace-admin
  (mirror `/admin/tenants` pattern); non-admins redirect to `/api-keys`.
- Preview routes are admin-only (non-admins shouldn't see what's coming
  with their permission set — avoids "why can't I use that"
  conversations).

## 7. Migration & rollout

Three goose migrations, in order, all reversible:

1. `20260622000001_user_kind.sql` — `users.kind` column + index.
2. `20260622000002_service_accounts.sql` — new table.
3. `20260622000003_api_keys_polymorphic.sql` — polymorphic owner + indexes.

Rollback path: down migrations reverse each. The polymorphic migration's
down reinstates `NOT NULL` on `user_id` and must refuse to run if any
`service_account_id IS NOT NULL` rows exist (avoiding silent data loss).
Goose doesn't natively check row counts in `-- +goose Down`, so the
down block uses a `DO $$ … RAISE EXCEPTION` guard:

```sql
-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM api_keys WHERE service_account_id IS NOT NULL) THEN
    RAISE EXCEPTION 'cannot rollback: % api_keys rows are owned by service accounts; revoke them first',
      (SELECT count(*) FROM api_keys WHERE service_account_id IS NOT NULL);
  END IF;
END $$;
DROP INDEX IF EXISTS api_keys_sa_name_unique;
DROP INDEX IF EXISTS api_keys_user_name_unique;
DROP INDEX IF EXISTS idx_api_keys_sa;
ALTER TABLE api_keys DROP CONSTRAINT api_keys_owner_exactly_one;
ALTER TABLE api_keys DROP COLUMN service_account_id;
ALTER TABLE api_keys ALTER COLUMN user_id SET NOT NULL;
-- The original UNIQUE (user_id, name) auto-named constraint is reinstated
-- via a partial-index-style UNIQUE constraint.
ALTER TABLE api_keys ADD CONSTRAINT api_keys_user_id_name_key UNIQUE (user_id, name);
-- +goose StatementEnd
```

No data backfill. No feature flag — the polymorphic column is invisible
to existing surfaces.

### 7.1 RLS — no policy changes

Per resolved Q5: the SA shadow user's id flows into
`SET LOCAL app.user_id = '<shadow_user_id>'` identically to a human
user. RLS policies in `services/metadata`, `services/audit`,
`services/tenant`, `services/proxy`, `services/webhook`,
`services/scanner` are **unchanged** — they're already
`user_id`-agnostic and don't care about principal kind. Analytics
surfaces that want to distinguish human-vs-SA action counts do a
`LEFT JOIN users ON kind` in the read path only.

### 7.2 Proto contract — semantic shift, no syntactic change

`proto/auth/v1/auth.proto` is byte-identical after this sprint, but
`ValidateAPIKeyResponse.user_id` and `ValidateTokenResponse.user_id`
now carry **either** a human user id or a service-account shadow user
id. This is a semantic shift that downstream services
(`registry-core`, `registry-webhook`, `registry-audit`) currently
assume is a human. Two mitigations:

1. The proto file gains a comment on each affected `user_id` field:
   `// May be a service-account shadow user id; join users.kind to distinguish.`
2. **A new CLAUDE.md §14 decision-log entry** captures the shadow-user
   pattern so future services don't grow assumptions:

   > **22. Service-account principal pattern: shadow users (FE-API-048).**
   > Service accounts authenticate as synthetic `users.kind='service_account'`
   > rows. `ValidateAPIKey`/`ValidateToken` return that id in `user_id`;
   > downstream services treat it as an opaque actor identifier. RBAC,
   > audit, RLS, and JWT machinery require no changes. Distinguishing
   > principal kind is the responsibility of the read-path
   > (`LEFT JOIN users ON kind`), not the write path. Codified
   > 2026-06-21.

## 8. Testing

Per `docs/TESTING.md`. The QA review surfaced 10 must-have tests beyond
the original draft; they are listed first.

### 8.1 Mandatory test matrix (QA review)

| # | Behaviour | Why |
|---|---|---|
| T1 | Polymorphic CHECK violation, both directions (both owners set; neither set) | The CHECK is the only thing standing between us and orphan keys. |
| T2 | Partial-unique-index collision matrix: human + SA may share a name; two SAs same name → conflict | §3 calls this out; assert it. |
| T3 | Down-migration `20260622000003` refuses to run if any `service_account_id IS NOT NULL` row exists | §7 promises this; the `DO $$ RAISE EXCEPTION` block must be exercised. |
| T4 | Soft-disable vs hard-delete divergence: disable preserves SA + keys + shadow user + audit trail; delete cascades all | Both modes exist for a reason; pin the difference. |
| T5 | `ValidateAPIKey` cross-tenant guard: SA in tenant B + key presented with `X-Tenant-ID: A` → 401 + `pentest.cross_tenant_attempt` audit row | Security HIGH H1. |
| T6 | `ValidateAPIKey` scope intersection: SA `allowed_scopes={read}`, key `scopes={read,write}`, request a write action → denied | Q1 retroactive shrink. |
| T7 | `ValidateAPIKey` fire-and-forget `last_used_at` failure does not affect validation result | Regression turns a metrics blip into a 401 storm; OCI conformance protector. |
| T8 | Shadow-user filter sweep: table-driven test against every `…Human…` repository method | One source of truth for the kind guard. |
| T9 | `/access/activity` cross-tenant: admin in A queries `principal_user_id` in B → 404 with identical body + identical timing to "not found" | Existence-oracle prevention. |
| T10 | `ValidateAPIKey` latency floor benchmark before/after, regression cap <5% | OCI conformance suite pulls thousands of blobs through this path. |

### 8.2 services/auth

- Repository:
  - `CreateServiceAccount` creates SA + shadow user atomically in one tx
    (rollback if either insert fails).
  - `DeleteServiceAccount` cascades to keys + role assignments + shadow
    user (verified via row counts pre/post).
  - `ListHumans` / `GetHumanByEmail` / `GetHumanByID` / `CountHumans`
    each exclude `kind='service_account'` — covered in T8 as a sweep.
- Service:
  - Scope-allowlist enforcement on key create — request scope ⊆
    `allowed_scopes` else `InvalidArgument` (no leak of which scope was
    rejected).
  - `ValidateAPIKey` cross-tenant guard (T5).
  - `ValidateAPIKey` scope intersection (T6).
  - `ValidateAPIKey` returns the shadow user's `user_id` for SA keys.
  - Disabled SA's keys → `PermissionDenied` (T4).
  - `revoke:user:<shadow_user_id>` Redis key blocks `ValidateToken` for
    a JWT issued before the disable.
- Filter coverage (T8):
  - Login refuses `kind='service_account'` even when the SSO IdP
    returns the synthetic email.
  - Password reset lookup excludes them.
  - SSO email-match excludes them (this is the SSO `GetByEmail` finding
    the code review caught).
  - JWT `Subject` introspection on a shadow_user_id without explicit SA
    context returns 404, not the shadow row.
  - `role_assignments.ListMembers` projects `(user_id, kind, sa_id_or_null,
    display_name)` so admin surfaces can render SAs distinctly.
- HTTP handler tests for every new route — admin-gate negatives, body
  validation negatives (name regex, allowed_scopes regex per §7 input
  validation rules in CLAUDE.md), 4 MiB body cap (SEC-018).
- Audit emission tests: each mutating handler asserts the expected row
  shape against a fake `AuditServiceClient`.

### 8.3 services/management

- Handler tests for `/orgs/{org}/members` user picker — must not include
  shadow users.
- `/admin/tenants` headcount — counts humans only.
- `/api/v1/access/activity` proxy (if management ever proxies it — the
  current spec puts it on auth directly, so this becomes a "no proxy
  required" assertion).

### 8.4 frontend/

- Route guard tests:
  - `/api-keys/service-accounts` non-admin → redirect to `/api-keys`.
  - Sidebar `AccessSubNav` Workspace + Preview groups invisible to
    non-admins (no FOUC where the labels render for one frame before
    redirect — QA's manual smoke #5).
- `PreviewBanner` renders with `role="status"`, disabled controls have
  both `disabled` and `aria-disabled="true"`, and the "Available in
  Sprint NN" reason is exposed via `aria-describedby`.
- `ScopeShrinkConfirmDialog` shows the impacted-key count returned by
  the preflight endpoint.
- `Topbar` avatar branches on `/users/me` `type` field: bot glyph + SA
  name for `type=service_account`, profile chip for `type=user`.
- ComingSoon component is **not** used (these are previews with dummy
  data, not stubs).

### 8.5 Cross-service integration

- `libs/testutil/containers/auth_with_audit.go` — new helper that boots
  both auth-postgres and audit-postgres + audit gRPC over `bufconn`.
  Used by:
  - The `/access/activity` end-to-end test (T9 lives here).
  - The audit-emission tests for SA CRUD (avoids stubbing every audit
    write).
- Existing OCI conformance run (75/75) must remain green; the
  `ValidateAPIKey` benchmark (T10) is the early-warning system.

### 8.6 Test scaffolding

- `services/auth/internal/testutil/fixtures.go` — adds
  `NewServiceAccount(t, tenant, name, allowedScopes…) (sa, shadowUser)`.
- `services/auth/internal/testutil/fixtures.go` — adds
  `NewAPIKeyForSA(t, sa, scopes…) (key, rawSecret)`.
- Mockery regen for `audit.AuditServiceClient` so handler-level unit
  tests don't need the full audit container.
- `infra/dev-seed/service_accounts.sql` — seeds the dev tenant
  (`98dbe36b-ef28-4903-b25c-bff1b2921c9e`) with three SAs so the new
  UI is non-empty on first boot:
  - `ci-prod` (active, 2 keys, `allowed_scopes={pull,push}`)
  - `old-bot` (disabled, 1 key)
  - `orphaned-creator-sa` (active, `created_by` is an already-deleted
    admin — exercises the `SET NULL` + audit-snapshot fallback in
    the UI).

### 8.7 Manual smoke checklist

1. Workspace admin creates SA → issues key → `docker login` with it →
   pushes a tag against `services/core` `/v2/` → audit row exists with
   the shadow user's id → `/api-keys/activity` shows the event with the
   right source IP.
2. **Orphan-creator flow** — SA created by admin X → admin X is deleted
   from `users` → SA's keys still validate; UI shows the audit-snapshot
   fallback for "created by."
3. **Soft-disable round-trip** — disable an SA → wait 31 minutes (past
   the longest expected JWT TTL) → re-enable → existing keys validate
   again without re-issue. Audit log shows `disabled` then `enabled`.
4. **Scope-shrink** — SA has `{read,write}`, an existing key uses
   `{read,write}` → admin PATCHes `allowed_scopes={read}` → the
   `ScopeShrinkConfirmDialog` shows "1 active key affected" →
   confirming → an immediate `docker push` with that key returns 401.
5. **Activity facade with deleted principal** — query
   `/access/activity?principal_user_id=<just-deleted-shadow-user>` →
   404 (the user lookup fails before the audit query — confirmed
   acceptable since audit retention is the audit page's problem, not
   this surface's).
6. Non-admin cannot see service-accounts or preview routes in the rail.
   No FOUC.

## 9. Status tracker updates

- **`status.md`** — add row:
  - `FE-API-048` IN PROGRESS — Service accounts + activity hub. Affects
    `services/auth`, `services/management`, `frontend`. Notes: see
    `docs/superpowers/specs/2026-06-21-service-accounts-and-access-hub-design.md`.
- **`FE-STATUS.md`** — add Sprint 10 row + `/api-keys/*` route table
  entries.
- **`futures.md`** — new section "Tier 2 — Access: machine identity &
  policy" with FUT-001..FUT-004 written up with the dummy-data preview
  surface noted as already shipped.
- **`CLAUDE.md` §4.2 `registry-auth`** — add `service_accounts` to "Owns"
  column.
- **`CLAUDE.md` §14 Decision Log** — add row 22 (shadow-user pattern; see
  §7.2 for the canonical wording).
- **`security.md`** — add proactive notes referencing the resolved HIGH
  findings: cross-tenant guard in `ValidateAPIKey` (PENTEST-AUTH-001
  pre-merge), JWT revoke pattern extension (PENTEST-AUTH-002 pre-merge).

## 10. Open questions for implementation

All five blocking questions from the design/security/QA review round
are resolved and folded into the spec. Recap of where each landed:

| Q | Resolution | Spec section |
|---|---|---|
| Q1. Scope-shrink semantics | Retroactive — `effective = key.scopes ∩ sa.allowed_scopes` at validate time. UI shows impacted-key count via preflight before save. | §5.4, §6.2 |
| Q2. `/users/me` shape for SA callers | Sanitised principal envelope — `{id, type:"service_account", service_account:{…}, email:null, display_name}`. Human flow unchanged. | §5.6 |
| Q3. `created_by ON DELETE` | `SET NULL` + audit-snapshot at creation captures creator email/name so provenance survives. Admins remain deletable. | §4.2 |
| Q4. JWT revocation on SA disable | SA keys: instant via per-call DB check (no new infrastructure). JWTs: `revoke:user:<shadow_id>` Redis key checked in `ValidateToken`. | §5.5 |
| Q5. RLS `app.user_id` for SAs | Shadow user id flows in unchanged. RLS policies are not touched. Analytics distinguish via `users.kind` join only where needed. | §7.1 |

Notes carried forward (not blocking):

- The `services/audit` schema column is `actor_id TEXT` (not the
  presumed `actor_user_id UUID`), with the existing index
  `idx_audit_events_actor (actor_id, occurred_at DESC)` — confirmed
  during spec self-review. The activity facade passes `user_id.String()`
  as the filter value. No audit schema change needed.
- `ValidateAPIKey` already writes `last_used_at` asynchronously via
  `repository.TouchLastUsed` (`services/auth/internal/service/auth.go:352`).
  An earlier draft of §4.4 proposed adding this; the code review caught
  the false premise. The hot path needs no writeback change.

---

**Design approved by user 2026-06-21.** Implementation plan to follow via
`writing-plans` skill.
