-- +goose Up

CREATE TABLE audit_events (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    actor_id    TEXT        NOT NULL,
    actor_type  TEXT        NOT NULL
                            CHECK (actor_type IN ('user', 'robot', 'system')),
    actor_ip    TEXT        NOT NULL DEFAULT '',
    action      TEXT        NOT NULL,
    resource    TEXT        NOT NULL DEFAULT '',
    outcome     TEXT        NOT NULL
                            CHECK (outcome IN ('success', 'failure')),
    metadata    JSONB       NOT NULL DEFAULT '{}',
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
) PARTITION BY RANGE (occurred_at);

-- Default partition covering the first year; add new partitions via migration.
CREATE TABLE audit_events_default PARTITION OF audit_events DEFAULT;

-- Per CLAUDE.md §4.10: append-only — prevent any UPDATE or DELETE.
CREATE RULE no_update_audit AS ON UPDATE TO audit_events DO INSTEAD NOTHING;
CREATE RULE no_delete_audit AS ON DELETE TO audit_events DO INSTEAD NOTHING;

CREATE INDEX idx_audit_events_tenant_occurred ON audit_events(tenant_id, occurred_at DESC);
CREATE INDEX idx_audit_events_actor           ON audit_events(actor_id, occurred_at DESC);
CREATE INDEX idx_audit_events_action          ON audit_events(action, occurred_at DESC);

-- +goose Down

DROP TABLE IF EXISTS audit_events CASCADE;
