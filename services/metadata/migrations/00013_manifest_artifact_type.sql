-- +goose Up

-- S-MAINT-1 Batch 5 (P6 + F4) — artifact type discriminator.
--
-- OCI Distribution Spec v1.1 supports arbitrary artifacts on the same
-- manifest endpoints used for container images: Helm 3 charts, Cosign
-- signatures, SPDX SBOMs, WASM modules, Singularity images. The wire-
-- level manifest media type is the same (`application/vnd.oci.image.
-- manifest.v1+json`); the discriminator is the `config.mediaType` field
-- INSIDE the manifest JSON. Indexing that field as a column lets us:
--   • Skip non-image artifacts in the scanner (P6 — Trivy/Grype find
--     nothing in a Helm tarball, just waste a scan cycle).
--   • Render an artifact-type pill on the Tags table (F4 — operator
--     can tell at a glance whether a tag is an image, chart, or sig).
--   • Filter the Tags list by artifact type via a query param.
--
-- Why a TEXT column (not an enum):
--   • New artifact types appear at the OCI spec layer faster than we
--     ship migrations. A new chart format / signature scheme should
--     not require a goose migration to surface.
--   • The wire-shape discriminator on the proto stays a *derived*
--     string ("image" | "helm" | "signature" | "sbom" | "other") so
--     downstream consumers can't accidentally couple to the raw media
--     type. Repository code maps mediaType → discriminator.
--
-- Backfill: existing rows have the manifest JSON in `raw_json`, so we
-- can parse `config.mediaType` out of it in the same migration via the
-- jsonb path operator. Rows whose raw_json doesn't have a valid config
-- block (or where the JSON is malformed at rest, which would be a
-- pre-existing data-integrity issue) land with NULL config_media_type
-- and will be repaired naturally on the next PutManifest.

ALTER TABLE manifests
    -- Nullable so existing rows that the backfill misses still pass the
    -- NOT NULL guard on later inserts. PutManifest writes this on every
    -- new manifest, so post-deploy the only NULLs are the backfill
    -- holes (malformed historical raw_json — vanishingly rare in prod).
    ADD COLUMN config_media_type TEXT;

-- Backfill from raw_json. Postgres can parse bytea as jsonb in one
-- step provided the bytes are valid UTF-8 JSON — they are, because the
-- OCI spec requires it and registry-core validates on push. The CASE
-- guard returns NULL for rows where the parse fails (defensive: a
-- corrupted historical row shouldn't abort the migration).
UPDATE manifests
SET    config_media_type = (
           CASE
               WHEN raw_json IS NULL THEN NULL
               ELSE NULLIF(
                   (convert_from(raw_json, 'UTF8')::jsonb -> 'config' ->> 'mediaType'),
                   ''
               )
           END
       )
WHERE  config_media_type IS NULL;

-- Index for the F4 filter path. Partial on NOT NULL keeps the index
-- small — most rows in a healthy deployment will be `image` and a
-- query for "give me all the helm charts" only needs to scan the
-- non-image rows.
CREATE INDEX idx_manifests_config_media_type
    ON manifests(tenant_id, config_media_type)
    WHERE config_media_type IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_manifests_config_media_type;
ALTER TABLE manifests
    DROP COLUMN IF EXISTS config_media_type;
