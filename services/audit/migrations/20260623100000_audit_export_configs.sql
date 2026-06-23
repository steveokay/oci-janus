-- +goose Up

-- Per-tenant audit-log streaming destinations (futures.md Tier 1 #4).
--
-- One row per tenant. The exporter consumer on services/audit reads
-- this table once per delivery, formats the audit event into the
-- chosen wire format (syslog / CEF / webhook), and ships it to
-- target_url. Failures retry with exponential backoff; exhausted
-- messages land in `dlx.audit-export` for operator triage.
--
-- Sensitive material (hmac_secret + bearer_token) is encrypted at
-- rest with the same AES-256-GCM key the SSO admin handler uses
-- (`SSO_CREDENTIAL_KEY_HEX` / equivalent on the audit service —
-- separate env var so the audit service can be configured in
-- isolation). Storing them as BYTEA after Seal() so a raw DB read
-- doesn't leak the secret.
--
-- A NULL config row (or `enabled = FALSE`) means "no streaming for
-- this tenant" — the exporter consumer skips the event entirely.
-- This is the default so existing tenants stay opt-in.

CREATE TABLE audit_export_configs (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID        NOT NULL UNIQUE,
    enabled         BOOLEAN     NOT NULL DEFAULT TRUE,

    -- 'syslog_rfc5424' | 'cef' | 'webhook'.
    -- CHECK constraint instead of a separate lookup table because
    -- the format set is tiny + tightly coupled to the renderer code.
    format          TEXT        NOT NULL
                                CHECK (format IN ('syslog_rfc5424', 'cef', 'webhook')),

    -- target_url shape depends on format:
    --   syslog: `syslog+tcp://host:6514` or `syslog+tls://host:6514`
    --   cef:    same syslog-style URL (CEF rides on syslog framing)
    --   webhook: `https://host/path` (HTTPS-only, validated at write)
    target_url      TEXT        NOT NULL,

    -- AES-256-GCM-encrypted shared secret for webhook HMAC signing.
    -- NULL for syslog/CEF formats (those don't authenticate the
    -- payload; transport TLS does the auth).
    hmac_secret     BYTEA,

    -- AES-256-GCM-encrypted Authorization bearer token for webhook
    -- targets that prefer bearer over HMAC. Optional; HMAC is the
    -- recommended path. NULL when unset.
    bearer_token    BYTEA,

    -- JSONB allowlist + denylist filter on routing keys. Shape:
    --   {"include": ["push.completed", "scan.completed"], "exclude": ["webhook.*"]}
    -- NULL (or empty arrays) means "send every event." Wildcard
    -- patterns supported: trailing `.*` matches any suffix
    -- (`auth.*` → `auth.provider_created` + `auth.user_sso_provisioned`).
    event_filters   JSONB,

    -- Observability: last successful + last failed delivery
    -- timestamps + the last error string for the operator's UI.
    -- dlx_depth is a cached count of stuck events; the consumer
    -- updates it on each DLX park, the FE polls it from the GET
    -- config endpoint, and a separate "drain" admin action can
    -- replay from the DLX (Phase 2 follow-up).
    last_success_at TIMESTAMPTZ,
    last_attempt_at TIMESTAMPTZ,
    last_error      TEXT,
    dlx_depth       INTEGER     NOT NULL DEFAULT 0,

    created_by      UUID,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The exporter consumer's hot path is "look up this tenant's
-- destination on every audit event." Tenant_id is already the
-- UNIQUE key but the explicit index ensures Postgres uses an
-- index-only scan even when the table grows (each tenant only
-- has one row, but the planner's heuristics may pick a seq scan
-- on small tables otherwise).
CREATE INDEX idx_audit_export_configs_tenant
    ON audit_export_configs(tenant_id)
    WHERE enabled = TRUE;

-- +goose Down

DROP TABLE IF EXISTS audit_export_configs;
