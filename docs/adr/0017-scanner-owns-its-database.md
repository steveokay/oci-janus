# ADR-0017: `services/scanner` gets its own DB for scan policies + compliance reports

**Status:** ACCEPTED.
**Date:** 2026-06-20.
**Phase:** Initial.

## Context

`services/scanner` was previously DB-less and asked `services/metadata` to persist scan policies. That placed enforcement logic far from the data it gates on, and compliance-report generation needed cross-replica job claim semantics.

## Decision

Give `services/scanner` its own Postgres database with `scan_policies` and `compliance_reports` tables. Async compliance reports claim work via `FOR UPDATE SKIP LOCKED`.

## Consequences

Policy edits commit alongside the enforcement code; report generation scales across replicas without a queue broker. Splits the metadata schema into two databases — cross-DB joins are no longer possible.

## Verified by

`services/scanner/migrations/20260620000001_scan_policies_and_reports.sql` and `services/scanner/internal/repository/repository.go`.
