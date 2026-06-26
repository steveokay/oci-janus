-- +goose Up
-- +goose StatementBegin

-- OCI conformance test user — credentials match CONFORMANCE_USERNAME/PASSWORD
-- in services/core/Makefile. Used exclusively by the CI conformance suite;
-- carries no role assignments so even if the password hash leaks, the worst
-- an attacker can do is authenticate as a zero-permission user. Acceptable
-- residual risk for now; will be replaced by a CI-time bootstrap step in a
-- future REDESIGN-001 follow-up (filed in futures.md as REDESIGN-001 polish).
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

-- The dev admin user (id 00000000-0000-0000-0000-000000000002, email
-- admin@dev.local, password Admin1234!) was previously seeded here. REMOVED
-- in REDESIGN-001 Phase 2.6 / RM-008 / Top-5 #5: shipping a known argon2
-- hash in the Docker image is a known-credentials security risk for any
-- production deploy that forgets to overwrite the admin.
--
-- The replacement workflow:
--   * Local dev: run `make dev-bootstrap` after `docker compose up`
--     (creates the same admin@dev.local / Admin1234! / tenant 98dbe36b-…
--     so existing dev workflows keep working)
--   * Production: run `registry-auth bootstrap` as a one-shot per the
--     infra/runbooks/bootstrap-first-admin.md runbook.
--
-- The companion migrations that granted org-scoped admin and the
-- platform-admin marker (20260618000001, 20260618000002) were deleted in
-- the same change. The bootstrap CLI grants the platform-admin marker
-- directly to the user it creates.

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DELETE FROM users WHERE id IN (
    '00000000-0000-0000-0000-000000000003'
);
-- +goose StatementEnd
