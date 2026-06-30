# ADR-0028: Bootstrap CLI replaces the dev-seed admin migration

**Status:** ACCEPTED.
**Date:** 2026-06-27.
**Phase:** REDESIGN-001 Phase 3.1.b + Phase 2.6.

## Context

The original platform shipped `services/auth/migrations/...seed_dev_admin.sql` writing a hardcoded admin row + password hash into every deployment. Top-5 #5 in the 2026-06-26 system review flagged this as CRITICAL — every prod image carried a known-good admin credential.

## Decision

New `registry-auth bootstrap` subcommand replaces the migration. Operator runs it once per deployment with `--admin-email --admin-username --admin-password-stdin --tenant-name`. Idempotency enforced via `tenant.deployment_metadata.bootstrap_tenant_id`.

## Consequences

No shared credentials in shipped images. Bootstrap is one-shot per deployment; re-running on an already-bootstrapped tenant is a no-op. Removing the bootstrap CLI would leave single-mode deployments with no path to a first admin.

## Verified by

`services/auth/internal/bootstrap/bootstrap.go` (the subcommand implementation) and `services/auth/internal/bootstrap/bootstrap_test.go` (idempotency coverage).
