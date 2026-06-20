-- +goose Up
-- FE-API-033: per-tag SBOM storage.
--
-- The scanner already writes one scan_results row per (tenant, manifest_digest)
-- via UpsertScanResult / UpdateScanStatus. This migration adds two nullable
-- columns so each scan can carry the SBOM blob it produced:
--
--   sbom_format — short identifier of the wire format ("spdx-json", and
--                 reserved for "cyclonedx-json" once the scanner emits it).
--                 NULL marks a row whose SBOM has not been generated yet.
--   sbom_json   — the raw SBOM bytes. BYTEA so future binary formats fit
--                 without another column; SPDX JSON is also UTF-8 bytes.
--
-- Both columns are NULLABLE on purpose: existing scan_results rows pre-date
-- the column and stay un-SBOM'd until they are rescanned. The management
-- BFF returns 404 `{code: "no-sbom"}` for any tag whose latest scan row
-- has sbom_json IS NULL.
ALTER TABLE scan_results
    ADD COLUMN sbom_format TEXT NULL,
    ADD COLUMN sbom_json   BYTEA NULL;

-- +goose Down
ALTER TABLE scan_results DROP COLUMN IF EXISTS sbom_json;
ALTER TABLE scan_results DROP COLUMN IF EXISTS sbom_format;
