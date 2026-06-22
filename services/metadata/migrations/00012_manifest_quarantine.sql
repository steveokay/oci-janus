-- +goose Up

-- FE-API-050 — pull-time quarantine.
--
-- When a scan exceeds the effective block_on_severity policy for a
-- manifest, services/scanner stamps the row via the metadata
-- UpdateManifestQuarantine RPC; services/core checks the flag on every
-- GetManifest and returns 451 Unavailable For Legal Reasons. Operator
-- override (manual quarantine / lift) goes through services/management.
--
-- All four columns are nullable / default-zero so existing rows
-- transparently land as "not quarantined" without a backfill.
ALTER TABLE manifests
    -- quarantined drives the pull-time gate. Default false so existing
    -- rows are unaffected; the scanner flips it after each scan that
    -- violates the policy.
    ADD COLUMN quarantined        BOOLEAN     NOT NULL DEFAULT FALSE,
    -- quarantine_reason is operator-readable text shown on the
    -- pull-rejection 451 body and on the dashboard's "Quarantined"
    -- banner. Limited to 1024 chars to keep the on-the-wire shape
    -- bounded — the scanner's reason today fits in well under 100.
    ADD COLUMN quarantine_reason  TEXT,
    -- quarantined_at captures the FIRST quarantine event. Re-applying
    -- quarantine doesn't overwrite this — the handler is idempotent on
    -- repeated true→true transitions so the audit trail keeps a
    -- stable "originally quarantined at" timestamp.
    ADD COLUMN quarantined_at     TIMESTAMPTZ,
    -- quarantined_by is "scanner" for automatic policy enforcement, or
    -- the user_id UUID (as text — the scanner doesn't have a users
    -- FK, and treating it as opaque keeps the column simple). NULL
    -- when not quarantined.
    ADD COLUMN quarantined_by     TEXT;

-- Partial index supports the future "list all quarantined manifests
-- for this tenant" query (admin surface). Tiny because the predicate
-- excludes the common case.
CREATE INDEX idx_manifests_quarantined
    ON manifests(tenant_id, repo_id)
    WHERE quarantined = TRUE;

-- +goose Down

DROP INDEX IF EXISTS idx_manifests_quarantined;
ALTER TABLE manifests
    DROP COLUMN IF EXISTS quarantined_by,
    DROP COLUMN IF EXISTS quarantined_at,
    DROP COLUMN IF EXISTS quarantine_reason,
    DROP COLUMN IF EXISTS quarantined;
