# FUT-003 Token Policies Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Lift `/api-keys/policies` from preview to live — workspace-admin sets a single policy (`max_ttl_days`, `rotation_interval_days`, `idle_revoke_days`), the auth service enforces the cap on new keys, an hourly background worker revokes idle keys, and every API key gains a `last_used_at` timestamp that FUT-004 will consume.

**Architecture:**
- New `token_policies` table on `services/auth` (one row per tenant, all fields nullable — NULL = policy disabled for that dimension).
- New `api_keys.last_used_at` (5-min Redis-debounced updater on every `ValidateAPIKey`; fail-OPEN on Redis).
- New `api_keys.rotation_due_at` (set on create when `rotation_interval_days` is configured; consumed by FUT-004).
- Enforcement at `CreateAPIKey`: reject requested TTL > cap with `codes.InvalidArgument`. **Grandfather** existing keys (they keep working; only new keys check the cap).
- Background worker: hourly cron, `UPDATE ... WHERE last_used_at < now() - idle_revoke_days`, emit `auth.key_revoked` with `reason = idle_revoked`.
- 2 new gRPC RPCs (`GetTokenPolicy` / `PutTokenPolicy`) + 2 BFF routes.
- FE: live `PoliciesPanel` replaces preview; sidebar graduation.

**Tech Stack:** Go 1.25 / `pgx/v5` / `redis/go-redis/v9` (already vendored); React 18 / TanStack Query / Vitest.

**Spec:** [`../specs/2026-06-30-api-keys-tier2-backend-design.md`](../specs/2026-06-30-api-keys-tier2-backend-design.md) §Feature 3 (FUT-003).

**Branch:** `feat/fut-003-token-policies` (already created off updated `main`).

**Sequencing note:** FUT-003 must land BEFORE FUT-004 — FUT-004 hard-depends on the `last_used_at` + `rotation_due_at` columns this PR adds.

---

## File Structure

**Created:**

| Path | Responsibility |
|---|---|
| `services/auth/migrations/20260702000001_token_policies.sql` | `token_policies` table + `api_keys.last_used_at` + `api_keys.rotation_due_at` + partial index |
| `services/auth/internal/repository/token_policy.go` | CRUD on `token_policies` (single row per tenant) |
| `services/auth/internal/repository/token_policy_test.go` | Repo tests (testcontainers) |
| `services/auth/internal/service/token_policy.go` | `TokenPolicyService` (Get/Put + input validation) |
| `services/auth/internal/service/token_policy_test.go` | Service tests |
| `services/auth/internal/service/last_used_debounce.go` | Redis-debounced `last_used_at` updater helper |
| `services/auth/internal/service/last_used_debounce_test.go` | Debounce tests (miniredis) |
| `services/auth/internal/worker/idle_revoke.go` | Hourly cron loop; per-tenant revoke of idle keys |
| `services/auth/internal/worker/idle_revoke_test.go` | Worker tests (testcontainers + fake clock) |
| `services/auth/internal/handler/grpc_token_policy.go` | 2 gRPC admin handlers |
| `services/auth/internal/handler/grpc_token_policy_test.go` | gRPC handler tests |
| `services/management/internal/handler/access_token_policy.go` | 2 BFF admin routes |
| `services/management/internal/handler/access_token_policy_test.go` | BFF tests |
| `frontend/src/lib/api/token-policy.ts` | `useTokenPolicy()` + `usePutTokenPolicy()` hooks |
| `frontend/src/components/access/PoliciesPanel.tsx` | Live panel (replaces preview) |
| `frontend/src/components/access/__tests__/PoliciesPanel.test.tsx` | Component tests |

**Modified:**

