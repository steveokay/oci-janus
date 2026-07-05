# Service Accounts + `/api-keys` Hub (FE-API-048) — Implementation Plan

> **✅ SHIPPED — FE-API-048 service-accounts + /api-keys hub (2026-06). Plan complete; canonical status in `status.md` / `FE-STATUS.md`. Task checkboxes left unticked — this is a subagent-driven execution artifact, not a live tracker.**

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add workspace-owned service-account API keys to `services/auth`, plus a `/api-keys` hub UI with live service-accounts/activity surfaces and four dummy-data preview surfaces for FUT-001..004.

**Architecture:** Polymorphic `api_keys` owner column (nullable `user_id` XOR `service_account_id`) + shadow-user-per-SA so RBAC/audit/RLS keep using `user_id` unchanged. All new HTTP routes on `services/auth` (no proto changes; `/apikeys` is already HTTP-only). Frontend `/api-keys` becomes a sub-routed hub. The four preview surfaces ship as real pages with mock data + an a11y-compliant `PreviewBanner`.

**Tech Stack:** Go 1.25.7 (`pgx/v5`, `goose`, `log/slog`), PostgreSQL 16, Redis 7, React 19 + Vite 6 + TanStack Router v1 + TanStack Query v5 + Tailwind v4.

**Spec:** `docs/superpowers/specs/2026-06-21-service-accounts-and-access-hub-design.md`

**Branch:** `feat/sprint-10`

**Dev tenant for manual testing:** `98dbe36b-ef28-4903-b25c-bff1b2921c9e`. Dev admin: `admin` / `Admin1234!dev`.

**Commit convention:** Conventional Commits (`feat(auth):` / `feat(frontend):` / `test(auth):` / `docs(spec):`). Include the `Co-Authored-By` trailer used in recent merges.

---

## File map

### Created
- `services/auth/migrations/20260622000001_user_kind.sql`
- `services/auth/migrations/20260622000002_service_accounts.sql`
- `services/auth/migrations/20260622000003_api_keys_polymorphic.sql`
- `services/auth/internal/repository/service_account.go`
- `services/auth/internal/repository/service_account_test.go`
- `services/auth/internal/service/service_account.go`
- `services/auth/internal/service/service_account_test.go`
- `services/auth/internal/service/activity.go`
- `services/auth/internal/service/activity_test.go`
- `services/auth/internal/handler/http_service_accounts.go`
- `services/auth/internal/handler/http_service_accounts_test.go`
- `services/auth/internal/handler/http_access_activity.go`
- `services/auth/internal/handler/http_access_activity_test.go`
- `services/auth/internal/testutil/sa_fixtures.go`
- `libs/testutil/containers/auth_with_audit.go`
- `infra/dev-seed/service_accounts.sql`
- `scripts/lint-user-queries.sh`
- `frontend/src/lib/api/service-accounts.ts`
- `frontend/src/lib/api/activity.ts`
- `frontend/src/components/access/AccessHubLayout.tsx`
- `frontend/src/components/access/AccessSubNav.tsx`
- `frontend/src/components/access/PreviewBanner.tsx`
- `frontend/src/components/access/ServiceAccountsTable.tsx`
- `frontend/src/components/access/ServiceAccountDetail.tsx`
- `frontend/src/components/access/CreateServiceAccountDialog.tsx`
- `frontend/src/components/access/ScopeShrinkConfirmDialog.tsx`
- `frontend/src/components/access/ActivityTable.tsx`
- `frontend/src/components/access/previews/TrustPreview.tsx`
- `frontend/src/components/access/previews/HelpersPreview.tsx`
- `frontend/src/components/access/previews/PoliciesPreview.tsx`
- `frontend/src/components/access/previews/ReviewPreview.tsx`
- `frontend/src/routes/_authenticated.api-keys.index.tsx`
- `frontend/src/routes/_authenticated.api-keys.service-accounts.tsx`
- `frontend/src/routes/_authenticated.api-keys.activity.tsx`
- `frontend/src/routes/_authenticated.api-keys.trust.tsx`
- `frontend/src/routes/_authenticated.api-keys.helpers.tsx`
- `frontend/src/routes/_authenticated.api-keys.policies.tsx`
- `frontend/src/routes/_authenticated.api-keys.review.tsx`

### Modified
- `services/auth/internal/repository/user.go` — add `…Human…` methods + `…AnyKind` rename
- `services/auth/internal/repository/apikey.go` — polymorphic owner support; partial unique indexes
- `services/auth/internal/repository/rbac.go` — `ListMembers` projects principal kind
- `services/auth/internal/service/auth.go` — `ValidateAPIKey` cross-tenant guard + scope intersection
- `services/auth/internal/service/sso.go` — switch `GetByEmail` → `GetHumanByEmail`
- `services/auth/internal/handler/http.go` — wire new routes + grow `POST /apikeys` body
- `services/auth/internal/handler/http_users_me.go` — sanitised principal envelope for SA callers
- `services/management/internal/handler/rbac.go` — `/orgs/{org}/members` user picker excludes shadow users
- `services/management/internal/handler/admin_tenants.go` — headcount excludes shadow users
- `frontend/vite.config.ts` — add `/api/v1/service-accounts` and `/api/v1/access` proxy entries
- `frontend/src/routes/_authenticated.api-keys.tsx` — convert into hub layout shell
- `frontend/src/components/shell/topbar.tsx` — branch avatar on `/users/me` `type` field
- `proto/auth/v1/auth.proto` — add semantic-shift comments on `user_id` fields (no field changes)
- `CLAUDE.md` — §4.2 (Owns column) + §14 (decision-log row 22)
- `status.md` — FE-API-048 IN PROGRESS row
- `FE-STATUS.md` — Sprint 10 row + new `/api-keys/*` route entries
- `futures.md` — promote FUT-001..004 into "Tier 2 — Access: machine identity & policy"
- `security.md` — proactive notes for PENTEST-AUTH-001 (cross-tenant) + PENTEST-AUTH-002 (JWT revoke pattern)

---

## Backend tasks

### Task 1: Migration — `users.kind` column

**Files:**
- Create: `services/auth/migrations/20260622000001_user_kind.sql`
- Test: `services/auth/internal/repository/migrations_test.go` (modify if exists; create otherwise)

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'human'
        CHECK (kind IN ('human', 'service_account'));

CREATE INDEX idx_users_tenant_kind ON users (tenant_id, kind);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_users_tenant_kind;
ALTER TABLE users DROP COLUMN kind;
-- +goose StatementEnd
```

- [ ] **Step 2: Write the failing test (up + down round-trip on a fresh DB)**

In `services/auth/internal/repository/migrations_test.go` (testcontainers pattern; if no `_test.go` for migrations exists, this becomes the first):

```go
func TestMigration_UserKind_RoundTrip(t *testing.T) {
    ctx := context.Background()
    pool, cleanup := testutil.StartPostgres(t, ctx)
    defer cleanup()

    require.NoError(t, gooseUpTo(ctx, pool, "20260622000001"))

    // Existing seeded rows defaulted to 'human'
    var kind string
    err := pool.QueryRow(ctx, `SELECT kind FROM users LIMIT 1`).Scan(&kind)
    require.NoError(t, err)
    require.Equal(t, "human", kind)

    // CHECK rejects unknown values
    _, err = pool.Exec(ctx, `UPDATE users SET kind='robot' WHERE id=(SELECT id FROM users LIMIT 1)`)
    require.Error(t, err, "CHECK should reject unknown kind")

    require.NoError(t, gooseDownTo(ctx, pool, "20260609000002"))
    // Column should be gone
    var hasCol bool
    pool.QueryRow(ctx, `
        SELECT EXISTS(
            SELECT 1 FROM information_schema.columns
            WHERE table_name='users' AND column_name='kind'
        )`).Scan(&hasCol)
    require.False(t, hasCol)
}
```

(If `testutil.StartPostgres`, `gooseUpTo`, `gooseDownTo` helpers don't exist, copy the pattern from `services/scanner/internal/repository/migrations_test.go` — it's the most recent migration round-trip test in the repo.)

- [ ] **Step 3: Run the test, expect FAIL** (migration file not yet created in the embedded FS)

Run: `cd services/auth && go test ./internal/repository/ -run TestMigration_UserKind_RoundTrip -v`
Expected: FAIL with "no migrations to run" or similar.

- [ ] **Step 4: Add the migration file to the embed.FS** — goose discovers via the existing `migrations.go`. No code change needed in that file; the new `.sql` is picked up automatically.

- [ ] **Step 5: Run the test, expect PASS**

Run: `cd services/auth && go test ./internal/repository/ -run TestMigration_UserKind_RoundTrip -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add services/auth/migrations/20260622000001_user_kind.sql services/auth/internal/repository/migrations_test.go
git commit -m "feat(auth): users.kind column for FE-API-048 shadow-user pattern

$(printf 'Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>')"
```

---

### Task 2: Migration — `service_accounts` table

**Files:**
- Create: `services/auth/migrations/20260622000002_service_accounts.sql`
- Test: `services/auth/internal/repository/migrations_test.go` (append)

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- +goose StatementBegin
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

CREATE INDEX idx_service_accounts_tenant_active
    ON service_accounts (tenant_id)
    WHERE disabled_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE service_accounts;
-- +goose StatementEnd
```

- [ ] **Step 2: Write the failing test (UNIQUE on `(tenant_id, name)` + cascade)**

Append to `migrations_test.go`:

```go
func TestMigration_ServiceAccounts_UniqueAndCascade(t *testing.T) {
    ctx := context.Background()
    pool, cleanup := testutil.StartPostgres(t, ctx)
    defer cleanup()
    require.NoError(t, gooseUpTo(ctx, pool, "20260622000002"))

    tenant := uuid.New()
    // Seed a "creator" human user
    var creatorID uuid.UUID
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind)
        VALUES ($1, 'admin@example.com', '', 'human')
        RETURNING id`, tenant).Scan(&creatorID))

    // Shadow user 1
    var shadow1 uuid.UUID
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind)
        VALUES ($1, 'sa+1@internal.invalid', '', 'service_account')
        RETURNING id`, tenant).Scan(&shadow1))

    // Insert first SA
    var sa1 uuid.UUID
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO service_accounts (tenant_id, shadow_user_id, name, created_by)
        VALUES ($1, $2, 'ci-prod', $3) RETURNING id`,
        tenant, shadow1, creatorID).Scan(&sa1))

    // Second SA with same name in same tenant must fail UNIQUE
    var shadow2 uuid.UUID
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind)
        VALUES ($1, 'sa+2@internal.invalid', '', 'service_account')
        RETURNING id`, tenant).Scan(&shadow2))
    _, err := pool.Exec(ctx, `
        INSERT INTO service_accounts (tenant_id, shadow_user_id, name, created_by)
        VALUES ($1, $2, 'ci-prod', $3)`, tenant, shadow2, creatorID)
    require.Error(t, err, "UNIQUE (tenant_id, name) should reject duplicate")

    // Deleting creator nulls created_by
    _, err = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, creatorID)
    require.NoError(t, err)
    var createdBy *uuid.UUID
    require.NoError(t, pool.QueryRow(ctx,
        `SELECT created_by FROM service_accounts WHERE id=$1`, sa1).Scan(&createdBy))
    require.Nil(t, createdBy, "created_by must be NULL after creator delete")

    // Deleting shadow user cascades to service_accounts
    _, err = pool.Exec(ctx, `DELETE FROM users WHERE id=$1`, shadow1)
    require.NoError(t, err)
    var saCount int
    require.NoError(t, pool.QueryRow(ctx,
        `SELECT count(*) FROM service_accounts WHERE id=$1`, sa1).Scan(&saCount))
    require.Equal(t, 0, saCount, "service_accounts row should cascade-delete with shadow user")
}
```

