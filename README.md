# OCI-Janus — Production-Grade Multi-Tenant Docker Registry

[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/Go-1.25.7-00ADD8?logo=go)](https://go.dev)
[![OCI Distribution Spec](https://img.shields.io/badge/OCI_Spec-v1.1-262261)](https://github.com/opencontainers/distribution-spec)
[![GitHub Sponsors](https://img.shields.io/badge/Sponsor-%E2%9D%A4-ea4aaa?logo=github)](https://github.com/sponsors/steveokay)
[![PRs Welcome](https://img.shields.io/badge/PRs-welcome-brightgreen.svg)](CONTRIBUTING.md)

> **An open-source, self-hostable OCI registry platform.** Apache 2.0 licensed — fork it, run it in your own infrastructure, contribute back. Feature scope sits alongside Docker Hub, Harbor, Nexus, and AWS ECR.

If your team needs a self-hosted registry with multi-tenancy, custom domains, vulnerability scanning, signed-image admission, audit-log streaming, and RBAC — OCI-Janus is built to be the thing you `git clone`, configure, and run. See **[Quick Start (Local Dev)](#quick-start-local-dev)** to be pushing images in 5 minutes, or **[`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md)** to deploy it in your own infrastructure.

## 💖 Support the project

If this saves your team time, please consider [sponsoring on GitHub](https://github.com/sponsors/steveokay). Sponsorship directly funds maintenance time and helps prioritise community-requested features. Reporting bugs, opening PRs, and writing docs all help just as much.

---

## Table of Contents

1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Services](#services)
4. [Quick Start (Local Dev)](#quick-start-local-dev)
5. [Self-Hosting](#self-hosting)
6. [Configuration Reference](#configuration-reference)
7. [Development Guide](#development-guide)
8. [Testing](#testing)
9. [Security](#security)
10. [Deployment](#deployment)
11. [Operations](#operations)
12. [Contributing](#contributing)
13. [License](#license)

---

## Overview

### Core Capabilities

| Feature | Status |
|---|---|
| OCI Distribution Spec v1.1 (push / pull / delete / list) | Implemented |
| Multi-tenant with per-tenant custom domains | Implemented |
| JWT (RS256) + API key authentication | Implemented |
| mTLS between all internal services | Implemented (dev certs via cert-init; private keys are `chmod 600`, owned by uid 65532; prod via cert-manager) |
| Pull-through proxy cache for upstream registries | Implemented |
| Pluggable storage (MinIO / AWS S3 / GCS / Azure Blob) | Implemented |
| Vulnerability scanner plugin interface | Implemented (external process JSON-RPC only; Trivy default) |
| Image signing — Cosign (Sigstore) + Notary v2 | Implemented (ECDSA P-256, Vault key backend) |
| Webhook delivery with retries + HMAC signing | Implemented |
| Immutable audit log | Implemented (append-only PostgreSQL partition) |
| Garbage collection worker | Implemented (mark-sweep, dry-run / manifests / blobs / full modes) |
| Tag immutability — repo-wide flag + per-tag pin | Implemented (rejects re-pushes with `400 MANIFEST_INVALID`) |
| Signed-image admission — repo-wide `require_signature` + per-repo trusted-key allowlist | Implemented (Phase 1 + Phase 2: rejects unsigned pulls with `403 DENIED`; non-empty allowlist narrows to approved `key_id`s only) |
| RBAC at org / repo level | Implemented (4 roles — owner / admin / writer / reader — assignable to users + service accounts; platform-admin marker scope `(admin, org, *)`) |
| Per-tenant SSO — OAuth 2.0 + PKCE + SAML 2.0 SP | Implemented (Google / GitHub / Microsoft / generic OIDC + SAML IdP; auto-provisioning; AES-256-GCM-encrypted client secrets) — see [`docs/SAML.md`](docs/SAML.md) |
| Service accounts + scoped API keys | Implemented (FE-API-048: shadow-user model, per-key scope intersection, polymorphic api_keys table) |
| Per-tenant scan policies + compliance reports | Implemented (FE-API-018/019: block-on-severity rules per repo; SPDX JSON 2.3 SBOMs + hand-rolled PDF reports) — see [`docs/SCANNER.md`](docs/SCANNER.md) |
| Retention policies (age / version-count / max-idle-days) | Implemented (FE-API-037..043: dry-run preview, daily evaluation, audit trail) |
| Pull / push analytics + per-repo activity | Implemented (FE-API-030/042: PG14 `date_bin` time-series, configurable sample rate, repo-scoped activity tab) |
| Audit log streaming to SIEM | Implemented (futures.md Tier 1 #4 Phase 1 + Phase 2: syslog RFC 5424 / CEF / HTTPS webhook; AES-256-GCM-encrypted secrets; SSRF guard; durable `audit.export` + `dlx.audit-export` queues with operator-controlled drain + live DLX depth) — see [`docs/SIEM-EXPORT.md`](docs/SIEM-EXPORT.md) |

### Technology Stack

| Concern | Choice |
|---|---|
| Language | Go 1.23+ (toolchain `go1.25.7` in go.mod) |
| Database | PostgreSQL 16 |
| Cache / Rate-limiting | Redis 7 |
| Message broker | RabbitMQ 3.13 (Quorum Queues) |
| Object storage (default dev) | MinIO |
| Service mesh | mTLS (cert-manager in K8s, openssl in dev via `cert-init`) |
| Observability | OpenTelemetry → Jaeger / Grafana Tempo / Datadog |
| Container base image | `gcr.io/distroless/static-debian12` |
| Secret management | HashiCorp Vault (dev), K8s External Secrets Operator (prod) |

---

## Architecture

> For the full system architecture with detailed sequence diagrams and flow breakdowns, see [ARCHITECTURE.md](ARCHITECTURE.md).
> For how the system works in production (Kubernetes, TLS, secrets, end-to-end push/pull flows), see [prod-flow.md](prod-flow.md).

```
                     ┌────────────────────────────────────────┐
                     │         registry-gateway (Traefik)      │
                     │  TLS termination · Host-based routing   │
                     │  Rate limiting · X-Tenant-ID injection  │
                     └──────────────┬─────────────────────────┘
                                    │ HTTPS
             ┌──────────────────────┼──────────────────────┐
             │                      │                      │
      ┌──────▼──────┐       ┌───────▼──────┐      ┌───────▼──────┐
      │ registry-   │       │ registry-    │      │ registry-    │
      │   auth      │       │   core       │      │   proxy      │
      │ JWT issuance│       │ OCI API      │      │ Pull-through │
      │ API keys    │       │ /v2/ routes  │      │ cache        │
      └──────┬──────┘       └──────┬───────┘      └──────┬───────┘
             │ gRPC (mTLS)         │ gRPC (mTLS)         │ gRPC (mTLS)
             │               ┌─────▼──────┐              │
             │               │ registry-  │              │
             │               │ metadata   │              │
             │               │ PostgreSQL │              │
             │               └─────┬──────┘              │
             │                     │ gRPC                │
             │               ┌─────▼──────┐              │
             └──────────────►│ registry-  │◄─────────────┘
                             │  storage   │
                             │ MinIO/S3/  │
                             │ GCS/Azure  │
                             └────────────┘

  Async (RabbitMQ topic exchange: registry.events):
  registry-core ──push.completed──► registry-scanner
                ──push.completed──► registry-audit
                ──push.completed──► registry-webhook
  registry-scanner ──scan.completed──► registry-metadata
  registry-scanner ──scan.completed──► registry-webhook
```

---

## Services

| Service | Port (HTTP) | Port (gRPC) | Description |
|---|---|---|---|
| `registry-gateway` | 443/80 | — | Traefik ingress, TLS, host routing |
| `registry-auth` | 8080 | 50051 | JWT issuance, API key management, per-tenant SSO (OAuth + SAML), RBAC, service accounts |
| `registry-core` | 8081 | 50052 | OCI Distribution Spec v1.1 implementation; signed-image + tag-immutability admission |
| `registry-storage` | 8082 | 50053 | Storage abstraction (MinIO/S3/GCS/Azure) |
| `registry-metadata` | 8083 | 50054 | Registry metadata (repos, tags, manifests, blobs, retention, trusted keys) |
| `registry-proxy` | 8084 | 50055 | Pull-through proxy cache for upstream registries |
| `registry-scanner` | 8085 | 50056 | Vulnerability scan orchestration + plugin host + scan policies + compliance reports |
| `registry-signer` | 8086 | 50057 | Cosign + Notary v2 image signing — see [`docs/SIGNING.md`](docs/SIGNING.md) |
| `registry-webhook` | 8087 | — | Webhook delivery worker |
| `registry-audit` | 8088 | — | Immutable audit log writer + query / analytics API |
| `registry-gc` | 8089 | — | Garbage collection worker |
| `registry-tenant` | 8090 | 50060 | Tenant lifecycle + custom domain management |
| `registry-management` | 8091 | — | REST BFF for the dashboard (and CLI / Terraform) — translates HTTP → gRPC, no gRPC server of its own |

---

## Quick Start (Local Dev)

### Prerequisites

- Go 1.23+ (toolchain in `go.mod` pins `go1.25.7`)
- Node.js 20+ and `npm` (for the frontend)
- Docker + Docker Compose v2
- `buf` CLI v1.x (for proto codegen)
- `golangci-lint` v1.61+ (for linting)
- (Optional) `helm` v3.14+ and `cosign` v2.x if you want to exercise the OCI helm push/pull and cosign sign workflows against the dev stack

### 1. Clone and set up the workspace

```bash
git clone https://github.com/steveokay/oci-janus.git
cd oci-janus
go work sync
```

### 2. Start infrastructure

```bash
cd infra/docker-compose
cp .env.example .env          # edit POSTGRES_PASSWORD etc.
docker compose up -d postgres redis rabbitmq minio jaeger vault vault-init
```

Wait for all services to be healthy:
```bash
docker compose ps
```

### 3. Configure services

Each service reads from environment variables. Copy the example files:

```bash
# Auth service (requires JWT key generation)
cp services/auth/.env.example services/auth/.env

# Proxy service (requires a 32-byte hex credential encryption key)
cp services/proxy/.env.example services/proxy/.env
# Generate key: openssl rand -hex 32
```

For `registry-auth`, you must generate an RSA key pair:
```bash
openssl genrsa -out /tmp/jwt.pem 4096
openssl rsa -in /tmp/jwt.pem -pubout -out /tmp/jwt.pub
# Base64-encode and set JWT_PRIVATE_KEY_B64 / JWT_PUBLIC_KEY_B64 in services/auth/.env
```

### 4. Run all services via Docker Compose

```bash
cd infra/docker-compose
docker compose up -d
```

Or run individual services locally for development:

```bash
# Terminal 1 — auth
cd services/auth && go run ./cmd/server

# Terminal 2 — core
cd services/core && go run ./cmd/server

# etc.
```

### 5. Verify

```bash
# OCI version check (should return 401 or 200 after token)
curl -i http://localhost:8081/v2/

# Auth token endpoint
curl -s http://localhost:8080/.well-known/jwks.json | jq .

# Docker login
docker login localhost:8081 -u <user> -p <api-key>

# Docker push
docker tag myimage:latest localhost:8081/myorg/myimage:latest
docker push localhost:8081/myorg/myimage:latest

# Helm push / pull / install (charts use the same /v2/ surface as images
# — admission gates and immutability apply uniformly across artifact types)
helm registry login localhost:8081 -u <user> --password-stdin --plain-http
helm push my-chart-0.1.0.tgz oci://localhost:8081/myorg --plain-http
helm pull oci://localhost:8081/myorg/my-chart --version 0.1.0 --plain-http

# Sign an image (any OCI artifact — works for helm charts too)
# via the dashboard API…
curl -X POST http://localhost:8091/api/v1/repositories/myorg/myimage/tags/v1/sign \
     -H "Authorization: Bearer $JWT" \
     -d '{"signer_id":"ci-bot"}'
# …or with cosign CLI against the same public key
cosign verify --key /tmp/registry-signer-pub.pem localhost:8081/myorg/myimage:v1

# Pull through proxy cache (once an upstream is registered — see local-setup.md §6)
# The proxy participates in the standard Docker token-auth flow — no manual token needed.
docker login localhost:8084 -u <user> -p <password>
docker pull localhost:8084/cache/dockerhub/library/alpine:3.20

# Dashboard
open http://localhost:5173        # local FE (npm run dev)
```

---

## Self-Hosting

OCI-Janus is built to be self-hosted from day one. There's no proprietary "cloud" version — running it yourself gives you the same feature set as anyone else.

### Pick your deployment path

| Path | Best for | Time to first push |
|---|---|---|
| **Docker Compose** | Single-host deployments, dev environments, small teams | ~10 min |
| **Kubernetes (Helm chart)** | Production deployments, multi-replica, cloud providers | ~30 min |
| **Fork and customise** | You want to vendor / brand / modify before deploying | varies |

Full walkthrough in **[`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md)** — covers the fork → configure → deploy → operate lifecycle. The short version:

```bash
# 1. Fork on GitHub, then clone YOUR fork
git clone https://github.com/<your-org>/oci-janus.git
cd oci-janus

# 2. Generate production secrets
openssl genrsa -out jwt.pem 4096                              # JWT signing key
openssl rsa -in jwt.pem -pubout -out jwt.pub                  # Public counterpart
openssl rand -hex 32                                          # Proxy credential AES key
openssl rand -hex 32                                          # Audit-export AES key

# 3. Configure
cd infra/docker-compose
cp .env.example .env                                          # Edit secrets, change defaults
# (See docs/SELF-HOSTING.md for the full env-var list per service)

# 4. Bring up the stack
docker compose up -d

# 5. Verify
curl http://localhost:8081/v2/                                # 401 = working (needs auth)
docker login localhost:8081 -u admin -p <your-admin-password>
docker tag alpine:3.20 localhost:8081/<org>/alpine:3.20
docker push localhost:8081/<org>/alpine:3.20
```

Kubernetes path uses the Helm chart at `infra/helm/registry/` — see [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) for chart layout and [`prod-flow.md`](prod-flow.md) for the production wiring (cert-manager, External Secrets Operator, TLS, etc.).

---

## Configuration Reference

All services use environment variables. No YAML config files are committed (only `.env.example` files with placeholder values).

### Common Variables (all services)

| Variable | Description | Default |
|---|---|---|
| `GRPC_ADDR` | gRPC listen address | `:50051` (varies per service) |
| `HTTP_ADDR` | HTTP listen address | `:8080` (varies per service) |
| `LOG_LEVEL` | `debug`/`info`/`warn`/`error` | `info` |
| `LOG_FORMAT` | `json` (prod) or `text` (dev) | `json` |
| `DB_DSN` | PostgreSQL connection string (`sslmode=require` in prod; `sslmode=prefer` in dev compose) | — |
| `DB_MAX_CONNS` | pgxpool max connections | `20` |
| `REDIS_ADDR` | Redis address | — |
| `REDIS_PASSWORD` | Redis password | — |
| `OTEL_EXPORTER` | `jaeger`/`tempo`/`datadog`/`stdout` | `stdout` |
| `OTEL_ENDPOINT` | OTLP endpoint URL | — |
| `OTEL_SERVICE_NAME` | Service name in traces | set per service |
| `OTEL_INSECURE` | Set to `true` for local dev (OTLP without TLS). Never set in staging/production. | `false` |
| `MTLS_CA_CERT_PATH` | mTLS CA certificate path | — |
| `MTLS_CERT_PATH` | mTLS service certificate path | — |
| `MTLS_KEY_PATH` | mTLS service private key path | — |

### `registry-auth`

| Variable | Description |
|---|---|
| `JWT_PRIVATE_KEY_B64` | Base64-encoded PEM RSA private key (RS256 signing) |
| `JWT_PUBLIC_KEY_B64` | Base64-encoded PEM RSA public key |
| `ARGON2_TIME` | Argon2id time cost (default `3`) |
| `ARGON2_MEMORY` | Argon2id memory (KiB, default `65536`) |
| `ARGON2_THREADS` | Argon2id parallelism (default `4`) |
| `RATE_LIMIT_BURST` | Max failed auth attempts per IP per minute |
| `TRUSTED_PROXY_CIDRS` | Comma-separated CIDRs of trusted reverse proxies. When set, `X-Forwarded-For` is used for IP rate limiting instead of TCP peer address. | `` (empty — falls back to RemoteAddr) |

### `registry-storage`

| Variable | Description |
|---|---|
| `STORAGE_DRIVER` | `minio` / `s3` / `gcs` / `azure` / `filesystem` |
| `STORAGE_MINIO_ENDPOINT` | MinIO endpoint (e.g. `minio:9000`) |
| `STORAGE_MINIO_ACCESS_KEY` | MinIO access key |
| `STORAGE_MINIO_SECRET_KEY` | MinIO secret key |
| `STORAGE_MINIO_BUCKET` | Bucket name |
| `STORAGE_S3_BUCKET` | AWS S3 bucket |
| `STORAGE_S3_REGION` | AWS region |

### `registry-proxy`

| Variable | Description |
|---|---|
| `AUTH_REALM` | URL Docker clients use to fetch tokens — must be publicly reachable (default `http://localhost:8080/auth/token`) |
| `AUTH_GRPC_ADDR` | `registry-auth` gRPC address |
| `STORAGE_GRPC_ADDR` | `registry-storage` gRPC address |
| `CREDENTIAL_KEY_HEX` | 64-char hex key (32 bytes) for AES-256-GCM credential encryption |
| `UPSTREAM_HTTP_TIMEOUT_SECS` | Per-request timeout to upstream (default `30`) |
| `UPSTREAM_MAX_RESPONSE_BYTES` | Max response body per upstream layer (default `21474836480` = 20 GiB) |

### `registry-scanner`

| Variable | Description |
|---|---|
| `SCANNER_PLUGIN_PATH` | Absolute path to external scanner process binary (e.g. trivy wrapper) |
| `SCANNER_PLUGIN_CHECKSUM` | SHA256 hex checksum of the plugin binary |
| `SCANNER_WORKER_COUNT` | Concurrent scan workers (default `4`) |
| `SCANNER_JOB_TIMEOUT_SECONDS` | Per-scan timeout (default `600`) |

---

## Development Guide

### Repository Layout

```
/
├── go.work                  # Workspace — links all modules for local dev
├── proto/                   # .proto source files + generated stubs
├── libs/                    # Shared Go libraries (no business logic)
│   ├── auth/                # JWT, API key, mTLS helpers
│   ├── config/loader/       # Viper-based env-var config loader
│   ├── crypto/aes/          # AES-256-GCM helpers
│   ├── crypto/argon2/       # Argon2id password hashing
│   ├── middleware/          # gRPC + HTTP middleware (auth, tracing, logging)
│   ├── observability/       # OpenTelemetry bootstrap
│   ├── rabbitmq/            # Typed publisher + consumer + event definitions
│   └── testutil/            # Testcontainers helpers, fixtures
├── services/                # 13 services — one per row in the catalogue above
│   ├── audit/               # Append-only audit log + analytics
│   ├── auth/                # JWT, API keys, SSO, RBAC, service accounts
│   ├── core/                # OCI Distribution Spec
│   ├── gateway/             # Traefik ingress wrapper + TLS termination
│   ├── gc/                  # Garbage collection worker
│   ├── management/          # REST BFF for the dashboard / CLI / Terraform
│   ├── metadata/            # Registry metadata (PostgreSQL)
│   ├── proxy/               # Pull-through proxy cache
│   ├── scanner/             # Vulnerability scanning + compliance reports
│   ├── signer/              # Cosign + Notary v2 signing
│   ├── storage/             # Storage abstraction
│   ├── tenant/              # Tenant lifecycle + custom domains
│   └── webhook/             # Outbound webhook dispatcher
├── frontend/                # React + TanStack Router dashboard
└── infra/
    ├── docker-compose/      # Full local dev stack
    ├── helm/                # Kubernetes Helm charts
    └── runbooks/            # Operational runbooks
```

### Adding a New Service

1. Create `services/<name>/` with the standard layout (`cmd/server/main.go`, `internal/`, `migrations/`, `Dockerfile`, `go.mod`)
2. Add the module to `go.work` under `use`
3. Add a `replace` directive for `libs` and `proto/gen/go` in the service's `go.mod`
4. Add the service name to `SERVICES` in the root `Makefile`
5. Define any proto messages in `proto/` and run `make proto`
6. Register the service in `infra/docker-compose/docker-compose.yml`

### Protobuf Codegen

Proto files live in `proto/`. Generated stubs are committed to `proto/gen/go/` and must never be edited by hand.

```bash
# Regenerate all stubs
make proto

# Lint proto files
make proto-lint

# Check for breaking changes against main
make proto-breaking
```

### Database Migrations

Each service that owns a database schema has SQL migration files under `migrations/` using `pressly/goose` format. Migrations are embedded in the binary and run automatically at startup.

```sql
-- +goose Up
CREATE TABLE ...;

-- +goose Down
DROP TABLE ...;
```

Rules:
- Never drop a column — add and migrate in a separate step
- Every migration requires a reversible `-- +goose Down` block
- File naming: `YYYYMMDDHHMMSS_<description>.sql` (or sequential `00001_`, `00002_`)

### mTLS Certificates (Local Dev)

```bash
make dev-certs    # generates certs/ with CA + per-service certs using openssl (via cert-init container)
```

In production (Kubernetes), cert-manager issues certificates automatically using an internal CA issuer.

---

## Testing

### Unit Tests

```bash
# All services
make test

# Single service
make test-auth

# With race detector (default in Makefile)
cd services/auth && go test -race ./...
```

Coverage target: **80% minimum per service** (enforced in CI).

Test naming convention: `Test<FunctionName>_<scenario>_<expectedOutcome>`

### Integration Tests

Integration tests use [Testcontainers for Go](https://golang.testcontainers.org/) to spin up real PostgreSQL, Redis, RabbitMQ, and MinIO instances per test suite. They are excluded from the default `go test ./...` run.

```bash
# All services
make test-integration

# Single service
cd services/auth && make test-integration
```

Build tag: `//go:build integration`

### OCI Conformance Tests

`registry-core` must pass the [OCI Distribution Spec conformance suite](https://github.com/opencontainers/distribution-spec/tree/main/conformance).

```bash
cd services/core && make test-conformance
```

> **Note:** OCI conformance suite passes 75/75 tests (0 failures, 5 optional-feature skips). Runs in CI on every PR to `main`.

---

## Security

### Image signing & key management

See **[`docs/SIGNING.md`](docs/SIGNING.md)** for the canonical reference.

In short: image signing is handled by `registry-signer` against a private
key held in HashiCorp Vault (dev mode locally, Vault prod cluster in
production, AWS / GCP / Azure KMS deferred). The key never leaves Vault;
`services/signer` only asks Vault to sign / verify on its behalf. The
dashboard exposes Sign + Verify-now buttons under the tag-detail Signing
tab, and `cosign verify --key <key> <image>` from your laptop works as
an independent check against the same public key.

### Authentication Flow

```
Docker client → GET /v2/ → 401 WWW-Authenticate: Bearer realm="..."
             → POST /auth/token (Basic auth or API key)
             → 200 { "token": "<JWT RS256>" }
             → GET /v2/<name>/manifests/<ref>  Authorization: Bearer <JWT>
             → registry-core validates JWT via registry-auth gRPC (cached in Redis by JTI)
```

Key properties:
- **JWT TTL**: 300 seconds (5 minutes). Non-configurable. Docker clients re-request automatically.
- **API keys**: Stored as Argon2id hashes. Returned in plaintext only once at creation.
- **Token revocation**: JTI stored in Redis. Checked on every validation.
- **Key rotation**: Multiple public keys in JWKS, tagged by `kid`. Rotation is zero-downtime.

### Tenant Isolation

- All DB rows have `tenant_id UUID NOT NULL`
- All queries filter by `tenant_id`
- PostgreSQL Row Level Security (RLS) as a second layer
- Storage keys prefixed with `tenant_id`
- RabbitMQ messages include `tenant_id` in payload and headers

### Upstream Credential Security (Proxy)

Upstream registry passwords are encrypted at rest using AES-256-GCM. The key is loaded from `CREDENTIAL_KEY_HEX` at startup — never stored in the database. Credentials are never logged or included in error responses.

SSRF protection: the upstream HTTP client validates all upstream URLs against private/loopback CIDR ranges at registration time and on every connection attempt. The following ranges are blocked: `10.0.0.0/8`, `172.16.0.0/12`, `192.168.0.0/16`, `127.0.0.0/8`, `169.254.0.0/16`, `100.64.0.0/10`, `::1/128`, `fc00::/7`, `fe80::/10`.

### Known Security Issues

See [`security.md`](security.md) for the full issue tracker. **All SEC-001..SEC-036 are RESOLVED** as of 2026-06-19 (resolution notes inline in each row). The three rounds of post-merge pentests also closed every CRITICAL + HIGH + MEDIUM finding; the remaining open items are LOW-severity follow-ups:

| ID | Severity | Description |
|---|---|---|
| PENTEST-030 | LOW | No per-endpoint test-dispatch throttle on the webhook `Test` action — already gated by RBAC; tracked for a global rate-limit pass |
| PENTEST-033 | LOW (partial) | Postman environment file: login password is now a secret-typed `{{password}}` var, but `NewUser1234!` is still inlined in the `createUser` body and the dev tenant UUID still has a defaulted value — cosmetic cleanup |

---

## Deployment

### Docker Compose (Development)

```bash
cd infra/docker-compose
cp .env.example .env
docker compose up -d
```

UI endpoints:
| Service | URL |
|---|---|
| Traefik dashboard | http://localhost:8888 |
| Jaeger traces | http://localhost:16686 |
| RabbitMQ management | http://localhost:15672 (registry/registry) |
| MinIO console | http://localhost:9001 (minioadmin/minioadmin) |
| Vault UI | http://localhost:8200 (token: dev-root-token) |

### Kubernetes (Production)

Helm charts live in `infra/helm/registry/`. Each service has its own sub-chart.

```bash
# Install umbrella chart
helm upgrade --install registry ./infra/helm/registry \
  -f infra/helm/registry/values.prod.yaml \
  --namespace registry \
  --create-namespace

# Check rollout
kubectl -n registry get pods
kubectl -n registry rollout status deployment/registry-auth
```

Each service chart includes:
- `Deployment` with readiness + liveness probes (gRPC health check)
- `PodDisruptionBudget` (minAvailable: 1)
- `HorizontalPodAutoscaler` (CPU + queue depth)
- `NetworkPolicy` (default deny, explicit allowlists)
- `SecretProviderClass` (External Secrets Operator → Vault or AWS Secrets Manager)

### CI/CD Pipeline

GitHub Actions runs path-filtered jobs per service. A change to `services/core/` only triggers the core pipeline; a change to `libs/` triggers all service pipelines in parallel.

Each pipeline stage:
1. `lint` — `golangci-lint`
2. `test` — `go test -race ./...`
3. `security` — `govulncheck`, `gosec`, `gitleaks`
4. `build` — multi-stage Docker build (distroless final image)
5. `conformance` — OCI Distribution Spec suite (core only)
6. `integration` — testcontainers integration tests
7. `publish` — push image (semver tag on release)
8. `deploy-staging` — `helm upgrade` to staging
9. `deploy-prod` — manual approval gate → `helm upgrade` to prod

---

## Operations

### Health Checks

Every service exposes:
- `grpc.health.v1.Health/Check` — for Kubernetes readiness/liveness probes
- `GET /healthz` (internal HTTP port) — returns 200 OK when ready
- `GET /metrics` (internal HTTP port) — Prometheus metrics

### Observability

All services emit structured JSON logs with `trace_id`, `span_id`, `tenant_id`, and `service` fields on every log line (never tokens or secrets).

Traces flow to the configured OTLP exporter (`OTEL_EXPORTER`). Set `OTEL_EXPORTER=stdout` for local development.

Standard Prometheus metrics (defined in `libs/observability/metrics`):
- `registry_http_request_duration_seconds` — HTTP request latency histogram
- `registry_grpc_request_duration_seconds` — gRPC request latency histogram
- `registry_storage_operation_duration_seconds` — storage operation latency
- `registry_rabbitmq_messages_consumed_total` — async event throughput
- `registry_active_uploads_total` — in-progress blob upload gauge

Each service exposes `/metrics` on a dedicated port `:9090` (separate from the business port) so Kubernetes NetworkPolicy can grant Prometheus access without exposing the OCI API surface (SEC-025).

### Runbooks

| Runbook | Path |
|---|---|
| Secret rotation | `infra/runbooks/secret-rotation.md` |
| MinIO encryption setup | `infra/runbooks/minio-encryption.md` |
| Notary v2 root key ceremony | `infra/runbooks/notary-root-key-ceremony.md` |

### Garbage Collection

GC runs nightly by default (configurable cron in `registry-gc`). Always run in dry-run mode first:

```bash
# Dry run — no deletions
GC_MODE=dry-run registry-gc

# Full GC
GC_MODE=full registry-gc
```

Safety guarantees: blobs younger than `GC_BLOB_MIN_AGE` (default 1 hour) are never deleted, protecting in-flight uploads.

---

## Frontend

The React/TypeScript UI lives in `frontend/`. It uses Vite, TanStack Router (file-based), Tailwind CSS v4, react-hook-form + zod, and Sonner for notifications.

```bash
cd frontend
npm install
npm run dev        # http://localhost:5173
npm run build
npm run typecheck
```

### Implemented surface (Beacon rebuild)

The current UI is the Beacon rebuild (PR #14 merged 2026-06-19), tracked in [`FE-STATUS.md`](FE-STATUS.md). The pre-Beacon UI is archived to `frontend-archive-v1`.

| Surface | Route | Status |
|---|---|---|
| Login (+ SSO buttons) | `/login` | ✅ Done |
| Dashboard | `/` | ✅ Done |
| Repositories list / detail (with Settings tab: immutability + signed-image admission + trusted-keys allowlist + scan policy + retention) | `/repositories`, `/repositories/:org/:repo` | ✅ Done |
| Helm charts list / detail (artifact-type-filtered view of the same repo) | `/helm` | ✅ Done |
| Tag detail (Security / Push history / Layers / Signing) | `/repositories/:org/:repo/tags/:tag` | ✅ Done |
| Security center (Overview / Vulnerabilities / Scans / Remediation) | `/security` | ✅ Done (Policies → per-repo Settings tab) |
| Activity / Notifications (range chips + event-type filters) | `/activity` | ✅ Done |
| Members + RBAC (workspace + org-scoped) | `/members`, `/orgs/:org/members`, `/orgs/:org/settings` | ✅ Done |
| Webhooks (list + detail + CRUD + delivery log + test + rotate) | `/webhooks`, `/webhooks/:id` | ✅ Done |
| Workspace identity + custom domains | `/workspace/domains` | ✅ Done |
| Audit log streaming to SIEM (config + test event) | `/workspace/audit-export` | ✅ Done (Tier 1 #4 Phase 1) |
| Profile | `/profile` | ✅ Done |
| Access hub — personal keys, service accounts, activity, preview surfaces (trust / helpers / policies / review) | `/api-keys`, `/api-keys/service-accounts`, `/api-keys/activity` | ✅ Done (FE-API-048) |
| Platform admin — tenants + scanner adapter selector | `/admin/tenants`, `/admin/scanner` | ✅ Done (REM-011 Phase 2) |

The login page POSTs to `POST /api/v1/login`; the Bearer token is stored in Zustand memory only (never `localStorage` — FE-SEC-001/002) and is silently refreshed 60 seconds before expiry via `POST /api/v1/token/refresh`. The Axios 401 interceptor clears auth state and redirects to `/login?reason=session_expired`. Beacon ships with full dark-mode parity, Cmd+K command palette, sonner toasts, and TanStack Query for all data fetching.

---

## Contributing

We welcome contributions of every size — bug reports, doc improvements, feature PRs, security disclosures, the works. Full guide in [`CONTRIBUTING.md`](CONTRIBUTING.md).

### Quick rules
1. Read [`CLAUDE.md`](CLAUDE.md) before you write code — it's the canonical reference for architecture decisions, security rules, and coding standards.
2. **One change per PR.** Run `make lint test` before opening.
3. Proto changes must pass `make proto-breaking` (no backward-incompatible field removals).
4. New code needs unit tests. Bug fixes need a regression test.
5. Security issues: don't open public issues — use [GitHub's private vulnerability reporting](https://github.com/steveokay/oci-janus/security/advisories/new) instead.

### Picking something to work on
- **Open remediation work:** [`status-tracker.md`](status-tracker.md) — currently-open `REM-*` and `PENTEST-*` items.
- **Prioritised backlog:** [`futures.md`](futures.md) — Tier 1 (production gates) / Tier 2 (operationally valuable) / Tier 3 (polish).
- **`good first issue`** label on GitHub Issues (when populated) — small, well-scoped tasks.

### Sponsorship

If your company uses OCI-Janus, [sponsor the project on GitHub](https://github.com/sponsors/steveokay). It directly funds maintenance time and helps prioritise community requests.

---

## License

OCI-Janus is licensed under the [Apache License 2.0](LICENSE). You are free to:

- Use it commercially (run it in production, host it for paying customers)
- Modify it (fork, vendor, customise)
- Distribute it (rebrand, redistribute, embed)
- Use it privately

Subject to:
- Including the original LICENSE in distributions
- Stating significant changes you've made
- Including any NOTICE file we ship (currently none)

There's no CLA — Apache 2.0's inbound contribution clause is sufficient. By submitting a PR, you agree to license your contribution under the same Apache 2.0 terms.

---

### Documentation map

| Doc | What's in it |
|---|---|
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | How to contribute — workflow, style, what gets accepted |
| [`CODE_OF_CONDUCT.md`](CODE_OF_CONDUCT.md) | Community standards (Contributor Covenant 2.1) |
| [`.github/SECURITY.md`](.github/SECURITY.md) | Security policy — how to report a vulnerability + our response commitments |
| [`CLAUDE.md`](CLAUDE.md) | Canonical architecture + coding rules (the source of truth when code disagrees) |
| [`docs/SELF-HOSTING.md`](docs/SELF-HOSTING.md) | Fork → configure → deploy in your own infrastructure (Docker Compose + Kubernetes Helm paths) |
| [`docs/SERVICES.md`](docs/SERVICES.md) | Per-service endpoint / gRPC / schema / env-var reference |
| [`docs/EVENTS.md`](docs/EVENTS.md) | RabbitMQ routing keys + payload shapes |
| [`docs/SIGNING.md`](docs/SIGNING.md) | Image signing + signed-image admission policy (Phase 1 + Phase 2) |
| [`docs/SIEM-EXPORT.md`](docs/SIEM-EXPORT.md) | Audit-log streaming to syslog / CEF / HTTPS webhook |
| [`docs/CUSTOM-DOMAINS.md`](docs/CUSTOM-DOMAINS.md) | Per-tenant custom domain registration + DNS verification + primary swap (Cloudflare walkthrough, troubleshooting) |
| [`docs/SAML.md`](docs/SAML.md) | Per-tenant SAML SP setup + IdP metadata flow |
| [`docs/SCANNER.md`](docs/SCANNER.md) | Scanner plugin protocol + adapter selection (Trivy / Grype / Clair) |
| [`docs/TESTING.md`](docs/TESTING.md) | Coverage targets, integration tests, OCI conformance |
| [`docs/CI-CD.md`](docs/CI-CD.md) | Pipeline stages, Docker build rules, versioning |
| [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) | Compose + Helm chart layout |
| [`docs/postman/`](docs/postman/) | Postman collection covering every public `/api/v1/*` route |
| [`status-tracker.md`](status-tracker.md) | Currently-open remediation work (lean by design — items live here while in flight) |
| [`status.md`](status.md) | Completed work log — historical record + per-item resolution notes; items land here once cleared from the tracker |
| [`security.md`](security.md) | Security issue tracker (SEC-* / PENTEST-* lifecycle) |
| [`futures.md`](futures.md) | Prioritised backlog of unsprinted items |
| [`FE-STATUS.md`](FE-STATUS.md) | Frontend roadmap + sprint status |

---

> Architecture questions? Open a [Discussion](https://github.com/steveokay/oci-janus/discussions).
> Security issues? [Report privately](https://github.com/steveokay/oci-janus/security/advisories/new) — see [`CONTRIBUTING.md`](CONTRIBUTING.md) for details.
