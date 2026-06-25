-- +goose Up

-- FUT-019 Phase 2 — scheduled notifications + per-user preferences.
--
-- Two tables back the scheduled-notification feature:
--
--   scheduled_notifications        the queue. A scheduler worker inserts
--                                  rows when a category is due (e.g.
--                                  monthly scanner_freshness check). A
--                                  dispatcher worker drains pending rows
--                                  with `FOR UPDATE SKIP LOCKED`,
--                                  renders one notification_events row
--                                  per recipient that hasn't opted out
--                                  of the category, then marks the
--                                  scheduled row delivered.
--
--   user_notification_preferences  per-user opt-in matrix. One row per
--                                  (user_id, category). Defaults to
--                                  bell_enabled=TRUE / email_enabled=
--                                  FALSE / webhook_enabled=FALSE so a
--                                  brand-new user starts subscribed to
--                                  every bell category without lock-in
--                                  to email/webhook channels we haven't
--                                  proven yet.
--
-- Tenant isolation: every row carries tenant_id, every read filters on
-- it. Matches the rest of the audit schema (no RLS — application-layer
-- isolation is the documented posture per CLAUDE.md §9).
--
-- Status enum on scheduled_notifications:
--   pending   → not yet picked up (initial state on insert)
--   in_progress → claimed by dispatcher (FOR UPDATE SKIP LOCKED winner)
--   delivered → notification_events rows successfully written
--   failed    → all retry attempts exhausted (attempt counter > 3)

CREATE TABLE scheduled_notifications (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL,
    category        TEXT        NOT NULL,
    due_at          TIMESTAMPTZ NOT NULL,
    payload         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    status          TEXT        NOT NULL DEFAULT 'pending',
    attempts        INT         NOT NULL DEFAULT 0,
    last_error      TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at    TIMESTAMPTZ,
    CONSTRAINT scheduled_notifications_status_check
        CHECK (status IN ('pending', 'in_progress', 'delivered', 'failed'))
);

-- Dispatcher lookup pattern: "what's due RIGHT NOW that hasn't been
-- claimed?". The partial index narrows the scan to only the rows the
-- dispatcher cares about — most rows in the table will be 'delivered'
-- and we want the planner to ignore them outright.
CREATE INDEX scheduled_notifications_due_idx
    ON scheduled_notifications (due_at)
    WHERE status = 'pending';

-- Idempotency guard for the scheduler: a (tenant_id, category, due_at)
-- triple shouldn't insert twice. Lets a scheduler retry after a crash
-- without double-scheduling. due_at is truncated to the hour at write
-- time so a retry at 14:01 doesn't bypass the unique on the 14:00 row.
CREATE UNIQUE INDEX scheduled_notifications_unique_idx
    ON scheduled_notifications (tenant_id, category, due_at);

-- Per-user preferences. A missing row means "use defaults" (bell on,
-- email off, webhook off) — we don't backfill at install time so an
-- ops install with N users doesn't pay an N-row INSERT cost. The PATCH
-- endpoint UPSERTs.
CREATE TABLE user_notification_preferences (
    user_id          UUID        NOT NULL,
    tenant_id        UUID        NOT NULL,
    category         TEXT        NOT NULL,
    bell_enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    email_enabled    BOOLEAN     NOT NULL DEFAULT FALSE,
    webhook_enabled  BOOLEAN     NOT NULL DEFAULT FALSE,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, category)
);

-- Tenant-scoped lookup pattern (admin listing all users in a tenant
-- with their preferences for a given category — not used today but a
-- Phase 3 feature we want to keep cheap).
CREATE INDEX user_notification_preferences_tenant_idx
    ON user_notification_preferences (tenant_id, category);

-- +goose Down

DROP INDEX IF EXISTS user_notification_preferences_tenant_idx;
DROP TABLE IF EXISTS user_notification_preferences;
DROP INDEX IF EXISTS scheduled_notifications_unique_idx;
DROP INDEX IF EXISTS scheduled_notifications_due_idx;
DROP TABLE IF EXISTS scheduled_notifications;
