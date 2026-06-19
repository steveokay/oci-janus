-- +goose Up
-- Add an aggregate image-size column to manifests so we can render per-tag size
-- in the management API without re-parsing the raw OCI JSON on every list.
--
-- For an image manifest (schemaVersion 2 with `config` + `layers`):
--   image_size_bytes = config.size + SUM(layers[].size)
-- For an image index (`manifests` array of platform-specific manifests):
--   image_size_bytes = SUM(manifests[].size)   -- doc sizes, not per-platform image sizes
-- New rows are populated by `PutManifest` in Go (parseImageSize); existing rows
-- start at the DEFAULT 0 and stay there until the operator-run backfill script
-- documented in infra/runbooks/manifest-image-size-backfill.md sweeps them.
--
-- PENTEST-028 (2026-06-19): the original migration also ran an unbounded
-- `FOR r IN SELECT … FROM manifests` DO loop here. Goose runs each migration
-- in a single transaction, so on a tenant with hundreds of thousands of rows
-- that loop would (a) hold one connection + one long transaction for the full
-- backfill, (b) block autovacuum on `manifests` for the duration, and
-- (c) stall registry-metadata startup since goose up runs before Serve.
-- The backfill has been moved out of the migration entirely — see the runbook.
ALTER TABLE manifests ADD COLUMN image_size_bytes BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE manifests DROP COLUMN IF EXISTS image_size_bytes;