- [ ] **Step 3: Run, expect FAIL** (migration not yet picked up).

Run: `cd services/auth && go test ./internal/repository/ -run TestMigration_ServiceAccounts -v`
Expected: FAIL with `relation "service_accounts" does not exist`.

- [ ] **Step 4: Verify the new `.sql` file is in the directory.** No code change required — goose embeds via existing `migrations.go`.

- [ ] **Step 5: Run, expect PASS.**

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add services/auth/migrations/20260622000002_service_accounts.sql services/auth/internal/repository/migrations_test.go
git commit -m "feat(auth): service_accounts table + cascade test (FE-API-048)

$(printf 'Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>')"
```

---

### Task 3: Migration — polymorphic `api_keys` + DO$$ down-guard

**Files:**
- Create: `services/auth/migrations/20260622000003_api_keys_polymorphic.sql`
- Test: `services/auth/internal/repository/migrations_test.go` (append)

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- +goose StatementBegin
ALTER TABLE api_keys ALTER COLUMN user_id DROP NOT NULL;
ALTER TABLE api_keys ADD COLUMN service_account_id UUID
    REFERENCES service_accounts(id) ON DELETE CASCADE;
ALTER TABLE api_keys ADD CONSTRAINT api_keys_owner_exactly_one
    CHECK ((user_id IS NULL) <> (service_account_id IS NULL));

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_user_id_name_key;

CREATE UNIQUE INDEX api_keys_user_name_unique
    ON api_keys (user_id, name) WHERE user_id IS NOT NULL;
CREATE UNIQUE INDEX api_keys_sa_name_unique
    ON api_keys (service_account_id, name) WHERE service_account_id IS NOT NULL;

CREATE INDEX idx_api_keys_sa
    ON api_keys (service_account_id) WHERE service_account_id IS NOT NULL;
-- +goose StatementEnd

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
ALTER TABLE api_keys ADD CONSTRAINT api_keys_user_id_name_key UNIQUE (user_id, name);
-- +goose StatementEnd
```

- [ ] **Step 2: Write the failing tests — T1 (CHECK both directions), T2 (partial unique), T3 (down refusal)**

Append to `migrations_test.go`:

```go
func TestMigration_ApiKeysPolymorphic_CHECK(t *testing.T) {
    ctx := context.Background()
    pool, cleanup := testutil.StartPostgres(t, ctx)
    defer cleanup()
    require.NoError(t, gooseUpTo(ctx, pool, "20260622000003"))

    tenant := uuid.New()
    var human uuid.UUID
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind)
        VALUES ($1, 'h@example.com', '', 'human') RETURNING id`, tenant).Scan(&human))

    // Neither owner → reject
    _, err := pool.Exec(ctx, `
        INSERT INTO api_keys (tenant_id, name, key_hash, key_prefix)
        VALUES ($1, 'k1', '', '')`, tenant)
    require.Error(t, err, "CHECK should reject NULL/NULL")

    // Both owners → reject (need a real SA)
    var shadow, sa uuid.UUID
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind)
        VALUES ($1, 'sa+x@internal.invalid', '', 'service_account') RETURNING id`,
        tenant).Scan(&shadow))
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO service_accounts (tenant_id, shadow_user_id, name)
        VALUES ($1, $2, 'sa-x') RETURNING id`, tenant, shadow).Scan(&sa))
    _, err = pool.Exec(ctx, `
        INSERT INTO api_keys (tenant_id, user_id, service_account_id, name, key_hash, key_prefix)
        VALUES ($1, $2, $3, 'k2', '', '')`, tenant, human, sa)
    require.Error(t, err, "CHECK should reject both set")
}

func TestMigration_ApiKeysPolymorphic_PartialUnique(t *testing.T) {
    ctx := context.Background()
    pool, cleanup := testutil.StartPostgres(t, ctx)
    defer cleanup()
    require.NoError(t, gooseUpTo(ctx, pool, "20260622000003"))

    tenant := uuid.New()
    var human, shadow, sa uuid.UUID
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind)
        VALUES ($1, 'h@example.com', '', 'human') RETURNING id`, tenant).Scan(&human))
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind)
        VALUES ($1, 'sa+y@internal.invalid', '', 'service_account') RETURNING id`,
        tenant).Scan(&shadow))
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO service_accounts (tenant_id, shadow_user_id, name)
        VALUES ($1, $2, 'sa-y') RETURNING id`, tenant, shadow).Scan(&sa))

    // Human + SA can share a name
    _, err := pool.Exec(ctx, `INSERT INTO api_keys (tenant_id, user_id, name, key_hash, key_prefix)
        VALUES ($1, $2, 'ci-prod', 'h1', 'h1p1')`, tenant, human)
    require.NoError(t, err)
    _, err = pool.Exec(ctx, `INSERT INTO api_keys (tenant_id, service_account_id, name, key_hash, key_prefix)
        VALUES ($1, $2, 'ci-prod', 's1', 's1p1')`, tenant, sa)
    require.NoError(t, err, "human+SA may share name")

    // Two SA keys with same name → conflict
    _, err = pool.Exec(ctx, `INSERT INTO api_keys (tenant_id, service_account_id, name, key_hash, key_prefix)
        VALUES ($1, $2, 'ci-prod', 's2', 's2p1')`, tenant, sa)
    require.Error(t, err, "partial UNIQUE should block second SA key with same name")
}

func TestMigration_ApiKeysPolymorphic_DownRefuses(t *testing.T) {
    ctx := context.Background()
    pool, cleanup := testutil.StartPostgres(t, ctx)
    defer cleanup()
    require.NoError(t, gooseUpTo(ctx, pool, "20260622000003"))

    tenant := uuid.New()
    var shadow, sa uuid.UUID
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind)
        VALUES ($1, 'sa+z@internal.invalid', '', 'service_account') RETURNING id`,
        tenant).Scan(&shadow))
    require.NoError(t, pool.QueryRow(ctx, `
        INSERT INTO service_accounts (tenant_id, shadow_user_id, name)
        VALUES ($1, $2, 'sa-z') RETURNING id`, tenant, shadow).Scan(&sa))
    _, err := pool.Exec(ctx, `INSERT INTO api_keys (tenant_id, service_account_id, name, key_hash, key_prefix)
        VALUES ($1, $2, 'k', 'h', 'p')`, tenant, sa)
    require.NoError(t, err)

    err = gooseDownTo(ctx, pool, "20260622000002")
    require.Error(t, err, "down must refuse when SA keys exist")
    require.Contains(t, err.Error(), "cannot rollback")
}
```

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Verify migration file is in place; no other code needed.**

- [ ] **Step 5: Run, expect PASS.**

- [ ] **Step 6: Commit**

```bash
git add services/auth/migrations/20260622000003_api_keys_polymorphic.sql services/auth/internal/repository/migrations_test.go
git commit -m "feat(auth): polymorphic api_keys owner + DO\$\$ down-guard (FE-API-048)

T1 (CHECK both directions), T2 (partial unique collision matrix),
T3 (down-migration refusal when SA keys exist) per spec §8.1.

$(printf 'Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>')"
```

---

### Task 4: User repository — add `…Human…` + `…AnyKind` methods

**Files:**
- Modify: `services/auth/internal/repository/user.go`
- Modify: `services/auth/internal/repository/user_test.go`

- [ ] **Step 1: Write the failing test (T8 sweep)**

```go
func TestUserRepo_HumanGuards(t *testing.T) {
    ctx := context.Background()
    repo := newUserRepoWithMigrations(t, ctx) // helper that boots PG + runs migrations

    tenant := uuid.New()
    human, err := repo.Create(ctx, CreateUserInput{
        TenantID: tenant, Email: "h@example.com", PasswordHash: "x", Kind: "human"})
    require.NoError(t, err)
    sa, err := repo.Create(ctx, CreateUserInput{
        TenantID: tenant, Email: "sa+1@internal.invalid", PasswordHash: "", Kind: "service_account"})
    require.NoError(t, err)

    cases := []struct {
        name string
        run  func(t *testing.T)
    }{
        {"ListHumans excludes SA", func(t *testing.T) {
            users, err := repo.ListHumans(ctx, tenant, ListOpts{})
            require.NoError(t, err)
            ids := idsOf(users)
            require.Contains(t, ids, human.ID)
            require.NotContains(t, ids, sa.ID)
        }},
        {"GetHumanByEmail rejects SA synthetic email", func(t *testing.T) {
            _, err := repo.GetHumanByEmail(ctx, tenant, "sa+1@internal.invalid")
            require.ErrorIs(t, err, ErrNotFound)
        }},
        {"GetHumanByID rejects SA shadow id", func(t *testing.T) {
            _, err := repo.GetHumanByID(ctx, sa.ID)
            require.ErrorIs(t, err, ErrNotFound)
        }},
        {"CountHumans excludes SA", func(t *testing.T) {
            n, err := repo.CountHumans(ctx, tenant)
            require.NoError(t, err)
            require.EqualValues(t, 1, n)
        }},
        {"GetUserAnyKind returns SA when asked", func(t *testing.T) {
            got, err := repo.GetUserAnyKind(ctx, sa.ID)
            require.NoError(t, err)
            require.Equal(t, "service_account", got.Kind)
        }},
    }
    for _, c := range cases {
        t.Run(c.name, c.run)
    }
}
```

- [ ] **Step 2: Run, expect FAIL** (methods don't exist).

- [ ] **Step 3: Implement the methods** in `user.go`. Pattern: copy each existing query, add `AND kind='human'` for the guarded variant. Rename the existing kind-agnostic methods with the `…AnyKind` suffix; keep them unexported unless the SA code path needs them.

```go
// ListHumans returns active users with kind='human'. SAs (kind='service_account')
// are filtered out at the repository layer so every caller inherits the guard.
func (r *Repo) ListHumans(ctx context.Context, tenantID uuid.UUID, opts ListOpts) ([]User, error) {
    // ...existing ListUsers body, with "AND kind='human'" injected into WHERE...
}

func (r *Repo) GetHumanByEmail(ctx context.Context, tenantID uuid.UUID, email string) (*User, error) { /* ... */ }
func (r *Repo) GetHumanByID(ctx context.Context, id uuid.UUID) (*User, error)                     { /* ... */ }
func (r *Repo) CountHumans(ctx context.Context, tenantID uuid.UUID) (int64, error)                 { /* ... */ }
func (r *Repo) GetUserAnyKind(ctx context.Context, id uuid.UUID) (*User, error)                    { /* ... */ }
```

The existing `ListUsers`, `GetByEmail`, `GetByID`, `CountTenantUsers` methods are kept temporarily as **deprecated thin wrappers** that call the `…Human` variant. They will be removed at the end of Task 6 once all callers are migrated.

- [ ] **Step 4: Run, expect PASS.**

- [ ] **Step 5: Commit**

```bash
git add services/auth/internal/repository/user.go services/auth/internal/repository/user_test.go
git commit -m "feat(auth): repository.User…Human… helpers carry kind='human' guard (FE-API-048)

