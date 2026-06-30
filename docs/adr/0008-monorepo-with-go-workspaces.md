# ADR-0008: Monorepo with Go workspaces

**Status:** ACCEPTED.
**Date:** 2026-06-09.
**Phase:** Initial.

## Context

Cross-service changes (proto bumps, shared lib edits) in a multi-repo layout require version-bump churn for every consumer. The team wanted atomic refactors without that overhead.

## Decision

Single monorepo `github.com/steveokay/oci-janus` with one `go.mod` per service and a root `go.work` linking them plus `libs/` and `proto/gen/go`. Each service `go.mod` stays self-contained so `GOWORK=off go build` works inside Docker.

## Consequences

Breaking a `libs/` interface forces an in-PR update to every caller — no multi-repo version dance. `go.work` and `go.work.sum` are committed.

## Verified by

`go.work` at repo root listing all 13 services + `libs/` + `proto/gen/go`.
