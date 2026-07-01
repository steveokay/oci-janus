-- +goose Up

-- FUT-020 — image promotion history.
--
-- One row per successful PromoteTag call. A promotion is the atomic act
-- of copying a source tag's manifest_digest onto a destination tag inside
-- the same transaction as the tags upsert (see repository.PromoteTag).
--
-- Column shape:
--
--   src_org / src_repo / src_tag / src_digest — the source at promotion time.
--   dst_org / dst_repo / dst_tag / dst_digest — the destination after promotion.
--   Today src_digest ALWAYS equals dst_digest — the whole point of a
--   promotion is atomic tag copy — but the columns are split so a future
--   re-sign / co-sign / re-tag workflow can diverge them without needing
--   a schema migration.
--
--   actor_user_id — the JWT-derived user id of the operator who triggered
--   the promotion, or NULL for CLI / bot / SA-key-driven promotions where
--   the caller was an API key not owned by a human user. Nullable rather
--   than a required FK so a later user deletion doesn't cascade a
--   promotion row out of the history table.
--
--   note — optional operator-visible comment. Bounded by the BFF at
--   256 chars; TEXT DEFAULT '' means the audit event carries a
--   deterministic value even when no note was supplied.
--
--   promoted_at — the transaction time, populated by NOW() at insert.
--
-- Tenant-isolation posture matches every other table in this schema —
-- app-layer WHERE clauses in services/metadata; RLS is documented in
-- CLAUDE.md §9 but not applied per-table today (Phase 7 pending).

CREATE TABLE promotions (
    id             UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID        NOT NULL,
    src_org        TEXT        NOT NULL,
    src_repo       TEXT        NOT NULL,
    src_tag        TEXT        NOT NULL,
    src_digest     TEXT        NOT NULL,
    dst_org        TEXT        NOT NULL,
    dst_repo       TEXT        NOT NULL,
    dst_tag        TEXT        NOT NULL,
    dst_digest     TEXT        NOT NULL,
    actor_user_id  UUID,
    note           TEXT        NOT NULL DEFAULT '',
    promoted_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The "recent promotions for this tenant" query drives the workspace-
-- wide promotions view; sorting by promoted_at DESC keeps the newest
-- entries at the front of the index without a Sort node on every read.
CREATE INDEX idx_promotions_tenant_time
    ON promotions (tenant_id, promoted_at DESC);

-- The repo detail page shows "promotions that TOUCH this repo" —
-- source OR destination. The dst-side index covers the destination
-- lookup pattern; the source-side lookup is served by the tenant_time
-- index above via a filter (there is no need for a symmetric src index
-- because most promotions are read from the destination's perspective
-- — the operator watching a `prod` repo cares about "what got promoted
-- INTO this repo?" more than "what got promoted FROM this repo?").
CREATE INDEX idx_promotions_dst_lookup
    ON promotions (tenant_id, dst_org, dst_repo, promoted_at DESC);

-- +goose Down

DROP INDEX IF EXISTS idx_promotions_dst_lookup;
DROP INDEX IF EXISTS idx_promotions_tenant_time;
DROP TABLE IF EXISTS promotions;
