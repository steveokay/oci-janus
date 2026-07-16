# Design — MCP service-account provenance + Connected-Agents surface

> **Status:** Approved design (2026-07-16). Ready for implementation planning.
> **Scope:** Make MCP one-click-connect service accounts *discoverable* and their
> scopes *honestly labelled* — so an operator browsing service accounts knows
> what `mcp-agent-<base36>` is, who made it, whether it's still used, and that
> its `*:read` scopes are advisory. Covers **Part 1 (provenance + honest labels)**
> and **Part 2 (Connected-Agents surface)** of the 3-part initiative.
> **Explicitly out of scope / deferred to its own spec:** **Part 3 — enforcing the
> `*:read` scopes** as real permission gates on the BFF read routes (carries a
> dashboard-breaking, principal-aware risk; tracked separately).
> **Platform posture:** single-tenant (ADR-0031); global admin = admin. Every SA
> is admin-created, so the free-form/advisory-scope situation is the *default*,
> not an edge case — this design assumes no multi-tenant / delegation nuance.

---

## 1. Problem

The MCP one-click-connect flow (`frontend/src/lib/api/mcp.ts`) mints a service
account named `mcp-agent-<base36-timestamp>` (e.g. `mcp-agent-mrmg5t20`) plus one
API key, stamped with a read-only scope vocabulary
`MCP_KEY_SCOPES = repo:read, scan:read, audit:read, access:read, signer:read`.
An operator looking at the service-accounts list has three invisible gaps:

1. **Provenance** — nothing marks the SA as MCP-minted. The only signal is the
   `mcp-agent-` name convention, which you must already know. There is **no
   `origin` column** on `service_accounts` today, and the list gives no
   created-by / purpose context.
2. **Truthfulness of scopes** — the `*:read` strings *look* enforced but aren't
   gated on the MCP read routes (they are advisory labels; `mcp.ts:10` says so,
   and `validateScopes` in `http_service_accounts.go:796` checks scope *format*
   `^[a-z][a-z0-9_:]{0,63}$` only, never membership of a fixed set). Displaying a
   permission you don't enforce is a misleading-security surface.
3. **Lifecycle** — these accumulate (one per "Connect" click, timestamp-named,
   easy to orphan) with no at-a-glance last-used / revoke story.

This design closes the **discoverability** and **honesty-of-labelling** gaps.
Making the scopes *actually enforced* (which would remove gap 2 at the root
rather than labelling around it) is Part 3, a separate spec.

## 2. Scope & non-goals

**In scope:**
- **Part 1** — a durable `origin` marker on service accounts (`'manual'` default,
  `'mcp-connect'` for MCP-minted), backfilled for existing `mcp-agent-%` rows,
  threaded through the create path, exposed on SA read models, and surfaced in
  the FE as a badge + an advisory-scope tooltip.
- **Part 2** — a Connected-Agents view (Settings) that filters SAs to
  `origin='mcp-connect'` and shows created-at, created-by, last-used (from
  `api_keys.last_used_at`, which already exists), and one-click revoke.

**Non-goals (deferred):**
- **Part 3 — enforcing `*:read` scopes** on BFF read routes so display == reality.
  Requires principal-aware gating (JWT/browser users carry roles, not `*:read`
  scopes, so a naïve gate breaks the dashboard). Its own spec.
- No change to the MCP scope *vocabulary* itself (`MCP_KEY_SCOPES` stays as-is).
- No change to how raw API keys authenticate (role-less → reader-gated routes).
- No rename of existing `mcp-agent-*` SAs (the `origin` column + backfill make
  them discoverable without touching names).

## 3. Architecture — data flow

```
FE "Create SA" form ──origin:'manual'(default)──┐
FE MCP connect card ──origin:'mcp-connect'──────┤
                                                 ▼
        management BFF  POST /api/v1/service-accounts  (forwards origin)
                                                 ▼
        registry-auth  CreateServiceAccount RPC (origin field)
                                                 ▼
        service_accounts.origin  (TEXT NOT NULL DEFAULT 'manual')
                                                 │
        ListServiceAccounts / Get ── returns origin (+ last_used_at agg) ──▶ FE
                                                 │
        FE service-accounts list ─ badge + advisory tooltip when origin='mcp-connect'
        FE Settings ▸ Connected Agents ─ filter origin='mcp-connect' + revoke
```

## 4. Part 1 — provenance + honest labels

### 4.1 Data model (migration)
`services/auth/migrations/<ts>_service_account_origin.sql` (goose, up + down):

```sql
-- up
ALTER TABLE service_accounts
  ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual';

-- backfill existing MCP-minted rows by their name convention so current
-- mcp-agent-* SAs become discoverable retroactively.
UPDATE service_accounts
  SET origin = 'mcp-connect'
  WHERE name LIKE 'mcp-agent-%';

-- down
ALTER TABLE service_accounts DROP COLUMN origin;
```

- `origin` is a free TEXT with an application-enforced small enum (`'manual'`,
  `'mcp-connect'`) — kept as TEXT (not a PG enum) so future origins (`'scim'`,
  `'terraform'`) don't need a type migration. Application validates membership.
- Per CLAUDE.md §11: additive column, has a down migration, no column drop of an
  existing field. The backfill is idempotent (safe to re-run).

### 4.2 Proto + create path
- **Proto** (`proto/auth/v1/*.proto`): add `string origin = N;` to the
  `CreateServiceAccountRequest` and to the `ServiceAccount` message returned by
  Get/List. Additive field numbers only (buf breaking clean). Regenerate stubs.