| Path | Why |
|---|---|
| `proto/auth/v1/auth.proto` | Add 2 RPCs + `TokenPolicy` + `Get/PutTokenPolicyRequest/Response` messages; extend `KeyRevokedPayload` with `reason` enum |
| `proto/gen/go/auth/v1/*.pb.go` | Regenerated stubs |
| `services/auth/internal/service/auth.go` | Enforce `max_ttl_days` in `CreateAPIKey`; set `rotation_due_at`; call debounced updater in `ValidateAPIKey` |
| `services/auth/internal/repository/apikey.go` | Add `UpdateLastUsedAt` + `SetRotationDueAt` + `ListIdleKeys` methods |
| `services/auth/internal/handler/grpc.go` | Add `tokenPolicy` field + `WithTokenPolicyService` |
| `services/auth/internal/server/server.go` | Wire `TokenPolicyService` + start idle-revoke worker goroutine |
| `services/auth/cmd/server/main.go` | Pass idle-revoke worker config |
| `libs/rabbitmq/events/events.go` | Add `RoutingTokenPolicyChanged` const + payload; extend `KeyRevokedPayload.Reason` |
| `services/audit/internal/eventconsumer/consumer.go` | Add `mapEvent` case for `RoutingTokenPolicyChanged`; use reason field in `key_revoked` case |
| `services/management/internal/handler/handler.go` | Register 2 new `/api/v1/access/token-policy` routes |
| `services/management/internal/server/server.go` | Wire the BFF handler |
| `infra/docker-compose/docker-compose.yml` | No new env — the worker is on-by-default in `registry-auth` |
| `frontend/vite.config.ts` | Add `/api/v1/access/token-policy` → `:8091` proxy entry ABOVE the `/api/v1/access` catchall (same shape as FUT-001's oidc-trust proxy fix) |
| `frontend/src/routes/_authenticated.api-keys.policies.tsx` | Swap `PoliciesPreview` for `PoliciesPanel` |
| `frontend/src/components/access/AccessSubNav.tsx` | Move `Token policies` from Preview to Workspace section (Preview count 2 → 1 with FUT-001 already graduated) |
| `frontend/src/components/access/__tests__/AccessSubNav.test.tsx` | Update graduation regression tests |

**Deleted:**

| Path | Why |
|---|---|
| `frontend/src/components/access/previews/PoliciesPreview.tsx` | Replaced by `PoliciesPanel` |

**Tracker:**
- `status-tracker.md` — add `REM-024` on start; remove on merge
- `status.md` — resolution row on merge
- `futures.md` — collapse FUT-003 to `**DONE — see status.md (REM-024)**` stub

---

## Task 1: Proto — 2 RPCs + `TokenPolicy` message + `KeyRevokedPayload.reason`

**Files:**
- Modify: `proto/auth/v1/auth.proto`
- Regenerate: `proto/gen/go/auth/v1/*.pb.go`

- [ ] **Step 1.1: Add messages**

Append to the messages block (after the FUT-001 OIDC trust messages from PR #224):

```protobuf
// FUT-003 — workspace-wide token policy. All fields NULL/unset = disabled
// for that dimension. Applied at CreateAPIKey (max_ttl_days) and by the
// idle-revoke worker (idle_revoke_days). rotation_interval_days sets a
// rotation_due_at on new keys; FUT-004 surfaces the lapsed keys.
message TokenPolicy {
  string tenant_id                 = 1;
  google.protobuf.Int32Value max_ttl_days           = 2;
  google.protobuf.Int32Value rotation_interval_days = 3;
  google.protobuf.Int32Value idle_revoke_days       = 4;
  google.protobuf.Timestamp updated_at              = 5;
  string updated_by_user_id        = 6;
}

message GetTokenPolicyRequest  { string tenant_id = 1; }
message PutTokenPolicyRequest {
  string tenant_id                                  = 1;
  google.protobuf.Int32Value max_ttl_days           = 2;
  google.protobuf.Int32Value rotation_interval_days = 3;
  google.protobuf.Int32Value idle_revoke_days       = 4;
  // actor_id — the calling admin's user id (BFF plumbs this from the
  // JWT claims into the gRPC call). Recorded in updated_by_user_id
  // and in the auth.token_policy.changed audit event.
  string actor_id                                   = 5;
}
```

Import `google/protobuf/wrappers.proto` at the top if not already imported (search the file).

- [ ] **Step 1.2: Add RPCs**

After the FUT-001 exchange RPC in `service AuthService`:

```protobuf
  // FUT-003 — workspace-wide token policy.
  rpc GetTokenPolicy(GetTokenPolicyRequest) returns (TokenPolicy);
  rpc PutTokenPolicy(PutTokenPolicyRequest) returns (TokenPolicy);
```

- [ ] **Step 1.3: Regenerate + commit**

```bash
make proto
git add proto/auth/v1/auth.proto proto/gen/go/auth/
git commit -m "feat(proto/auth): add TokenPolicy Get/Put RPCs (FUT-003)"
```

---

## Task 2: Migration — `token_policies` + `api_keys` columns + index

**Files:**
- Create: `services/auth/migrations/20260702000001_token_policies.sql`

- [ ] **Step 2.1: Write the migration**

```sql
-- +goose Up
-- +goose StatementBegin

-- FUT-003 — workspace-wide token policy. One row per tenant.
-- All NULL fields = policy disabled for that dimension.
CREATE TABLE token_policies (
    tenant_id              UUID        PRIMARY KEY,
    max_ttl_days           INTEGER,             -- NULL = no cap
    rotation_interval_days INTEGER,             -- NULL = no force-rotation
    idle_revoke_days       INTEGER,             -- NULL = no idle-revoke
    updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by_user_id     UUID
);

-- api_keys.last_used_at — updated (Redis-debounced) on every successful
-- ValidateAPIKey. Consumed by the idle-revoke worker + FUT-004.
ALTER TABLE api_keys ADD COLUMN last_used_at TIMESTAMPTZ;

-- api_keys.rotation_due_at — set on CreateAPIKey when the workspace
-- policy has rotation_interval_days configured. Consumed by FUT-004.
ALTER TABLE api_keys ADD COLUMN rotation_due_at TIMESTAMPTZ;

-- Partial index for the idle-revoke worker's per-tenant scan. Filtering
-- on `revoked_at IS NULL` keeps the index small (revoked keys are dead
-- weight for the worker's query).
CREATE INDEX idx_api_keys_idle_check
    ON api_keys (tenant_id, last_used_at)
    WHERE revoked_at IS NULL;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_api_keys_idle_check;
ALTER TABLE api_keys DROP COLUMN IF EXISTS rotation_due_at;
ALTER TABLE api_keys DROP COLUMN IF EXISTS last_used_at;
DROP TABLE IF EXISTS token_policies;

-- +goose StatementEnd
```

- [ ] **Step 2.2: Commit**

```bash
git add services/auth/migrations/20260702000001_token_policies.sql
git commit -m "feat(auth): migration — token_policies + api_keys columns (FUT-003)"
```

---

## Task 3: Repository — `token_policy.go` + extend `apikey.go`

**Files:**
- Create: `services/auth/internal/repository/token_policy.go`
- Create: `services/auth/internal/repository/token_policy_test.go`
- Modify: `services/auth/internal/repository/apikey.go` (add 3 methods)

- [ ] **Step 3.1: Write the failing tests**

Mirror the FUT-001 `oidc_trust_test.go` pattern (build tag `integration`, testcontainers PG16). Sub-tests:

For `token_policy.go`:
- `GetOrDefault_ReturnsEmptyForUnsetTenant` — no row = zero policy (all NULL), not an error
- `Upsert_InsertsNewRow`
- `Upsert_UpdatesExistingRow`
- `Upsert_PreservesUnsetFieldsOnPartialUpdate` — nil pointer means "don't change"

For `apikey.go` extensions:
- `UpdateLastUsedAt_UpdatesTimestamp`
- `SetRotationDueAt_UpdatesTimestamp`
- `ListIdleKeys_ReturnsOnlyKeysOlderThanThreshold` — seed 3 keys with varying `last_used_at`, assert only the old-enough one appears
- `ListIdleKeys_ExcludesRevokedKeys` — the partial index shape

- [ ] **Step 3.2: Implement the repository**

`token_policy.go`:

```go
package repository

import (
    "context"
    "errors"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/steveokay/oci-janus/libs/errors/codes"
)

// TokenPolicy is the in-memory shape of a row in token_policies. All
// three limit fields are pointers because NULL semantically means
// "policy disabled for this dimension" — distinguishing that from
// "zero days" (which the schema forbids at validation time).
type TokenPolicy struct {
    TenantID             uuid.UUID
    MaxTTLDays           *int32
    RotationIntervalDays *int32
    IdleRevokeDays       *int32
    UpdatedAt            time.Time
    UpdatedByUserID      *uuid.UUID
}

type TokenPolicyRepo struct {
    pool *pgxpool.Pool
}

func NewTokenPolicyRepo(pool *pgxpool.Pool) *TokenPolicyRepo {
    return &TokenPolicyRepo{pool: pool}
}

// GetOrDefault returns the policy for the given tenant. If no row
// exists, returns a zero-valued TokenPolicy with all limit fields nil
// (semantically "no policy") — NOT an error. Callers that must
// distinguish "no policy" from "policy with all nil fields" should
// treat both identically per FUT-003 grandfathering semantics.
func (r *TokenPolicyRepo) GetOrDefault(ctx context.Context, tenantID uuid.UUID) (*TokenPolicy, error) {
    // SELECT with LEFT JOIN sentinel or single-row query with NoRows fallback.
    row := r.pool.QueryRow(ctx, `
        SELECT tenant_id, max_ttl_days, rotation_interval_days, idle_revoke_days,
               updated_at, updated_by_user_id
          FROM token_policies WHERE tenant_id = $1
    `, tenantID)
    var out TokenPolicy
    err := row.Scan(&out.TenantID, &out.MaxTTLDays, &out.RotationIntervalDays,
                    &out.IdleRevokeDays, &out.UpdatedAt, &out.UpdatedByUserID)
    if errors.Is(err, pgx.ErrNoRows) {
        return &TokenPolicy{TenantID: tenantID}, nil
    }
    if err != nil {
        return nil, codes.MapDBError(err)
    }
    return &out, nil
}

// Upsert inserts or updates the row for the given tenant. Fields whose
// pointer is nil are NOT touched on update — so a partial update that
// only sets max_ttl_days does not clobber rotation_interval_days.
// updated_by_user_id is always set from the input (recorded for audit
// even if all other fields are nil).
func (r *TokenPolicyRepo) Upsert(ctx context.Context, in TokenPolicy) (*TokenPolicy, error) {
    // COALESCE(EXCLUDED.field, token_policies.field) preserves the old
    // value on nil-in inputs during ON CONFLICT.
    _, err := r.pool.Exec(ctx, `
        INSERT INTO token_policies (tenant_id, max_ttl_days, rotation_interval_days,
                                    idle_revoke_days, updated_by_user_id, updated_at)
             VALUES ($1, $2, $3, $4, $5, now())
        ON CONFLICT (tenant_id) DO UPDATE SET
            max_ttl_days           = COALESCE(EXCLUDED.max_ttl_days,           token_policies.max_ttl_days),
            rotation_interval_days = COALESCE(EXCLUDED.rotation_interval_days, token_policies.rotation_interval_days),
            idle_revoke_days       = COALESCE(EXCLUDED.idle_revoke_days,       token_policies.idle_revoke_days),
            updated_by_user_id     = EXCLUDED.updated_by_user_id,
            updated_at             = now()
    `, in.TenantID, in.MaxTTLDays, in.RotationIntervalDays, in.IdleRevokeDays, in.UpdatedByUserID)
    if err != nil {
        return nil, codes.MapDBError(err)
    }
    return r.GetOrDefault(ctx, in.TenantID)
}

// Clear removes the tenant's row entirely (equivalent to "no policy").
// Not exposed at the RPC layer today — kept for future support tooling.
func (r *TokenPolicyRepo) Clear(ctx context.Context, tenantID uuid.UUID) error {
    _, err := r.pool.Exec(ctx, `DELETE FROM token_policies WHERE tenant_id = $1`, tenantID)
    return err
}
```

Extend `apikey.go` with:

```go
// UpdateLastUsedAt bumps the timestamp on the given key. Called by the
// FUT-003 Redis-debounced updater — misses (e.g. Redis unreachable +
// debounce skipped) are tolerated because the worst-case impact is a
// slightly-later idle-revoke evaluation.
func (r *APIKeyRepo) UpdateLastUsedAt(ctx context.Context, id uuid.UUID, at time.Time) error {
    _, err := r.pool.Exec(ctx,
        `UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, id, at)
    return err
}

