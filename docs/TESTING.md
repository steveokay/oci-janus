# Testing Requirements

## Unit Tests

- Location: `_test.go` files alongside source.
- Coverage target: **80% minimum per service** (enforced in CI).
- Test naming: `Test<FunctionName>_<scenario>_<expectedOutcome>`.
- Use table-driven tests for validation logic.
- No real network calls in unit tests — use interfaces and mocks.
- Mocks generated with `mockery` (config in `.mockery.yaml` per repo).

## Integration Tests

- Location: `internal/testutil/integration/`.
- Use `testcontainers-go` (helpers in `libs/testutil/containers`).
- Spin up real PostgreSQL, Redis, RabbitMQ, MinIO per test suite.
- Integration tests tagged with `//go:build integration` — excluded from default `go test ./...`.
- Run with `make test-integration`.

## OCI Spec Conformance

- `registry-core` must pass the OCI Distribution Spec conformance test suite.
- Run with `make test-conformance` in `services/core`.
- Conformance tests run in CI on every PR to `main`.
- Current status: **75/75 PASS, 5 skipped** (skips are optional spec features not advertised).
- Reference: https://github.com/opencontainers/distribution-spec/tree/main/conformance

## Security Tests

- SAST: `gosec` run in CI on every PR.
- Dependency audit: `govulncheck` in CI for every service workflow.
- Secret scanning: `gitleaks` workflow on every push and PR.
- Integration: OWASP ZAP baseline scan against staging environment (weekly).

## Per-service test coverage (as of Sprint 5)

| Service | Coverage | Notes |
|---|---|---|
| libs | 80%+ | Foundation packages all covered |
| auth | 80%+ | grpc + http handlers + service layer |
| core | 80%+ | http handlers + service registry/auth client |
| audit | 80%+ | 11 gRPC handler tests via bufconn |
| management | 80%+ | 31 handler tests covering all REST routes |
| metadata, storage, scanner, proxy, webhook, gc, tenant, signer, gateway | Not assessed | Sprint 6 backlog |
