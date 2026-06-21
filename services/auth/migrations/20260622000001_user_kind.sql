-- +goose Up
-- +goose StatementBegin
ALTER TABLE users
    ADD COLUMN kind TEXT NOT NULL DEFAULT 'human'
        CHECK (kind IN ('human', 'service_account'));

CREATE INDEX idx_users_tenant_kind ON users (tenant_id, kind);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_users_tenant_kind;
ALTER TABLE users DROP COLUMN kind;
-- +goose StatementEnd
