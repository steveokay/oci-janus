-- +goose Up

-- FUT-021 — CVSS-gated admission policy.
--
-- When `max_cvss_score` is NULL (default), no gate is enforced —
-- the pull path behaves as it did pre-FUT-021.
--
-- When set to a non-null integer 0-100, services/core.GetManifest
-- consults the scanner's stored result for the manifest digest and
-- rejects the pull (403 DENIED) when the top CVSS score exceeds the
-- threshold. Standard CVSS v3.1 uses the 0.0-10.0 range; we store
-- 100 * score to avoid floating-point comparisons and to give
-- operators finer-grained control (e.g. block at 89 = CRITICAL only,
-- 69 = HIGH + CRITICAL).
--
-- Load-bearing invariants (verified in services/core admission
-- unit tests):
--   1. NULL max_cvss_score           → no gate, always allow.
--   2. Non-null, no scan report yet  → fail-OPEN (allow, log Info).
--   3. Non-null, scanner unreachable → fail-OPEN (allow, log Warn).
--   4. Non-null, top_cvss > value    → deny (fail-CLOSED).
--   5. Non-null, top_cvss == value   → allow (`>` not `>=`).
--
-- The CHECK constraint enforces the 0-100 range at the storage layer
-- so a compromised gRPC handler can't persist an out-of-band value
-- (defence in depth alongside the handler-side validation).

ALTER TABLE repositories
    ADD COLUMN max_cvss_score INTEGER;

ALTER TABLE repositories
    ADD CONSTRAINT max_cvss_range CHECK (
        max_cvss_score IS NULL
        OR (max_cvss_score >= 0 AND max_cvss_score <= 100)
    );

-- +goose Down

ALTER TABLE repositories DROP CONSTRAINT IF EXISTS max_cvss_range;
ALTER TABLE repositories DROP COLUMN IF EXISTS max_cvss_score;
