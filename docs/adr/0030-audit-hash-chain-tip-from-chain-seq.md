# ADR-0030: Audit hash-chain tip derived from `audit_events.chain_seq`

**Status:** ACCEPTED.
**Date:** 2026-06-30.
**Phase:** REDESIGN-001 Phase 6.12.

## Context

Initial hash-chain design used `audit_chain_tip(tenant_id PK, row_hash, updated_at)` with UPDATE granted to `registry_audit_app`. Pre-PR security-agent flagged this as HIGH BLOCKER: a compromised audit service could rewrite the tip + INSERT a forged row chained off it without the linked-list verifier noticing.

## Decision

Drop the writable tip table. Derive the tip from `audit_events` via `ORDER BY chain_seq DESC LIMIT 1`. `registry_audit_app` keeps INSERT-only on `audit_events` (FORCE RLS denies UPDATE/DELETE per ADR-0015); the advisory lock alone serialises per-tenant inserters.

## Consequences

Tamper-evidence is now structural — no role has the privilege required to rewrite the chain. Verifier code stays the same; tip lookup is one extra ORDER BY query.

## Verified by

`services/audit/internal/repository/hashchain.go` — `chain_seq` ordering for tip derivation, plus `services/audit/migrations/20260630120000_audit_hash_chain.sql`.
