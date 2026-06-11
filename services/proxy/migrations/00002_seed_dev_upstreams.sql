-- +goose Up
-- +goose StatementBegin

-- Seed the dockerhub upstream for the dev tenant so pull-through works
-- immediately after a clean stack reset without manual database inserts.
-- auth_type=none relies on Docker Hub's unauthenticated pull tier (rate-limited;
-- supply basic credentials for higher limits in production).
INSERT INTO upstream_registries (tenant_id, name, url, auth_type, ttl_seconds, enabled)
VALUES (
    '98dbe36b-ef28-4903-b25c-bff1b2921c9e',
    'dockerhub',
    'https://registry-1.docker.io',
    'none',
    3600,
    true
) ON CONFLICT (tenant_id, name) DO NOTHING;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM upstream_registries
WHERE tenant_id = '98dbe36b-ef28-4903-b25c-bff1b2921c9e'
  AND name = 'dockerhub';
-- +goose StatementEnd
