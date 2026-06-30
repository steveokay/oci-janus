# ADR-0009: pgx/v5 with raw SQL, no ORM

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

ORMs hide query shape, leak abstractions at scale, and make RLS + advisory-lock + `FOR UPDATE SKIP LOCKED` patterns awkward.

## Decision

Use `pgx/v5` with `pgxpool` directly. All SQL is raw and parameterised; migrations run via `pressly/goose` from `embed.FS`.

## Consequences

Repositories own their SQL; query plans are predictable. Connection-pool exhaustion is mapped to `codes.ResourceExhausted` via `libs/errors/codes.MapDBError` so callers can back off correctly.

## Verified by

`libs/config/loader/loader.go:DBConfig.PoolConfig` — the canonical pgxpool builder used by every repository.