Pushes the spec §4.1 filter from handler layer down to repository — every
single-row lookup (login, password reset, SSO email match, JWT subject)
inherits the guard with no per-caller code. Code review caught that the
original 'filter list-of-users surfaces' framing missed single-row paths.

$(printf 'Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>')"
```

---

### Task 5: ServiceAccount repository

**Files:**
- Create: `services/auth/internal/repository/service_account.go`
- Create: `services/auth/internal/repository/service_account_test.go`

- [ ] **Step 1: Define the types + signatures**

Add to `service_account.go`:

```go
package repository

import (
    "context"
    "time"

    "github.com/google/uuid"
)

type ServiceAccount struct {
    ID             uuid.UUID
    TenantID       uuid.UUID
    ShadowUserID   uuid.UUID
    Name           string
    Description    string
    AllowedScopes  []string
    CreatedBy      *uuid.UUID  // nullable per ON DELETE SET NULL
    CreatedAt      time.Time
    DisabledAt     *time.Time
}

type ServiceAccountWithStats struct {
    ServiceAccount
    ActiveKeyCount int32
    LastUsedAt     *time.Time
}

type CreateServiceAccountInput struct {
    TenantID      uuid.UUID
    Name          string
    Description   string
    AllowedScopes []string
    CreatedBy     uuid.UUID
}

type UpdateServiceAccountInput struct {
    ID            uuid.UUID
    TenantID      uuid.UUID
    Name          *string
    Description   *string
    AllowedScopes *[]string
    Disabled      *bool   // true → set disabled_at=now(); false → clear
}

type ServiceAccountRepo struct {
    pool DBPool
}

func NewServiceAccountRepo(pool DBPool) *ServiceAccountRepo { return &ServiceAccountRepo{pool: pool} }
```

- [ ] **Step 2: Write failing tests for atomic create + cascade delete**

```go
func TestServiceAccountRepo_CreateAtomic(t *testing.T) {
    ctx := context.Background()
    pool, users, repo := setupSARepo(t, ctx)
    tenant, creator := seedHuman(t, ctx, users, "admin@example.com")

    sa, shadowID, err := repo.CreateAtomic(ctx, CreateServiceAccountInput{
        TenantID: tenant, Name: "ci-prod", AllowedScopes: []string{"pull","push"}, CreatedBy: creator,
    })
    require.NoError(t, err)
    require.NotEqual(t, uuid.Nil, sa.ID)
    require.Equal(t, "ci-prod", sa.Name)
    require.Equal(t, "service_account", getUserKind(t, ctx, pool, shadowID))
    require.Equal(t, "sa+"+sa.ID.String()+"@internal.invalid", getUserEmail(t, ctx, pool, shadowID))
}

func TestServiceAccountRepo_DeleteCascades(t *testing.T) {
    ctx := context.Background()
    pool, users, repo := setupSARepo(t, ctx)
    tenant, creator := seedHuman(t, ctx, users, "admin@example.com")
    sa, shadow, _ := repo.CreateAtomic(ctx, CreateServiceAccountInput{
        TenantID: tenant, Name: "x", CreatedBy: creator})

    require.NoError(t, repo.Delete(ctx, sa.ID))
    require.Equal(t, 0, countRows(t, ctx, pool, "service_accounts", "id=$1", sa.ID))
    require.Equal(t, 0, countRows(t, ctx, pool, "users",           "id=$1", shadow))
}
```

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Implement `CreateAtomic`, `Get`, `List`, `Update`, `Delete`, `CountKeysAffectedByScopeShrink`**

```go
// CreateAtomic inserts shadow user + service_accounts row in one tx.
// The shadow user's email is sa+<sa_id>@internal.invalid — synthesised
// AFTER we know the SA id, so the email is deterministic from the SA id.
func (r *ServiceAccountRepo) CreateAtomic(ctx context.Context, in CreateServiceAccountInput) (*ServiceAccount, uuid.UUID, error) {
    tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
    if err != nil {
        return nil, uuid.Nil, err
    }
    defer tx.Rollback(ctx)

    // 1. Generate SA id up front so the shadow email is deterministic.
    saID := uuid.New()
    syntheticEmail := "sa+" + saID.String() + "@internal.invalid"

    var shadowID uuid.UUID
    if err := tx.QueryRow(ctx, `
        INSERT INTO users (tenant_id, email, password_hash, kind, is_active)
        VALUES ($1, $2, '', 'service_account', true)
        RETURNING id`, in.TenantID, syntheticEmail).Scan(&shadowID); err != nil {
        return nil, uuid.Nil, fmt.Errorf("insert shadow user: %w", err)
    }

    if in.AllowedScopes == nil {
        in.AllowedScopes = []string{}
    }
    sa := &ServiceAccount{}
    if err := tx.QueryRow(ctx, `
        INSERT INTO service_accounts
            (id, tenant_id, shadow_user_id, name, description, allowed_scopes, created_by)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
        RETURNING id, tenant_id, shadow_user_id, name, description, allowed_scopes,
                  created_by, created_at, disabled_at`,
        saID, in.TenantID, shadowID, in.Name, in.Description, in.AllowedScopes, in.CreatedBy).
        Scan(&sa.ID, &sa.TenantID, &sa.ShadowUserID, &sa.Name, &sa.Description,
             &sa.AllowedScopes, &sa.CreatedBy, &sa.CreatedAt, &sa.DisabledAt); err != nil {
        return nil, uuid.Nil, fmt.Errorf("insert service_account: %w", err)
    }

    if err := tx.Commit(ctx); err != nil {
        return nil, uuid.Nil, err
    }
    return sa, shadowID, nil
}

// Delete cascades to shadow user → cascades to api_keys via FKs.
func (r *ServiceAccountRepo) Delete(ctx context.Context, id uuid.UUID) error {
    _, err := r.pool.Exec(ctx,
        `DELETE FROM users WHERE id = (SELECT shadow_user_id FROM service_accounts WHERE id=$1)`, id)
    return err
}

func (r *ServiceAccountRepo) Get(ctx context.Context, id uuid.UUID) (*ServiceAccount, error) { /* ... */ }
func (r *ServiceAccountRepo) List(ctx context.Context, tenantID uuid.UUID, includeDisabled bool, pageSize int, pageToken string) ([]ServiceAccountWithStats, string, error) { /* keyset on (created_at desc, id desc) */ }
func (r *ServiceAccountRepo) Update(ctx context.Context, in UpdateServiceAccountInput) (*ServiceAccount, error) { /* COALESCE-style UPDATE with RETURNING */ }
func (r *ServiceAccountRepo) CountKeysAffectedByScopeShrink(ctx context.Context, saID uuid.UUID, proposed []string) (int64, error) {
    // Counts active keys whose scopes contains a value not in `proposed`.
    var n int64
    err := r.pool.QueryRow(ctx, `
        SELECT count(*) FROM api_keys
        WHERE service_account_id = $1
          AND is_active = true
          AND EXISTS (SELECT 1 FROM unnest(scopes) s WHERE NOT (s = ANY($2)))`, saID, proposed).Scan(&n)
    return n, err
}
```

- [ ] **Step 5: Run, expect PASS.**

- [ ] **Step 6: Commit**

```bash
git add services/auth/internal/repository/service_account*.go
git commit -m "feat(auth): ServiceAccountRepo — atomic create + cascade delete (FE-API-048)

CreateAtomic generates the SA id up front so the shadow user's synthetic
email sa+<sa_id>@internal.invalid is deterministic per spec §4.1.
Delete cascades via shadow user → ON DELETE CASCADE on api_keys.

$(printf 'Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>')"
```

---

### Task 6: APIKeyRepo — polymorphic owner

**Files:**
- Modify: `services/auth/internal/repository/apikey.go`
- Modify: `services/auth/internal/repository/apikey_test.go`

- [ ] **Step 1: Update `CreateAPIKeyRequest` type to accept either owner**

```go
type CreateAPIKeyRequest struct {
    TenantID         uuid.UUID
    UserID           *uuid.UUID   // exactly one of UserID / ServiceAccountID must be non-nil
    ServiceAccountID *uuid.UUID
    Name             string
    KeyHash          string
    KeyPrefix        string
    Scopes           []string
    ExpiresAt        *time.Time
}
```

- [ ] **Step 2: Write failing tests**

```go
func TestAPIKeyRepo_PolymorphicCreate(t *testing.T) {
    // human-owned key works
    // sa-owned key works
    // both set → error from CHECK
    // neither set → error from CHECK
}

func TestAPIKeyRepo_LookupReturnsOwner(t *testing.T) {
    // Lookup by prefix returns the right ServiceAccountID for SA keys
    // and UserID for human keys.
}
```

Full test bodies follow the pattern from Task 5 — instantiate, insert, assert.

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Implement**

```go
func (r *APIKeyRepo) Create(ctx context.Context, req CreateAPIKeyRequest) (*APIKey, error) {
    // Defence-in-depth — caller should have validated, but the CHECK is the backstop.
    bothNil  := req.UserID == nil && req.ServiceAccountID == nil
    bothSet  := req.UserID != nil && req.ServiceAccountID != nil
    if bothNil || bothSet {
        return nil, fmt.Errorf("apikey: exactly one of UserID/ServiceAccountID must be set")
    }
    if req.Scopes == nil { req.Scopes = []string{} } // matches existing nil→[] fix on services/auth

    key := &APIKey{}
    err := r.pool.QueryRow(ctx, `
        INSERT INTO api_keys (tenant_id, user_id, service_account_id, name, key_hash, key_prefix, scopes, expires_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
        RETURNING id, tenant_id, user_id, service_account_id, name, key_hash, key_prefix,
                  scopes, expires_at, last_used_at, is_active, created_at`,
        req.TenantID, req.UserID, req.ServiceAccountID, req.Name, req.KeyHash, req.KeyPrefix,
        req.Scopes, req.ExpiresAt).Scan(
        &key.ID, &key.TenantID, &key.UserID, &key.ServiceAccountID, &key.Name,
        &key.KeyHash, &key.KeyPrefix, &key.Scopes, &key.ExpiresAt,
        &key.LastUsedAt, &key.IsActive, &key.CreatedAt)
    return key, err
}
```

Add `ServiceAccountID *uuid.UUID` to the `APIKey` struct + scan into it in every existing lookup query (`GetByID`, `GetByPrefix`, `ListByUser`). Add `ListByServiceAccount(ctx, saID)`.

- [ ] **Step 5: Run, expect PASS.**

- [ ] **Step 6: Commit**

---

### Task 7: RBAC `ListMembers` projects principal kind

**Files:**
- Modify: `services/auth/internal/repository/rbac.go`
- Modify: `services/auth/internal/repository/rbac_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestRBAC_ListMembers_ProjectsKind(t *testing.T) {
    ctx := context.Background()
    rbac, saRepo, users := setupRBACWithSA(t, ctx)
    tenant, admin := seedHuman(t, ctx, users, "admin@example.com")
    sa, _, _ := saRepo.CreateAtomic(ctx, CreateServiceAccountInput{
        TenantID: tenant, Name: "ci-prod", CreatedBy: admin})

    // Grant admin a role on org "acme"
    require.NoError(t, rbac.GrantRole(ctx, admin, "admin", "org", "acme", admin))
    // Grant the SA shadow user a role on org "acme"
    require.NoError(t, rbac.GrantRole(ctx, sa.ShadowUserID, "writer", "org", "acme", admin))

    members, err := rbac.ListMembers(ctx, tenant, "org", "acme")
    require.NoError(t, err)
    require.Len(t, members, 2)

    var sawHuman, sawSA bool
    for _, m := range members {
        switch m.Kind {
        case "human":
            require.Equal(t, admin, m.UserID)
            sawHuman = true
        case "service_account":
            require.Equal(t, sa.ShadowUserID, m.UserID)
            require.Equal(t, sa.ID, *m.ServiceAccountID)
            require.Equal(t, "ci-prod", m.DisplayName)
            sawSA = true
        }
    }
    require.True(t, sawHuman && sawSA)
}
```

- [ ] **Step 2: Run, expect FAIL.**

- [ ] **Step 3: Implement** — `ListMembers` joins `users` for `kind` and LEFT JOINs `service_accounts` on `shadow_user_id` for the SA name:

```go
type Member struct {
    UserID           uuid.UUID
    Kind             string         // 'human' | 'service_account'
    DisplayName      string         // user.display_name for humans, SA.name for SAs
    ServiceAccountID *uuid.UUID
    Role             string
    GrantedBy        uuid.UUID
}