// SetRotationDueAt records the deadline for a required rotation. Called
// during CreateAPIKey when the workspace policy has a rotation cadence.
// Nil `at` clears the deadline.
func (r *APIKeyRepo) SetRotationDueAt(ctx context.Context, id uuid.UUID, at *time.Time) error {
    _, err := r.pool.Exec(ctx,
        `UPDATE api_keys SET rotation_due_at = $2 WHERE id = $1`, id, at)
    return err
}

// IdleKey is the projection ListIdleKeys returns — the columns the
// worker + audit emitter both need.
type IdleKey struct {
    ID          uuid.UUID
    TenantID    uuid.UUID
    OwnerUserID uuid.UUID
    LastUsedAt  *time.Time
}

// ListIdleKeys returns non-revoked keys whose last_used_at is older
// than the given cutoff, restricted to the given tenant. Rows with
// NULL last_used_at are ALSO returned — a key that was created but
// never used is idle by definition. Uses the partial idx_api_keys_idle_check
// index (WHERE revoked_at IS NULL), so scans are proportional to the
// tenant's live-key count.
func (r *APIKeyRepo) ListIdleKeys(ctx context.Context, tenantID uuid.UUID, cutoff time.Time) ([]IdleKey, error) {
    rows, err := r.pool.Query(ctx, `
        SELECT id, tenant_id, owner_user_id, last_used_at
          FROM api_keys
         WHERE tenant_id = $1
           AND revoked_at IS NULL
           AND (last_used_at IS NULL OR last_used_at < $2)
    `, tenantID, cutoff)
    if err != nil { return nil, codes.MapDBError(err) }
    defer rows.Close()
    var out []IdleKey
    for rows.Next() {
        var k IdleKey
        if err := rows.Scan(&k.ID, &k.TenantID, &k.OwnerUserID, &k.LastUsedAt); err != nil {
            return nil, err
        }
        out = append(out, k)
    }
    return out, rows.Err()
}
```

(Adjust field names — the existing `APIKey` struct likely calls the owner column `UserID` or similar. Read `apikey.go` first to match.)

Run: `cd services/auth && go test ./internal/repository/ -tags=integration -run "TokenPolicy|APIKey" -v` → all pass.

- [ ] **Step 3.3: Commit**

```bash
git add services/auth/internal/repository/token_policy.go services/auth/internal/repository/token_policy_test.go services/auth/internal/repository/apikey.go
git commit -m "feat(auth): TokenPolicyRepo + apikey ListIdleKeys/UpdateLastUsedAt (FUT-003)"
```

---

## Task 4: Redis-debounced `last_used_at` updater

**Files:**
- Create: `services/auth/internal/service/last_used_debounce.go`
- Create: `services/auth/internal/service/last_used_debounce_test.go`

- [ ] **Step 4.1: Write the failing test (uses miniredis — already in test dependencies from FUT-001's rate-limit test)**

Sub-tests:
- `FirstCallWins` — SET NX succeeds → UPDATE runs
- `SecondCallWithinWindowSkipped` — SET NX fails → UPDATE does NOT run
- `SecondCallAfterWindowRuns` — expire the key, second call wins
- `RedisDown_FailOpen` — miniredis Close()'d → UPDATE runs inline (no debounce)
- `UpdateFailureIsLoggedNotFatal` — repo returns error → debounce still records the SET NX (avoids retry storms)

- [ ] **Step 4.2: Implement**

```go
package service

