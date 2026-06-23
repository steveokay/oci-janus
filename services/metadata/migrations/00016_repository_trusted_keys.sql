-- +goose Up

-- Signed-image admission Phase 2 — per-repo trusted-key allowlist
-- (futures.md Tier 1 #3).
--
-- Phase 1 (migration 00015) shipped the `require_signature` repo flag
-- on a "ANY signature passes" contract. Phase 2 narrows the gate to
-- only signatures produced by an operator-approved key_id. When this
-- table is empty for a repo, services/core falls back to Phase 1
-- behaviour so flipping `require_signature` on first doesn't require
-- the operator to also pre-seed the allowlist — they can incrementally
-- pin keys after the rollout. Removing every entry widens the gate
-- back to "ANY signature passes" by design; intentional lockdown is a
-- separate posture tracked as a Phase 3 follow-up.
--
-- `key_id` matches the `signatures.key_id` column written by
-- services/signer when SignManifest succeeds. For the Vault-Transit
-- backend that's the truncated SHA256 of the public key; for Cosign
-- keyless it would be the Fulcio cert fingerprint. Either way it's a
-- stable, copy-pasteable string an operator can identify.
--
-- `display_name` is operator-supplied context ("ci-prod-2026", "Alice's
-- laptop") so the FE doesn't render a wall of opaque hex strings.
-- NULL is permitted because key picker UX seeds the list directly from
-- recent signature key_ids where no name is known yet.
--
-- `added_by` is the user that approved the key. Nullable + ON DELETE
-- SET NULL so deleting a user doesn't orphan their approvals or block
-- the audit chain — the audit_events row still records who acted.

CREATE TABLE repository_trusted_keys (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id         UUID        NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    tenant_id       UUID        NOT NULL,
    key_id          TEXT        NOT NULL,
    display_name    TEXT,
    added_by        UUID,
    added_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (repo_id, key_id)
);

-- Lookup pattern is always "for this repo, what keys are trusted?" —
-- services/core's admission gate runs this on every pull when the
-- require_signature flag is on. The (repo_id, key_id) UNIQUE
-- constraint already serves that read; the additional index on
-- (tenant_id, repo_id) keeps the access pattern indexed when the
-- repo_id isn't the leading predicate (e.g. admin tooling listing
-- every trusted key across an org).
CREATE INDEX repository_trusted_keys_tenant_repo_idx
    ON repository_trusted_keys (tenant_id, repo_id);

-- Tenant isolation is enforced at the app layer in services/metadata
-- like every other table in this schema — RLS is documented in
-- CLAUDE.md §9 but not currently applied here, so adding it for one
-- table would be inconsistent and confusing. The repository methods
-- always include `tenant_id = $N` in the WHERE clause.

-- +goose Down

DROP TABLE IF EXISTS repository_trusted_keys;
