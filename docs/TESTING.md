# Testing Requirements

## Unit Tests

- Location: `_test.go` files alongside source.
- Coverage target: **80% minimum per service** (enforced in CI).
- Test naming: `Test<FunctionName>_<scenario>_<expectedOutcome>`.
- Use table-driven tests for validation logic.
- No real network calls in unit tests — use interfaces and mocks.
- Mocks generated with `mockery` (config in `.mockery.yaml` per repo).

## Integration Tests

- Location: `internal/testutil/integration/` or alongside the unit tests under `//go:build integration`.
- Use `testcontainers-go` (helpers in `libs/testutil/containers`).
- Spin up real PostgreSQL, Redis, RabbitMQ, MinIO per test suite. Each service that owns its own DB schema (`auth`, `metadata`, `audit`, `webhook`, `tenant`, `proxy`, `scanner`, `gc`, `signer`) gets a fresh PG container with that service's `embed.FS` migrations applied; cross-service tests use `libs/testutil/containers/auth_with_audit.go` or provision their own multi-DB bundles inline.
- Integration tests tagged with `//go:build integration` — excluded from default `go test ./...`.
- Run with `make test-integration`.

### Multi-service testcontainer helpers

`libs/testutil/containers/auth_with_audit.go` (FE-API-048 T18) — `Bundle{AuthPool, AuditPool, AuditConn, Cleanup}` for tests that need both the auth DB and the audit DB at once. The helper boots two Postgres testcontainers and accepts caller-supplied `fs.FS` migration sets via `AuthWithAuditOpts` (so `libs/` does not have to import either service module — the activity facade integration test inlines audit migrations as `fstest.MapFS`).

`services/auth/internal/testutil/sa_fixtures.go` — `NewServiceAccount(t, ctx, saRepo, userRepo, tenant, name, allowedScopes…) → (*ServiceAccount, shadowUserID)` and `NewAPIKeyForSA(t, ctx, keyRepo, sa, name, scopes…) → (keyID, rawSecret)` seed the polymorphic api_keys schema at the repository layer (skipping service-layer audit emission for tests that just need rows in place).

## OCI Spec Conformance

- `registry-core` must pass the OCI Distribution Spec conformance test suite.
- Run with `make test-conformance` in `services/core`.
- Conformance tests run in CI on every PR to `main`.
- Current status: **75/75 PASS, 5 skipped** (skips are optional spec features not advertised).
- Reference: https://github.com/opencontainers/distribution-spec/tree/main/conformance

## Security Tests

- SAST: `gosec` run in CI on every PR.
- Dependency audit: `govulncheck` runs as a nightly consolidated sweep across all Go modules (`.github/workflows/ci-security.yml`), not per-service-per-PR — the per-service jobs were retired under REM-016. Since the Go 1.25.11 bump (REM-016 closed, PR #256) the sweep is a **blocking gate**: a failure means a genuinely new vulndb entry, not deferred noise.
- Secret scanning: `gitleaks` workflow on every push and PR.
- Integration: OWASP ZAP baseline scan against staging environment (weekly).
- Repo-layer kind guard (FE-API-048 §4.1): every `FROM users` read in `services/auth/internal/repository/` goes through a kind-guarded `…Human…` helper (`GetHumanByEmail`, `GetHumanByID`, …) or carries an `-- allow-any-kind` annotation, so shadow service-account rows can't leak onto the human-auth path. The former `scripts/lint-user-queries.sh` CI enforcement was removed under REM-015 (it exited non-zero with no diagnostic signal); the guard now lives in the repository-helper contract itself.

## Per-service test coverage (as of 2026-06-21)

| Service | Coverage | Notes |
|---|---|---|
| libs | 80%+ | Foundation packages all covered |
| auth | 80%+ | grpc + http handlers + service layer; +12 OAuth + 9 SAML tests landed with FE-API-034 (`df39d13` + `4e3d939`) |
| core | 80%+ | http handlers + service registry/auth client |
| audit | 80%+ | gRPC handler tests via bufconn; analytics + notifications + repo_activity covered after FE-API-004/008/030 |
| management | 80%+ | 73+ handler tests covering every BFF REST route landed in this wave (webhooks, security center, admin tenants, GC, signing, SBOM, SSO admin) |
| webhook | 80%+ | Dispatcher + grpc handler tests including PENTEST-027 sanitize-URL and PENTEST-031 generic error |
| metadata | partial | Repository + grpc handler tests landed alongside every FE-API-014..020 + FE-API-031/033 RPC |
| scanner | partial | Repository + report worker tests landed with FE-API-018/019 (`f40365f`) |
| gc | partial | 7 repo + 17 gc handler tests landed with FE-API-032 (`92e6028`) |
| tenant | partial | Worker tests (REM-004) + domain/Update RPC tests with FE-API-027/029 |
| storage, proxy, signer, gateway | Not assessed | Sprint 6 backlog |
