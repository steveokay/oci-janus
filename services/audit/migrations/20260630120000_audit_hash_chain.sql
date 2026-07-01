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
--   chain_seq : a globally-monotonic IDENTITY column that records the
--               server-side insertion order. The tip lookup orders by
--               chain_seq DESC instead of relying on a separate writable
--               tip table — see SEC-044 below.
--
-- The chain is per-tenant — rows from different tenants do NOT link. This
-- keeps verification cheap (walk one tenant at a time) and ensures one
-- noisy tenant cannot stall another's INSERT path.
--
-- SEC-044 redesign (2026-06-30 security-agent BLOCKER): the initial
-- design used a writable `audit_chain_tip` table. Granting UPDATE on
-- that table to `registry_audit_app` defeated the whole tamper-evidence
-- posture — a compromised audit service could rewrite the tip to an
-- earlier row_hash and INSERT a forged row chained off it, and the
-- linked-list walk would still appear consistent. We now derive the
-- tip from `audit_events` itself:
--
--     SELECT row_hash FROM audit_events
--      WHERE tenant_id = $1
--      ORDER BY chain_seq DESC
--      LIMIT 1;
--
-- registry_audit_app keeps INSERT-only on audit_events (FORCE RLS
-- already denies UPDATE/DELETE per Decision #15). Concurrent inserters
-- are serialised by pg_advisory_xact_lock(tenant_key) as before; no
-- FOR UPDATE is needed because the advisory lock IS the serialisation
-- primitive. An attacker holding the role can still APPEND a row, but
-- cannot rewrite chain_seq or row_hash on existing rows — any
-- attempted fork-and-graft requires UPDATE, which is not granted.
--
-- prev_hash default: a single 0x00 byte, signalling "I am the start of
-- the chain." On INSERT the application MUST overwrite this with the
-- tip row_hash (or leave it on the genesis row). The DEFAULT exists so
-- a misbehaving client that drops the column from the INSERT gets a
-- NOT NULL violation rather than silently producing an unchained row.
--
-- row_hash has no DEFAULT — every INSERT MUST compute it. NOT NULL
-- ensures a malformed inserter fails loudly.

-- REM-022 (2026-07-01): audit_events is a PARTITIONED table
-- (PARTITION BY RANGE (occurred_at), with audit_events_default as the
-- default partition). PostgreSQL forbids
--   ADD COLUMN chain_seq BIGINT GENERATED ALWAYS AS IDENTITY
-- on partitioned tables (SQLSTATE 42P16 — "cannot recursively add
-- identity column to table that has child tables"). The original PR
-- #208 shipped that form assuming a non-partitioned table and every
-- dev/prod DB with the partition crashloops audit at startup.
--
-- Fix: use an explicit sequence + DEFAULT nextval(...). Sequences ARE
-- allowed on partitioned tables (the DEFAULT propagates to every
-- partition). Behaviour is IDENTICAL to GENERATED ALWAYS AS IDENTITY
-- for our tamper-evidence contract:
--   - Single shared sequence across every partition
--   - Monotonic increment, never reused
--   - Application cannot supply a value (the DEFAULT wins because
--     `chain_seq` is not in the INSERT column list; if a compromised
--     client tries to override it, the audit-app role has no UPDATE
--     grant on audit_events so the row would fail the FORCE RLS
--     policy anyway)
-- VerifyChain's tip query (ORDER BY chain_seq DESC LIMIT 1) is
-- unchanged. See status-tracker.md REM-022 for the incident notes.

ALTER TABLE audit_events
    ADD COLUMN prev_hash BYTEA NOT NULL DEFAULT decode('00', 'hex'),
    ADD COLUMN row_hash  BYTEA NOT NULL DEFAULT decode('00', 'hex');

-- Drop the row_hash default after table-level add so future inserts MUST
-- provide an explicit value. The prev_hash default stays so the genesis
-- row's "no previous row" case is expressed cleanly without application
-- branching at the SQL layer.
ALTER TABLE audit_events ALTER COLUMN row_hash DROP DEFAULT;

CREATE SEQUENCE audit_events_chain_seq;
ALTER TABLE audit_events
    ADD COLUMN chain_seq BIGINT NOT NULL DEFAULT nextval('audit_events_chain_seq');
ALTER SEQUENCE audit_events_chain_seq OWNED BY audit_events.chain_seq;

-- Grant USAGE on the sequence to the audit app role. Under the
-- previous GENERATED ALWAYS AS IDENTITY form Postgres implicitly
-- granted this because the sequence was created and owned by the
-- table's identity metadata; with an explicit sequence we must
-- grant explicitly or every INSERT from registry_audit_app fails
-- with SQLSTATE 42501 (permission denied for sequence).
GRANT USAGE ON SEQUENCE audit_events_chain_seq TO registry_audit_app;

-- Per-tenant tip lookup index. The Insert path does
--   SELECT row_hash FROM audit_events
--    WHERE tenant_id = $1
--    ORDER BY chain_seq DESC LIMIT 1
-- on every insert; the DESC index makes that a single btree lookup
-- regardless of how many rows the tenant has. The same index also
-- accelerates VerifyChain's per-tenant scan in chain_seq order.
CREATE INDEX IF NOT EXISTS idx_audit_events_tenant_chain_seq
    ON audit_events (tenant_id, chain_seq DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

-- Down migration is destructive: dropping the columns loses the chain
-- linkage permanently for any rows inserted under this schema. This is
-- acceptable for dev rollback only — production rollback should be
-- forward-only (a fix-forward migration that, for example, rebuilds
-- the chain after a verified-good checkpoint). Documented per §11.
DROP INDEX IF EXISTS idx_audit_events_tenant_chain_seq;
ALTER TABLE audit_events
    DROP COLUMN IF EXISTS chain_seq,
    DROP COLUMN IF EXISTS row_hash,
    DROP COLUMN IF EXISTS prev_hash;
DROP SEQUENCE IF EXISTS audit_events_chain_seq;

-- +goose StatementEnd
