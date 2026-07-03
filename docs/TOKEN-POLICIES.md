[//]: # (FUT-003 — workspace token policy reference. Pair this with docs/AUTH.md for API-key mechanics and docs/ACCESS-REVIEW.md for the FUT-004 stale-key surface.)

# Token Policies — API-Key Lifecycle Guardrails

> Canonical reference for the FUT-003 **token policy** feature: the three
> guardrails an operator can set on API-key lifetime, and exactly how each
> one is enforced. Read this when you're setting policy from the dashboard,
> wiring the BFF routes, or debugging why a `CreateAPIKey` call was rejected
> or why a key got auto-revoked.
>
> For general API-key mechanics (Argon2 verify, the 60s Redis cache, the
> `Bearer key.<uuid>.<secret>` form) see [`AUTH.md`](AUTH.md). For the
> stale-key review surface that consumes `rotation_due_at`, see
> [`ACCESS-REVIEW.md`](ACCESS-REVIEW.md).

---

## TL;DR

- **One policy row, three optional knobs.** A single `token_policies` row
  holds `max_ttl_days`, `rotation_interval_days`, and `idle_revoke_days`.
  Each column is **nullable** — `NULL` means "this dimension is off." An
  operator opts in to individual guardrails without configuring all three.
- **`max_ttl_days`** caps how long a newly-created API key can live.
  Enforced synchronously inside `CreateAPIKey`.
- **`rotation_interval_days`** stamps a `rotation_due_at` deadline on each
  new key. FUT-004's access-review surface reads that deadline; FUT-003
  only writes it.
- **`idle_revoke_days`** drives a background worker that revokes keys whose
  `last_used_at` is older than the threshold.
- **Grandfathering is structural.** Tightening a policy never re-validates
  or revokes *existing* keys — the enforcement path only ever consults the
  policy for the create call in front of it (except idle-revoke, which is
  the one deliberate exception and is floored at 7 days so it can't be a
  mass-revoke button).
- **Admin-only.** Both BFF routes are tenant-admin gated; `tenant_id` and
  the actor id are sourced from the JWT, never from the request body.

---

## 1. The policy row

`services/auth/migrations/20260702000001_token_policies.sql` creates:

```sql
CREATE TABLE token_policies (
    tenant_id              UUID        PRIMARY KEY,
    max_ttl_days           INTEGER,             -- NULL = no cap
    rotation_interval_days INTEGER,             -- NULL = no force-rotation
    idle_revoke_days       INTEGER,             -- NULL = no idle-revoke
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by_user_id     UUID
);
```

The same migration widens `api_keys`:

- `rotation_due_at TIMESTAMPTZ` — set on create when the policy has a
  rotation interval; `NULL` means "no rotation deadline."
- `revoke_reason TEXT` — recorded on revocation so an operator can tell a
  manual admin revoke (`"manual"`) from the idle worker's automated action
  (`"idle_revoked"`). (`"rotation_lapsed"` is reserved for FUT-004.)
- `idx_api_keys_idle_check` — a partial index on `(tenant_id, last_used_at)
  WHERE is_active = true` so the idle-revoke worker's per-tenant scan walks
  the index directly.

`NULL` vs. `0`: the service layer rejects zero (and anything `< 1`) at
validation time, so the database never stores a nonsense value. `NULL` is
the only "disabled" signal.

---

## 2. The three knobs and how each is enforced

### 2.1 `max_ttl_days` — cap on API-key expiry

Enforced inside `Service.CreateAPIKey`
(`services/auth/internal/service/auth.go`). The policy is consulted
**before** the Argon2 hash so a rejected call doesn't waste a hash round:

```
policy = tokenPolicy.GetOrDefault(tenant)          # zero-value when no row
if policy.MaxTTLDays != nil:
    maxAllowed = now + MaxTTLDays days
    if expiresAt == nil:      expiresAt = maxAllowed          # clamp (SEC-064)
    elif expiresAt > maxAllowed:  reject InvalidArgument
```

- A requested expiry beyond the cap → `codes.InvalidArgument`
  (`"requested TTL exceeds workspace max (N days)"`), surfaced by the BFF
  as `400`.
- **SEC-064 (resolved 2026-07-01):** the initial implementation guarded on
  `expiresAt != nil`, so a caller could bypass a `max_ttl_days=30` policy
  simply by omitting the expiry field — the key persisted with
  `expires_at = NULL` and validated forever. The check now treats a
  missing expiry as "clamp to the policy cap" rather than "skip
  enforcement." The policy applies whether or not the caller sends an
  `expires_at`.

### 2.2 `rotation_interval_days` — force-rotation deadline

When the policy has a rotation interval, `CreateAPIKey` computes
`rotation_due_at = now + interval` and stamps it on the new key (via
`SetRotationDueAt`, immediately after the row is created). This write is
**best-effort**: if it fails, the key is still usable and the failure is
logged — it is *not* rolled back. A `NULL` `rotation_due_at` is treated as
"no rotation required."

FUT-003 only *writes* the deadline. Nothing in FUT-003 revokes an overdue
key — that's FUT-004's access-review surface (see
[`ACCESS-REVIEW.md`](ACCESS-REVIEW.md)), which reads `rotation_due_at` to
flag keys as rotation-lapsed.

### 2.3 `idle_revoke_days` — auto-revoke of unused keys

Drives the background worker in §3. A key is idle when its `last_used_at`
is older than `now - idle_revoke_days`. **Never-used keys count as idle**:
the worker's query matches `last_used_at IS NULL OR last_used_at < cutoff`,
so a key created but never presented is swept once it crosses the window.

---

## 3. The idle-revoke worker

`services/auth/internal/worker/idle_revoke.go`. Constructed at auth-service
startup and run in a goroutine until the process context is cancelled.

### Cadence

- Default tick period **1 hour** (`tickPeriod`, overridable via
  `WithTickPeriod` — tests pin it short).
- An **immediate first tick** fires on boot, so a fresh start doesn't leave
  idle keys lingering for up to an hour before the first sweep.

### Per-tick flow

1. `ListTenantsWithIdleRevoke` returns only tenants with a **non-null**
   `idle_revoke_days`. Tenants without the policy configured do zero work —
   absence means "no work," not "process with a default."
2. For each such tenant (`tickTenant`):
   1. **Acquire a pooled connection and pin it** — advisory locks are
      per-session in Postgres, so lock + unlock must run on the same
      connection.
   2. `SELECT pg_try_advisory_lock($key)` where `$key` is an FNV-64a hash
      of `"idle-revoke:" || tenant_uuid`. **This is the multi-replica
      guard:** if another auth replica already holds the tenant's lock,
      `pg_try_advisory_lock` returns false and this replica skips the
      tenant silently — the holder does the work on its own tick. The
      `"idle-revoke:"` salt keeps the key from colliding with the same
      tenant's GC advisory lock.
   3. Re-load the policy (`GetOrDefault`) and re-check `idle_revoke_days` is
      still non-nil — it may have been cleared between the enumerate and
      the per-tenant read.
   4. `cutoff = now - idle_revoke_days days`; `ListIdleKeys(tenant, cutoff)`
      walks the partial index.
   5. For each idle key: `RevokeWithReason(id, "idle_revoked")` then publish
      `auth.key_revoked` with `reason="idle_revoked"`.
   6. Deferred `pg_advisory_unlock`.

### Failure semantics (all log-and-continue)

- An error on tenant *N* never blocks tenants *N+1…M*.
- A per-key revoke failure is logged and skipped so one bad row doesn't
  freeze the sweep.
- The DB revoke is the source of truth: a publish failure after a
  successful revoke is logged but not rolled back (the audit gap is visible
  as the missing event). A nil publisher (broker-less dev/test stack)
  simply skips the event.

---

## 4. Grandfathering — why tightening never locks you out

Tightening a policy must never brick keys an operator is actively using.
Two structural properties guarantee this:

- **`CreateAPIKey` only consults the policy for the create call in front of
  it.** There is no batch job that re-checks existing keys against
  `max_ttl_days` or `rotation_interval_days`. Raise the cap, lower it,
  toggle rotation — already-issued keys keep their original `expires_at`
  and `rotation_due_at`.
- **`GetOrDefault` returns a zero-valued policy (all limits `NULL`) when no
  row exists**, and the auth `Service` treats a nil token-policy dependency
  as "no policy." So a freshly bootstrapped deployment behaves exactly like
  the pre-FUT-003 path — no cap, no rotation, no idle sweep — until an admin
  opts in.

The **one** intentional exception is idle-revoke, which *does* act on
existing keys. Its 7-day floor (§5) exists precisely so a newly-applied
policy can't nuke low-activity CI bots on the very next tick.

---

## 5. Validation bounds

Enforced in `TokenPolicyService.Put`
(`services/auth/internal/service/token_policy.go`); every failure is
`codes.InvalidArgument`, surfaced by the BFF as `400` with the auth
service's original message:

| Field | Rule |
|---|---|
| `max_ttl_days` | non-nil ⇒ `1 ≤ N ≤ 3650` (1 day … 10 years) |
| `rotation_interval_days` | non-nil ⇒ `1 ≤ N ≤ 3650` |
| `idle_revoke_days` | non-nil ⇒ `7 ≤ N ≤ 3650` — the **7-day floor** guards against a fresh policy mass-revoking bots on the next tick |
| `actor_id` | required (non-zero) — the BFF plumbs it from the JWT sub |

A `nil` field is always accepted and means "preserve existing value" — the
repository UPSERT uses `COALESCE(EXCLUDED.field, token_policies.field)` so a
partial update that only touches one knob doesn't clobber the others.
`updated_at` and `updated_by_user_id` are refreshed on every `Put`.

---

## 6. Admin BFF routes

`services/management/internal/handler/access_token_policy.go`. Two routes,
both `authMW`-gated **and** tenant-admin gated
(`isTenantAdminOrPlatformAdmin` ⇒ `403` otherwise):

| Method + path | RPC | Notes |
|---|---|---|
| `GET /api/v1/access/token-policy` | `auth.GetTokenPolicy` | `200` even with no policy row — all three limits render as JSON `null` |
| `PUT /api/v1/access/token-policy` | `auth.PutTokenPolicy` | Partial update; nulls preserved |

**Isolation (CLAUDE.md §9):**

- `tenant_id` **always** comes from JWT claims
  (`middleware.TenantIDFromContext`) — never from a body field.
- `actor_id` **always** comes from the JWT sub
  (`middleware.UserIDFromContext`) — never from a body field. It lands in
  `updated_by_user_id` and in the audit event.

**Wire shape.** The three limits are `google.protobuf.Int32Value` wrappers
on the proto and `*int32` in the JSON body/response, so `null` ("unset /
preserve") is distinguishable from an explicit value. The `PUT` body carries
*only* the three limits — no `tenant_id`, no `actor_id`.

**gRPC-layer trust boundary.** The auth service's gRPC handler
(`grpc_token_policy.go`) trusts its caller and does *not* re-check RBAC —
the tenant-admin gate lives in the BFF (the same pattern as
`grpc_oidc_trust.go`). If the auth service was built without a token-policy
service wired, the RPCs return `codes.Unimplemented` so the caller learns
the feature is off.

---

## 7. Audit trail

A successful `Put` emits **`auth.token_policy.changed`** carrying a
before/after snapshot of the three limits, so `/activity` renders the
diff and "last changed by … at …".

**SEC-067 (resolved 2026-07-01):** a no-op `Put` (e.g. an all-`null` body
against a tenant with no policy) previously produced an empty-diff audit
row that read like real activity — "credit-laundering" into the audit
trail. The emitter now compares before/after snapshots and **skips the emit
entirely when they are byte-identical.**

Idle-revoke actions surface separately as `auth.key_revoked`
(`reason="idle_revoked"`), one event per swept key.

---

## 8. Reference: code map

| Concern | File |
|---|---|
| Migration (table + `api_keys` columns + index) | `services/auth/migrations/20260702000001_token_policies.sql` |
| Repository (GetOrDefault / Upsert / ListTenantsWithIdleRevoke / Clear) | `services/auth/internal/repository/token_policy.go` |
| Admin service (validation + audit emit) | `services/auth/internal/service/token_policy.go` |
| `max_ttl_days` + `rotation_due_at` enforcement (SEC-064) | `services/auth/internal/service/auth.go` (`CreateAPIKey`) |
| Idle-revoke worker | `services/auth/internal/worker/idle_revoke.go` |
| Idle-key query + `RevokeWithReason` + `SetRotationDueAt` | `services/auth/internal/repository/apikey.go` |
| gRPC handlers (Get / Put) | `services/auth/internal/handler/grpc_token_policy.go` |
| BFF admin routes | `services/management/internal/handler/access_token_policy.go` |
| Proto (RPCs + `TokenPolicy` message) | `proto/auth/v1/auth.proto` |

---

> **Last updated:** see `git log -- docs/TOKEN-POLICIES.md`.
> **Found a gap?** PR welcome — this doc is the canonical reference, so any
> divergence between code and this file is the file's bug.