import (
    "context"
    "errors"
    "log/slog"
    "time"

    "github.com/google/uuid"
    "github.com/redis/go-redis/v9"

    "github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// lastUsedDebounceWindow bounds how often we write last_used_at per
// key. 5 minutes matches manifests.last_pulled_at posture — tight
// enough for FUT-004's idle-revoke evaluation, loose enough that a
// bot pulling every second doesn't hammer the write path.
const lastUsedDebounceWindow = 5 * time.Minute

// lastUsedUpdater debounces api_keys.last_used_at updates via Redis
// so high-RPS CI bots don't turn ValidateAPIKey into an unbounded
// write path. On Redis errors the debounce is skipped and the write
// runs inline — the debounce is an optimisation, not a security or
// correctness boundary.
type lastUsedUpdater struct {
    redis  *redis.Client
    repo   apiKeyLastUsedRepo
    logger *slog.Logger
}

type apiKeyLastUsedRepo interface {
    UpdateLastUsedAt(ctx context.Context, id uuid.UUID, at time.Time) error
}

func newLastUsedUpdater(rd *redis.Client, repo apiKeyLastUsedRepo, logger *slog.Logger) *lastUsedUpdater {
    return &lastUsedUpdater{redis: rd, repo: repo, logger: logger}
}

// Touch is fire-and-forget. Returns immediately; the write happens in
// a goroutine so ValidateAPIKey stays hot. Callers MUST use a context
// that outlives the request (`context.Background()` is appropriate).
func (u *lastUsedUpdater) Touch(ctx context.Context, keyID uuid.UUID) {
    go u.touchNow(ctx, keyID)
}

// touchNow is called synchronously by tests + async by Touch.
func (u *lastUsedUpdater) touchNow(ctx context.Context, keyID uuid.UUID) {
    now := time.Now().UTC()

    if u.redis != nil {
        key := "lastused:debounce:" + keyID.String()
        set, err := u.redis.SetNX(ctx, key, "1", lastUsedDebounceWindow).Result()
        if err == nil && !set {
            // Debounce wins: another request touched this key inside the
            // 5-min window. Skip the DB write.
            return
        }
        if err != nil && !errors.Is(err, redis.Nil) {
            // Redis is down or slow — fail-open. Fall through and run
            // the DB write inline. Log at Info (this is expected during
            // Redis restarts and shouldn't page).
            u.logger.Info("lastused debounce redis error; falling open", "err", err)
        }
    }

    if err := u.repo.UpdateLastUsedAt(ctx, keyID, now); err != nil {
        u.logger.Warn("lastused UPDATE failed", "key_id", keyID, "err", err)
    }
}
```

Wire the updater into `Service.ValidateAPIKey`: at the end of the successful path, call `s.lastUsedUpdater.Touch(context.Background(), key.ID)`. Test the wiring in `auth_test.go` (miniredis + fake repo — assert `Touch` was called on a successful validation, not called on failure paths).

- [ ] **Step 4.3: Commit**

```bash
git add services/auth/internal/service/last_used_debounce.go services/auth/internal/service/last_used_debounce_test.go services/auth/internal/service/auth.go
git commit -m "feat(auth): Redis-debounced last_used_at updater (FUT-003)"
```

---

## Task 5: `TokenPolicyService` (Get/Put with validation)

**Files:**
- Create: `services/auth/internal/service/token_policy.go`
- Create: `services/auth/internal/service/token_policy_test.go`

Service wrapping the repo with input validation + audit emission.

Validation (all reject with `codes.InvalidArgument`):
- Each field, if non-nil, must be in the range `[1, 3650]` (1 day to 10 years). 0 or negative rejected.
- `idle_revoke_days`, if set, must be ≥ some floor (recommend 7 days) — set-and-forget worker running against a fresh policy shouldn't nuke every key on the next tick.

On success: emit `auth.token_policy.changed` audit event with the before/after per-field diff. Payload shape defined in Task 9.

Sub-tests:
- `Put_Success`
- `Put_RejectsZeroDays`
- `Put_RejectsAboveMaxDays`
- `Put_RejectsTooShortIdleRevoke`
- `Put_EmitsAuditWithDiff`
- `Get_ReturnsPolicyOrEmpty`

- [ ] **Step 5.1–5.3: Test → impl → commit**

```bash
git add services/auth/internal/service/token_policy.go services/auth/internal/service/token_policy_test.go
git commit -m "feat(auth): TokenPolicyService Get/Put with validation + audit (FUT-003)"
```

---

## Task 6: Enforcement in `CreateAPIKey`

**Files:**
- Modify: `services/auth/internal/service/auth.go` (CreateAPIKey)

- [ ] **Step 6.1: Read the current CreateAPIKey**

```bash
grep -n "func.*CreateAPIKey\|expires_at\|ExpiresAt" services/auth/internal/service/auth.go | head -20
```

Understand the current create flow: how the expiry is set, where the SA branch splits from the human branch, etc.

- [ ] **Step 6.2: Add the policy-consultation branch**

Before persisting a new key, load the tenant's policy. If `MaxTTLDays` is set:
- If the caller-requested `expires_at - now() > MaxTTLDays * 24h`, reject with `codes.InvalidArgument`, message `"requested TTL exceeds workspace max (%d days)"`.

If `RotationIntervalDays` is set:
- Compute `rotationDueAt := now() + RotationIntervalDays * 24h`.
- Persist it on the new row via `SetRotationDueAt` (or fold into the initial INSERT if that's cleaner — the repo may need an extra optional arg).

Grandfathering (test explicitly): existing keys are not touched. Only NEW `CreateAPIKey` calls consult the policy.

- [ ] **Step 6.3: Tests**

Add to `auth_test.go`:
- `CreateAPIKey_RejectsTTLAboveCap`
- `CreateAPIKey_AllowsTTLAtCap`
- `CreateAPIKey_SetsRotationDueAtWhenPolicySet`
- `CreateAPIKey_ExistingKeysGrandfathered` (seed old key, apply new policy, verify old key still validates)

- [ ] **Step 6.4: Commit**

```bash
git add services/auth/internal/service/auth.go services/auth/internal/service/auth_test.go
git commit -m "feat(auth): enforce max_ttl_days + set rotation_due_at on CreateAPIKey (FUT-003)"
```

---

## Task 7: `idle_revoke` background worker

**Files:**
- Create: `services/auth/internal/worker/idle_revoke.go`
- Create: `services/auth/internal/worker/idle_revoke_test.go`

Hourly cron. For each tenant that has a policy with `idle_revoke_days` set:
1. `ListIdleKeys(tenantID, cutoff = now - idle_revoke_days)`
2. For each returned key: `UPDATE api_keys SET revoked_at = now(), revoke_reason = 'idle_revoked'` (add the `revoke_reason` column in the same task's repo change if it doesn't exist — check).
3. Emit `auth.key_revoked` per row with `Reason = "idle_revoked"`.

**Multi-mode discovery:** iterate `SELECT tenant_id FROM token_policies WHERE idle_revoke_days IS NOT NULL`. In single mode there's just one row.

**Concurrency:** the worker runs once per process. On multi-replica auth, use `pg_try_advisory_lock(hash("idle-revoke-tenant-" + tenantID))` per tenant to prevent duplicate revokes across replicas. Skip if the lock isn't acquired (another replica is already handling this tenant).

**Fake clock:** the test injects `now func() time.Time` so time-dependent behaviour is deterministic.

- [ ] **Step 7.1: Write the failing tests**

Sub-tests:
- `Tick_RevokesIdleKeys` — seed 3 keys (idle / recent / already-revoked), tick once, assert only the idle non-revoked key is revoked
- `Tick_EmitsAuditEventPerRevocation`
- `Tick_NoOpWhenPolicyIsNil`
- `Tick_NoOpWhenIdleRevokeDaysIsNil`
- `Tick_SkipsTenantsWithoutAdvisoryLock` (simulate second replica holding the lock via a manual `pg_advisory_lock` on the test connection)
- `Tick_CascadesFailureButKeepsGoing` — one tenant's DB call fails, next tenant still processed

- [ ] **Step 7.2: Implement**

Skeleton:

```go
package worker

import (
    "context"
    "log/slog"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// Publisher is the narrow interface IdleRevoke uses to emit
// auth.key_revoked events. Kept small so tests can fake it.
type Publisher interface {
    PublishKeyRevoked(ctx context.Context, tenantID, keyID, ownerUserID uuid.UUID, reason string) error
}

type IdleRevoke struct {
    pool       *pgxpool.Pool
    apiKeys    *repository.APIKeyRepo
    policies   *repository.TokenPolicyRepo
    pub        Publisher
    now        func() time.Time
    logger     *slog.Logger
    tickPeriod time.Duration
}

func NewIdleRevoke(pool *pgxpool.Pool, apiKeys *repository.APIKeyRepo, policies *repository.TokenPolicyRepo,
                   pub Publisher, logger *slog.Logger) *IdleRevoke {
    return &IdleRevoke{
        pool: pool, apiKeys: apiKeys, policies: policies, pub: pub,
        now: time.Now, logger: logger, tickPeriod: time.Hour,
    }
}

// Run blocks until ctx is cancelled. Ticks every tickPeriod (1h in
// prod; overridable in tests). Each tick sweeps every tenant with
// idle_revoke_days configured.
func (w *IdleRevoke) Run(ctx context.Context) {
    // Immediate first tick so a fresh boot doesn't leave idle keys
    // lingering for up to 1h.
    w.tickOnce(ctx)
    ticker := time.NewTicker(w.tickPeriod)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            w.tickOnce(ctx)
        }
    }
}

