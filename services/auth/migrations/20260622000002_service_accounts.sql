-- +goose Up
-- +goose StatementBegin
CREATE TABLE service_accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    shadow_user_id  UUID NOT NULL UNIQUE
                       REFERENCES users(id) ON DELETE CASCADE,
    name            TEXT NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    allowed_scopes  TEXT[] NOT NULL DEFAULT '{}',
    created_by      UUID REFERENCES users(id) ON DELETE SET NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at     TIMESTAMPTZ,
    UNIQUE (tenant_id, name)
);

CREATE INDEX idx_service_accounts_tenant_active
    ON service_accounts (tenant_id)
    WHERE disabled_at IS NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE service_accounts;
-- +goose StatementEnd
