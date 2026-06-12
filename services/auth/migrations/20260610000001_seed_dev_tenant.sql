-- +goose Up
-- +goose StatementBegin

-- Dev tenant — fixed UUID used by DEV_DEFAULT_TENANT_ID in local Compose stack.
INSERT INTO users (id, tenant_id, username, email, password_hash, is_active)
VALUES (
    '00000000-0000-0000-0000-000000000002',
    '98dbe36b-ef28-4903-b25c-bff1b2921c9e',
    'admin',
    'admin@dev.local',
    '$argon2id$v=19$m=65536,t=3,p=2$3kHdg2SOxbVRyPf3LEhy1g$nnZN9kUaP7QjM1kgm6g0RpIQ/orFgJk0Uc5GMnfvCDg',
    true
)
ON CONFLICT (tenant_id, username) DO NOTHING;

-- OCI conformance test user — credentials match CONFORMANCE_USERNAME/PASSWORD in services/core/Makefile.
INSERT INTO users (id, tenant_id, username, email, password_hash, is_active)
VALUES (
    '00000000-0000-0000-0000-000000000003',
    '98dbe36b-ef28-4903-b25c-bff1b2921c9e',
    'conformance',
    'conformance@dev.local',
    '$argon2id$v=19$m=65536,t=3,p=2$nzGi4w5n1X/PxLHwHdo/pQ$UUz56fCariQ+Nfu+ga7xUAqIN/wcVOHchS3fBRQlCdE',
    true
)
ON CONFLICT (tenant_id, username) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM users WHERE id IN (
    '00000000-0000-0000-0000-000000000002',
    '00000000-0000-0000-0000-000000000003'
);
-- +goose StatementEnd
