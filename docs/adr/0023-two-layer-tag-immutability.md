# ADR-0023: Two-layer tag immutability — `repositories.immutable_tags` + `tags.immutable`

**Status:** ACCEPTED.
**Date:** 2026-06-23.
**Phase:** Initial.

## Context

Some repos want every tag pinned (release repos); others want most tags mutable but a small set pinned (mixed dev + release repos). A single layer would not serve both.

## Decision

Repo-wide `repositories.immutable_tags` flag is the table-stakes posture; per-tag `tags.immutable` is the lighter alternative. Per-tag pin wins precedence — the repo flag is the second RPC only when the same-digest fast-path didn't fire. Same-digest re-pushes are idempotent (not a "move") and pass; metadata-reachability failures fail OPEN (warn + continue).

## Consequences

Operators can mix mutable + immutable tags in one repo; a transient DB blip doesn't reject every push. `services/core.checkTagImmutable` carries the precedence ladder.

## Verified by

`services/core/internal/service/registry.go` — `checkTagImmutable` (per-tag precedence + same-digest fast path).
