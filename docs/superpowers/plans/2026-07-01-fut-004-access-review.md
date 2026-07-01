# FUT-004 Access Review Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lift `/api-keys/review` from preview to live — a weekly background job flags API keys that are stale (`last_used_at < threshold`) OR have a lapsed rotation deadline (`rotation_due_at < now()`); operators see the list in `/api-keys/review` and pick per-row Revoke / Keep / Snooze 30d. Last of the FUT-001..004 batch.

**Architecture:**
- No new table — reuses FUT-003's `api_keys.last_used_at` + `rotation_due_at` + `token_policies.idle_revoke_days` (default 90d if unset).
- One new column: `api_keys.review_snoozed_until TIMESTAMPTZ`.
- Weekly background worker (`services/auth/internal/worker/access_review.go`) with per-tenant `pg_try_advisory_lock` (multi-replica safe, mirrors FUT-003's idle-revoke worker).
- Nudge-only (spec Decision #4) — the worker DOES NOT auto-revoke; it emits `auth.access_review_due` audit + notification events + surfaces the list. FUT-003's `idle_revoke` is the auto-action; FUT-004 is the human-in-the-loop review.
- 2 new gRPC RPCs: `ListStaleKeys(tenant) → {keys, suggested_action}` + `SnoozeAPIKeyReview(key_id, days)`.
- 2 new BFF routes: `GET /api/v1/access/review/stale` (workspace-admin OR owner) + `POST /api/v1/access/review/snooze`. Revoke reuses existing `DELETE /api/v1/api-keys/:id`.
- FE: live `ReviewPanel` with 3 buttons (Revoke / Keep / Snooze 30d) per row + toasts.
- Sidebar graduation: last one — `Access review` graduates from Preview → Workspace. Preview count 1 → 0 (Preview section collapses entirely).

**Tech Stack:** Go 1.25 / `pgx/v5`; React 18 / TanStack Query / Vitest.

**Spec:** [`../specs/2026-06-30-api-keys-tier2-backend-design.md`](../specs/2026-06-30-api-keys-tier2-backend-design.md) §Feature 4 (FUT-004).

**Branch:** `feat/fut-004-access-review` (already off `main`).

**Sequencing note:** FUT-004 hard-depended on FUT-003's `last_used_at` + `rotation_due_at`. Both shipped in PR #225 + hotfix #226. Ready to build.

---

## File Structure

**Created:**

| Path | Responsibility |
|---|---|
| `services/auth/migrations/20260703000001_api_keys_review_snoozed.sql` | `api_keys.review_snoozed_until` column |
| `services/auth/internal/service/access_review.go` | `AccessReviewService`: `ListStaleKeys` + `SnoozeAPIKeyReview` |
| `services/auth/internal/service/access_review_test.go` | Service tests (staleness thresholds + suggested_action heuristic + snooze validation) |
| `services/auth/internal/worker/access_review.go` | Weekly cron: emit `auth.access_review_due` + notifications |
| `services/auth/internal/worker/access_review_test.go` | Worker tests (fake clock + advisory lock + skip snoozed) |
| `services/auth/internal/handler/grpc_access_review.go` | 2 gRPC handlers |
| `services/auth/internal/handler/grpc_access_review_test.go` | Handler tests |
| `services/management/internal/handler/access_review.go` | 2 BFF routes |
| `services/management/internal/handler/access_review_test.go` | BFF tests |
| `frontend/src/lib/api/access-review.ts` | `useStaleKeys()` + `useSnoozeKey()` hooks |
| `frontend/src/components/access/ReviewPanel.tsx` | Live panel (replaces preview) |
| `frontend/src/components/access/__tests__/ReviewPanel.test.tsx` | Component tests |

**Modified:**

| Path | Why |
|---|---|
| `proto/auth/v1/auth.proto` | Add `ListStaleKeys` + `SnoozeAPIKeyReview` RPCs + `StaleKey` message + suggested_action enum |
| `proto/gen/go/auth/v1/*.pb.go` | Regenerated stubs |
| `services/auth/internal/repository/apikey.go` | Add `ListStaleKeys(tenantID, cutoff) []StaleKey` + `SetReviewSnoozedUntil(keyID, until)` methods |
| `libs/rabbitmq/events/events.go` | Add `RoutingAccessReviewDue` + `RoutingAccessReviewSnoozed` constants + payload types |
| `services/audit/internal/eventconsumer/consumer.go` | Add mapEvent cases for the 2 new routing keys |
| `services/auth/internal/handler/grpc.go` | Add `accessReview` field + `WithAccessReviewService` |
| `services/auth/internal/server/server.go` | Wire `AccessReviewService` + `publishAccessReviewDue` / `publishAccessReviewSnoozed` emitter cases + start worker goroutine |
| `services/management/internal/handler/handler.go` | Register 2 new routes |
| `frontend/vite.config.ts` | Add `/api/v1/access/review` → `:8091` proxy entry ABOVE `/api/v1/access` catchall |
| `frontend/src/routes/_authenticated.api-keys.review.tsx` | Swap `ReviewPreview` for `ReviewPanel` |
| `frontend/src/components/access/AccessSubNav.tsx` | Move `Access review` to Workspace section. Preview count 1 → 0 (entire Preview section collapses; consider dropping it entirely from `SECTIONS` if empty, or leave the button as-is for future features) |
| `frontend/src/components/access/__tests__/AccessSubNav.test.tsx` | Final graduation regression test — Preview section is empty/removed |

**Deleted:**

| Path | Why |
|---|---|
| `frontend/src/components/access/previews/ReviewPreview.tsx` | Replaced by `ReviewPanel` |

**Tracker:**
- `status-tracker.md` — add `REM-026` when work starts; remove on merge
- `status.md` — resolution row on merge
- `futures.md` — collapse FUT-004 to `**DONE — see status.md (REM-026)**`

---

## Task 1: Proto — 2 RPCs + StaleKey + SuggestedAction enum + payloads

**Files:**
- Modify: `proto/auth/v1/auth.proto`
- Regenerate: `proto/gen/go/auth/v1/*.pb.go`

- [ ] **Step 1.1: Add messages after the FUT-003 TokenPolicy block**

```protobuf
// FUT-004 — periodic access review. A weekly worker flags stale keys
// (last_used_at older than the tenant's idle_revoke_days threshold OR
// rotation_due_at in the past); operators pick Revoke / Keep / Snooze 30d.
// Nudge-only — the worker does NOT auto-revoke; FUT-003's idle_revoke is
// the auto-action.

enum SuggestedAction {
  SUGGESTED_ACTION_UNSPECIFIED = 0;
  SUGGESTED_ACTION_REVOKE      = 1;
  SUGGESTED_ACTION_KEEP        = 2;
  SUGGESTED_ACTION_SNOOZE      = 3;
}

message StaleKey {
  string id                                   = 1;
  string tenant_id                            = 2;
  string owner_user_id                        = 3;
  string name                                 = 4;
  google.protobuf.Timestamp last_used_at      = 5;
  google.protobuf.Timestamp rotation_due_at   = 6;
  google.protobuf.Timestamp review_snoozed_until = 7;
  SuggestedAction suggested_action            = 8;
  string reason                               = 9;  // "idle" | "rotation_lapsed" | "both"
}

message ListStaleKeysRequest  { string tenant_id = 1; }
message ListStaleKeysResponse { repeated StaleKey keys = 1; }

message SnoozeAPIKeyReviewRequest {
  string key_id   = 1;
  int32  days     = 2;  // must be in [1, 90]
  string actor_id = 3;
}
```

- [ ] **Step 1.2: Add RPCs**

```protobuf
  // FUT-004 — access review (nudge-only stale-key surface).
  rpc ListStaleKeys(ListStaleKeysRequest) returns (ListStaleKeysResponse);
  rpc SnoozeAPIKeyReview(SnoozeAPIKeyReviewRequest) returns (StaleKey);
```

- [ ] **Step 1.3: Regenerate + commit**

```bash
make proto
git add proto/auth/v1/auth.proto proto/gen/go/auth/
git commit -m "feat(proto/auth): add ListStaleKeys + SnoozeAPIKeyReview RPCs (FUT-004)"
```

---

## Task 2: Migration — `api_keys.review_snoozed_until`

**Files:**
- Create: `services/auth/migrations/20260703000001_api_keys_review_snoozed.sql`

- [ ] **Step 2.1: Write the migration**

```sql
-- +goose Up
-- +goose StatementBegin

-- FUT-004 — access review snooze. Operator-picked deferral date; the
-- weekly access-review worker skips keys whose review_snoozed_until is
-- in the future. NULL = never snoozed (default).
ALTER TABLE api_keys ADD COLUMN review_snoozed_until TIMESTAMPTZ;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

ALTER TABLE api_keys DROP COLUMN IF EXISTS review_snoozed_until;

-- +goose StatementEnd
```

- [ ] **Step 2.2: Commit**

```bash
git add services/auth/migrations/20260703000001_api_keys_review_snoozed.sql
git commit -m "feat(auth): migration — api_keys.review_snoozed_until (FUT-004)"
```

---

## Task 3: Repository — `ListStaleKeys` + `SetReviewSnoozedUntil`

**Files:**
- Modify: `services/auth/internal/repository/apikey.go`
- Modify: `services/auth/internal/repository/apikey_test.go` (or the FUT-003 integration test file — pick the existing testcontainers pattern)

- [ ] **Step 3.1: Add `StaleKey` struct + methods**

```go
// StaleKey is the projection ListStaleKeys returns — the columns the
// access-review worker + FE both need to display + reason about staleness.
type StaleKey struct {
    ID                 uuid.UUID
    TenantID           uuid.UUID
    OwnerUserID        uuid.UUID
    Name               string
    LastUsedAt         *time.Time
    RotationDueAt      *time.Time
    ReviewSnoozedUntil *time.Time
}

// ListStaleKeys returns non-revoked keys whose last_used_at is older
// than the given cutoff OR whose rotation_due_at is in the past.
// Snoozed keys (review_snoozed_until > now()) are excluded.
// NULL last_used_at counts as stale (never-used keys are always due for
// review). Uses the FUT-003 partial idle-check index.
func (r *APIKeyRepo) ListStaleKeys(ctx context.Context, tenantID uuid.UUID, staleCutoff time.Time) ([]StaleKey, error) {
    rows, err := r.pool.Query(ctx, `
        SELECT id, tenant_id, owner_user_id, name, last_used_at, rotation_due_at, review_snoozed_until
          FROM api_keys
         WHERE tenant_id = $1
           AND is_active = true
           AND (review_snoozed_until IS NULL OR review_snoozed_until < now())
           AND (
                 last_used_at IS NULL
                 OR last_used_at < $2
                 OR (rotation_due_at IS NOT NULL AND rotation_due_at < now())
               )
    `, tenantID, staleCutoff)
    if err != nil { return nil, codes.MapDBError(err) }
    defer rows.Close()
    var out []StaleKey
    for rows.Next() {
        var k StaleKey
        if err := rows.Scan(&k.ID, &k.TenantID, &k.OwnerUserID, &k.Name,
                            &k.LastUsedAt, &k.RotationDueAt, &k.ReviewSnoozedUntil); err != nil {
            return nil, err
        }
        out = append(out, k)
    }
    return out, rows.Err()
}

// SetReviewSnoozedUntil marks the key as "operator explicitly deferred
// this review until <until>." Passing a nil `until` clears the snooze.
// The weekly worker skips any row whose review_snoozed_until is in the
// future. Existing row is a no-op if `until` equals current value.
func (r *APIKeyRepo) SetReviewSnoozedUntil(ctx context.Context, keyID uuid.UUID, until *time.Time) error {
    _, err := r.pool.Exec(ctx,
        `UPDATE api_keys SET review_snoozed_until = $2 WHERE id = $1`,
        keyID, until)
    return err
}

// GetTenantIDForKey — small helper the BFF needs to enforce "workspace
// admin OR owner" before calling snooze. Returns ErrNotFound when the
// key doesn't exist so the BFF can return 404 without leaking existence.
func (r *APIKeyRepo) GetTenantIDForKey(ctx context.Context, keyID uuid.UUID) (uuid.UUID, uuid.UUID, error) {
    var tenantID, ownerUserID uuid.UUID
    err := r.pool.QueryRow(ctx,
        `SELECT tenant_id, owner_user_id FROM api_keys WHERE id = $1`, keyID,
    ).Scan(&tenantID, &ownerUserID)
    if errors.Is(err, pgx.ErrNoRows) {
        return uuid.Nil, uuid.Nil, ErrNotFound
    }
    return tenantID, ownerUserID, err
}
```

- [ ] **Step 3.2: Tests**

Testcontainers sub-tests (mirror FUT-003 `TestIdleRevoke`):
- `ListStaleKeys_IdleKeyReturned`
- `ListStaleKeys_RotationLapsedReturned`
- `ListStaleKeys_NeverUsedReturned` (NULL last_used_at is stale)
- `ListStaleKeys_SnoozedKeyExcluded`
- `ListStaleKeys_RecentKeyExcluded`
- `ListStaleKeys_RevokedKeyExcluded`
- `SetReviewSnoozedUntil_Updates + Clears`
- `GetTenantIDForKey_ReturnsNotFoundForMissing`

- [ ] **Step 3.3: Commit**

```bash
git add services/auth/internal/repository/apikey.go services/auth/internal/repository/apikey_test.go
git commit -m "feat(auth): APIKeyRepo ListStaleKeys + SetReviewSnoozedUntil (FUT-004)"
```

---

## Task 4: Service — `AccessReviewService`

**Files:**
- Create: `services/auth/internal/service/access_review.go`
- Create: `services/auth/internal/service/access_review_test.go`

Small service. Methods:

- `ListStaleKeys(ctx, tenantID) → []StaleKeyView` — loads the tenant's `token_policies.idle_revoke_days` (default 90 if unset), computes `staleCutoff = now - idle_revoke_days`, calls repo `ListStaleKeys`, decorates each with `SuggestedAction` + `Reason` per heuristic below.
- `SnoozeAPIKeyReview(ctx, keyID, days, actorID) → StaleKeyView` — validate `days ∈ [1, 90]`; compute `until = now + days`; call repo `SetReviewSnoozedUntil`; emit `auth.access_review.snoozed` audit; return the updated row (loaded via existing `GetByID`).

**Suggested-action heuristic** (per spec §Feature 4):
- If `rotation_due_at IS NOT NULL AND rotation_due_at < now()`: `SUGGESTED_ACTION_REVOKE`, `reason = "rotation_lapsed"`.
- Else if `last_used_at < (staleCutoff - 14d)` (well past the threshold): `SUGGESTED_ACTION_REVOKE`, `reason = "idle"`.
- Else if `last_used_at` is within 14 days of the threshold: `SUGGESTED_ACTION_KEEP`, `reason = "idle"`.
- Else: `SUGGESTED_ACTION_SNOOZE` (uncertain case), `reason = "idle"`.

Tests:
- `ListStaleKeys_UsesDefaultThresholdWhenPolicyUnset` (90d)
- `ListStaleKeys_UsesPolicyIdleRevokeDaysWhenSet` (e.g. 30d)
- `ListStaleKeys_SuggestedActionHeuristics` — table-driven, 4 cases
- `SnoozeAPIKeyReview_RejectsDaysOutOfRange` (0, 91)
- `SnoozeAPIKeyReview_EmitsAuditEvent`

Commit:

```bash
git add services/auth/internal/service/access_review.go services/auth/internal/service/access_review_test.go
git commit -m "feat(auth): AccessReviewService with suggested_action heuristic (FUT-004)"
```

---

## Task 5: Weekly worker — `access_review`

**Files:**
- Create: `services/auth/internal/worker/access_review.go`
- Create: `services/auth/internal/worker/access_review_test.go`

Mirror FUT-003's `idle_revoke` worker shape (advisory lock + fake clock):

- Weekly cron (`tickPeriod = 7 * 24 * time.Hour`, overridable via `WithTickPeriod` for tests).
- Immediate first tick on boot.
- For each tenant with any stale key (query all tenants — spec allows a simpler `SELECT DISTINCT tenant_id FROM api_keys WHERE is_active = true`; if we want to be even tighter, restrict to tenants whose `idle_revoke_days` is set or fall back to 90d default), take `pg_try_advisory_lock('access-review:' || tenant_id)`.
- Call `AccessReviewService.ListStaleKeys`.
- For each stale key: emit `auth.access_review_due` audit event with the key id + reason + owner id.

The worker does NOT create `notification_events` rows itself — the audit consumer's mapEvent case for `RoutingAccessReviewDue` is responsible for that (FUT-019's notification bell pattern). If the audit consumer's notification path doesn't exist yet, add a comment noting the plumbing is via the existing `FUT-019 Phase 1` bell feed (already shipped).

Tests:
- `Tick_EmitsAuditPerStaleKey`
- `Tick_SkipsSnoozedKeys`
- `Tick_HonoursAdvisoryLock`
- `Tick_UsesDefaultThresholdWhenPolicyUnset`
- `Tick_EmitsRotationLapsedReason` (seed a key with `rotation_due_at < now()` but `last_used_at` fresh)

Commit:

```bash
git add services/auth/internal/worker/access_review.go services/auth/internal/worker/access_review_test.go
git commit -m "feat(auth): weekly access-review worker (FUT-004)"
```

---

## Task 6: gRPC handlers

**Files:**
- Create: `services/auth/internal/handler/grpc_access_review.go`
- Create: `services/auth/internal/handler/grpc_access_review_test.go`

Thin wrappers. Mirror FUT-001 `grpc_oidc_trust.go` / FUT-003 `grpc_token_policy.go` shape.

Commit:

```bash
git add services/auth/internal/handler/grpc_access_review.go services/auth/internal/handler/grpc_access_review_test.go services/auth/internal/handler/grpc.go
git commit -m "feat(auth): gRPC ListStaleKeys + SnoozeAPIKeyReview (FUT-004)"
```

---

## Task 7: Audit events

**Files:**
- Modify: `libs/rabbitmq/events/events.go`
- Modify: `services/auth/internal/server/server.go` (add 2 new publisher cases)
- Modify: `services/audit/internal/eventconsumer/consumer.go` (add 2 new mapEvent cases)

**Learned from #226 hotfix:** every new routing key MUST have a case in `rabbitMQAuditEmitter.Emit`. Do NOT rely on the `default:` arm.

Add:

```go
const RoutingAccessReviewDue      = "auth.access_review.due"
const RoutingAccessReviewSnoozed  = "auth.access_review.snoozed"

type AccessReviewDuePayload struct {
    TenantID    string `json:"tenant_id"`
    KeyID       string `json:"key_id"`
    OwnerUserID string `json:"owner_user_id"`
    Name        string `json:"name"`
    Reason      string `json:"reason"` // "idle" | "rotation_lapsed" | "both"
    DaysIdle    int32  `json:"days_idle,omitempty"`
}

type AccessReviewSnoozedPayload struct {
    TenantID       string `json:"tenant_id"`
    KeyID          string `json:"key_id"`
    ActorID        string `json:"actor_id"`
    SnoozedUntil   string `json:"snoozed_until"` // RFC3339
    DaysSnoozed    int32  `json:"days_snoozed"`
}
```

In `server.go rabbitMQAuditEmitter.Emit`:

```go
case events.RoutingAccessReviewDue:
    return e.publishAccessReviewDue(ctx, ev)
case events.RoutingAccessReviewSnoozed:
    return e.publishAccessReviewSnoozed(ctx, ev)
```

Add helpers that unmarshal from `Fields[payload_json]` (same pattern FUT-003 hotfix #226 established for `publishTokenPolicyChanged`) OR construct the payload from individual Fields — either works, pick one consistent with the emitter surface.

In `consumer.go`, add mapEvent cases per the CLAUDE.md §10 catalogue invariant.

Commit:

```bash
git add libs/rabbitmq/events/events.go services/auth/internal/server/server.go services/audit/internal/eventconsumer/consumer.go
git commit -m "feat(audit): catalogue access_review.due + .snoozed (FUT-004)"
```

---

## Task 8: BFF — 2 routes on `services/management`

**Files:**
- Create: `services/management/internal/handler/access_review.go`
- Create: `services/management/internal/handler/access_review_test.go`
- Modify: `services/management/internal/handler/handler.go`

Routes:

- `GET /api/v1/access/review/stale` — **workspace-admin OR owner-of-any-listed-key**. Spec says "Owners see their own keys; admins see all." Filter at the BFF: admins get the full list; non-admin callers get the subset where `owner_user_id == caller.sub`.
- `POST /api/v1/access/review/snooze` — body `{key_id, days}`. Gate: workspace-admin OR owner of that specific key. Validate `days ∈ [1, 90]` at the BFF too (defence in depth on top of BE).

Revoke goes through existing `DELETE /api/v1/api-keys/:id`.

Auth pattern: mirror the existing owner-vs-admin gate in the codebase — search for handlers that already discriminate on ownership (activity, notifications). If no such pattern exists, use `isTenantAdminOrPlatformAdmin` for the list route and require workspace-admin for the snooze route too. Note the compromise in a follow-up REM item.

Actor id from JWT `sub` → gRPC `SnoozeAPIKeyReviewRequest.ActorID`.

Sub-tests:
- 2 happy-path (admin list + admin snooze)
- 2 admin-deny (non-admin list returns 200 with empty; non-admin snooze of foreign key returns 403 or 404)
- Owner-of-key can snooze their own key

Commit:

```bash
git add services/management/internal/handler/access_review.go services/management/internal/handler/access_review_test.go services/management/internal/handler/handler.go
git commit -m "feat(management): access-review stale + snooze BFF routes (FUT-004)"
```

---

## Task 9: FE — hooks + Vite proxy

**Files:**
- Create: `frontend/src/lib/api/access-review.ts`
- Modify: `frontend/vite.config.ts`

```typescript
export interface StaleKey {
  id: string;
  tenant_id: string;
  owner_user_id: string;
  name: string;
  last_used_at: string | null;
  rotation_due_at: string | null;
  review_snoozed_until: string | null;
  suggested_action: "REVOKE" | "KEEP" | "SNOOZE" | "UNSPECIFIED";
  reason: "idle" | "rotation_lapsed" | "both" | "";
}

export function useStaleKeys() { /* GET /access/review/stale */ }
export function useSnoozeKey() { /* POST /access/review/snooze, invalidate ['stale-keys'] */ }
```

Vite proxy: add ABOVE `/api/v1/access` catchall:

```typescript
"/api/v1/access/review": { target: "http://localhost:8091", changeOrigin: true },
```

Commit:

```bash
git add frontend/src/lib/api/access-review.ts frontend/vite.config.ts
git commit -m "feat(frontend): useStaleKeys + useSnoozeKey hooks + Vite proxy (FUT-004)"
```

---

## Task 10: FE — `ReviewPanel`

**Files:**
- Create: `frontend/src/components/access/ReviewPanel.tsx`
- Create: `frontend/src/components/access/__tests__/ReviewPanel.test.tsx`
- Modify: `frontend/src/routes/_authenticated.api-keys.review.tsx`
- Delete: `frontend/src/components/access/previews/ReviewPreview.tsx`

Component (mirror `TrustPanel.tsx`):
- Unconditional `<header>` with heading "Access review" + description
- Loading / error / empty ("Nothing to review today — all keys are fresh") states
- Amber alert banner at top: "N keys due for review" (count from data)
- Table: `Key name | Owner | Last used | Reason | Actions`
- Three action buttons per row:
  - **Revoke** — calls existing `useRevokeAPIKey()` hook; visual emphasis when `suggested_action === REVOKE`
  - **Keep** — clears any snooze (calls `useSnoozeKey().mutate({key_id, days: 0})` OR add an explicit clear endpoint; simplest: just close the row visually — the next weekly worker tick will re-surface if still stale)
  - **Snooze 30d** — calls `useSnoozeKey().mutate({key_id, days: 30})`
- Toast on success ("Key revoked" / "Snoozed for 30 days") — use sonner (already vendored; check sibling patterns)

Tests (mirror `TrustPanel.test.tsx` depth per REM-021 lesson):
- Renders heading + no `<PreviewBanner>` (SR-only `/Sprint 12.*FUT-004/i` not present)
- Loading state
- Empty state ("Nothing to review today")
- Populated state renders the table with N rows
- Snooze button calls the mutation with `days: 30`
- Revoke button calls the revoke mutation
- Suggested-action visual emphasis (Revoke button has distinctive class when `suggested_action === REVOKE`)

Route swap + preview deletion.

Commit:

```bash
git add frontend/src/components/access/ReviewPanel.tsx frontend/src/components/access/__tests__/ReviewPanel.test.tsx frontend/src/routes/_authenticated.api-keys.review.tsx frontend/src/components/access/previews/ReviewPreview.tsx
git commit -m "feat(frontend): live ReviewPanel + route swap + preview deletion (FUT-004)"
```

---

## Task 11: FE — sidebar graduation (LAST ONE)

**Files:**
- Modify: `frontend/src/components/access/AccessSubNav.tsx`
- Modify: `frontend/src/components/access/__tests__/AccessSubNav.test.tsx`

Move `Access review` from Preview → Workspace section. **Preview count 1 → 0.** The Preview section is now empty.

Two options for the empty Preview section:
- **Option A (recommended):** remove the entire `Preview` entry from `SECTIONS` so the flyout expander disappears. Cleaner UI once every preview graduates.
- Option B: leave the flyout button rendered but empty for future previews. Doesn't fit the current shipped state.

Pick Option A. The `readPreviewOpen` / `PREVIEW_OPEN_KEY` localStorage plumbing can stay (dead code cleanup is a follow-up).

Update `AccessSubNav.test.tsx`:
- Drop the "shows all preview links" test entirely (Preview section is gone).
- Drop the "preview flyout state persists in localStorage" test entirely.
- Update the "shows Yours, Workspace, and Preview sections for admin users" test to just Yours + Workspace.
- Add graduation regression test asserting `Access review` renders in Workspace.
- Update the non-admin test to assert Preview is absent (no expander).

Commit:

```bash
git add frontend/src/components/access/AccessSubNav.tsx frontend/src/components/access/__tests__/AccessSubNav.test.tsx
git commit -m "feat(frontend): graduate Access review + retire Preview section (FUT-004)"
```

---

## Task 12: Tracker hygiene

- [ ] Add REM-026 to `status-tracker.md`.
- [ ] Stub FUT-004 in `futures.md`.

Commit:

```bash
git add status-tracker.md futures.md
git commit -m "chore(trackers): REM-026 FUT-004 in-flight entry + REM-025 close-out"
```

---

## Task 13: Local CI gate

- [ ] `services/{auth,management,audit}` go vet + build + test
- [ ] FE 4-gate (lint / typecheck / test / build)
- [ ] `spec-lint` all 13 rules pass

---

## Task 14: 3-agent review batch (BEFORE `gh pr create` — lessons learned)

Per `feedback_review_agents_batch.md`. Fire all 3 in a single message. **Do NOT merge before the batch lands** — FUT-003's post-merge hotfix (#226) cost extra cycles.

Priority items:

**security-agent:**
- **Owner-vs-admin gate correctness on both routes** — a non-admin should NOT see other users' stale keys; a non-admin should NOT be able to snooze another user's key.
- **`SnoozeAPIKeyReview` days-bounds enforced at BOTH BE service AND BFF handler** (`[1, 90]`).
- **Audit routing keys have explicit cases in `rabbitMQAuditEmitter.Emit`** — mirror the FUT-003 #226 lesson.

**qa-agent:**
- Worker fake-clock test for weekly cadence.
- Advisory lock test (multi-replica safety).
- Snoozed-key exclusion test (worker skips snoozed).
- FE test depth (populated state + button click flows, not just shell).
- Audit catalogue lint invariant still passes.

**code-review-agent:**
- Suggested-action heuristic tested — the 4 branches all covered.
- Worker uses `pg_advisory_unlock` in defer, not just at end.
- BFF filter for non-admin owner-see-own-keys is correct.

Fold must-fixes inline; log should-fixes to REM-026.

---

## Task 15: PR + merge + rebuild

- [ ] `git push -u origin feat/fut-004-access-review`
- [ ] `gh pr create` with body covering: summary, 3-agent verdicts, all buttons wired, Preview section retired.
- [ ] Wait for review batch to complete + fold must-fixes.
- [ ] Only then merge.
- [ ] `docker compose build registry-auth registry-audit registry-management && docker compose up -d`
- [ ] Verify: `curl` the new routes; confirm 401 without Bearer, 200 with; visit `/api-keys/review` → live panel; visit sidebar → Preview section is gone.

---

## Notes for the executor

- Per CLAUDE.md `feedback_code_comments`: every new file gets top-of-file comment + per-function doc strings.
- Per `feedback_git_workflow`: commit on the current branch. Never commit to main.
- Per CLAUDE.md §15.1: all 4 FE CI equivalents.
- Per CLAUDE.md §10: audit catalogue completeness — every new routing key MUST be in `mapEvent` OR carry `// audit: skip`.
- **NEW LESSON from FUT-003 hotfix #226:** every new routing key MUST also have an explicit case in `services/auth/internal/server/server.go rabbitMQAuditEmitter.Emit` — don't let the `default:` arm swallow it.
- **NEW LESSON from FUT-003 SEC-064:** don't guard policy enforcement on nullable input fields (like `expiresAt`). If nil could mean "caller didn't specify," think about what the safe default is + clamp/reject explicitly.
- Nudge-only posture is spec Decision #4 — the worker MUST NOT auto-revoke. FUT-003's `idle_revoke` is the auto-action. FUT-004's job is the human-in-the-loop review.
- Empty Preview section retirement — this is the LAST FUT of the batch, so the Preview flyout finally disappears from the sidebar. Enjoy.