func (r *RBACRepo) ListMembers(ctx context.Context, tenantID uuid.UUID, scopeType, scopeValue string) ([]Member, error) {
    rows, err := r.pool.Query(ctx, `
        SELECT u.id, u.kind,
               COALESCE(sa.name, u.display_name, '') AS display_name,
               sa.id AS sa_id,
               r.name, ra.granted_by
        FROM role_assignments ra
        JOIN roles r          ON r.id = ra.role_id
        JOIN users u          ON u.id = ra.user_id
        LEFT JOIN service_accounts sa ON sa.shadow_user_id = u.id
        WHERE ra.tenant_id = $1
          AND ra.scope_type = $2
          AND ra.scope_value = $3`,
        tenantID, scopeType, scopeValue)
    // ...scan loop returning []Member...
}
```

- [ ] **Step 4: Run, expect PASS.**

- [ ] **Step 5: Commit**

---

### Task 8: Service-layer — `ServiceAccountService`

**Files:**
- Create: `services/auth/internal/service/service_account.go`
- Create: `services/auth/internal/service/service_account_test.go`

- [ ] **Step 1: Define the service struct + dependencies**

```go
type ServiceAccountService struct {
    sa       *repository.ServiceAccountRepo
    users    *repository.UserRepo
    keys     *repository.APIKeyRepo
    rbac     *repository.RBACRepo
    audit    AuditEmitter   // small interface — wraps audit.AuditServiceClient
    redis    RedisCmdable
}

type AuditEmitter interface {
    Emit(ctx context.Context, ev audit.Event) error
}
```

- [ ] **Step 2: Write failing tests covering T4 (cascade), audit emission, scope-shrink preflight**

```go
func TestServiceAccount_Create_EmitsAudit(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newSAService(t, ctx)
    tenant, admin := fakes.seedHuman("admin@example.com")

    sa, err := svc.Create(ctx, ServiceAccountInput{
        TenantID: tenant, Name: "ci-prod", AllowedScopes: []string{"pull","push"}, ActorUserID: admin,
    })
    require.NoError(t, err)

    require.Len(t, fakes.audit.Events, 1)
    ev := fakes.audit.Events[0]
    require.Equal(t, "service_account.created", ev.Action)
    require.Equal(t, admin.String(), ev.ActorID)
    require.Equal(t, sa.ID.String(), ev.Resource)
    require.Equal(t, "admin@example.com", ev.Fields["creator_email"])
}

func TestServiceAccount_Disable_SetsRedisRevoke(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newSAService(t, ctx)
    sa := fakes.seedSA("ci-prod")

    require.NoError(t, svc.SetDisabled(ctx, sa.ID, sa.TenantID, true, fakes.admin))

    val, err := fakes.redis.Get(ctx, "revoke:user:"+sa.ShadowUserID.String()).Result()
    require.NoError(t, err)
    require.NotEmpty(t, val, "revoke key must be set on disable")
}

func TestServiceAccount_Delete_Cascades(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newSAService(t, ctx)
    sa := fakes.seedSA("doomed")
    fakes.seedSAKey(sa, "k1")

    require.NoError(t, svc.Delete(ctx, sa.ID, fakes.admin))
    require.Empty(t, fakes.keys.ListByServiceAccount(ctx, sa.ID))
    _, err := fakes.users.GetUserAnyKind(ctx, sa.ShadowUserID)
    require.ErrorIs(t, err, repository.ErrNotFound)
}

func TestServiceAccount_ScopeShrinkPreflight(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newSAService(t, ctx)
    sa := fakes.seedSA("c") // allowed_scopes={read,write}
    fakes.seedSAKey(sa, "k1", "read", "write")
    fakes.seedSAKey(sa, "k2", "read")

    n, err := svc.CountKeysAffectedByScopeShrink(ctx, sa.ID, sa.TenantID, []string{"read"})
    require.NoError(t, err)
    require.EqualValues(t, 1, n, "only k1 has 'write' which is being removed")
}
```

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Implement** — `Create` (atomic via repo; emit `service_account.created` with creator snapshot), `Get`, `List`, `Update`, `SetDisabled` (also writes `revoke:user:<shadow>` with 25-minute TTL when disabling, deletes the key when enabling), `Delete`, `CountKeysAffectedByScopeShrink`, plus admin-gate helpers.

The audit event for creation includes the creator snapshot:

```go
fields := map[string]any{
    "service_account_id": sa.ID.String(),
    "name":               sa.Name,
    "description":        sa.Description,
    "allowed_scopes":     sa.AllowedScopes,
    "creator_email":      creator.Email,
    "creator_display_name": creator.DisplayName,
}
```

For `SetDisabled`:

```go
const revokeKeyTTL = 25 * time.Minute  // longer than the longest JWT TTL (5m default)

func (s *ServiceAccountService) SetDisabled(ctx context.Context, id, tenantID uuid.UUID, disabled bool, actor uuid.UUID) error {
    sa, err := s.sa.Update(ctx, repository.UpdateServiceAccountInput{
        ID: id, TenantID: tenantID, Disabled: &disabled,
    })
    if err != nil { return err }

    revokeKey := "revoke:user:" + sa.ShadowUserID.String()
    if disabled {
        if err := s.redis.Set(ctx, revokeKey, "1", revokeKeyTTL).Err(); err != nil {
            slog.WarnContext(ctx, "set revoke key failed", "err", err, "user_id", sa.ShadowUserID)
            // Best-effort; the DB row is authoritative for ValidateAPIKey.
        }
    } else {
        _ = s.redis.Del(ctx, revokeKey).Err()
    }

    action := "service_account.enabled"
    if disabled { action = "service_account.disabled" }
    return s.audit.Emit(ctx, audit.Event{
        Action: action, ActorID: actor.String(), Resource: sa.ID.String(),
    })
}
```

- [ ] **Step 5: Run, expect PASS.**

- [ ] **Step 6: Commit**

---

### Task 9: Service-layer — `ValidateAPIKey` cross-tenant + scope intersection (T5, T6, T7, T10)

**Files:**
- Modify: `services/auth/internal/service/auth.go`
- Modify: `services/auth/internal/service/service_repo_test.go`

- [ ] **Step 1: Define the new return shape**

```go
type ValidatedKey struct {
    UserID            uuid.UUID      // shadow user id for SA-authed callers
    TenantID          uuid.UUID
    Access            []RepositoryAccess
    PrincipalKind     string         // "human" | "service_account"
    ServiceAccountID  *uuid.UUID     // set when PrincipalKind=="service_account"
    EffectiveScopes   []string
}
```

- [ ] **Step 2: Write failing tests T5 + T6 + T7**

```go
func TestValidateAPIKey_CrossTenantGuard_T5(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newAuthService(t, ctx)
    tenantA, tenantB := uuid.New(), uuid.New()
    sa := fakes.seedSAInTenant(tenantB, "ci")
    keyID, rawSecret := fakes.issueSAKey(sa, "pull", "push")

    _, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{
        KeyID: keyID, RawSecret: rawSecret, RequestTenantID: &tenantA,
    })
    require.Error(t, err)
    require.Equal(t, codes.Unauthenticated, status.Code(err))

    // Audit row recorded
    require.True(t, fakes.audit.HasAction("pentest.cross_tenant_attempt"))
}

func TestValidateAPIKey_ScopeIntersection_T6(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newAuthService(t, ctx)
    sa := fakes.seedSA("c")
    fakes.setAllowedScopes(sa, "pull")           // shrunk
    keyID, secret := fakes.issueSAKey(sa, "pull", "push") // key still has both

    vk, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: keyID, RawSecret: secret})
    require.NoError(t, err)
    require.Equal(t, []string{"pull"}, vk.EffectiveScopes, "intersected with allowed_scopes")
}

func TestValidateAPIKey_LastUsedWritebackFailureIsolated_T7(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newAuthService(t, ctx)
    fakes.poisonTouchLastUsed = true            // forces the writeback path to error
    keyID, secret := fakes.issueHumanKey("alice")
    vk, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: keyID, RawSecret: secret})
    require.NoError(t, err, "writeback failure must not break validation")
    require.Equal(t, "human", vk.PrincipalKind)
}