func (w *IdleRevoke) tickOnce(ctx context.Context) {
    tenantIDs, err := w.listTenantsWithIdleRevoke(ctx)
    if err != nil {
        w.logger.Warn("idle_revoke: list tenants failed", "err", err)
        return
    }
    for _, tenantID := range tenantIDs {
        w.tickTenant(ctx, tenantID)
    }
}

// tickTenant handles ONE tenant per invocation. pg_try_advisory_lock
// prevents multi-replica double-work. All errors log-and-continue —
// this loop must not starve other tenants.
func (w *IdleRevoke) tickTenant(ctx context.Context, tenantID uuid.UUID) {
    // Acquire advisory lock; skip if another replica holds it.
    var locked bool
    if err := w.pool.QueryRow(ctx,
        `SELECT pg_try_advisory_lock(hashtext('idle-revoke-' || $1::text))`, tenantID,
    ).Scan(&locked); err != nil {
        w.logger.Warn("idle_revoke: lock query failed", "tenant_id", tenantID, "err", err)
        return
    }
    if !locked {
        return
    }
    defer func() {
        _, _ = w.pool.Exec(ctx,
            `SELECT pg_advisory_unlock(hashtext('idle-revoke-' || $1::text))`, tenantID)
    }()

    policy, err := w.policies.GetOrDefault(ctx, tenantID)
    if err != nil || policy.IdleRevokeDays == nil {
        return
    }
    cutoff := w.now().Add(-time.Duration(*policy.IdleRevokeDays) * 24 * time.Hour)
    keys, err := w.apiKeys.ListIdleKeys(ctx, tenantID, cutoff)
    if err != nil {
        w.logger.Warn("idle_revoke: list idle keys failed", "tenant_id", tenantID, "err", err)
        return
    }
    for _, k := range keys {
        if err := w.apiKeys.RevokeWithReason(ctx, k.ID, "idle_revoked", w.now()); err != nil {
            w.logger.Warn("idle_revoke: revoke failed", "key_id", k.ID, "err", err)
            continue
        }
        if err := w.pub.PublishKeyRevoked(ctx, tenantID, k.ID, k.OwnerUserID, "idle_revoked"); err != nil {
            w.logger.Warn("idle_revoke: publish failed", "key_id", k.ID, "err", err)
        }
    }
}

