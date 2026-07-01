# Database Conventions

> **Source of truth for database mechanics.** CLAUDE.md §11 holds only
> the rules; this file holds the pgx pool config, DSN format, migration
> mechanics, and read-replica routing. When code disagrees with this
> file, the code is wrong — but `libs/config/loader.DBConfig` and the
> per-service `migrations/` directories are the authoritative
> implementations.

## Table of Contents

1. [General Rules](#1-general-rules)
2. [Connection Pool](#2-connection-pool)
3. [Migration Rules](#3-migration-rules)
4. [Connection String](#4-connection-string)
5. [Read Replica Routing (REM-008)](#5-read-replica-routing-rem-008)

---

## 1. General Rules

- ORM: **none**. Use `pgx/v5` directly with `pgxpool`. Raw SQL only.
- All queries parameterised. Never use `fmt.Sprintf` to build SQL.
- Migrations: `pressly/goose` with SQL migrations embedded via `embed.FS` (`migrations/migrations.go`).
- Only `registry-metadata` has direct PostgreSQL access for metadata; `registry-auth`, `registry-tenant`, `registry-proxy`, `registry-webhook`, `registry-audit` have their own separate databases.
- Every query must use the request context for cancellation.
- Transactions: always use `defer tx.Rollback(ctx)` — only commit explicitly on success.
- Connection-pool exhaustion is mapped to `codes.ResourceExhausted` via `libs/errors/codes.MapDBError` (SEC-006).

---

## 2. Connection Pool

Connection pool is built from `libs/config/loader.DBConfig.PoolConfig()` which sets:

| Setting | Value | Rationale |
|---|---|---|
| `ConnectTimeout` | `5s` | Fail-fast when Postgres is unreachable at startup |
| `MaxConnLifetime` | `30m` | Recycle connections often enough that a rolling PG restart drains them within one deploy window |
| `MaxConnIdleTime` | `5m` | Free idle sockets so a horizontally-scaled service doesn't monopolise the pool |
| `MaxConns` | `$DB_MAX_CONNS` (default `20`) | Bounded per-service so a runaway consumer can't exhaust the shared PG connection budget |

Each service reads `DB_MAX_CONNS` from its config; the loader clamps it inside `PoolConfig()`.

---

## 3. Migration Rules

- **Never drop a column in a migration** — add a new column and migrate data in a separate step. Even if the schema thinks the column is unused, in-flight requests from an older binary may still reference it. Two migrations (add → backfill → cut writes → drop next release) is the safe form.
- Every migration must be reversible (down migration required). Down migrations may be destructive in dev; production rollback is forward-only (a fix-forward migration).
- Migration naming: `YYYYMMDDHHMMSS_<description>.sql`.
- Run migrations at startup in a separate step before serving traffic (use `goose up`).

---

## 4. Connection String

```
DB_DSN=postgres://<user>:<password>@<host>:<port>/<database>?sslmode=require
```

- `sslmode=require` is **mandatory in production**; `sslmode=disable` is rejected at startup (SEC-022).
- `sslmode=prefer` is permitted only in the local dev Compose stack.

---

## 5. Read Replica Routing (REM-008)

- `DB_DSN_REPLICA` (optional) configures a read pool routed by `repository.reader()`.
- The following queries currently route to the replica when configured:
  - `ListRepositories`
  - `ListTags`
  - `ListOrphanedBlobs`
- Without a replica DSN, all queries fall through to the primary pool — `repository.reader()` returns the primary pool as a passthrough.

---

> **Last updated:** see Git log.
