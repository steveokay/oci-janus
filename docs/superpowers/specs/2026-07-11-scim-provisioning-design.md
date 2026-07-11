# SCIM 2.0 Provisioning (Users-only v1) — Design

> **Status:** approved 2026-07-11. Feature branch `feat/scim-provisioning`.
> **Scope:** Tier 1 #5 (futures.md). Enterprise IdP-driven user provisioning +
> deprovisioning for `registry-auth`. **Users-only v1**; Groups deferred to v2.

---

## 1. Overview

SCIM (System for Cross-domain Identity Management, RFC 7642/7643/7644) lets an
enterprise IdP — Okta, Microsoft Entra ID, etc. — push user create / update /
deactivate into the registry automatically, so an employee provisioned in the
IdP appears in the registry and, critically, is **disabled here the moment they
are offboarded there**. `registry-auth` owns users, RBAC, and SSO, so SCIM lives
entirely in `registry-auth`.

This v1 implements **Users only** — the dominant Okta/Entra offboarding path —
and reuses two primitives that already exist:

- **`Service.SetUserDisabled`** (`services/auth/internal/service/tenant_users.go`)
  — the deprovision primitive. On disable it flips `status`→`disabled` /
  `is_active`→false, revokes all the user's JWT JTIs, disables every API key the
  user owns, and invalidates the API-key cache. SCIM `active:false` and
  `DELETE /Users/{id}` both route here.
- **`SSO.EnsureSSOUser` / `repository.CreateSSOUser`**
  (`services/auth/internal/service/sso.go`) — the provisioning pattern. Creates a
  passwordless (`password_hash=''`), `kind='human'` user and grants a baseline
  `reader` role. SCIM create mirrors this.

### Goals

- `POST /scim/v2/Users` provisions a user; `PATCH active:false` / `DELETE`
  deprovisions (disable, never row-delete).
- An IdP authenticates with a dedicated, deployment-wide SCIM bearer token —
  isolated from user JWTs and `key.<uuid>` API keys.
- `GET /scim/v2/Users` supports the filters + pagination Okta/Entra actually send.
- Discovery endpoints so an IdP can auto-configure.

### Non-goals (deferred to v2)

- **Groups** (`/scim/v2/Groups`) — needs a `scim_groups` mapping table (Group ↔
  (role, org-scope) tuple) + member-add/remove wired to `GrantRole`/`RevokeRoleScoped`.
- Multiple named SCIM tokens (one per IdP) — v1 is a single global token.
- `PATCH` operations beyond `active`, `userName`, `displayName`, `emails`.
- Complex SCIM filters (anything past `eq` on the three attributes below).

---

## 2. Decisions (locked 2026-07-11)

| # | Decision | Rationale |
|---|---|---|
| D1 | **Users-only v1**; Groups deferred | Covers the dominant Okta/Entra provision+offboard case; Groups need a new mapping table and roughly double the surface. |
| D2 | **Single global SCIM token** (`scim_config` table) | Mirrors the deployment-wide `global_sso_config` posture — one IdP ↔ one deployment. Avoids per-token lifecycle UI. |
| D3 | **Collision → link only passwordless** | Adopt (backfill `external_id`) only when the existing user is SSO/passwordless (`password_hash=''`); **409** for a local-password account, so an IdP can never silently take over a password login. Mirrors `EnsureSSOUser`'s email-recycle defense. |
| D4 | **Deprovision = disable, never delete** | Both `DELETE` and `active:false` route to `SetUserDisabled(...true)`. Preserves audit history + reuses the full disable blast radius. Matches the platform's non-destructive posture (CLAUDE.md §11). |
| D5 | **Baseline role on provision = `reader` @ org `*`** | SSO parity (`EnsureSSOUser` grants the same) so a provisioned user can log in and see reader content. Users-only has no Group→role path, so without this a provisioned user would have zero access. |
| D6 | **Filter subset = `userName eq`, `externalId eq`, `active eq` only** | Exactly what Okta/Entra send. `501`/`400` for anything more complex. |
| D7 | **SCIM auth = dedicated principal**, not user JWT or `key.<uuid>` | SCIM is a superuser provisioning surface; it gets its own Argon2-verified bearer + its own gate (`requireSCIMAuth`), authorized only for `/scim/v2/*`. |

---

## 3. Endpoints

All under `/scim/v2`, mounted via a new `RegisterSCIM(mux)` in
`services/auth/internal/handler/http.go`. Every route is gated by
`requireSCIMAuth` except none are public.

### Discovery (static JSON)

- `GET /scim/v2/ServiceProviderConfig` — advertise `patch.supported:true`,
  `filter:{supported:true, maxResults:200}`, `bulk.supported:false`,
  `changePassword.supported:false`, `sort.supported:false`,
  `authenticationSchemes:[{type:"oauthbearertoken"}]`.
- `GET /scim/v2/ResourceTypes` — the `User` resource type.
- `GET /scim/v2/Schemas` — the `urn:ietf:params:scim:schemas:core:2.0:User` schema.

### Users