func BenchmarkValidateAPIKey_T10(b *testing.B) {
    ctx := context.Background()
    svc, fakes := newAuthService(b, ctx)
    keyID, secret := fakes.issueHumanKey("alice")

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        _, err := svc.ValidateAPIKey(ctx, ValidateAPIKeyOpts{KeyID: keyID, RawSecret: secret})
        if err != nil { b.Fatal(err) }
    }
}
```

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Implement** — refactor `ValidateAPIKey` to branch on the polymorphic owner:

```go
func (s *Service) ValidateAPIKey(ctx context.Context, opts ValidateAPIKeyOpts) (*ValidatedKey, error) {
    key, err := s.apiKeys.GetByPrefix(ctx, opts.KeyID)
    if err != nil || !verifyHash(opts.RawSecret, key.KeyHash) {
        return nil, status.Error(codes.Unauthenticated, "invalid key")
    }
    if key.ExpiresAt != nil && key.ExpiresAt.Before(time.Now()) {
        return nil, status.Error(codes.Unauthenticated, "expired")
    }
    if !key.IsActive {
        return nil, status.Error(codes.PermissionDenied, "key revoked")
    }

    switch {
    case key.ServiceAccountID != nil:
        sa, err := s.serviceAccounts.Get(ctx, *key.ServiceAccountID)
        if err != nil { return nil, status.Error(codes.Internal, "lookup sa") }
        if sa.DisabledAt != nil {
            return nil, status.Error(codes.PermissionDenied, "service account disabled")
        }
        // Cross-tenant guard — spec §5.4 + security finding H1.
        if opts.RequestTenantID != nil && *opts.RequestTenantID != sa.TenantID {
            _ = s.audit.Emit(ctx, audit.Event{
                Action: "pentest.cross_tenant_attempt",
                ActorID: sa.ShadowUserID.String(),
                Fields: map[string]any{
                    "service_account_id": sa.ID, "key_id": key.ID,
                    "claimed_tenant": opts.RequestTenantID, "actual_tenant": sa.TenantID,
                },
            })
            return nil, status.Error(codes.Unauthenticated, "tenant mismatch")
        }
        eff := intersectScopes(key.Scopes, sa.AllowedScopes)
        if len(eff) == 0 {
            return nil, status.Error(codes.PermissionDenied,
                "all key scopes have been removed from the service account's allowlist; rotate the key")
        }
        // Best-effort last_used writeback (existing repo.TouchLastUsed path).
        go func() { _ = s.apiKeys.TouchLastUsed(context.Background(), key.ID) }()
        return &ValidatedKey{
            UserID: sa.ShadowUserID, TenantID: sa.TenantID,
            Access: mapScopesToAccess(eff),
            PrincipalKind: "service_account",
            ServiceAccountID: &sa.ID,
            EffectiveScopes: eff,
        }, nil

    case key.UserID != nil:
        // Existing human-user path. Wrap the existing TouchLastUsed call.
        go func() { _ = s.apiKeys.TouchLastUsed(context.Background(), key.ID) }()
        return &ValidatedKey{
            UserID: *key.UserID, TenantID: key.TenantID,
            Access: mapScopesToAccess(key.Scopes),
            PrincipalKind: "human",
            EffectiveScopes: key.Scopes,
        }, nil

    default:
        return nil, status.Error(codes.Internal, "api_key owner is null on both sides — should be prevented by CHECK")
    }
}
```

The `intersectScopes` helper is a 6-line `map[string]bool` intersection.

- [ ] **Step 5: Run all tests + benchmark; expect PASS + benchmark within 5% of baseline.**

Run: `cd services/auth && go test ./internal/service/ -run TestValidateAPIKey -v && go test ./internal/service/ -bench BenchmarkValidateAPIKey_T10 -benchmem`

- [ ] **Step 6: Commit**

---

### Task 10: SSO `GetByEmail` → `GetHumanByEmail`

**Files:**
- Modify: `services/auth/internal/service/sso.go`
- Modify: `services/auth/internal/service/sso_test.go`

- [ ] **Step 1: Write failing regression test**

```go
func TestSSO_RefusesShadowEmail(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newAuthService(t, ctx)
    sa := fakes.seedSA("ci-prod")
    syntheticEmail := "sa+" + sa.ID.String() + "@internal.invalid"

    // Simulate IdP returning the synthetic email.
    _, err := svc.HandleSSOCallback(ctx, SSOCallbackInput{
        Provider: fakes.provider, Email: syntheticEmail, Sub: "abc",
    })
    require.Error(t, err)
    require.Contains(t, err.Error(), "no human user", "must not authenticate as shadow user")
}
```

- [ ] **Step 2: Run, expect FAIL** (today `GetByEmail` returns the SA row).

- [ ] **Step 3: Implement** — change every `users.GetByEmail` call site in `sso.go` (lines ~450 and ~485 per the code-review report) to `users.GetHumanByEmail`. Error message becomes "no human user with email …".

- [ ] **Step 4: Run, expect PASS.**

- [ ] **Step 5: Commit**

---

### Task 11: Activity facade

**Files:**
- Create: `services/auth/internal/service/activity.go`
- Create: `services/auth/internal/service/activity_test.go`

- [ ] **Step 1: Define the type + dependency**

```go
type ActivityService struct {
    users *repository.UserRepo
    audit auditv1.AuditServiceClient
}

type PrincipalActivity struct {
    At         time.Time
    Action     string
    Repo       string
    SourceIP   string
    APIKeyID   string
    Status     string
}
```

- [ ] **Step 2: Write failing test (T9 cross-tenant 404)**

```go
func TestActivity_CrossTenant404_T9(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newActivityService(t, ctx)
    tenantA, tenantB := uuid.New(), uuid.New()
    adminA := fakes.seedHuman(tenantA, "a@x.com")
    targetB := fakes.seedHuman(tenantB, "b@x.com")

    _, err := svc.List(ctx, ListActivityOpts{
        CallerUserID: adminA, CallerTenantID: tenantA, CallerIsAdmin: true,
        TargetUserID: targetB,
    })
    require.Error(t, err)
    require.Equal(t, codes.NotFound, status.Code(err))
}
```

- [ ] **Step 3: Run, expect FAIL.**

- [ ] **Step 4: Implement** — order-of-checks per spec §5.3:

```go
func (s *ActivityService) List(ctx context.Context, opts ListActivityOpts) ([]PrincipalActivity, string, error) {
    target, err := s.users.GetUserAnyKind(ctx, opts.TargetUserID)
    if errors.Is(err, repository.ErrNotFound) || (err == nil && target.TenantID != opts.CallerTenantID) {
        // Identical shape + timing for "not found" and "wrong tenant"
        return nil, "", status.Error(codes.NotFound, "not found")
    }
    if err != nil { return nil, "", status.Error(codes.Internal, "lookup target") }

    if !opts.CallerIsAdmin && target.ID != opts.CallerUserID {
        return nil, "", status.Error(codes.NotFound, "not found")
    }

    resp, err := s.audit.GetAuditEvents(ctx, &auditv1.GetAuditEventsRequest{
        TenantId: opts.CallerTenantID.String(),
        ActorId:  target.ID.String(),
        PageSize: opts.PageSize,
        PageToken: opts.PageToken,
    })
    if err != nil { return nil, "", err }
    return trimEvents(resp.Events), resp.NextPageToken, nil
}
```

- [ ] **Step 5: Run, expect PASS.**

- [ ] **Step 6: Commit**

---

### Task 12: ValidateToken — `revoke:user:<id>` Redis check

**Files:**
- Modify: `services/auth/internal/service/auth.go` (function `ValidateToken`)
- Modify: `services/auth/internal/service/service_repo_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestValidateToken_RespectsUserRevoke(t *testing.T) {
    ctx := context.Background()
    svc, fakes := newAuthService(t, ctx)
    token, claims := fakes.issueJWT("alice")

    // Manually set the revoke key
    require.NoError(t, fakes.redis.Set(ctx, "revoke:user:"+claims.Subject, "1", time.Minute).Err())

    _, err := svc.ValidateToken(ctx, token)
    require.Error(t, err)
    require.Equal(t, codes.Unauthenticated, status.Code(err))
}
```

- [ ] **Step 2: Run, expect FAIL.**

- [ ] **Step 3: Implement** — after the JTI cache hit in `ValidateToken`, also check `revoke:user:<sub>`. If present, return Unauthenticated:

```go
if val, err := s.redis.Get(ctx, "revoke:user:"+claims.Subject).Result(); err == nil && val != "" {
    return nil, status.Error(codes.Unauthenticated, "principal revoked")
}
```

- [ ] **Step 4: Run, expect PASS.**

- [ ] **Step 5: Commit**

---

### Task 13: HTTP handler — `/service-accounts` CRUD

**Files:**
- Create: `services/auth/internal/handler/http_service_accounts.go`
- Create: `services/auth/internal/handler/http_service_accounts_test.go`
- Modify: `services/auth/internal/handler/http.go` (register routes)

- [ ] **Step 1: Write failing handler test (admin gate + audit + happy path)**

```go
func TestHTTP_CreateServiceAccount_RequiresAdmin(t *testing.T) {
    srv, fakes := newHTTPServer(t)
    nonAdminToken := fakes.issueJWT("alice", "reader")

    body := `{"name":"ci-prod","description":"GHA","allowed_scopes":["pull","push"]}`
    req := newJSONReq(t, "POST", "/api/v1/service-accounts", body, nonAdminToken)
    w := httptest.NewRecorder()
    srv.Handler.ServeHTTP(w, req)
    require.Equal(t, http.StatusForbidden, w.Code)
}

func TestHTTP_CreateServiceAccount_HappyPath(t *testing.T) {
    srv, fakes := newHTTPServer(t)
    adminToken := fakes.issueJWT("admin", "admin")

    body := `{"name":"ci-prod","description":"GHA","allowed_scopes":["pull","push"]}`
    req := newJSONReq(t, "POST", "/api/v1/service-accounts", body, adminToken)
    w := httptest.NewRecorder()
    srv.Handler.ServeHTTP(w, req)
    require.Equal(t, http.StatusCreated, w.Code)

    var resp serviceAccountResponse
    require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
    require.NotEmpty(t, resp.ID)
    require.Equal(t, "ci-prod", resp.Name)

    // Audit event landed
    require.True(t, fakes.audit.HasAction("service_account.created"))
}
```

- [ ] **Step 2: Run, expect FAIL.**

- [ ] **Step 3: Implement** — handler file:

```go
type serviceAccountHandler struct {
    svc          *service.ServiceAccountService
    requireAuth  func(*http.Request) (*JWTClaims, error)
    requireAdmin func(claims *JWTClaims, tenantID uuid.UUID) error
}

func (h *serviceAccountHandler) routes(r chi.Router) {
    r.Get("/service-accounts",                       h.list)
    r.Post("/service-accounts",                      h.create)
    r.Get("/service-accounts/{id}",                  h.get)
    r.Patch("/service-accounts/{id}",                h.update)
    r.Delete("/service-accounts/{id}",               h.delete)
    r.Post("/service-accounts/{id}/scopes/preflight", h.scopesPreflight)
    r.Get("/service-accounts/{id}/api-keys",         h.listKeys)
    r.Post("/service-accounts/{id}/api-keys",        h.issueKey)
    r.Delete("/service-accounts/{id}/api-keys/{keyID}", h.revokeKey)
}
```

Each handler: extract claims → admin gate → validate body (name regex `^[a-z0-9]+([._-][a-z0-9]+)*$` len ≤ 64 per CLAUDE.md §7; description max 280 chars; allowed_scopes each `^[a-z][a-z0-9_:]{0,63}$`) → call service → audit → JSON response. Use `writeError` (existing helper) for failures.

Wire `secureHeaders` middleware from `libs/middleware/http` on the route group (SEC-018 compliance per spec §5/M5).

Modify `services/auth/internal/handler/http.go` to register the handler:

```go
saHandler := &serviceAccountHandler{ svc: deps.SAService, requireAuth: h.requireAuth, requireAdmin: h.requireAdmin }
mux.Group(func(r chi.Router) {
    r.Use(middleware.SecureHeaders)
    saHandler.routes(r)
})
```

- [ ] **Step 4: Run all handler tests, expect PASS.**

- [ ] **Step 5: Commit**

---

### Task 14: HTTP — scope-shrink preflight + key issuance scope check

**Files:**
- Modify: `services/auth/internal/handler/http_service_accounts.go` (already touched in T13 — finish `scopesPreflight` + `issueKey`)
- Modify: `services/auth/internal/handler/http_service_accounts_test.go`

- [ ] **Step 1: Write failing tests**

```go
func TestHTTP_ScopesPreflight_CountsAffected(t *testing.T) {
    // SA has allowed_scopes={pull,push} + key with {pull,push}.
    // POST /service-accounts/{id}/scopes/preflight {"allowed_scopes":["pull"]}
    // → {"affected_keys":1}
}

func TestHTTP_IssueKey_RejectsOutOfAllowlistScope(t *testing.T) {
    // SA has allowed_scopes={pull}.
    // POST /service-accounts/{id}/api-keys {"name":"x","scopes":["pull","push"]}
    // → 400 BADREQUEST, body says "scope 'push' not allowed for this service account"
}
```

- [ ] **Step 2: Implement** — preflight calls `svc.CountKeysAffectedByScopeShrink`; key issuance validates `req.Scopes ⊆ sa.AllowedScopes` before calling the create.

- [ ] **Step 3: Run, expect PASS.**

- [ ] **Step 4: Commit**

---

### Task 15: HTTP — `/access/activity`

**Files:**
- Create: `services/auth/internal/handler/http_access_activity.go`
- Create: `services/auth/internal/handler/http_access_activity_test.go`

- [ ] **Step 1: Write failing tests including the 404-not-403 cross-tenant**

```go
func TestHTTP_Activity_CrossTenant404(t *testing.T) {
    // Admin in tenant A queries principal in tenant B → 404, body {"error":"NOT_FOUND"}
}

