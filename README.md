# OCI-Janus — Production-Grade Multi-Tenant Docker Registry

A self-hosted, OCI Distribution Spec v1.1-compliant Docker registry platform built in Go. Feature-equivalent to Docker Hub, Nexus, or AWS ECR with multi-tenancy, custom domains, pull-through proxy caching, vulnerability scanning, and image signing.

---

## Table of Contents

1. [Overview](#overview)
2. [Architecture](#architecture)
3. [Services](#services)
4. [Quick Start (Local Dev)](#quick-start-local-dev)
5. [Configuration Reference](#configuration-reference)
6. [Development Guide](#development-guide)
7. [Testing](#testing)
8. [Security](#security)
9. [Deployment](#deployment)
10. [Operations](#operations)

---

## Overview

### Core Capabilities

| Feature | Status |
|---|---|
| OCI Distribution Spec v1.1 (push / pull / delete / list) | Implemented |
| Multi-tenant with per-tenant custom domains | Implemented |
| JWT (RS256) + API key authentication | Implemented |
| mTLS between all internal services | Implemented (dev certs via cert-init; prod via cert-manager) |
| Pull-through proxy cache for upstream registries | Implemented |
| Pluggable storage (MinIO / AWS S3 / GCS / Azure Blob) | Implemented |
| Vulnerability scanner plugin interface | Implemented (external process JSON-RPC only; Trivy default) |
| Image signing — Cosign (Sigstore) + Notary v2 | Implemented (ECDSA P-256, Vault key backend) |
| Webhook delivery with retries + HMAC signing | Implemented |
| Immutable audit log | Implemented (append-only PostgreSQL partition) |
| Garbage collection worker | Implemented (mark-sweep, dry-run / manifests / blobs / full modes) |
| RBAC at org / repo / tag level | Scaffold |

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
| `registry-auth` | 8080 | 50051 | JWT issuance, API key management, credential validation |
| `registry-core` | 8081 | 50052 | OCI Distribution Spec v1.1 implementation |
| `registry-storage` | 8082 | 50053 | Storage abstraction (MinIO/S3/GCS/Azure) |
| `registry-metadata` | 8083 | 50054 | Registry metadata (repos, tags, manifests, blobs) |
| `registry-proxy` | 8084 | 50055 | Pull-through proxy cache for upstream registries |
| `registry-scanner` | 8085 | 50056 | Vulnerability scan orchestration + plugin host |
| `registry-signer` | 8086 | 50057 | Cosign + Notary v2 image signing |
| `registry-webhook` | 8087 | — | Webhook delivery worker |
| `registry-audit` | 8088 | — | Immutable audit log writer + query API |
| `registry-gc` | 8089 | — | Garbage collection worker |
| `registry-tenant` | 8090 | 50060 | Tenant lifecycle + custom domain management |

---

## Quick Start (Local Dev)

### Prerequisites

- Go 1.23+
- Docker + Docker Compose v2
- `buf` CLI v1.x (for proto codegen)
- `golangci-lint` v1.61+ (for linting)

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

# Pull through proxy (once an upstream is registered — see local-setup.md §6)
TOKEN=$(curl -s -X POST http://localhost:8080/api/v1/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"Admin1234!dev","tenant_id":"00000000-0000-0000-0000-000000000001"}' \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['token'])")
curl -s -H "Authorization: Bearer $TOKEN" \
  -H "X-Tenant-ID: 00000000-0000-0000-0000-000000000001" \
  "http://localhost:8084/v2/cache/dockerhub/library/alpine/manifests/3.20"
```

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
| `AUTH_GRPC_ADDR` | `registry-auth` gRPC address |
| `STORAGE_GRPC_ADDR` | `registry-storage` gRPC address |
| `CREDENTIAL_KEY_HEX` | 64-char hex key (32 bytes) for AES-256-GCM credential encryption |
| `UPSTREAM_HTTP_TIMEOUT_SECS` | Per-request timeout to upstream (default `30`) |
| `UPSTREAM_MAX_RESPONSE_BYTES` | Max response body per upstream layer (default `21474836480` = 20 GiB) |

### `registry-scanner`

| Variable | Description |
|---|---|
| `SCANNER_PLUGIN_PATH` | Path to scanner binary or `.so` plugin |
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
├── services/
│   ├── auth/                # JWT token service
│   ├── core/                # OCI Distribution Spec
│   ├── storage/             # Storage abstraction
│   ├── metadata/            # Registry metadata (PostgreSQL)
│   ├── proxy/               # Pull-through proxy cache
│   ├── scanner/             # Vulnerability scanning
│   └── ...
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

> **Note:** OCI conformance suite setup is pending (Sprint 4). Once wired it will run in CI on every PR to `main`.

---

## Security

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

See [`security.md`](security.md) for the full issue tracker. Summary of open MEDIUM+ issues:

| ID | Severity | Description |
|---|---|---|
| SEC-007 | MEDIUM | Missing `X-Content-Type-Options: nosniff` on auth and core HTTP responses |
| SEC-009 | MEDIUM | Auth IP rate limiting targets gateway IP, not client IP |
| SEC-012 | MEDIUM | Proxy blob handler may store partial blob on client disconnect |
| SEC-018 | MEDIUM | Audit HTTP endpoints missing security headers and body size limit |
| SEC-019 | MEDIUM | Six HTTP servers missing `ReadHeaderTimeout` (slowloris vector) |
| SEC-020 | MEDIUM | All HTTP servers missing `ReadTimeout`/`WriteTimeout` |
| SEC-021 | MEDIUM | Healthcheck binary uses `http.DefaultClient` without timeout |
| SEC-022 | MEDIUM | `sslmode=prefer` in docker-compose (should be `sslmode=require`) |
| SEC-023 | MEDIUM | Vault dev root token hardcoded in docker-compose |
| SEC-024 | MEDIUM | Dev TLS private keys world-readable (`chmod a+r *.key`) |

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
- `registry_active_uploads_total` — in-progress blob upload gauge

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

## Contributing

1. Follow the conventions in [CLAUDE.md](CLAUDE.md) — it is the canonical reference for architecture decisions, security rules, and coding standards.
2. Run `make lint test` before opening a PR.
3. All proto changes must pass `make proto-breaking` (no backward-incompatible field removals).
4. New services must include unit tests (80% coverage minimum) and an integration test suite.
5. Security issues go in `security.md` with the `SEC-NNN` identifier format.

---

> Architecture questions? Open an issue with the label `architecture`.
> Security issues? Follow the remediation steps in `security.md`.