func (w *IdleRevoke) listTenantsWithIdleRevoke(ctx context.Context) ([]uuid.UUID, error) {
    rows, err := w.pool.Query(ctx,
        `SELECT tenant_id FROM token_policies WHERE idle_revoke_days IS NOT NULL`)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []uuid.UUID
    for rows.Next() {
        var id uuid.UUID
        if err := rows.Scan(&id); err != nil { return nil, err }
        out = append(out, id)
    }
    return out, rows.Err()
}
```

Add `APIKeyRepo.RevokeWithReason(ctx, keyID, reason, at) error` — new signature; also update the existing manual-revoke path to pass `"manual"` as the reason so the field is always set consistently.

Add `revoke_reason TEXT` column via the same migration (append to `20260702000001` before the index).

- [ ] **Step 7.3: Wire into `server.go`**

Start the worker in a goroutine on process start; cancel on shutdown. Do NOT block startup on it.

- [ ] **Step 7.4: Commit**

```bash
git add services/auth/internal/worker/ services/auth/internal/server/server.go services/auth/internal/repository/apikey.go services/auth/migrations/20260702000001_token_policies.sql
git commit -m "feat(auth): idle_revoke worker + revoke_reason column (FUT-003)"
```

---

## Task 8: gRPC handlers — `Get/PutTokenPolicy`

**Files:**
- Create: `services/auth/internal/handler/grpc_token_policy.go`
- Create: `services/auth/internal/handler/grpc_token_policy_test.go`

Thin wrappers around `TokenPolicyService`. Follow the FUT-001 `grpc_oidc_trust.go` pattern.

`Get`: return the current policy (or empty for unset tenant).
`Put`: pass through, parse `Int32Value` wrappers into `*int32`.

Sub-tests: happy path + Unimplemented-when-not-wired (already established pattern).

- [ ] Commit:

```bash
git add services/auth/internal/handler/grpc_token_policy.go services/auth/internal/handler/grpc_token_policy_test.go services/auth/internal/handler/grpc.go
git commit -m "feat(auth): gRPC handlers for TokenPolicy Get/Put (FUT-003)"
```

---

## Task 9: Audit events — `auth.token_policy.changed` + `KeyRevokedPayload.reason`

**Files:**
- Modify: `libs/rabbitmq/events/events.go`
- Modify: `services/audit/internal/eventconsumer/consumer.go`

- [ ] **Step 9.1: events.go**

```go
const RoutingTokenPolicyChanged = "auth.token_policy.changed"

