[//]: # (FUT-004 — access review (stale-key nudge). Pair this with docs/TOKEN-POLICIES.md for the idle-revoke threshold it reads, and docs/EVENTS.md for the two routing keys it emits.)

# Access Review — Stale-Key Nudge

> Canonical reference for the FUT-004 access-review feature. Read this when
> you're touching the weekly stale-key worker, the `/api-keys/review`
> dashboard panel, the snooze flow, or the audit trail behind them —
> backend, frontend, ops, or threat-modeling.
>
> For the staleness threshold this feature reads (`idle_revoke_days`) and
> the FUT-003 idle-**revoke** worker it deliberately does not overlap with,
> see [`docs/TOKEN-POLICIES.md`](TOKEN-POLICIES.md). For the two audit
> events it emits, see [`docs/EVENTS.md`](EVENTS.md).

---

## TL;DR

- **It's a nudge, never a revoke.** A weekly worker flags API keys that
  have gone unused past a staleness cutoff and emits one
  `auth.access_review.due` event per stale key. That event lights up the
  notification bell and populates the `/api-keys/review` panel. The
  operator decides — Revoke / Keep / Snooze — per row. The feature *never*
  auto-revokes anything (spec Decision #4). Automatic idle-revocation is a
  separate feature (FUT-003).
- **The cutoff comes from token policy.** The worker reads
  `token_policies.idle_revoke_days` and treats a key as stale when it
  hasn't been used since `now - idle_revoke_days`. When no policy is
  configured the fallback is **90 days**.
- **Each row carries a suggested action.** The service decorates every
  stale key with `REVOKE` / `KEEP` / `SNOOZE` plus a `reason`
  (`idle` / `rotation_lapsed` / `both`) so the panel renders a
  recommendation without a second decision step.
- **Snooze defers the next nudge.** The operator picks N days in `[1, 90]`;
  the worker skips that key until `review_snoozed_until` passes.
- **"Send reminders" (email) shipped** with the FUT-019 email channel
  (2026-07-07) — the panel button drives the live Resend / SMTP transport.

---

## 1. Moving parts

```
                       ┌──────────────────────────────────────────────┐
                       │  weekly worker (services/auth)               │
                       │  worker/access_review.go                     │
   immediate first ──► │  • enumerate workspaces w/ active api_keys   │
   tick, then every    │  • pg_try_advisory_lock(tenant)  ← 1 replica │
   7 days              │  • ListStaleKeys → per stale key:            │
                       │      emit auth.access_review.due             │
                       └───────────────────┬──────────────────────────┘
                                           │ RabbitMQ (registry.events)
                       ┌───────────────────▼──────────────────────────┐
                       │  services/audit  eventconsumer/consumer.go   │
                       │  • map → audit_events row (actor=system)     │
                       │  • notification bell entry (FUT-019 Phase 1) │
                       └───────────────────┬──────────────────────────┘
                                           │
                       ┌───────────────────▼──────────────────────────┐
                       │  Browser — /api-keys/review panel            │
                       │  per row: [Revoke] [Keep] [Snooze 30d]       │
                       └──────┬──────────────────────┬────────────────┘
                              │ GET  .../stale       │ POST .../snooze
                       ┌──────▼──────────────────────▼────────────────┐
                       │  registry-management (BFF)                   │
                       │  handler/access_review.go                   │
                       └───────────────────┬──────────────────────────┘
                                           │ gRPC (mTLS)
                       ┌───────────────────▼──────────────────────────┐
                       │  services/auth  service/access_review.go     │
                       │  ListStaleKeys / SnoozeAPIKeyReview          │
                       └──────────────────────────────────────────────┘
```

The worker's only product per tick is the audit event — it mutates
nothing on any other service. Revoke is **not** a route in this feature:
the panel's Revoke button hits the existing
`DELETE /api/v1/api-keys/:id` route.

---

## 2. What counts as "stale"

`ListStaleKeys` (in `services/auth/internal/service/access_review.go`)
resolves the threshold, then hands a computed `staleCutoff` to the
repository query:

1. Read `token_policies.idle_revoke_days` for the workspace. Unset (no
   policy row, or a row with `idle_revoke_days` NULL) → **90 days**. A
   policy-read error also degrades to 90 with a warn log — the review
   surface is nudge-only, so showing keys against the default beats
   hiding them all behind a hard failure.
2. `staleCutoff = now - threshold*24h`.
3. The SQL (`APIKeyRepository.ListStaleKeys`) returns an active key when
   **both**:
   - it is not currently snoozed
     (`review_snoozed_until IS NULL OR review_snoozed_until < now()`), and
   - it is idle **or** rotation-lapsed:
     `last_used_at IS NULL OR last_used_at < staleCutoff OR (rotation_due_at IS NOT NULL AND rotation_due_at < now())`.

`owner_user_id` is `COALESCE(user_id, service_accounts.shadow_user_id)`
so human-owned and SA-owned keys land in the same list with a stable
owner id. Revoked (`is_active = false`) keys are never surfaced.

---

## 3. The suggested-action heuristic

`suggestedActionFor` is a pure function so its branches are unit-testable
in isolation. Given a stale key, the cutoff, and `now`:

| Condition | Action | Reason |
|---|---|---|
| `rotation_due_at` in the past **and** idle | `REVOKE` | `both` |
| `rotation_due_at` in the past (not idle) | `REVOKE` | `rotation_lapsed` |
| `last_used_at` is NULL (never used) | `REVOKE` | `idle` |
| `last_used_at` older than `cutoff - 14d` (grace) | `REVOKE` | `idle` |
| `last_used_at` within the 14-day grace window of the cutoff | `KEEP` | `idle` |
| otherwise (uncertain) | `SNOOZE` | `idle` |

The 14-day grace (`suggestedRevokeGraceDays`) is the padding between
"recently stale" and "well past due". A never-used key gets a firm
`REVOKE` — no reason to keep a key that was minted but never touched.
The uncertain fallback recommends `SNOOZE` so the safe default nudges the
operator to defer rather than decide. The action is advisory only; the
operator is free to pick any of the three.

The heuristic's only non-deterministic input is `now()`, which tests pin
via `WithClock`.

---

## 4. The snooze flow

`SnoozeAPIKeyReview` defers the next nudge for one key:

1. Validate `key_id` and `actor_id` are set and `days ∈ [1, 90]`. The
   range is checked explicitly (not `if days > 0`) so a caller can't pass
   `0` to secretly *clear* the snooze through this endpoint — clearing is
   a separate path if it's ever needed.
2. Look up the key's tenant + owner once (`GetTenantIDForKey`), used both
   for the tenant cross-check and to build the audit payload without a
   second round-trip.
3. Persist `review_snoozed_until = now + days*24h` via
   `SetReviewSnoozedUntil`.
4. Emit `auth.access_review.snoozed`.
5. Return a fresh `StaleKey` shape so the FE can render the snoozed badge
   optimistically without a list refetch.

`review_snoozed_until` is a nullable `TIMESTAMPTZ` on `api_keys`
(migration `20260703000001_api_keys_review_snoozed.sql`). NULL = never
snoozed. The column accepts any absolute timestamp, so future automation
could extend the window without a schema change; today only the
`[1, 90]`-day operator path writes it.

---

## 5. BFF routes

Both routes are auth-gated; the RBAC decision is made *inside* each
handler so the rule can be "workspace-admin OR key-owner" per row rather
than a coarse admin blanket. `tenant_id` and `actor_id` always come from
the JWT (`middleware.TenantIDFromContext` / `UserIDFromContext`) — never
from the request body.

### `GET /api/v1/access/review/stale`

Returns the stale-key list. Admins get the full workspace list; non-admin
callers get only the keys they own. A non-admin with zero owned stale
keys gets an **empty list, not a 403** — the panel renders the same
"Nothing to review today" empty state for everyone.

### `POST /api/v1/access/review/snooze`

Body: `{ "key_id": "...", "days": 30 }`. Defers the next nudge.

- `days ∈ [1, 90]` is revalidated at the BFF (400 before the RPC) for
  defence-in-depth and a legible error.
- **Pre-flight tenant-scoped scan (SEC-068):** every caller — admin or
  not — first resolves the key against *their own* tenant's stale list
  (`ListStaleKeys` is tenant-scoped). `ownerOfKey` walks that result:
  - key not in the caller's list → **404 `api key not found`**. The 404
    is deliberately opaque across "doesn't exist", "belongs to another
    tenant", and "exists here but isn't stale" so an attacker can't
    distinguish the branches by probing key UUIDs.
  - key present but owned by someone else, caller not admin → **403**.
- **Tenant assertion on the wire (SEC-069):** the BFF passes `tenant_id`
  into `SnoozeAPIKeyReviewRequest`. The auth service cross-checks it
  against the key's own tenant and returns `NotFound` (opaque, not
  `PermissionDenied`) on mismatch — a service-layer backstop so even a
  future BFF regression that dropped the pre-flight scan can't push a
  cross-tenant snooze to the DB write.

gRPC codes map to HTTP via `mapAccessReviewGRPCError`:
`InvalidArgument → 400`, `NotFound → 404`, `PermissionDenied → 403`,
`Unimplemented → 501` (feature not wired at startup), deadline/cancel
→ 503.

---

## 6. Audit trail

`services/audit`'s `eventconsumer` maps both events to `audit_events`:

| Event | `action` | Actor | Notes |
|---|---|---|---|
| `auth.access_review.due` | `auth.access_review.due` | `system` | One per stale key per tick; drives the notification bell. |
| `auth.access_review.snoozed` | `auth.access_review.snoozed` | JWT `sub` of the operator (falls back to `system` if empty) | Records who deferred + until when. |

`Resource` is the key id on both, so `/activity` groups the review and
snooze next to the key's other events.

**SEC-070 — malformed-payload drop:** both cases run
`json.Unmarshal` and, on error, log + **return nil** (ACK the message,
insert nothing) rather than stamping a blank-`Resource` row into
`audit_events`. The same hardening is a pending consumer-wide follow-up
for the older `mapEvent` cases (tracked in `security.md#SEC-070`).

The two routing keys and their payload shapes
(`AccessReviewDuePayload`, `AccessReviewSnoozedPayload`) are defined in
`libs/rabbitmq/events/events.go` and documented in
[`docs/EVENTS.md`](EVENTS.md). `AccessReviewDuePayload` carries an
optional `days_idle` the worker computes from `last_used_at`.

---

## 7. The weekly worker

`worker/access_review.go` runs one goroutine started at auth-service boot:

- **Cadence:** every 7 days, with an **immediate first tick** so a fresh
  boot doesn't leave the review queue empty for a week. Both are
  injectable (`WithTickPeriod`, `WithClock`) for tests.
- **Enumeration:** `ListTenantsWithActiveKeys` returns every workspace
  with at least one active key (reusing the `idx_api_keys_idle_check`
  partial index). Workspaces with only revoked keys are skipped as noise.
- **Single-flight per workspace:** `pg_try_advisory_lock` keyed on an
  FNV-64a hash of the tenant id, salted with `"access-review:"`. If
  another auth replica already holds the lock the tick skips silently.
  The salt namespaces this lock apart from FUT-003's idle-revoke lock
  (`"idle-revoke:"`), so the two workers can sweep the same workspace in
  parallel without contending. The lock is session-scoped, so the worker
  pins one pooled connection for the lock/unlock pair.
- **Failure isolation:** any error on one workspace logs and continues to
  the next; a per-key publish failure logs and continues to the next key.
  A broker hiccup never skips the rest of a sweep.
- **Nudge-only:** the worker never revokes. That job is FUT-003's
  idle-revoke worker.

---

## 8. Deferred & related work

- **"Send reminders" email button — FUT-019 (shipped 2026-07-07).** The
  panel button drives the live email transport (Resend / SMTP / Gmail);
  see the FUT-019 email-channel plan and [`docs/SERVICES.md` §10](SERVICES.md#10-registry-audit).
- **Idle auto-revoke — FUT-003.** The auto-action counterpart. FUT-004 is
  strictly the nudge; FUT-003 owns the `idle_revoke_days` policy and the
  worker that actually revokes. See [`docs/TOKEN-POLICIES.md`](TOKEN-POLICIES.md).
- **Clear-snooze path.** There is intentionally no operator endpoint to
  clear a snooze early; the repository supports a nil `until` but no route
  exposes it today.

---

## 9. File reference

| File | Why it exists |
|---|---|
| `services/auth/internal/service/access_review.go` | `ListStaleKeys`, `SnoozeAPIKeyReview`, the suggested-action heuristic + audit emit |
| `services/auth/internal/worker/access_review.go` | Weekly worker, advisory lock, `auth.access_review.due` emit |
| `services/auth/internal/repository/apikey.go` | `ListStaleKeys` / `SetReviewSnoozedUntil` / `GetTenantIDForKey` queries + `StaleKey` shape |
| `services/auth/migrations/20260703000001_api_keys_review_snoozed.sql` | Adds `api_keys.review_snoozed_until` |
| `services/management/internal/handler/access_review.go` | BFF `stale` + `snooze` routes, SEC-068 pre-flight, 404 anti-enumeration |
| `services/audit/internal/eventconsumer/consumer.go` | Maps both events to `audit_events`; SEC-070 malformed-payload drop |
| `libs/rabbitmq/events/events.go` | `RoutingAccessReview{Due,Snoozed}` + payload structs |
| `frontend/src/lib/api/access-review.ts` | TanStack Query hooks for the two BFF routes |

---

> **Last updated:** see `git log -- docs/ACCESS-REVIEW.md`.
> **Found a gap?** The code is the source of truth — any divergence
> between it and this file is the file's bug.
