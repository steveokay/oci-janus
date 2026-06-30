# ADR-0018: `services/gc` gets its own DB for GC run status

**Status:** ACCEPTED.
**Date:** 2026-06-21.
**Phase:** Initial.

## Context

`services/gc` ran sweeps on a cron and had no persistent status surface for the frontend; the new `RunNow` button needed both async semantics and visibility into in-flight runs.

## Decision

Add a `gc_runs` table in a dedicated `services/gc` database. `RunNow` INSERTs a queued row + non-blocking channel send; the cron loop drains queued rows via `ClaimNextQueued` (`FOR UPDATE SKIP LOCKED`). When `DB_DSN` is unset, the legacy in-process runner still works.

## Consequences

Status visibility + manual triggers without breaking the existing cron flow. The optional DSN preserves the simple deployment story for users who don't need the dashboard.

## Verified by

`services/gc/migrations/20260621000001_gc_runs.sql` and `services/gc/internal/runner/runner.go` (`ClaimNextQueued` consumer).
