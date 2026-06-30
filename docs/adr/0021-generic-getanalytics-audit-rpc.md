# ADR-0021: Generic `GetAnalytics` RPC over `services/audit` with BFF-supplied bucket origin

**Status:** ACCEPTED.
**Date:** 2026-06-21.
**Phase:** Initial.

## Context

The dashboard needed time-bucketed counts of `push.image` and similar events. `services/audit` already had the events; adding a parallel analytics service would have duplicated the storage.

## Decision

Single generic `GetAnalytics` RPC on `services/audit`. Uses PG14 `date_bin` so buckets align across replicas. The BFF owns the range→bucket mapping (24h→1h×24, 7d→6h×28, 30d→1d×30) and pre-allocates empty buckets so quiet periods report `count=0`.

## Consequences

One source of truth for activity analytics. Adding new bucket sizes is a BFF-only change.

## Verified by

`services/audit/internal/handler/analytics.go:GetAnalytics` and the `date_bin` SQL in `services/audit/internal/repository/repository.go`.
