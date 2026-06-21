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

**Filter call sites** — every list-of-users surface gets `WHERE kind='human'`
applied via a new `repository.ListHumans()` helper. The full audit list:

- `services/auth` — `ListUsers`, `CountTenantUsers`, login lookup, password
  reset lookup, SSO email-match path.
- `services/management` — `/users/me` adjacent listings, `/orgs/{org}/members`
  user picker, `/admin/tenants` headcount.

A grep for `SELECT … FROM users` in each service is part of the PR
checklist.

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
    created_by      UUID NOT NULL REFERENCES users(id),
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

**Why `created_by` references `users(id)` not "auth"** — preserves an audit
trail of which human admin created the SA, queryable without joining audit
events. The reference is `NOT NULL` because every SA is created by *some*
authenticated principal.

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

### 4.4 `api_keys.last_used_at` — wire it up

The column has existed since `20260609000002_create_api_keys.sql` but is
never written. As part of this sprint:

- `service.Service.ValidateAPIKey` does a fire-and-forget
  `UPDATE api_keys SET last_used_at = now() WHERE id = $1` on every
  successful validation (best-effort; failure logged, not surfaced).
- A 60-second debounce in-memory dedupe — we don't need second-by-second
  precision and high-throughput pulls would otherwise hammer the row.
  Pattern: `last_used_writeback` map keyed on `key_id` with a `time.Time`
  value; UPDATE only if the cached timestamp is >60s old.

This is required for FUT-004 (access review) and for the SA list view's
"Last used" column to be honest.

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
`user_id` that belongs to their tenant; the handler validates this by
loading the target user and rejecting cross-tenant requests with 404 (not
403 — never leak that the user exists elsewhere).

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
down reinstates `NOT NULL` on `user_id`; the down must first refuse to
run if any `service_account_id IS NOT NULL` rows exist (data loss).
Documented in the migration's `-- +goose Down` comment.

No data backfill. No feature flag — the polymorphic column is invisible
to existing surfaces.

## 8. Testing

Per `docs/TESTING.md`:

### 8.1 services/auth

- Repository:
  - `CreateServiceAccount` creates SA + shadow user atomically in one tx.
  - `DeleteServiceAccount` cascades to keys + role assignments + shadow user.
  - `ListHumans` excludes `kind='service_account'`; default `ListUsers`
    is deprecated in favour.
  - `UpdateAPIKeyLastUsed` honours the 60s debounce.
- Service:
  - Scope-allowlist enforcement on key create — request scope ⊆
    `allowed_scopes` else `InvalidArgument`.
  - `ValidateAPIKey` returns the shadow user's `user_id` and writes
    `last_used_at`.
  - Disabled SA's keys all `ValidateAPIKey` → `PermissionDenied`.
- Filter coverage tests:
  - Login refuses `kind='service_account'`.
  - Password reset lookup excludes them.
  - SSO email match excludes them.
- gRPC handler tests for every new RPC, including admin-gate negatives.

### 8.2 services/management

- Handler tests for each new route (happy path + admin-gate negative).
- `POST /service-accounts/{id}/api-keys` with out-of-allowlist scope
  returns 400 with a clear error.

### 8.3 frontend/

- Route guard tests: `/api-keys/service-accounts` non-admin → redirect.
- `AccessSubNav` renders Workspace + Preview groups only for admins.
- Preview routes render the banner; disabled controls show the tooltip.
- ComingSoon component is NOT used (these are previews, not stubs).

### 8.4 Manual smoke

- Workspace admin creates SA → issues key → uses it against
  `services/core` `/v2/_catalog` → audit row exists with the shadow
  user's id → `/api-keys/activity` shows the event with the right
  source IP.
- Non-admin cannot see service-accounts or preview routes in the rail.
- Disable an SA → its keys all return 401 within the JWT/Validate Redis
  TTL.

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

## 10. Open questions for implementation

None blocking. To raise during implementation if surprises appear:

- The Redis `jwt:valid:<jti>` cache uses `time.Until(claims.ExpiresAt)`
  TTL today. Disabling an SA mid-token-lifetime won't invalidate already-
  issued JWTs until they expire. If that gap is unacceptable, we add a
  `revoke:user:<id>` Redis key checked on every `ValidateToken` — same
  pattern as the existing JTI revocation. Defaulting to "accept the
  300s window" for this sprint; revisit if customers complain.
- The `services/audit` schema column is `actor_id TEXT` (not the
  presumed `actor_user_id UUID`), with the existing index
  `idx_audit_events_actor (actor_id, occurred_at DESC)` — confirmed
  during spec self-review. The activity facade passes `user_id.String()`
  as the filter value. No audit schema change needed.

---

**Design approved by user 2026-06-21.** Implementation plan to follow via
`writing-plans` skill.