| Method | Path | Behavior |
|---|---|---|
| `GET` | `/Users` | List; supports `filter` (D6) + `startIndex`/`count`; returns `ListResponse`. |
| `POST` | `/Users` | Provision (create or link-passwordless per D3). `201`. |
| `GET` | `/Users/{id}` | Read one; `404` if absent. |
| `PUT` | `/Users/{id}` | Full replace of the mutable attributes. |
| `PATCH` | `/Users/{id}` | RFC 7644 §3.5.2 ops on `active` (→ disable/enable), `userName`, `displayName`, `emails`. |
| `DELETE` | `/Users/{id}` | Map to `SetUserDisabled(...true)` (D4). `204`. |

---

## 4. Data model

Two additive goose migrations (each with a `-- +goose Down`).

### 4.1 `users.external_id`

```sql
ALTER TABLE users ADD COLUMN external_id TEXT;
CREATE UNIQUE INDEX users_tenant_external_id_uniq
  ON users (tenant_id, external_id) WHERE external_id IS NOT NULL;
-- optional provenance marker so SCIM-owned rows are distinguishable
ALTER TABLE users ADD COLUMN provisioned_via TEXT; -- 'scim' | NULL
```

`external_id` is the IdP's stable id, required for `filter=externalId eq`. The
partial unique index lets non-SCIM users keep `external_id IS NULL` without
colliding.

### 4.2 `scim_config` (single global row)

```sql
CREATE TABLE scim_config (
    id           SMALLINT PRIMARY KEY DEFAULT 1 CHECK (id = 1), -- singleton
    tenant_id    UUID NOT NULL,
    token_hash   TEXT,                    -- Argon2id PHC string; NULL = disabled
    enabled      BOOLEAN NOT NULL DEFAULT false,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    rotated_at   TIMESTAMPTZ,
    last_used_at TIMESTAMPTZ
);
```

