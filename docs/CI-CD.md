# CI/CD Pipeline

The monorepo has a single `.github/workflows/` directory. Jobs are **path-filtered** — a change under `services/core/` only triggers the `core` pipeline; a change under `libs/` triggers all service pipelines. Each service pipeline runs the same stages.

### Workflow inventory

One workflow file per service (`ci-<name>.yml`) plus shared jobs:

| Workflow | Triggers |
|---|---|
| `ci-audit.yml`, `ci-auth.yml`, `ci-core.yml`, `ci-gateway.yml`, `ci-gc.yml`, `ci-metadata.yml`, `ci-proxy.yml`, `ci-scanner.yml`, `ci-signer.yml`, `ci-storage.yml`, `ci-tenant.yml`, `ci-webhook.yml` | Path filter on `services/<name>/**`; runs stages 1–7. |
| `ci-libs.yml` | Path filter on `libs/**`; runs lint + test, then fans out to all per-service workflows (`libs/` is the shared layer — see "libs/ change triggers" below). |
| `ci-proto.yml` | Path filter on `proto/**`; runs `make proto-lint` + `make proto-breaking`. |
| `ci-ui.yml` | Path filter on `frontend/**`; runs `npx tsc --noEmit` + `npm run test` + `npm run build` against the Beacon dashboard. |
| `ci-gitleaks.yml` | Repo-wide secret scan on every PR. |

> **GAP — `services/management`.** No `ci-management.yml` exists today and no other workflow path-filters on `services/management/**`. A management-only change (BFF route, response shape) currently merges without dedicated CI coverage; reviewers rely on the broader integration suite + manual smoke. A `ci-management.yml` mirroring the other per-service workflows is on the next-sprint maintenance list.

## Stages

```
Per service (triggered by path filter):

1. lint           → golangci-lint (config in .golangci.yml at repo root)
                  → (services/auth only) scripts/lint-user-queries.sh
                    enforces the FE-API-048 kind-guard rule: any new
                    `FROM users WHERE` query in
                    services/auth/internal/repository/ must use a
                    `…Human…` helper or carry an `-- allow-any-kind`
                    annotation. Wired in .github/workflows/ci-auth.yml.
2. test           → go test -race ./...
3. security       → govulncheck, gosec, gitleaks
4. build          → docker build (multi-stage, distroless base)
5. conformance    → (services/core only) OCI conformance suite
6. integration    → make test-integration
7. publish        → push image to registry (semver tag on release)
8. deploy-staging → helm upgrade to staging namespace
9. deploy-prod    → manual approval gate → helm upgrade to prod

libs/ change triggers:
  → lint + test for libs/
  → then fan-out: stages 1–4 for every service (parallel)
```

## Docker Build Rules

- Multi-stage builds: builder stage (`golang:1.25-bookworm`), final stage (`gcr.io/distroless/static-debian12:nonroot`).
- Final image contains only the compiled binary and a static `healthcheck` binary.
- No shell in final image.
- Run as non-root user (`USER 65532:65532`).
- Image tagged with: `git SHA` (every build) + semver tag (releases).
- `docker scout` or `trivy image` scan in CI — fail build on CRITICAL CVEs.
- All services build with `GOWORK=off` so the workspace is not required inside Docker.

## Release Versioning

- Semantic versioning: `v<major>.<minor>.<patch>` — single version tag for the entire monorepo.
- `proto/` and `libs/` follow the same release cadence as services — no independent versioning.
- Breaking proto changes: major version bump of the monorepo tag; maintain backward-compatible stubs for one release cycle.
- Changelog: conventional commits enforced via `commitlint`; scoped commits preferred (e.g. `feat(core):`, `fix(libs/auth):`).