type TokenPolicyChangedPayload struct {
    TenantID  string        `json:"tenant_id"`
    ActorID   string        `json:"actor_id"`
    Before    PolicySnapshot `json:"before"`
    After     PolicySnapshot `json:"after"`
}

type PolicySnapshot struct {
    MaxTTLDays           *int32 `json:"max_ttl_days,omitempty"`
    RotationIntervalDays *int32 `json:"rotation_interval_days,omitempty"`
    IdleRevokeDays       *int32 `json:"idle_revoke_days,omitempty"`
}
```

Extend `KeyRevokedPayload` (find it; if missing add):

```go
type KeyRevokedPayload struct {
    TenantID     string `json:"tenant_id"`
    KeyID        string `json:"key_id"`
    OwnerUserID  string `json:"owner_user_id"`
    Reason       string `json:"reason"` // "manual" | "idle_revoked" | "rotation_lapsed"
}
```

- [ ] **Step 9.2: consumer.go mapEvent case**

Add a case for `RoutingTokenPolicyChanged` that translates the before/after diff into an `audit_events` row with `Action = "auth.token_policy.changed"` and `Metadata` carrying the JSON diff.

Extend the existing `auth.key_revoked` case (if present) to populate `metadata->reason` from the new field. If the case is missing, add it here.

Both cases exercised by the `TestAuditCatalogueCompleteness` invariant — should pass after both are added.

- [ ] **Step 9.3: Commit**

```bash
git add libs/rabbitmq/events/events.go services/audit/internal/eventconsumer/consumer.go
git commit -m "feat(audit): catalogue token_policy.changed + key_revoked reason (FUT-003)"
```

---

## Task 10: BFF — 2 routes on `services/management`

**Files:**
- Create: `services/management/internal/handler/access_token_policy.go`
- Create: `services/management/internal/handler/access_token_policy_test.go`
- Modify: `services/management/internal/handler/handler.go`

Follow the FUT-001 `access_oidc_trust.go` pattern.

Routes:

```go
mux.Handle("GET /api/v1/access/token-policy", authMW(http.HandlerFunc(h.handleGetTokenPolicy)))
mux.Handle("PUT /api/v1/access/token-policy", authMW(http.HandlerFunc(h.handlePutTokenPolicy)))
```

Gate on `isTenantAdminOrPlatformAdmin`. Tenant id from JWT claims. `actor_id` derived from JWT `sub` and passed into the gRPC request.

Body shape (JSON):

```json
{
  "max_ttl_days": 90,               // omit or null = clear
  "rotation_interval_days": null,
  "idle_revoke_days": 30
}
```

Response = same shape with `updated_at` + `updated_by_user_id` set.

Sub-tests: 2 happy-path + 2 admin-deny (403).

- [ ] Commit:

```bash
git add services/management/internal/handler/access_token_policy.go services/management/internal/handler/access_token_policy_test.go services/management/internal/handler/handler.go
git commit -m "feat(management): 2 TokenPolicy admin BFF routes (FUT-003)"
```

---

## Task 11: FE — hooks + Vite proxy

**Files:**
- Create: `frontend/src/lib/api/token-policy.ts`
- Modify: `frontend/vite.config.ts`

Hooks:

```typescript
export interface TokenPolicy {
  tenant_id: string;
  max_ttl_days: number | null;
  rotation_interval_days: number | null;
  idle_revoke_days: number | null;
  updated_at: string;
  updated_by_user_id: string | null;
}