- **auth service**: `CreateServiceAccountInput.Origin` threaded into
  `CreateServiceAccountService.Create` → `repository.CreateServiceAccountInput`
  → the INSERT. Default to `'manual'` when empty; **validate** against the enum
  (reject unknown values with `InvalidArgument`). The SA read model
  (`ServiceAccountService` output + repository row) carries `Origin`.
- **auth HTTP handler** (`http_service_accounts.go` create): accept optional
  `origin` in the request body; default `'manual'`.
- **management BFF**: forward `origin` on the create passthrough; expose `origin`
  on the SA list/get response DTOs.

### 4.3 Frontend labels
- The SA list row shows a badge when `origin === 'mcp-connect'`
  (e.g. `MCP connect · read-only`).
- The scopes cell for an MCP SA shows an ⓘ tooltip: *"These `*:read` scopes are
  advisory — MCP read access is governed by the key's reader role, not by these
  labels."* (Copy flips to "enforced" if/when Part 3 lands.)
- `frontend/src/lib/api/mcp.ts`: the mint call passes `origin: 'mcp-connect'`.
- The normal "Create service account" form sends nothing → BFF/auth default
  `'manual'`.

## 5. Part 2 — Connected-Agents surface

### 5.1 Data
- **Last-used** rides on the existing `api_keys.last_used_at` column (already
  present, indexed `(tenant_id, last_used_at)`). The SA list/get response gains a
  `last_used_at` field = MAX(`api_keys.last_used_at`) over the SA's keys (NULL =
  never used). This is a repository query addition, no migration.
- **Created-by** already exists (`service_accounts.created_by`, and the durable
  `service_account.created` audit row snapshots creator email/display name).
- **Origin filter**: `ListServiceAccounts` gains an optional `origin` filter
  param (proto + repository WHERE clause) so the view can request only
  `origin='mcp-connect'` rows.

### 5.2 Frontend
- New Settings route **Connected Agents (MCP)** (sidebar under Settings, grouped
  by operator mental model per the nav-grouping convention — it's an integration/
  agent surface, not "service accounts admin").
- Lists `origin='mcp-connect'` SAs: name, created-at, created-by, last-used
  (relative + absolute tooltip), and a **Revoke** button that deletes the SA
  (cascades its keys via the existing `DELETE /service-accounts/:id`). Revoke uses
  the existing confirm-dialog pattern.
- Empty state explains what MCP connect keys are + links to the MCP docs.
- The generic service-accounts admin page keeps showing all SAs (with the Part 1
  badge); Connected Agents is the focused, prunable view.

## 6. Error handling & edge cases

- Unknown `origin` value on create → `InvalidArgument` / 400 (closed enum,
  app-validated). Empty/absent → `'manual'`.
- Backfill matches on the `mcp-agent-%` name prefix; a manually-created SA that
  happens to be named `mcp-agent-*` would be mislabelled — acceptable (the name
  is reserved by convention; document it). Going forward, `origin` is set
  explicitly at create time, so new rows are authoritative regardless of name.
- `last_used_at` NULL (key never used) renders as "never" — a useful prune signal.
- Connected-Agents revoke is the existing destructive delete; confirm-dialog
  required (irreversible-action-control convention).
- A down-migration drop of `origin` is safe (no other table references it).

## 7. Testing

- **auth (Go, TDD):** repository create round-trips `origin`; default `'manual'`
  when empty; unknown value rejected; `ListServiceAccounts` origin filter returns
  only matching rows; `last_used_at` aggregation = MAX over keys (incl. NULL when
  no keys/never used). Migration up+down applies cleanly (testcontainers PG) and
  the backfill tags a seeded `mcp-agent-*` row.
- **management BFF (Go):** create forwards `origin`; list/get expose `origin` +
  `last_used_at`; origin filter passthrough.
- **frontend (vitest):** MCP mint sends `origin:'mcp-connect'`; SA list renders
  the badge + advisory tooltip only for MCP SAs; Connected-Agents view lists
  filtered rows, formats last-used, and wires revoke through the confirm dialog.
- **proto:** `buf lint` + `buf breaking` clean (additive only).
- **live-verify:** on the compose stack — click MCP connect (or mint via the
  path), confirm the new SA shows `origin='mcp-connect'`, appears badged in the SA
  list and in Connected Agents with created-by + last-used, and that revoke
  removes it + its key. Confirm a normal SA create is `origin='manual'` and
  unbadged. Confirm the backfill tagged the pre-existing `mcp-agent-*` rows.

## 8. Docs & tracker

- `docs/MCP.md`: document that MCP connect mints an `origin='mcp-connect'` SA,
  where to see/prune it (Connected Agents), and that the `*:read` scopes are
  advisory today (Part 3 will make them enforced).
- `docs/SERVICES.md`: note the `origin` column + the `ListServiceAccounts` origin
  filter + `last_used_at` aggregation on the auth service.
- `status.md` row; regenerate `openapi.json` / postman if the BFF DTOs change.
- Gates: auth + management build/vet/test/golangci-lint; all 4 FE gates; buf
  lint/breaking (CLAUDE.md §15).

## 9. Risks / open questions

- **Part 3 dependency framing:** this design *labels* the scopes as advisory. If
  Part 3 later enforces them, the FE tooltip copy + `docs/MCP.md` must flip from
  "advisory" to "enforced" — noted so the two specs stay in lock-step.
- **Sidebar placement** of Connected Agents (its own Settings item vs a tab on the
  service-accounts page) is a nav-grouping judgment; resolve during planning with
  the "operator mental model" convention. Recommendation: a distinct Settings item
  (it's an integration surface, and mixing it into SA-admin re-buries the problem).
- **Proto field numbers**: pick the next free numbers in each message; never reuse.
