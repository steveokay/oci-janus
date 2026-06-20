-- +goose Up

-- FE-API-018 — per-tenant scan policy. One row per tenant; absence implies
-- the dashboard's default policy (auto-scan on push, no severity block).
CREATE TABLE scan_policies (
    tenant_id            UUID PRIMARY KEY,
    auto_scan_on_push    BOOLEAN NOT NULL DEFAULT TRUE,
    -- block_on_severity is empty (no blocking) or one of the four standard
    -- scanner severities. CHECK constraint mirrors the BFF-level allowlist
    -- so an out-of-band SQL update can't bypass validation.
    block_on_severity    TEXT NOT NULL DEFAULT ''
        CHECK (block_on_severity IN ('','CRITICAL','HIGH','MEDIUM','LOW')),
    -- exempt_cves stores well-formed CVE IDs; BFF validates the entry shape
    -- (^CVE-\d{4}-\d{4,7}$) before forwarding so this column never holds
    -- attacker-controlled free-form text.
    exempt_cves          TEXT[] NOT NULL DEFAULT '{}',
    scanner_plugin       TEXT NOT NULL DEFAULT 'trivy',
    scanner_version_pin  TEXT NOT NULL DEFAULT '',
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- updated_by is the user_id of the last actor. Nullable because the
    -- dev seed may write a default row outside any user session.
    updated_by           UUID
);

-- FE-API-019 — async report generation jobs. Schema is append-only from the
-- caller's perspective; the background poller transitions rows through
-- pending → running → succeeded/failed and never deletes.
CREATE TYPE report_status AS ENUM ('pending','running','succeeded','failed');

CREATE TABLE compliance_reports (
    report_id     UUID PRIMARY KEY,
    tenant_id     UUID NOT NULL,
    requested_by  UUID NOT NULL,
    requested_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at    TIMESTAMPTZ,
    completed_at  TIMESTAMPTZ,
    status        report_status NOT NULL DEFAULT 'pending',
    error_message TEXT,
    -- pdf_path is the on-disk location of the rendered PDF (or text
    -- placeholder). v1 stores a local filesystem path; a production
    -- deployment should migrate this to an object-storage key and front it
    -- with a signed URL.
    pdf_path      TEXT,
    sbom_path     TEXT
);

-- Index supports the tenant + status filter used by ListComplianceReports
-- and the FOR UPDATE SKIP LOCKED poller scan (which filters by status alone).
CREATE INDEX idx_compliance_reports_tenant_status
    ON compliance_reports(tenant_id, status, requested_at DESC);

-- +goose Down

DROP TABLE IF EXISTS compliance_reports;
DROP TYPE  IF EXISTS report_status;
DROP TABLE IF EXISTS scan_policies;