export function useTokenPolicy() { /* GET /access/token-policy */ }
export function usePutTokenPolicy() { /* PUT /access/token-policy, invalidate ['token-policy'] */ }
```

Vite proxy: add ABOVE `/api/v1/access` catchall:

```typescript
"/api/v1/access/token-policy": { target: "http://localhost:8091", changeOrigin: true },
```

- [ ] Commit:

```bash
git add frontend/src/lib/api/token-policy.ts frontend/vite.config.ts
git commit -m "feat(frontend): useTokenPolicy hooks + Vite proxy (FUT-003)"
```

---

## Task 12: FE — `PoliciesPanel` + route swap

**Files:**
- Create: `frontend/src/components/access/PoliciesPanel.tsx`
- Create: `frontend/src/components/access/__tests__/PoliciesPanel.test.tsx`
- Modify: `frontend/src/routes/_authenticated.api-keys.policies.tsx`
- Delete: `frontend/src/components/access/previews/PoliciesPreview.tsx`

Component (mirror `TrustPanel.tsx`):
- Unconditional `<header>`
- Loading / error states
- Three numeric inputs (max TTL / rotation interval / idle revoke) — each with a "Disable" toggle since NULL is a valid state
- "Save" button → `usePutTokenPolicy().mutate(...)`
- Drop the "Allow per-key override" checkbox from the preview
- Change "Force rotation" help text to `"You'll see a reminder in the bell feed 14 days before expiry."` (email waits on FUT-019)

Tests (5+):
- Renders heading + no `<PreviewBanner>`
- Loading state
- Successful load populates inputs from server data
- Save calls the mutation with the current form state
- Validation rejects zero/negative days inline

Route swap: import `PoliciesPanel` in `_authenticated.api-keys.policies.tsx`. Delete `PoliciesPreview.tsx`.

- [ ] Commit:

```bash
git add frontend/src/components/access/PoliciesPanel.tsx frontend/src/components/access/__tests__/PoliciesPanel.test.tsx frontend/src/routes/_authenticated.api-keys.policies.tsx frontend/src/components/access/previews/PoliciesPreview.tsx
git commit -m "feat(frontend): live PoliciesPanel + route swap + preview deletion (FUT-003)"
```

---

## Task 13: FE — sidebar graduation

**Files:**
- Modify: `frontend/src/components/access/AccessSubNav.tsx`
- Modify: `frontend/src/components/access/__tests__/AccessSubNav.test.tsx`

Move `Token policies` from Preview → Workspace section. Drop `preview: true`. Preview count 2 → 1 (only Access review remains).

Update graduation regression tests: drop `Token policies` assertion from the "shows all preview links" test; add a new regression asserting it renders in Workspace without expansion.

- [ ] Commit:

```bash
git add frontend/src/components/access/AccessSubNav.tsx frontend/src/components/access/__tests__/AccessSubNav.test.tsx
git commit -m "feat(frontend): graduate Token policies out of Preview (FUT-003)"
```

---

## Task 14: Tracker hygiene

**Files:**
- Modify: `status-tracker.md` (add REM-024)
- Modify: `futures.md` (collapse FUT-003)

Same pattern as REM-021/REM-023.

- [ ] Commit:

```bash
git add status-tracker.md futures.md
git commit -m "chore(trackers): REM-024 FUT-003 in-flight entry + futures.md stub"
```

---

## Task 15: Local CI gate

- [ ] Backend: `services/{auth,audit,management}` go vet + build + test
- [ ] FE: `npm run lint && npm run typecheck && npm run test && npm run build`
- [ ] `spec-lint`: all 13 rules pass

---

## Task 16: 3-agent review batch

Same shape as FUT-001. Priority items:

**security-agent:**
- Grandfathering — verify existing keys STILL VALIDATE after a stricter policy applies
- `PutTokenPolicy` admin-gated + tenant-scoped (a workspace admin can't write another tenant's policy)
- Idle-revoke advisory lock prevents double-revoke across replicas
- `last_used_at` debounce is fail-OPEN (not a security boundary), but confirm the write path doesn't leak the key value

**qa-agent:**
- Grandfathering test present (seed old key, apply new policy, old key still validates)
- Idle-revoke worker has fake-clock test that seeds keys and asserts exactly the right ones are revoked
- Race detector coverage on the debounced updater
- Audit catalogue invariant still passes

**code-review-agent:**
- Adaptations documented
- Policy field pointer semantics clean (nil = unset, not zero)
- ON CONFLICT DO UPDATE with COALESCE for partial updates

Fold must-fixes inline. Log should-fixes to REM-024.

---

## Task 17: Open PR + rebuild containers

- [ ] `git push -u origin feat/fut-003-token-policies`
- [ ] `gh pr create` with body covering summary + grandfather semantics + audit events + 3-agent verdicts
- [ ] After PR opens: `docker compose build registry-auth registry-audit registry-management && docker compose up -d`
- [ ] Verify: `curl -H "Authorization: Bearer <admin-jwt>" http://localhost:8091/api/v1/access/token-policy` returns `200 {}` (empty policy for a fresh tenant); `PUT` with a body persists

---

## Notes for the executor

- Per CLAUDE.md `feedback_code_comments`: every new file gets a top-of-file comment + per-function doc strings.
- Per `feedback_git_workflow`: commit on the current branch. Never commit to main.
- Per CLAUDE.md §15.1: all 4 FE CI equivalents required.
- Per CLAUDE.md §11: raw SQL only, parameterised.
- Per CLAUDE.md §10: audit catalogue completeness — the new `RoutingTokenPolicyChanged` MUST have a mapEvent case OR carry `// audit: skip`.
- **Grandfathering is the load-bearing security invariant** — a wrong impl here means a stricter policy accidentally locks operators out of their own workspace. Test explicitly.
- **Idle-revoke worker advisory lock** — a wrong impl here means multiple auth replicas race + revoke the same key twice (harmless but noisy) OR emit duplicate audit events. Test with a manually-held `pg_advisory_lock` on a second connection.
- If a step's expected output diverges: stop, fix or report, don't proceed past it.
