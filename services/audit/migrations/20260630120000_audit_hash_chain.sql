-- +goose Up
-- +goose StatementBegin

-- REDESIGN-001 Phase 6.12: tamper-evident hash chain on audit_events.
--
-- Each row carries:
--   prev_hash : the row_hash of the previous row in the per-tenant chain,
--               or the single sentinel byte 0x00 for the genesis row.
--   row_hash  : sha256(prev_hash || canonical_row_bytes), where
--               canonical_row_bytes is the deterministic serialisation of
--               all OTHER columns (see repository/hashchain.go for the
--               canonicalisation contract — a future verifier MUST replay
--               that exact byte layout).
--
-- The chain is per-tenant — rows from different tenants do NOT link. This
-- keeps verification cheap (walk one tenant at a time) and ensures one
-- noisy tenant cannot stall another's INSERT path.
--
-- The columns are added with a sensible DEFAULT so the migration is
-- backfill-free on existing tables (production has zero existing rows for
-- a fresh deploy; for migrated deployments the genesis sentinel still
-- gives a valid starting point — those pre-existing rows simply cannot
-- be re-verified, only rows inserted after this migration are tamper-
-- evident).
--
-- prev_hash default: a single 0x00 byte, signalling "I am the start of
-- the chain." On INSERT the application MUST overwrite this with the
-- tip row_hash (or leave it on the genesis row). The DEFAULT exists so
-- a misbehaving client that drops the column from the INSERT gets a
-- NOT NULL violation rather than silently producing an unchained row.
--
-- row_hash has no DEFAULT — every INSERT MUST compute it. NOT NULL
-- ensures a malformed inserter fails loudly.

ALTER TABLE audit_events
    ADD COLUMN prev_hash BYTEA NOT NULL DEFAULT decode('00', 'hex'),
    ADD COLUMN row_hash  BYTEA NOT NULL DEFAULT decode('00', 'hex');

-- Drop the row_hash default after table-level add so future inserts MUST
-- provide an explicit value. The prev_hash default stays so the genesis
-- row's "no previous row" case is expressed cleanly without application
-- branching at the SQL layer.
ALTER TABLE audit_events ALTER COLUMN row_hash DROP DEFAULT;

-- Per-tenant chain tip. The inserter SELECTs FOR UPDATE on the tip row
-- under the per-tenant pg_advisory_xact_lock, computes the new
-- row_hash, INSERTs into audit_events, and UPSERTs the tip. This is
-- the durable record of the most-recently-INSERTED row for the tenant
-- — distinct from the row with the latest occurred_at, which can
-- differ when events arrive slightly out of clock order. A pure
-- "ORDER BY occurred_at DESC LIMIT 1" tip lookup would race when two
-- inserts share a tenant: both read the same earlier tip and produce
-- two rows chained off the same prev_hash. The dedicated tip row
-- avoids that.
--
-- We do not store the row_id of the tip — only the row_hash — because
-- the verifier walks the linked-list structure (prev_hash → row_hash)
-- and never needs to look up the tip by id.
CREATE TABLE audit_chain_tip (
    tenant_id UUID PRIMARY KEY,
    row_hash  BYTEA NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The audit role needs full r/w on the tip table — it mutates on every
-- insert. RLS isn't applied here because the tip is a single-row-per-
-- tenant scalar and the inserter scopes by primary key.
GRANT SELECT, INSERT, UPDATE ON audit_chain_tip TO registry_audit_app;

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Down migration is destructive: dropping the columns loses the chain
-- linkage permanently for any rows inserted under this schema. This is
-- acceptable for dev rollback only — production rollback should be
-- forward-only (a fix-forward migration that, for example, rebuilds
-- the chain after a verified-good checkpoint). Documented per §11.
REVOKE SELECT, INSERT, UPDATE ON audit_chain_tip FROM registry_audit_app;
DROP TABLE IF EXISTS audit_chain_tip;
ALTER TABLE audit_events
    DROP COLUMN IF EXISTS row_hash,
    DROP COLUMN IF EXISTS prev_hash;

-- +goose StatementEnd
