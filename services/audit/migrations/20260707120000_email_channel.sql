-- +goose Up
-- FUT-019 Phase 3 — email notification channel.
-- email_transport_config: one row per tenant. Secrets are AES-256-GCM
-- sealed under NOTIFY_EMAIL_KEY_HEX (see services/audit config); kek_version
-- tracks the KEK generation for rotate-kek (RED-FU-015).
CREATE TABLE email_transport_config (
    tenant_id          UUID PRIMARY KEY,
    provider           TEXT        NOT NULL DEFAULT 'resend',
    enabled            BOOLEAN     NOT NULL DEFAULT false,
    from_address       TEXT,
    from_name          TEXT,
    resend_api_key_enc BYTEA,
    smtp_host          TEXT,
    smtp_port          INT,
    smtp_username      TEXT,
    smtp_password_enc  BYTEA,
    smtp_tls_mode      TEXT        NOT NULL DEFAULT 'starttls',
    kek_version        SMALLINT    NOT NULL DEFAULT 1,
    last_test_at       TIMESTAMPTZ,
    last_test_ok       BOOLEAN,
    last_test_error    TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by         UUID,
    CONSTRAINT email_transport_provider_chk CHECK (provider IN ('resend','smtp')),
    CONSTRAINT email_transport_tls_chk CHECK (smtp_tls_mode IN ('starttls','implicit','none'))
);

-- email_deliveries: per-send log AND send queue. The dispatcher inserts
-- pending rows (one per opted-in recipient); the send loop drains them.
CREATE TABLE email_deliveries (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL,
    user_id             UUID        NOT NULL,
    to_address          TEXT        NOT NULL,
    category            TEXT        NOT NULL,
    subject             TEXT        NOT NULL,
    body_summary        TEXT        NOT NULL,
    link                TEXT,
    source_scheduled_id UUID        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'pending',
    attempts            INT         NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error          TEXT,
    provider            TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at             TIMESTAMPTZ,
    CONSTRAINT email_delivery_status_chk CHECK (status IN ('pending','sent','failed')),
    CONSTRAINT email_delivery_idem UNIQUE (source_scheduled_id, user_id)
);

CREATE INDEX idx_email_deliveries_claim
    ON email_deliveries (next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX idx_email_deliveries_user
    ON email_deliveries (tenant_id, user_id, created_at DESC);

-- +goose Down
DROP TABLE email_deliveries;
DROP TABLE email_transport_config;
