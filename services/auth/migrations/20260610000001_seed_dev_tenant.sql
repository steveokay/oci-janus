-- +goose Up
-- +goose StatementBegin

-- Dev tenant — fixed UUID used by DEV_DEFAULT_TENANT_ID in local Compose stack.
INSERT INTO users (id, tenant_id, username, email, password_hash, is_active)
VALUES (
    '00000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000001',
    'admin',
    'admin@dev.local',
    '$argon2id$v=19$m=65536,t=3,p=2$o5d+Kl4Ewd96MKFE6wqQ1w$NdruhP2AYbLv1JAnwj6VHGqsgtywlrR70euNs2fEzoM',
    true
)
ON CONFLICT (tenant_id, username) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM users WHERE id = '00000000-0000-0000-0000-000000000002';
-- +goose StatementEnd