func TestHTTP_Activity_NonAdminQueryingOther404(t *testing.T) {
    // Non-admin "alice" queries principal_user_id of "bob" → 404 (not 403)
}

func TestHTTP_Activity_SelfQueryWorks(t *testing.T) {
    // Non-admin "alice" queries her own user_id → 200, returns events
}
```

- [ ] **Step 2: Implement** — thin handler over `service.ActivityService.List`. Always returns 404 on the negative paths with body `{"error":"NOT_FOUND"}` and no extra detail.

- [ ] **Step 3: Run, expect PASS.**

- [ ] **Step 4: Commit**

---

### Task 16: `/users/me` sanitised principal envelope

**Files:**
- Modify: `services/auth/internal/handler/http_users_me.go`
- Modify: `services/auth/internal/handler/http_users_me_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestUsersMe_SAKeyCallerGetsPrincipalEnvelope(t *testing.T) {
    srv, fakes := newHTTPServer(t)
    sa := fakes.seedSA("ci-prod")
    keyID, secret := fakes.issueSAKey(sa, "pull", "push")

    req := httptest.NewRequest("GET", "/api/v1/users/me", nil)
    req.Header.Set("Authorization", "Bearer "+rawAPIKeyHeader(keyID, secret))
    w := httptest.NewRecorder()
    srv.Handler.ServeHTTP(w, req)
    require.Equal(t, http.StatusOK, w.Code)

    var resp meResponse
    require.NoError(t, json.NewDecoder(w.Body).Decode(&resp))
    require.Equal(t, "service_account", resp.Type)
    require.Equal(t, sa.ID.String(), resp.ServiceAccount.ID)
    require.Equal(t, "ci-prod", resp.ServiceAccount.Name)
    require.Equal(t, "ci-prod", resp.DisplayName)
    require.Nil(t, resp.Email, "synthetic email must not leak")
}

func TestUsersMe_HumanCallerKeepsExistingShape(t *testing.T) {
    // Returns type="user" + email/display_name as before
}
```

- [ ] **Step 2: Implement** — in the existing handler, after `ValidateAPIKey` (or `ValidateToken`), branch on `PrincipalKind`. For SA: fetch SA via `svc.ServiceAccounts.Get(shadowID)`, return the principal envelope. For human: return the existing shape with `"type":"user"` added.

- [ ] **Step 3: Run, expect PASS.**

- [ ] **Step 4: Commit**

---

### Task 17: `POST /apikeys` grows `service_account_id`

**Files:**
- Modify: `services/auth/internal/handler/http.go` (the existing `createAPIKey` handler from the WIP diff)
- Modify: `services/auth/internal/handler/http_test.go`

- [ ] **Step 1: Write failing test**

```go
func TestHTTP_CreateAPIKey_ForServiceAccount(t *testing.T) {
    srv, fakes := newHTTPServer(t)
    sa := fakes.seedSA("ci-prod")
    adminToken := fakes.issueJWT("admin", "admin")

    body := fmt.Sprintf(`{"name":"ghcr","service_account_id":"%s","scopes":["pull","push"]}`, sa.ID)
    req := newJSONReq(t, "POST", "/api/v1/apikeys", body, adminToken)
    w := httptest.NewRecorder()
    srv.Handler.ServeHTTP(w, req)
    require.Equal(t, http.StatusCreated, w.Code)
}
```

- [ ] **Step 2: Implement** — add optional `service_account_id string` to the JSON struct; when present, the handler:
  - requires admin role for the SA's tenant
  - calls `svc.CreateAPIKey(ctx, tenantID, nil, &saID, name, scopes, expiresAt)` (the polymorphic signature added in Task 6)
  - emits `service_account.key_issued` audit
- When absent, falls back to the existing user-key path with the WIP nil-scopes fix already in this branch.

- [ ] **Step 3: Run, expect PASS.**

- [ ] **Step 4: Commit**

---

### Task 18: Test scaffolding — fixtures + auth_with_audit container

**Files:**
- Create: `services/auth/internal/testutil/sa_fixtures.go`
- Create: `libs/testutil/containers/auth_with_audit.go`

- [ ] **Step 1: Implement** — fresh files; copy from `services/scanner/internal/testutil` for the container helper pattern. `auth_with_audit.go` boots an auth-postgres testcontainer + an audit-postgres testcontainer + an in-process audit gRPC over `bufconn` connected to the audit DB. Returns a `Bundle` with handles to both.

```go
type Bundle struct {
    AuthPool   *pgxpool.Pool
    AuditPool  *pgxpool.Pool
    AuditConn  *grpc.ClientConn
    Cleanup    func()
}
```

`sa_fixtures.go` adds:

```go
func NewServiceAccount(t testing.TB, ctx context.Context, repo *repository.ServiceAccountRepo, users *repository.UserRepo,
    tenant uuid.UUID, name string, allowedScopes ...string) (*repository.ServiceAccount, uuid.UUID) { /* ... */ }

func NewAPIKeyForSA(t testing.TB, ctx context.Context, keys *repository.APIKeyRepo, sa *repository.ServiceAccount,
    name string, scopes ...string) (string /*keyID*/, string /*rawSecret*/) { /* ... */ }
```

- [ ] **Step 2: Add a smoke test that just instantiates the bundle**

- [ ] **Step 3: Run, expect PASS.**

- [ ] **Step 4: Commit**

---

### Task 19: Integration test — activity facade end-to-end

**Files:**
- Create: `services/auth/internal/service/activity_integration_test.go` (uses `//go:build integration`)

- [ ] **Step 1: Write the test**

```go
//go:build integration

func TestIntegration_ActivityFacade_EndToEnd(t *testing.T) {
    ctx := context.Background()
    bundle := containers.NewAuthWithAudit(t, ctx)
    defer bundle.Cleanup()

    // Seed: SA in dev tenant, write 3 audit events for shadow user, query.
    sa := bundle.SeedSA("ci-prod")
    bundle.SeedAuditEvent(sa.ShadowUserID, "push.image", "myorg/myrepo:1.0")
    bundle.SeedAuditEvent(sa.ShadowUserID, "pull.image", "myorg/myrepo:1.0")
    bundle.SeedAuditEvent(sa.ShadowUserID, "auth.token_issued", "")

    activity, err := bundle.AuthAct.List(ctx, ListActivityOpts{
        CallerUserID:   bundle.AdminID,
        CallerTenantID: bundle.TenantID,
        CallerIsAdmin:  true,
        TargetUserID:   sa.ShadowUserID,
        PageSize:       10,
    })
    require.NoError(t, err)
    require.Len(t, activity, 3)
}
```

- [ ] **Step 2: Run with the integration tag, expect PASS** (build tag isolates from default unit suite per docs/TESTING.md convention).

Run: `cd services/auth && go test -tags=integration ./internal/service/ -run TestIntegration_ActivityFacade -v`

- [ ] **Step 3: Commit**

---

### Task 20: CI lint — `lint-user-queries.sh`

**Files:**
- Create: `scripts/lint-user-queries.sh`
- Modify: `.github/workflows/auth.yml` (or whichever per-service workflow handles auth — confirm by `ls .github/workflows/` and editing the auth one)

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# Fails CI if a new query against `users` in services/auth doesn't go through
# the kind-guarded `…Human…` helpers (see FE-API-048 spec §4.1).
set -euo pipefail

BAD=$(grep -rnE 'FROM\s+users\b' services/auth/internal/repository/ \
        --include='*.go' \
        | grep -v 'kind\s*=' \
        | grep -v 'sa_fixtures' \
        | grep -v 'GetUserAnyKind' \
        | grep -v -- '-- allow-any-kind' || true)

if [ -n "$BAD" ]; then
    echo "Found queries against users without a kind guard:"
    echo "$BAD"
    echo
    echo "Use a …Human… helper or annotate the line with '// allow-any-kind' if intentional."
    exit 1
fi
echo "OK"
```

- [ ] **Step 2: Run locally — should pass against the post-Task-4 codebase.**

```bash
chmod +x scripts/lint-user-queries.sh && ./scripts/lint-user-queries.sh
```

- [ ] **Step 3: Add a CI step to the auth workflow**

In `.github/workflows/auth.yml`, after the `go test` step:

```yaml
      - name: Lint user queries
        run: bash scripts/lint-user-queries.sh
```

- [ ] **Step 4: Commit**

---

### Task 21: Dev seed — `infra/dev-seed/service_accounts.sql`

**Files:**
- Create: `infra/dev-seed/service_accounts.sql`
- Modify: whichever file orchestrates dev seed loading (likely `infra/docker-compose/scripts/seed.sh` or a hook in the compose stack — locate by `grep -r dev-seed infra/`)

- [ ] **Step 1: Write the seed**

```sql
-- Dev seed for FE-API-048 — three service accounts under the dev tenant
-- 98dbe36b-ef28-4903-b25c-bff1b2921c9e so the new UI is non-empty on first boot.
-- Idempotent: ON CONFLICT DO NOTHING so re-running the seed is safe.

DO $$
DECLARE
    dev_tenant      UUID := '98dbe36b-ef28-4903-b25c-bff1b2921c9e';
    dev_admin       UUID := (SELECT id FROM users WHERE tenant_id = dev_tenant AND email = 'admin@dev.local' LIMIT 1);
    sa1_id          UUID := '11111111-aaaa-bbbb-cccc-000000000001';
    sa2_id          UUID := '11111111-aaaa-bbbb-cccc-000000000002';
    sa3_id          UUID := '11111111-aaaa-bbbb-cccc-000000000003';
    sa1_shadow      UUID;
    sa2_shadow      UUID;
    sa3_shadow      UUID;
BEGIN
    -- Active SA "ci-prod" with two keys
    INSERT INTO users (tenant_id, email, password_hash, kind)
    VALUES (dev_tenant, 'sa+' || sa1_id || '@internal.invalid', '', 'service_account')
    ON CONFLICT (tenant_id, email) DO NOTHING
    RETURNING id INTO sa1_shadow;

    INSERT INTO service_accounts (id, tenant_id, shadow_user_id, name, description, allowed_scopes, created_by)
    VALUES (sa1_id, dev_tenant, sa1_shadow, 'ci-prod',
            'GitHub Actions deploy bot for myapp', ARRAY['pull','push'], dev_admin)
    ON CONFLICT DO NOTHING;

    -- Disabled SA "old-bot" with one key
    INSERT INTO users (tenant_id, email, password_hash, kind)
    VALUES (dev_tenant, 'sa+' || sa2_id || '@internal.invalid', '', 'service_account')
    ON CONFLICT (tenant_id, email) DO NOTHING
    RETURNING id INTO sa2_shadow;
    INSERT INTO service_accounts (id, tenant_id, shadow_user_id, name, allowed_scopes, created_by, disabled_at)
    VALUES (sa2_id, dev_tenant, sa2_shadow, 'old-bot', ARRAY['pull'], dev_admin, now() - interval '7 days')
    ON CONFLICT DO NOTHING;

    -- Orphaned-creator SA: created_by NULL so the UI exercises the audit-snapshot fallback
    INSERT INTO users (tenant_id, email, password_hash, kind)
    VALUES (dev_tenant, 'sa+' || sa3_id || '@internal.invalid', '', 'service_account')
    ON CONFLICT (tenant_id, email) DO NOTHING
    RETURNING id INTO sa3_shadow;
    INSERT INTO service_accounts (id, tenant_id, shadow_user_id, name, allowed_scopes, created_by)
    VALUES (sa3_id, dev_tenant, sa3_shadow, 'orphaned-creator-sa', ARRAY['pull'], NULL)
    ON CONFLICT DO NOTHING;
