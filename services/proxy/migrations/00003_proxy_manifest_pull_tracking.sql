-- +goose Up
-- FUT-013: Track cache utilisation so /proxy/cache can surface real signals.
--
-- Adds three columns to proxy_manifests:
--   * last_pulled_at — most recent client-pull. NULL until first pull lands
--     after this migration; existing rows are NOT backfilled (we don't have
--     a history record). The FE renders NULL as "never" rather than
--     pretending a fetch-time pull happened.
--   * pull_count — cumulative pulls. UPDATEd async by the cache-hit path
--     in services/proxy/internal/handler/http.go. Zero on existing rows
--     by default — same "never pulled since the column existed" semantics.
--   * size_bytes — octet_length(body) materialised. Recomputed only on
--     insert/upsert (body length is immutable for a given digest, so this
--     is set-and-forget per row). Backfilled inline for existing rows so
--     GetCacheStats doesn't need a SUM(octet_length(body)) full scan.
--
-- Why a new index on (tenant_id, fetched_at DESC):
-- The list endpoint sorts by fetched_at descending (operators want
-- "what was cached most recently" at the top). The existing
-- idx_proxy_manifests_lookup is (tenant_id, upstream_id, image, reference)
-- — perfect for the OCI serve path, wrong for the list page.

-- +goose StatementBegin
ALTER TABLE proxy_manifests
    ADD COLUMN last_pulled_at TIMESTAMPTZ,
    ADD COLUMN pull_count     BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN size_bytes     BIGINT NOT NULL DEFAULT 0;

-- Backfill size_bytes for existing rows so stats aggregates are correct
-- from the first call (the alternative is a lazy backfill on first read,
-- which adds complexity for no upside on a table that only grows by
-- cache hits).
UPDATE proxy_manifests SET size_bytes = octet_length(body) WHERE size_bytes = 0;

CREATE INDEX idx_proxy_manifests_list ON proxy_manifests(tenant_id, fetched_at DESC);
-- +goose StatementEnd

-- +goose Down

-- +goose StatementBegin
DROP INDEX IF EXISTS idx_proxy_manifests_list;
ALTER TABLE proxy_manifests
    DROP COLUMN IF EXISTS size_bytes,
    DROP COLUMN IF EXISTS pull_count,
    DROP COLUMN IF EXISTS last_pulled_at;
-- +goose StatementEnd
