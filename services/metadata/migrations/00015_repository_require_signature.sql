-- +goose Up

-- Signed-image admission policy (futures.md Tier 1 #3).
--
-- When `require_signature = TRUE` on a repository, services/core
-- consults services/signer on every manifest GET. If the manifest
-- has no signatures recorded for the tenant, the pull is rejected
-- with 403 DENIED. The rejection has a clear error body so the
-- operator can act ("sign the image first or turn the policy off").
--
-- Default FALSE keeps the migration transparent for existing
-- tenants — opting in is a deliberate operator decision (same as
-- `immutable_tags` from migration 00014). The flag lives on the
-- repository row rather than a per-tenant setting because different
-- repos serve different threat models — a `tooling/` repo may stay
-- unsigned, while `prod/` requires signatures.
--
-- Phase 1 scope: ANY signature recorded against the manifest digest
-- passes the check. A per-repo trusted-key allowlist is a Phase 2
-- enhancement (futures.md Tier 1 #3 — "Per-repo 'trusted signer key'
-- list") tracked separately.

ALTER TABLE repositories
    ADD COLUMN require_signature BOOLEAN NOT NULL DEFAULT FALSE;

-- +goose Down

ALTER TABLE repositories DROP COLUMN IF EXISTS require_signature;