END $$;
```

- [ ] **Step 2: Wire it into the seed runner.** Find the loader (e.g. `infra/docker-compose/scripts/seed.sh` or similar) and append a call to this SQL after the existing user/tenant seeds.

- [ ] **Step 3: `docker compose down -v && docker compose up -d`, then `psql` and confirm three SAs exist in the dev tenant.**

- [ ] **Step 4: Commit**

---

## Frontend tasks

### Task 22: vite proxy — add SA + activity routes

**Files:**
- Modify: `frontend/vite.config.ts`

- [ ] **Step 1: Add the entries**

In the `proxy` block, after `"/api/v1/apikeys"`:

```ts
"/api/v1/service-accounts": { target: "http://localhost:8080", changeOrigin: true },
"/api/v1/access":           { target: "http://localhost:8080", changeOrigin: true },
```

- [ ] **Step 2: Restart `npm run dev` and confirm `/api/v1/service-accounts` 404s come from auth (port 8080), not management (port 8091).**

- [ ] **Step 3: Commit**

---

### Task 23: API hooks — service-accounts.ts + activity.ts

**Files:**
- Create: `frontend/src/lib/api/service-accounts.ts`
- Create: `frontend/src/lib/api/activity.ts`

- [ ] **Step 1: Implement** — copied pattern from `frontend/src/lib/api/api-keys.ts`. Each hook uses TanStack Query, the existing `apiClient` axios wrapper, and exposes invalidation keys.

```ts
// service-accounts.ts
export interface ServiceAccount {
  id: string;
  tenant_id: string;
  name: string;
  description: string;
  allowed_scopes: string[];
  shadow_user_id: string;
  created_by: string | null;
  created_at: string;
  disabled_at: string | null;
  active_key_count: number;
  last_used_at: string | null;
}

export const saKeys = {
  all: ["service-accounts"] as const,
  one: (id: string) => ["service-accounts", id] as const,
  keys: (id: string) => ["service-accounts", id, "api-keys"] as const,
};

export function useServiceAccounts(opts?: { includeDisabled?: boolean }) { /* ... */ }
export function useServiceAccount(id: string) { /* ... */ }
export function useCreateServiceAccount() { /* ... */ }
export function useUpdateServiceAccount() { /* ... */ }
export function useDisableServiceAccount() { /* PATCH { disabled: true } */ }
export function useDeleteServiceAccount() { /* ... */ }
export function useIssueSAKey(saID: string) { /* ... */ }
export function useRevokeSAKey(saID: string) { /* ... */ }
export function useScopeShrinkPreflight() {
  return useMutation({
    mutationFn: async (args: { saID: string; allowedScopes: string[] }) => {
      const { data } = await apiClient.post<{ affected_keys: number }>(
        `/service-accounts/${args.saID}/scopes/preflight`,
        { allowed_scopes: args.allowedScopes },
      );
      return data.affected_keys;
    },
  });
}
```

```ts
// activity.ts
export interface PrincipalActivity {
  at: string;
  action: string;
  repo: string;
  source_ip: string;
  api_key_id: string;
  status: "ok" | "denied";
}
export function useActivity(principalUserID?: string, limit = 50) {
  return useQuery({
    enabled: !!principalUserID,
    queryKey: ["access-activity", principalUserID, limit] as const,
    queryFn: async () => {
      const { data } = await apiClient.get<{ activity: PrincipalActivity[]; next_page_token?: string }>(
        `/access/activity`, { params: { principal_user_id: principalUserID, limit } });
      return data;
    },
    staleTime: 10_000,
  });
}
```

- [ ] **Step 2: Vitest type-check passes (`npm run typecheck`).**

- [ ] **Step 3: Commit**

---

### Task 24: AccessHubLayout + AccessSubNav + route restructure

**Files:**
- Modify: `frontend/src/routes/_authenticated.api-keys.tsx` (convert to hub shell)
- Create: `frontend/src/routes/_authenticated.api-keys.index.tsx` (Personal keys — the existing content moves here)
- Create: `frontend/src/components/access/AccessHubLayout.tsx`
- Create: `frontend/src/components/access/AccessSubNav.tsx`

- [ ] **Step 1: Convert `_authenticated.api-keys.tsx` into a layout route with `<Outlet />`**

```tsx
export const Route = createFileRoute("/_authenticated/api-keys")({
  component: ApiKeysHub,
});

function ApiKeysHub(): React.ReactElement {
  return (
    <AccessHubLayout>
      <Outlet />
    </AccessHubLayout>
  );
}
```

- [ ] **Step 2: Create `_authenticated.api-keys.index.tsx`** with the original `ApiKeysSection`-only content (header + the personal keys table).

- [ ] **Step 3: Implement `AccessSubNav`** — vertical rail, three groups (Yours / Workspace / Preview). Admin-gated groups read the JWT claims via the existing `useAuth()` (or whatever the codebase already has — locate via `grep -r isAdmin frontend/src/lib`).

```tsx
export function AccessSubNav() {
  const { isWorkspaceAdmin } = useAuth();
  return (
    <nav aria-label="Access" className="w-48 shrink-0 space-y-6 text-sm">
      <NavGroup label="Yours">
        <NavItem to="/api-keys">Personal keys</NavItem>
      </NavGroup>
      {isWorkspaceAdmin && (
        <>
          <NavGroup label="Workspace">
            <NavItem to="/api-keys/service-accounts">Service accounts</NavItem>
            <NavItem to="/api-keys/activity">Activity</NavItem>
          </NavGroup>
          <NavGroup label="Preview" muted>
            <NavItem to="/api-keys/trust" preview>Federated trust</NavItem>
            <NavItem to="/api-keys/helpers" preview>Credential helpers</NavItem>
            <NavItem to="/api-keys/policies" preview>Token policies</NavItem>
            <NavItem to="/api-keys/review" preview>Access review</NavItem>
          </NavGroup>
        </>
      )}
    </nav>
  );
}
```

`AccessHubLayout`: flex row with `<AccessSubNav />` on the left and `{children}` on the right.

- [ ] **Step 4: Visually confirm `/api-keys` still shows personal keys.**

- [ ] **Step 5: Commit**

---

### Task 25: Service accounts list page + Create dialog

**Files:**
- Create: `frontend/src/routes/_authenticated.api-keys.service-accounts.tsx`
- Create: `frontend/src/components/access/ServiceAccountsTable.tsx`
- Create: `frontend/src/components/access/CreateServiceAccountDialog.tsx`

- [ ] **Step 1: Route file with admin guard**

```tsx
export const Route = createFileRoute("/_authenticated/api-keys/service-accounts")({
  beforeLoad: ({ context }) => {
    if (!context.auth.isWorkspaceAdmin) {
      throw redirect({ to: "/api-keys" });
    }
  },
  component: ServiceAccountsPage,
});
```

- [ ] **Step 2: `ServiceAccountsTable` columns** — Name, Description, Active keys (badge), Last used (relative), Allowed scopes (chips), Status (Active / Disabled). Row click sets the selected SA id in URL search params (`?id=…`) so the drawer is deep-linkable.

- [ ] **Step 3: `CreateServiceAccountDialog`** — name field with the regex hint, description, allowed-scopes chip editor with autocomplete from a hardcoded `KNOWN_SCOPES = ["pull","push","scan","admin"]`. On submit, `useCreateServiceAccount()` then opens the drawer to the new SA.

- [ ] **Step 4: Smoke** — `npm run dev`, login as admin, navigate to `/api-keys/service-accounts`, create an SA. Verify table updates.

- [ ] **Step 5: Commit**

---

### Task 26: ServiceAccountDetail drawer + ScopeShrinkConfirmDialog

**Files:**
- Create: `frontend/src/components/access/ServiceAccountDetail.tsx`
- Create: `frontend/src/components/access/ScopeShrinkConfirmDialog.tsx`
- Modify: `frontend/src/routes/_authenticated.api-keys.service-accounts.tsx` (mount drawer)

- [ ] **Step 1: Drawer sections** — Identity (inline-editable name + description; chip editor for allowed_scopes), API keys (reused pattern from `ApiKeysSection` but pointed at `/service-accounts/{id}/api-keys`; create dialog's scope picker filters to SA's allowed_scopes), Activity preview (top 5 events via `useActivity(sa.shadow_user_id, 5)` + "View all" link to `/api-keys/activity?user={shadow_id}`), Danger zone (Disable/Re-enable + Delete).

- [ ] **Step 2: ScopeShrinkConfirmDialog flow** — when the chip editor removes a scope:

```tsx
const preflight = useScopeShrinkPreflight();

