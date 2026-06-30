# OCI-Janus

Self-hosted OCI registry with mTLS between every service, multi-key JWT signing, tamper-evident audit, and optional multi-tenant mode for SaaS operators.

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25.7-00ADD8?logo=go)](https://go.dev)
[![OCI Distribution Spec](https://img.shields.io/badge/OCI_Spec-v1.1-262261)](https://github.com/opencontainers/distribution-spec)
[![GitHub Sponsors](https://img.shields.io/badge/Sponsor-%E2%9D%A4-ea4aaa?logo=github)](https://github.com/sponsors/steveokay)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

OCI-Janus is a production-grade OCI Distribution Spec v1.1 registry written in Go. It's built for teams who want the feature scope of Docker Hub / Harbor / ECR — image push/pull, vulnerability scanning, signing, RBAC, audit — without the cloud bill or the operational footprint of `distribution/distribution` plus a handful of glued-on services. The differentiators relative to plain `distribution/distribution`: mTLS between every internal service (not just at the edge), multi-key JWT signing with hot rotation, pluggable storage drivers (MinIO/S3/GCS/Azure/filesystem), pluggable scanner plugins (Trivy, Grype, Clair), Cosign + Notary v2 signing, a tamper-evident audit log with per-tenant SHA-256 hash chain, and an optional multi-tenant mode (`DEPLOYMENT_MODE=multi`) for operators who do need SaaS-style isolation.

---

## Quick start

```bash
git clone https://github.com/steveokay/oci-janus.git
cd oci-janus
make dev-certs              # generate self-signed mTLS certs for local services
docker compose -f infra/docker-compose/docker-compose.yml up -d
make dev-bootstrap          # create the first admin (admin / Admin1234!)
```

What just happened:

1. `make dev-certs` writes a local CA + per-service certs to `certs/` (Kubernetes deployments use cert-manager instead).
2. `docker compose up -d` brings up Postgres, Redis, RabbitMQ, MinIO, Vault, Jaeger, and all 13 registry services on the `registry.events` topic.
3. `make dev-bootstrap` runs `registry-auth bootstrap` inside the auth container to create the first tenant + admin user (idempotent — safe to re-run).
4. The dashboard is at `http://localhost:5173` (Vite dev server, no TLS); the OCI `/v2/` API is at `http://localhost:8081`. For production deployment guidance, see [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md).
5. Full bootstrap walkthrough (production paths, password from stdin, tenant id pinning) lives in [`infra/runbooks/bootstrap-first-admin.md`](infra/runbooks/bootstrap-first-admin.md).

`docker login localhost:8081 -u admin -p Admin1234!` and you're pushing.

---

## Architecture

```
                        ┌─────────────────────────────────────────┐
                        │           registry-gateway               │
                        │   (Traefik/Nginx + TLS termination)      │
                        │   Routes by host header (custom domains) │
                        └────────────┬────────────────────────────┘
                                     │ HTTPS
               ┌─────────────────────┼──────────────────────┐
               │                     │                      │
        ┌──────▼──────┐      ┌───────▼──────┐      ┌───────▼──────┐
        │ registry-   │      │ registry-    │      │ registry-    │
        │   auth      │      │   core       │      │   proxy      │
        │ (JWT/API    │      │ (OCI API)    │      │ (pull-thru)  │
        │  key issue) │      │              │      │              │
        └──────┬──────┘      └───────┬──────┘      └──────┬───────┘
               │ gRPC                │ gRPC                │ gRPC
               │              ┌──────▼──────┐             │
               │              │  registry-  │             │
               │              │  metadata   │             │
               │              │ (PostgreSQL)│             │
               │              └──────┬──────┘             │
               │                     │ gRPC               │
               │              ┌──────▼──────┐             │
               └─────────────►│  registry-  │◄────────────┘
                              │   storage   │
                              │(MinIO/S3/   │
                              │  GCS/Azure) │
                              └─────────────┘

        Async (RabbitMQ topic exchange `registry.events`):
        registry-core ──push.completed──► registry-scanner / audit / webhook
        registry-scanner ──scan.completed──► registry-metadata / webhook
        registry-gc ──gc.run──► registry-storage (delete blobs)
        registry-auth ──rbac.role_granted──► registry-audit
```

The 13 services fall into three groups. The **edge** is `registry-gateway` (Traefik) — TLS termination, host-based routing, rate limiting. The **data plane** handles bytes on the OCI API: `registry-core` (the `/v2/` spec implementation), `registry-storage` (blob backends), `registry-proxy` (pull-through cache). The **control plane** owns identity, metadata, and lifecycle: `registry-auth` (JWT + API keys + SSO + RBAC), `registry-metadata` (repos/tags/manifests source of truth), `registry-tenant` (tenant CRUD + deployment metadata), `registry-scanner` (vuln scans + compliance reports), `registry-signer` (Cosign + Notary v2), `registry-webhook`, `registry-audit` (tamper-evident log), `registry-gc` (mark-sweep), and `registry-management` (REST BFF for the dashboard, CLI, and Terraform).

Canonical rules live in [`CLAUDE.md`](CLAUDE.md); per-decision history lives in [`docs/adr/`](docs/adr/).

---

## Deployment modes

The `DEPLOYMENT_MODE` env var picks the posture; the schema and wire format are identical across both.

| Mode | Use case | Bootstrap | FE chrome |
|---|---|---|---|
| `single` (default) | Self-hosted, one-team OSS deploy | One tenant via `registry-auth bootstrap` | Tenant switcher / plan UI hidden |
| `multi` | SaaS-style multi-tenant | First tenant via bootstrap; subsequent via Settings → Platform → Tenants | Full SaaS surface |

In single mode `services/tenant.CreateTenant` returns `FAILED_PRECONDITION` on the second insert, and the `SingleTenantInjector` interceptor stamps every request with the bootstrap tenant id. See [ADR-0025](docs/adr/0025-single-tenant-default-deployment-mode.md) for the rationale; upgrading from v1 → v2: [`docs/MIGRATION-v1-to-v2.md`](docs/MIGRATION-v1-to-v2.md).

---

## Features

**Identity**
- Multi-key JWT (RS256) signing with hot rotation via `JWT_KEY_RING_PATH` + JWKS at `/.well-known/jwks.json`
- API keys (Argon2id hashed) with 60s Redis verify cache for high-RPS CI bots
- Global SSO: OAuth 2.0 + PKCE (Google / GitHub / Microsoft / generic OIDC) and SAML 2.0 SP
- Service accounts as shadow users — scoped per-key, polymorphic owner lookup
- RBAC at org / repo level (owner / admin / writer / reader) + typed `users.is_global_admin`

**Storage**
- Pluggable drivers: MinIO, AWS S3, GCP Cloud Storage, Azure Blob, local filesystem
- Per-tenant key prefixing; no presigned URLs leak to clients
- Pull-through proxy cache for upstream registries with AES-256-GCM credential storage

**Security**
- mTLS between every internal gRPC call, hot-reloading on cert-manager rotation
- Per-server peer-CN allowlist (`MTLS_PEER_CN_ALLOWLIST`) for defence-in-depth
- Cosign (Sigstore) + Notary v2 image signing against Vault-backed keys
- Signed-image admission: repo-wide `require_signature` + per-repo trusted-key allowlist
- Two-layer tag immutability: `repositories.immutable_tags` + per-tag `tags.immutable`
- Tamper-evident audit log: FORCE RLS + per-tenant SHA-256 hash chain, INSERT-only role
- AES-256-GCM secret encryption with versioned ciphertext prefix for future KEK rotation

**Observability**
- OpenTelemetry traces to Jaeger / Grafana Tempo / Datadog (pluggable exporter)
- Prometheus metrics on a dedicated `:9090` port — NetworkPolicy-friendly
- Structured `log/slog` JSON logs with `trace_id` / `span_id` / `tenant_id` on every line
- Audit-log streaming to SIEM (syslog RFC 5424 / CEF / HTTPS webhook)

**Operations**
- Pluggable vulnerability scanner plugins (Trivy default; Grype / Clair adapters) via external-process JSON-RPC
- Per-tenant scan policies + SPDX 2.3 SBOMs + hand-crafted PDF compliance reports
- Mark-sweep garbage collection with `pg_try_advisory_lock` per tenant
- Retention policies (age / version-count / max-idle-days) with dry-run preview
- Webhook delivery with retries, HMAC signing, SSRF block-list, and delivery log
- Pull / push analytics via PG14 `date_bin` time-series

---

## Configuration

OCI-Janus is configured entirely through environment variables; no YAML config files are committed.

- `.env.example` in each `services/<name>/` directory documents every env var that service consumes.
- `infra/docker-compose/` ships a working local dev stack — `cp .env.example .env`, edit secrets, `docker compose up -d`.
- `infra/helm/registry/` ships the Kubernetes Helm chart for production. See [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) for chart layout, cert-manager wiring, and External Secrets Operator integration.

---

## Documentation

| File | What |
|---|---|
| [`CLAUDE.md`](CLAUDE.md) | Canonical platform rules + Decision Log |
| [`docs/adr/`](docs/adr/) | Per-decision ADRs with verified-by code pointers |
| [`docs/SERVICES.md`](docs/SERVICES.md) | Per-service detail (endpoints, gRPC, schemas, env vars) |
| [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) | Fork → configure → deploy in your own infrastructure |
| [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) | Docker Compose + Helm chart layout |
| [`docs/SAML.md`](docs/SAML.md), [`docs/SIGNING.md`](docs/SIGNING.md), [`docs/SCANNER.md`](docs/SCANNER.md) | Feature deep dives (SSO, signing, scanner plugins) |
| [`docs/SIEM-EXPORT.md`](docs/SIEM-EXPORT.md) | Audit-log streaming to syslog / CEF / HTTPS webhook |
| [`docs/EVENTS.md`](docs/EVENTS.md) | RabbitMQ routing keys + payload shapes |
| [`docs/TESTING.md`](docs/TESTING.md), [`docs/CI-CD.md`](docs/CI-CD.md) | Coverage targets, OCI conformance, pipeline stages |
| [`infra/runbooks/`](infra/runbooks/) | Operator procedures (bootstrap admin, secret rotation, MinIO encryption, Notary root key) |

---

## Contributing

Workflow: feature branch → PR → `main` (no direct commits to `main`). One change per PR. Run the local CI gates before pushing:

```bash
make build && make test && make lint
```

If your branch touches `proto/`, also run `make proto` to regenerate the committed stubs. Frontend branches additionally need `npm run lint && npm run typecheck && npm run test && npm run build` (see [`CLAUDE.md` §15](CLAUDE.md#15-workflow-gates) for the workflow-gate rationale).

Full contributor guide: [`CONTRIBUTING.md`](CONTRIBUTING.md). Security disclosures: [GitHub private vulnerability reporting](https://github.com/steveokay/oci-janus/security/advisories/new), not public issues.

---

## License

OCI-Janus is licensed under the [Apache License 2.0](LICENSE). Use it commercially, modify it, redistribute it, host it for paying customers — subject to including the original LICENSE and stating significant changes. There's no CLA; Apache 2.0's inbound contribution clause governs.

### Acknowledgements

Built on top of the OCI Distribution Spec, `pgx/v5`, `crewjam/saml`, `sigstore/cosign`, `notaryproject/notation`, `traefik`, OpenTelemetry, RabbitMQ, MinIO, and the broader Go and CNCF ecosystems.

If your team uses OCI-Janus, please consider [sponsoring on GitHub](https://github.com/sponsors/steveokay) — it directly funds maintenance time and helps prioritise community-requested features.
