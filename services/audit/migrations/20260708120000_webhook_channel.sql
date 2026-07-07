-- +goose Up
-- FUT-019 Webhook channel — a single admin-configured org webhook that
-- receives one signed POST per scheduled notification for enabled categories.
--
-- notification_webhook_config: one row per tenant. The HMAC secret is
-- AES-256-GCM sealed under NOTIFY_WEBHOOK_KEY_HEX (see services/audit config);
-- kek_version tracks the KEK generation for rotate-kek (RED-FU-015).
-- enabled_categories is the tenant-level per-category selection driven by the
-- Settings › Notifications matrix Webhook column.
CREATE TABLE notification_webhook_config (
    tenant_id          UUID PRIMARY KEY,
    url                TEXT,
    secret_enc         BYTEA,
    enabled            BOOLEAN     NOT NULL DEFAULT false,
    enabled_categories TEXT[]      NOT NULL DEFAULT '{}',
    kek_version        SMALLINT    NOT NULL DEFAULT 1,
    last_test_at       TIMESTAMPTZ,
    last_test_ok       BOOLEAN,
    last_test_error    TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by         UUID
);

-- notification_webhook_deliveries: per-send log AND send queue. The dispatcher
-- inserts one pending row per scheduled notification (shared endpoint, so one
-- row — NOT one per user); the send loop drains them.
CREATE TABLE notification_webhook_deliveries (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL,
    category            TEXT        NOT NULL,
    subject             TEXT        NOT NULL,
    body_summary        TEXT        NOT NULL,
    link                TEXT,
    source_scheduled_id UUID        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'pending',
    attempts            INT         NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error          TEXT,
    response_status     INT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at        TIMESTAMPTZ,
    CONSTRAINT webhook_delivery_status_chk CHECK (status IN ('pending','delivered','failed')),
    CONSTRAINT webhook_delivery_idem UNIQUE (source_scheduled_id)
);

CREATE INDEX idx_webhook_deliveries_claim
    ON notification_webhook_deliveries (next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX idx_webhook_deliveries_tenant
    ON notification_webhook_deliveries (tenant_id, created_at DESC);

-- Grants — the audit runtime pool authenticates as the low-privilege
-- registry_audit_app role (SEC-001, migration 20240101000002). Without these
-- GRANTs every query fails with "permission denied" surfaced as codes.Internal
-- (the exact FUT-019 email bug fixed in migration 20260707130000 / PR #290).
-- SELECT+INSERT+UPDATE covers every verb the repository issues; no DELETE path.
GRANT INSERT, SELECT, UPDATE ON notification_webhook_config     TO registry_audit_app;
GRANT INSERT, SELECT, UPDATE ON notification_webhook_deliveries TO registry_audit_app;

-- +goose Down
REVOKE INSERT, SELECT, UPDATE ON notification_webhook_deliveries FROM registry_audit_app;
REVOKE INSERT, SELECT, UPDATE ON notification_webhook_config     FROM registry_audit_app;
DROP TABLE notification_webhook_deliveries;
DROP TABLE notification_webhook_config;