async function onCommitScopeChange(newScopes: string[]) {
  const removed = sa.allowed_scopes.filter(s => !newScopes.includes(s));
  if (removed.length === 0) {
    return saveDirectly(newScopes);
  }
  const affected = await preflight.mutateAsync({ saID: sa.id, allowedScopes: newScopes });
  setShrinkDialog({ open: true, affected, newScopes });
}
```

The dialog body reads: "This will narrow N active keys. Existing tokens with the removed scope will stop working immediately."

- [ ] **Step 3: Manual smoke per §8.7 #4** — shrink scope, confirm dialog shows correct count, save → next `docker push` with the affected key returns 401.

- [ ] **Step 4: Commit**

---

### Task 27: Activity table

**Files:**
- Create: `frontend/src/routes/_authenticated.api-keys.activity.tsx`
- Create: `frontend/src/components/access/ActivityTable.tsx`

- [ ] **Step 1: Page filters** — principal dropdown (humans + SAs from `useServiceAccounts()` and `useUsers()` for admins; self only for non-admins), action multi-select, time range 24h/7d/30d (default 7d). Keyset pagination via "Load more".

- [ ] **Step 2: Table columns** — When (relative + tooltip absolute), Principal (user/SA name), Action, Repo, IP, Key (prefix), Status (badge).

- [ ] **Step 3: Empty state** — "No activity in this window."

- [ ] **Step 4: Commit**

---

### Task 28: Preview surfaces + PreviewBanner with a11y

**Files:**
- Create: `frontend/src/components/access/PreviewBanner.tsx`
- Create: `frontend/src/components/access/previews/TrustPreview.tsx`
- Create: `frontend/src/components/access/previews/HelpersPreview.tsx`
- Create: `frontend/src/components/access/previews/PoliciesPreview.tsx`
- Create: `frontend/src/components/access/previews/ReviewPreview.tsx`
- Create: `frontend/src/routes/_authenticated.api-keys.trust.tsx`
- Create: `frontend/src/routes/_authenticated.api-keys.helpers.tsx`
- Create: `frontend/src/routes/_authenticated.api-keys.policies.tsx`
- Create: `frontend/src/routes/_authenticated.api-keys.review.tsx`

- [ ] **Step 1: `PreviewBanner` with a11y attributes**

```tsx
interface PreviewBannerProps { sprint: string; futureID: string; }
export function PreviewBanner({ sprint, futureID }: PreviewBannerProps) {
  return (
    <div
      role="status"
      aria-live="polite"
      className="rounded-lg border border-amber-300 bg-amber-50 px-4 py-3 text-sm"
    >
      <strong className="font-medium">Preview.</strong> This surface ships in{" "}
      <strong>{sprint}</strong> (<code>{futureID}</code>). The data below is
      illustrative. Have feedback? Drop it in{" "}
      <code>futures.md</code>.
    </div>
  );
}
```

- [ ] **Step 2: `TrustPreview`** — cards listing dummy GHA / GitLab / Buildkite trusts + disabled "New trust relationship" button. Every disabled control has `disabled aria-disabled="true" aria-describedby={tipID}` + a hidden span carrying "Available in Sprint 11."

- [ ] **Step 3: `HelpersPreview`** — tabbed code blocks (docker login, k8s YAML, terraform, GHA snippet) with working Copy buttons. Key selector defaults to a hardcoded dummy.

- [ ] **Step 4: `PoliciesPreview`** — three policy cards with disabled sliders.

- [ ] **Step 5: `ReviewPreview`** — dummy table of stale keys with disabled action buttons.

- [ ] **Step 6: Each route file** is a 5-liner: `createFileRoute(...)({ component: () => <PreviewLayoutWrapper> ... </PreviewLayoutWrapper> })` plus the corresponding preview component.

- [ ] **Step 7: a11y check** — screen-reader (`axe-core` dev tool) reports no violations on each preview route.

- [ ] **Step 8: Commit**

---

### Task 29: Topbar avatar branches on `/users/me` `type`

**Files:**
- Modify: `frontend/src/components/shell/topbar.tsx`

- [ ] **Step 1: Read `type` from `useMe()` (locate via `grep -r "users/me" frontend/src`). Branch:**

```tsx
{me.type === "service_account" ? (
  <BotAvatar name={me.display_name} />
) : (
  <ProfileChip email={me.email} name={me.display_name} />
)}
```

- [ ] **Step 2: Login flow normally → avatar still shows human. Login with SA key (via CLI dev workflow) → avatar shows bot.**

- [ ] **Step 3: Commit**

---

## Cross-cutting tasks

### Task 30: Frontend tests — route guards + PreviewBanner

**Files:**
- Create: `frontend/src/routes/__tests__/api-keys.service-accounts.route.test.tsx`
- Create: `frontend/src/components/access/__tests__/PreviewBanner.test.tsx`
- Create: `frontend/src/components/access/__tests__/AccessSubNav.test.tsx`

- [ ] **Step 1: Route guard test**

```tsx
test("non-admin loading /api-keys/service-accounts redirects to /api-keys", async () => {
  const router = createTestRouter({ auth: { isWorkspaceAdmin: false } });
  await router.navigate({ to: "/api-keys/service-accounts" });
  expect(router.state.location.pathname).toBe("/api-keys");
});
```

- [ ] **Step 2: PreviewBanner test**

```tsx
test("PreviewBanner exposes role=status with the sprint reason", () => {
  render(<PreviewBanner sprint="Sprint 11" futureID="FUT-001" />);
  const banner = screen.getByRole("status");
  expect(banner).toHaveAttribute("aria-live", "polite");
  expect(banner).toHaveTextContent("Sprint 11");
  expect(banner).toHaveTextContent("FUT-001");
});
```

- [ ] **Step 3: AccessSubNav test**

```tsx
test("AccessSubNav hides Workspace + Preview groups for non-admins", () => {
  render(<AccessSubNav />, { wrapper: withAuth({ isWorkspaceAdmin: false }) });
  expect(screen.queryByText("Workspace")).not.toBeInTheDocument();
  expect(screen.queryByText("Preview")).not.toBeInTheDocument();
});
```

- [ ] **Step 4: `npm run test` — all PASS.**

- [ ] **Step 5: Commit**

---

### Task 31: Docs — CLAUDE.md, proto comments, status, futures, security

**Files:**
- Modify: `CLAUDE.md` (§4.2 Owns column, §14 decision-log row 22)
- Modify: `proto/auth/v1/auth.proto` (comments on `user_id` fields)
- Modify: `status.md` (FE-API-048 row)
- Modify: `FE-STATUS.md` (Sprint 10 row + new route entries)
- Modify: `futures.md` (Tier 2 — Access: machine identity & policy section with FUT-001..004)
- Modify: `security.md` (proactive notes for the two resolved HIGH items)
- Modify: `frontend/src/routes/_authenticated.api-keys.tsx` (remove the "future scope" comment block — those features are now real routes)

- [ ] **Step 1: CLAUDE.md §4.2** — change the `registry-auth` "Owns" cell to include `service_accounts`.

- [ ] **Step 2: CLAUDE.md §14** — append row 22:

```markdown
| 22 | Service-account principal pattern: shadow users (FE-API-048) | Each service account auto-provisions a `users.kind='service_account'` row. `ValidateAPIKey`/`ValidateToken` return that id in `user_id`; downstream services treat it as an opaque actor. RBAC/audit/RLS/JWT machinery unchanged. Distinguishing principal kind is a read-path concern (`LEFT JOIN users ON kind`), not a write-path one. | 2026-06-21 |
```

- [ ] **Step 3: proto/auth/v1/auth.proto** — add comments:

```proto
  // user_id is the authenticated principal. May be a service-account shadow
  // user id (kind='service_account'); join users.kind to distinguish.
  string user_id    = 2;
```

on the `ValidateTokenResponse.user_id` and `ValidateAPIKeyResponse.user_id` fields.

- [ ] **Step 4: status.md** — append a new row to the FE-API tracker:

```markdown
| FE-API-048 | DONE ✅ | — | Service accounts + /api-keys access hub. Three goose migrations + 9 new HTTP routes on services/auth + shadow-user principal pattern + activity facade over services/audit + four preview surfaces in the frontend hub. Spec: docs/superpowers/specs/2026-06-21-service-accounts-and-access-hub-design.md. Plan: docs/superpowers/plans/2026-06-21-service-accounts-fe-api-048.md. |
```

- [ ] **Step 5: FE-STATUS.md** — add Sprint 10 + routes:

```markdown
| FE-API-048 | Service accounts + activity hub | DONE ✅ | `/api-keys` hub with sub-routes for personal keys, service accounts, activity, plus four preview surfaces (trust/helpers/policies/review) carrying dummy data + a11y-compliant PreviewBanner. |
```

- [ ] **Step 6: futures.md** — promote the four items into a new "Tier 2 — Access: machine identity & policy" section with the dummy-preview surface noted as already shipped.

- [ ] **Step 7: security.md** — add proactive notes:

```markdown
### PENTEST-AUTH-001 — Polymorphic api_keys cross-tenant guard (resolved pre-merge)
Closed by FE-API-048 implementation. `ValidateAPIKey` for service-account
keys verifies the request's claimed tenant matches `service_accounts.tenant_id`;
mismatch returns Unauthenticated + writes a `pentest.cross_tenant_attempt`
audit row. Test: T5 in spec §8.1.

### PENTEST-AUTH-002 — JWT revocation pattern extended to per-user (resolved pre-merge)
Closed by FE-API-048 implementation. `ValidateToken` consults
`revoke:user:<user_id>` Redis key set by `SetDisabled` on a service account.
Closes the 300s JTI window for the SA disable path. Pattern is documented
under CLAUDE.md §7 "JWT Validation."
```

- [ ] **Step 8: frontend/src/routes/_authenticated.api-keys.tsx** — drop the "Future scope" comment that listed SAs / activity / IP allowlists; those have either shipped or have preview routes.

- [ ] **Step 9: Commit**

```bash
git commit -m "docs: FE-API-048 closure — CLAUDE.md §14, proto comments, trackers"
```

---

### Task 32: Manual smoke — §8.7 checklist

Not a code task — but explicitly tracked here so the implementer doesn't skip it.

- [ ] **Step 1: Workflow #1** — Workspace admin creates SA → issues key → uses it with `docker login` → push a tag → check audit + `/api-keys/activity`.

- [ ] **Step 2: Workflow #2** — Orphan-creator: SA created by admin X → delete X via SQL → SA's keys still validate; UI shows "created by … (deactivated)".

- [ ] **Step 3: Workflow #3** — Soft-disable round-trip + revoke key TTL behaviour.

- [ ] **Step 4: Workflow #4** — Scope-shrink: dialog shows correct count, save, immediate 401 on affected key.

- [ ] **Step 5: Workflow #5** — `/access/activity` of a deleted shadow user → 404 (not 500).

- [ ] **Step 6: Workflow #6** — non-admin login → no FOUC of admin nav.

- [ ] **Step 7: OCI conformance re-run** — `make oci-conformance` (or whatever the repo target is — check `Makefile`) shows 75/75. If it regresses, the `BenchmarkValidateAPIKey_T10` benchmark from Task 9 is the first place to look.

- [ ] **Step 8: If everything passes, file the closing PR.**

---

## Self-review (writing-plans skill checklist)

**Spec coverage** — every section's requirements have at least one task:

- §3 forks: Task 1+2+3 (polymorphic owner with CHECK), Task 5+6 (allowed_scopes column + free-form TEXT[]), Task 5+6 (shadow user pattern), Task 8 (disable + delete both implemented).
- §4 data model: Tasks 1–3 (migrations), Task 4 (kind guard pushed to repo layer).
- §5.1–5.4 wire + cross-tenant + scope intersection: Tasks 13–17 (HTTP routes), Task 9 (`ValidateAPIKey`).
- §5.5 JWT revoke: Task 12 (`ValidateToken` Redis check) + Task 8 (`SetDisabled` writes the key).
- §5.6 `/users/me` envelope: Task 16.
- §5.7 audit vocabulary: Tasks 8 + 13 (audit assertions on every mutating handler).
- §6 UI hub: Tasks 22 (proxy), 23 (hooks), 24 (layout), 25 (SA list), 26 (drawer + scope shrink), 27 (activity), 28 (previews + a11y).
- §7 migration + RLS unchanged + proto note: Tasks 1–3 (migrations), Task 31 (proto comments + CLAUDE.md §14).
- §8 testing matrix T1–T10: T1+T2+T3 in Task 3, T4 in Task 8, T5+T6+T7+T10 in Task 9, T8 in Task 4, T9 in Task 15 (+ integration in Task 19). Scaffolding in Task 18. Dev seed in Task 21.
- §9 doc updates: Task 31.
- §10 open-questions all already folded into the spec; resolutions are implementer-visible above.

**Placeholder scan** — no "TBD" / "TODO" / "fill in" / "similar to Task N". Where I omitted `Get/List/Update` bodies in Task 5 they reference the prior CRUD pattern in the same file by example; reasonable since the engineer just opened the file.

**Type consistency** — `ValidatedKey`, `ServiceAccount`, `ServiceAccountWithStats`, `CreateServiceAccountInput`, `UpdateServiceAccountInput`, `Member`, `PrincipalActivity`, `CreateAPIKeyRequest` all defined once and reused with the same name everywhere they appear. The `intersectScopes` helper is mentioned in Task 9 as a 6-line internal helper — implementer writes it inline.

---

**Plan complete.**