Singleton via the `CHECK (id = 1)` + fixed default (mirrors the single-config
posture of `global_sso_config`). The runtime role gets `SELECT, INSERT, UPDATE`
in the same migration (the #290 grant-in-same-migration lesson).

---

## 5. Auth boundary

New middleware `requireSCIMAuth(next)` in `internal/handler/http_scim.go`:

1. Extract the `Authorization: Bearer <token>` value (reuse `libs/auth/bearer`).
2. Load the single `scim_config` row; if `enabled=false` or `token_hash IS NULL`
   → `401` SCIM error (feature not configured — no oracle).
3. Argon2-verify the presented token against `token_hash` (constant-time; same
   discipline as `api_keys.key_hash`). Mismatch → `401`.
4. Best-effort `last_used_at = now()` (fire-and-forget, non-blocking).
5. Resolve the bootstrap tenant from context (single-tenant posture — the SCIM
   base URL implicitly scopes to it; no tenant in the SCIM path).

The SCIM principal is **not** a user: it carries no RBAC roles and is authorized
**only** for `/scim/v2/*`. It never flows through `requireAuth` and cannot be
used on any other route.

### Admin token management (Phase 3)

Three admin-gated (`callerIsTenantAdmin` / `effectiveGlobalAdmin`) endpoints on
the normal authenticated surface to generate / rotate / disable the SCIM token —
the raw token is shown **once** on generate (mirrors the invite-token pattern),
stored only as an Argon2 hash. BFF passthrough + a small Settings panel.

---

## 6. User ↔ `users` attribute mapping

| SCIM attribute | `users` column | Notes |
|---|---|---|
| `id` | `id` (UUID) | SCIM resource id. |
| `externalId` | `external_id` | IdP's stable id. |
| `userName` | `username` | Derive via `DeriveSSOUsername(email)` when only email is supplied. Validated against the `^[a-zA-Z0-9_-]{3,64}$` allowlist. |
| `emails[primary].value` | `email` | |
| `displayName` / `name.formatted` | `display_name` | |
| `active` | `status` / `is_active` | `true`→`SetUserDisabled(...false)`, `false`→`SetUserDisabled(...true)`. |
| `meta.created` / `meta.lastModified` | `created_at` / `updated_at` | |
| — | `kind='human'`, `password_hash=''`, `provisioned_via='scim'` | always, mirroring `CreateSSOUser`. |

Secrets are never returned (there are none — SCIM users are passwordless).

---

## 7. Provisioning / deprovisioning behavior

- **Create (`POST /Users`):** validate `userName`/`externalId` against the
  handler allowlists. If no user with this `email` exists → create via a new
  `repository.CreateSCIMUser` (mirrors `CreateSSOUser`: passwordless, `kind='human'`,
  stamps `external_id` + `provisioned_via='scim'`), then grant `reader` @ org `*`
  (D5). If a user with this `email` exists → **D3**: link (backfill `external_id`)
  iff `password_hash=''`, else `409 Conflict`.
- **Re-provision idempotency:** a `POST` for an `externalId` that already maps to
  a user returns that user (`200`/`409` per RFC — we return `409` with the
  existing id in `detail`, which Okta/Entra treat as "already exists, reconcile").
- **`active` toggle:** `PATCH`/`PUT` setting `active:false` → `SetUserDisabled(...true)`;
  `active:true` → `SetUserDisabled(...false)`. `DELETE` = `active:false`.
- **`invited` interaction:** a SCIM provision targeting an email that exists as an
  `invited` local user is treated as a passwordless-link candidate only if that
  invited row is passwordless; the SCIM activation supersedes the invite flow
  (the IdP is now the source of truth). Documented explicitly so it isn't a
  surprise.

---

## 8. Filtering + pagination + errors

- **Filter (D6):** parse `filter` for exactly `userName eq "x"`, `externalId eq "y"`,
  `active eq true|false`. Anything else → `400` with SCIM `scimType:"invalidFilter"`
  (or `501` for unsupported operators). A `GET /Users` with no filter lists all
  (paged).
- **Pagination:** `startIndex` (1-based) + `count`; response carries
  `totalResults`, `startIndex`, `itemsPerPage`. Backed by a keyset/offset paged
  query in `repository.ListSCIMUsers`.
- **Error envelope:** every error returns the SCIM shape
  `{"schemas":["urn:ietf:params:scim:api:messages:2.0:Error"], "status":"<code>", "scimType":"...", "detail":"..."}`.

---

## 9. Security considerations

- SCIM token is Argon2id-hashed at rest; the raw value is shown once and never
  stored or logged. `requireSCIMAuth` is constant-time and fail-closed
  (unconfigured/disabled → `401`, never a silent allow).
- The SCIM principal is authorization-isolated to `/scim/v2/*`; it cannot call
  any user or admin route.
- D3 (link-only-passwordless) prevents IdP takeover of local-password accounts.
- Deprovision reuses `SetUserDisabled`, so a disabled user's live JWTs + API keys
  are revoked immediately (no lingering access after offboarding).
- Input allowlists at the handler layer (username/email/externalId) — no
  unvalidated strings to SQL (parameterised throughout, per CLAUDE.md §11).
- Never log token material or full request bodies (CLAUDE.md §10).

---

## 10. Files touched (all in `registry-auth`)

| File | Change |
|---|---|
| `migrations/<ts>_users_external_id.sql` | `external_id` + partial unique index + `provisioned_via` (+ down). |
| `migrations/<ts>_scim_config.sql` | `scim_config` singleton + runtime-role grants (+ down). |
| `internal/repository/scim.go` | token get/set, `GetUserByExternalID`, `CreateSCIMUser`, `ListSCIMUsers` (paged+filtered), `SetExternalID`. |
| `internal/service/scim_users.go` | create / collision / list / get / put / patch / delete + `active`→`SetUserDisabled`. |
| `internal/service/scim_token.go` | generate / rotate / disable + Argon2 verify. |
| `internal/handler/http_scim.go` | `requireSCIMAuth`, `RegisterSCIM(mux)`, discovery endpoints, SCIM (de)serialization types, error envelope. |
| `internal/handler/http.go` | wire `RegisterSCIM` + the 3 admin token-mgmt routes. |
| `services/management/internal/handler/*` | BFF passthrough for admin token mgmt (Phase 3). |
| `frontend/src/*` | Settings panel: SCIM base URL + token generate/rotate/disable (Phase 3). |
| `.env.example`, `docs/SERVICES.md`, `docs/AUTH.md` | doc the SCIM surface + config. |

---

## 11. Phasing

- **Phase 1 — schema + token + auth boundary + discovery.** Migrations,
  `scim.go` token CRUD, `requireSCIMAuth`, `RegisterSCIM`, static discovery
  endpoints, SCIM error envelope. Exit: an IdP can authenticate and read discovery.
- **Phase 2 — Users endpoints (the core).** create/collision, list+filter+page,
  get, put, patch (`active`), delete → disable. Exit: full provision → list →
  deactivate → reactivate cycle works.
- **Phase 3 — admin token management + FE.** generate/rotate/disable endpoints,
  BFF passthrough, Settings panel.

Phases 1–2 are the shippable core; Phase 3 is the operator ergonomics layer.

---

## 12. Testing

- **Unit:** token Argon2 verify (match/mismatch/disabled); filter parser
  (`userName eq` / `externalId eq` / `active eq` / rejected filters); collision
  link-vs-409 branch (passwordless links, local-password 409); `active` toggle →
  `SetUserDisabled`; username derivation.
- **Integration (real Postgres):** full lifecycle — provision → `GET /Users`
  (filter by userName + externalId) → `PATCH active:false` (assert JTIs +
  API keys revoked via the `SetUserDisabled` path) → `PATCH active:true`; auth
  boundary — bad token / disabled config / non-`/scim/v2` path all `401`/rejected;
  pagination (`startIndex`/`count`) over a seeded set.
- **Conformance note:** SCIM is not part of OCI conformance (that's registry-core).

---

## 13. Deferred / v2 backlog

- Groups (`/scim/v2/Groups` + `scim_groups` mapping + `GrantRole`/`RevokeRoleScoped`).
- Multiple named SCIM tokens with per-token `last_used_at` + revoke UI.
- Richer `PATCH` (paths beyond active/userName/displayName/emails).
- `Enterprise User` extension schema (`urn:ietf:...:2.0:User:enterprise`) if a
  customer needs department/manager attributes.
