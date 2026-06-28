# CLAUDE.md — OCI-Compliant Docker Registry Platform

> ⚠ **ARCHITECTURE REDESIGN IN FLIGHT (2026-06-26).**
> A deep system review (`.claude/reviews/system-review-2026-06-26.md`) surfaced significant drift between this file's claims and the codebase. The agreed direction (REDESIGN-001 in `status-tracker.md`, plan in `.claude/plans/2026-06-26-single-tenant-redesign.md`) is:
> - **Default deployment mode shifts to `single` (self-hosted single-tenant);** `DEPLOYMENT_MODE=multi` preserves the SaaS capability.
> - **Custom domains, per-tenant SSO, tenant signup, plan/billing UI are being removed.**
> - **Settings IA collapses Admin + Workspace + Account into one role-gated `/settings` page.** No standalone Admin / Deployment sidebar group.
> - **`is_global_admin` typed primitive replaces the `scope_value='*'` magic-string platform-admin convention.** In single mode, workspace admins are effective global admins.
> - **Several security/spec claims in this file are aspirational** until the redesign Phases 1, 5, 6, 7 land: RLS coverage (§9), mTLS hot reload (§7), fail-closed Redis check (§7), JWT cache on management path (§7), audit-event catalogue completeness (§10). Treat these sections as the *target state*, not the current state, until Phase 7.1 rewrites them.
>
> Until the redesign ships, prefer the plan + review docs over this file when they conflict.

> **Purpose:** Canonical rules for AI-assisted development of the Docker Registry Platform.
> This file holds prescriptive conventions and pointers; descriptive content lives next to the code.
> When code disagrees with this file, the code is wrong — but proto files (`proto/*/v1/*.proto`) and migration files (`services/*/migrations/`) are the authoritative contracts.

---

## Table of Contents

1. [Project Overview](#1-project-overview)
2. [Repository Structure](#2-repository-structure)
3. [Architecture Overview](#3-architecture-overview)
4. [Service Catalogue](#4-service-catalogue) — summary; detail in [`docs/SERVICES.md`](docs/SERVICES.md)
5. [Shared Libraries](#5-shared-libraries)
6. [Communication Patterns](#6-communication-patterns)
7. [Authentication & Security](#7-authentication--security)
8. [Storage Layer](#8-storage-layer)
9. [Multi-Tenancy & Custom Domains](#9-multi-tenancy--custom-domains)
10. [Observability](#10-observability)
11. [Database Conventions](#11-database-conventions)
12. [gRPC Conventions](#12-grpc-conventions)
13. [Security Hardening Rules](#13-security-hardening-rules)
14. [Decision Log](#14-decision-log)
15. [Workflow Gates](#15-workflow-gates)

External references:
- [`docs/SERVICES.md`](docs/SERVICES.md) — per-service detail (endpoints, gRPC, schemas, env vars)
- [`docs/EVENTS.md`](docs/EVENTS.md) — RabbitMQ routing keys + payloads
- [`docs/TESTING.md`](docs/TESTING.md) — coverage targets, integration tests, OCI conformance
- [`docs/CI-CD.md`](docs/CI-CD.md) — pipeline stages, Docker build rules, versioning
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — Compose + Helm chart layout
- `security.md` — SEC-001..SEC-036 hardening items (status + resolution notes)
- `status-tracker.md` — currently open remediation + security items (lean by design)
- `status.md` — completed-work log + resolution notes (items move here once cleared from `status-tracker.md`)

---

## 1. Project Overview

A production-grade, multi-tenant OCI-compliant Docker registry platform built in Go. Equivalent in feature scope to Docker Hub / Nexus / AWS ECR, designed for self-hosted deployment.

### Core Capabilities

- Full OCI Distribution Spec v1.1 compliance (push, pull, delete, list, referrers)
- Multi-tenant with per-tenant custom domains
- JWT (RS256) + API key authentication; mTLS between all internal services
- Per-tenant SSO: OAuth 2.0 + PKCE (Google / GitHub / Microsoft / generic OIDC) and SAML 2.0 SP (auto-provisioning, AES-256-GCM-encrypted client secrets) — see [`docs/SAML.md`](docs/SAML.md)
- Pluggable storage: MinIO, AWS S3, GCP Cloud Storage, Azure Blob
- Pluggable vulnerability scanner interface (external-process JSON-RPC) with per-tenant scan policies + compliance reports (SPDX SBOM + PDF)
- Image signing via Cosign (Sigstore) and Notary v2 — see [`docs/SIGNING.md`](docs/SIGNING.md)
- Pull-through proxy cache for upstream registries
- RBAC at org / repo level (owner / admin / writer / reader); platform-admin marker scope `(admin, org, *)`
- Webhook delivery with retries and HMAC signing
- Full append-only audit trail (Postgres RLS + low-privilege role)
- Pluggable observability: OpenTelemetry → Jaeger, Grafana Tempo, or Datadog

### Language & Runtime

| Concern | Choice |
|---|---|
| Backend language | Go 1.23+ (toolchain `go 1.25.7` across all modules) |
| Protobuf/gRPC codegen | `buf` CLI v1.x |
| Database | PostgreSQL 16 |
| Cache / Rate limiting | Redis 7 (Valkey acceptable) |
| Message broker | RabbitMQ 3.13 with Quorum Queues |
| Container runtime (dev) | Docker + Docker Compose v2 |

---

## 2. Repository Structure

All code lives in a single monorepo at `github.com/steveokay/oci-janus`. Go services each have their own `go.mod` (for isolated Docker builds) and are linked together via a root `go.work` file for local development.

```
github.com/steveokay/oci-janus/
├── go.work / go.work.sum         # links all service modules + libs/ + proto/gen/go/
├── proto/                        # All .proto files + generated Go stubs (committed)
├── libs/                         # Shared Go module — see §5
├── services/                     # 13 services — see §4 summary, docs/SERVICES.md detail
├── frontend/                     # React/TypeScript dashboard
├── infra/                        # Compose, Helm charts, runbooks
├── docs/                         # Detailed references split out of CLAUDE.md
├── .github/workflows/            # Path-filtered CI jobs per service
├── Makefile                      # make build / test / lint (each fans out to per-service targets via `$(addprefix …,$(SERVICES))`)
└── .golangci.yml                 # Shared linter config
```

### Go Workspace

`go.work` at the root links all service modules and `libs/` together. Each service `go.mod` remains self-contained for Docker builds (`COPY services/auth . && GOWORK=off go build` works without the workspace). `go.work.sum` is committed alongside `go.work`.

### Per-Service Layout (Go services)

```
<service>/
├── cmd/server/main.go        # Entrypoint only — no business logic
├── internal/
│   ├── config/               # Viper-based config loading
│   ├── server/               # gRPC and/or HTTP server setup
│   ├── handler/              # gRPC handlers / HTTP handlers
│   ├── service/              # Business logic (pure functions where possible)
│   ├── repository/           # Database access (no raw SQL outside this package)
│   ├── middleware/           # Auth, logging, tracing middleware
│   └── testutil/             # Test helpers, fixtures, mocks
├── migrations/               # SQL migration files (goose format)
├── Dockerfile
├── .env.example              # All env vars documented, no defaults for secrets
├── go.mod / go.sum
├── buf.gen.yaml              # Points to ../../proto for codegen
└── Makefile
```

---

## 3. Architecture Overview

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

---

## 4. Service Catalogue

> One-line summary per service. Full endpoints, gRPC definitions, schemas, env vars, and impl rules live in [`docs/SERVICES.md`](docs/SERVICES.md).

| # | Service | Purpose | Owns | Notable |
|---|---|---|---|---|
| 1 | `registry-gateway` | TLS termination + host-based tenant resolution + rate limit | — | Traefik v3 |
| 2 | `registry-auth` | JWT issuance, API keys, RBAC permission checks, per-tenant SSO (OAuth + SAML) | Postgres (auth schema — incl. `auth_providers`, `auth_login_sessions`, `users.sso_provider_id`, `service_accounts`) | RS256, 300s TTL, JTI revocation in Redis; PKCE S256 OAuth; SAML SP via `crewjam/saml` |
| 3 | `registry-core` | OCI Distribution Spec v1.1 — `/v2/` API | — | Streams blobs, checkAccess on every handler; tag immutability preflight on `PutManifest` rejects re-pushes with `400 MANIFEST_INVALID` when `repositories.immutable_tags=true` OR `tags.immutable=true` |
| 4 | `registry-storage` | Pluggable blob storage abstraction | Object store backend | MinIO/S3/GCS/Azure/filesystem |
| 5 | `registry-metadata` | Source of truth for repos/tags/manifests/scans/SBOMs | Postgres (metadata schema) | gRPC-only access; Redis cache; read-replica routing; per-tag SBOM columns |
| 6 | `registry-proxy` | Pull-through cache for upstream registries | Postgres (proxy schema) | Upstream creds AES-256-GCM; `store.queued` retry |
| 7 | `registry-scanner` | Vulnerability scan orchestration + plugin host + scan policies + compliance reports | Postgres (scanner schema — `scan_policies`, `compliance_reports`) | External-process JSON-RPC; checksum-validated binary; async report worker with `FOR UPDATE SKIP LOCKED`; SPDX JSON 2.3 + hand-crafted PDF renderer |
| 8 | `registry-signer` | Cosign + Notary v2 signing/verification | Postgres (signatures table) | Vault dev mode locally; KMS in prod |
| 9 | `registry-webhook` | Reliable webhook delivery with retries + HMAC + delivery payload retrieval | Postgres (webhook schema) | SSRF block-list; HTTPS-only; `GetDelivery` for FE inspection |
| 10 | `registry-audit` | Immutable audit log + analytics + notifications | Postgres (audit schema) | `FORCE ROW LEVEL SECURITY`, `registry_audit_app` role; PG14 `date_bin` time-series |
| 11 | `registry-gc` | Mark-sweep garbage collection + GC status visibility | Postgres (`gc_runs` table) | `pg_try_advisory_lock`; FNV-64a key per tenant; async `RunNow` queues a row + drains via `FOR UPDATE SKIP LOCKED` |
| 12 | `registry-tenant` | Tenant CRUD (incl. rename + plan), custom domain CRUD + verification | Postgres (tenant schema — `tenants.slug`, `tenant_domains.is_primary`) | DNS TXT challenge; 24h/48h notification; atomic primary swap |
| 13 | `registry-management` | REST BFF for the dashboard (and CLI/Terraform) | — | No gRPC server; translates HTTP → gRPC; mounts SSO + signer + scanner + gc routes when their gRPC addrs are set |

---

## 5. Shared Libraries

**Module path:** `github.com/steveokay/oci-janus/libs`

All shared packages live here. Services import specific sub-packages.

```
libs/
├── auth/bearer           # Bearer-token header parsing (RFC 7235, case-insensitive scheme)
├── auth/mtls             # mTLS client + server config builders (cert reloading)
├── cmd/healthcheck       # Tiny CLI used as the Kubernetes liveness/readiness probe
├── config/loader         # Viper config loader + DBConfig pool tuning + dev defaults
├── crypto/aes            # AES-256-GCM helpers
├── crypto/argon2         # Argon2id password hashing helpers
├── errors/codes          # Canonical error codes + MapDBError (pgxpool exhaustion → ResourceExhausted)
├── middleware/grpc       # Unary + stream interceptors + REM-007 read cache
├── middleware/http       # Request ID, tracing, auth, secure headers
├── observability/metrics # Common Prometheus metric definitions
├── observability/otel    # OTEL bootstrap (Bootstrap + shutdown; HTTP + gRPC instrumentation)
├── rabbitmq/consumer     # Consumer with DLX + manual ack
├── rabbitmq/events       # All typed event definitions (see docs/EVENTS.md)
├── rabbitmq/publisher    # Typed event publisher with confirm mode
├── scanner/plugin        # Scanner interface + ScanRequest/ScanResult types
├── storage/driver        # Driver interface definition (no implementations)
└── testutil/             # testcontainers helpers (PG/Redis/RabbitMQ/MinIO + auth+audit bundle) + fixtures
```

JWT signing + verification lives in `services/auth` (not in `libs/`) — production code only ever needs to *validate* tokens, so the validator helper sits next to its single caller (the auth-service HTTP middleware). API-key hashing also lives in `services/auth/internal/service/apikey.go` for the same reason — the hashing is paired with the polymorphic owner lookup that only auth owns.

**Rules:**
- No business logic in `libs/`. Only utilities and interfaces.
- No circular imports. Services import from `libs/`; `libs/` never imports from `services/`.
- All public functions must have godoc comments.
- A breaking change to a `libs/` interface requires a PR that updates all affected services in the same commit — no multi-repo version dance.

---

## 6. Communication Patterns

### gRPC (synchronous, internal)

- All internal service-to-service calls use gRPC over mTLS.
- Proto files live in `proto/`; generated stubs committed to `proto/gen/go/`.
- Each service has a `buf.gen.yaml` pointing to `../../proto` for regeneration.
- Use `grpc.NewClient` (not deprecated `grpc.Dial`) with a timeout context — never silently hang.
- Always set deadlines on outgoing gRPC calls: `ctx, cancel := context.WithTimeout(ctx, 5*time.Second)`.
- gRPC health check protocol (`grpc.health.v1`) implemented by every service.
- Client-side retry: 3 attempts, exponential backoff, only on `UNAVAILABLE` and `DEADLINE_EXCEEDED`.
- Trigger eager connection establishment via `conn.Connect()` at startup so the first inbound request does not stall during the TLS/HTTP-2 handshake.

### RabbitMQ (asynchronous, event-driven)

- Exchange type: `topic` for all events (`registry.events`).
- Quorum queues only (no classic queues in production).
- Publishers use confirm mode — wait for broker ACK before returning.
- Consumers use manual ACK — ACK only after successful processing.
- All publishes route through `libs/rabbitmq/publisher`.
- Dead-letter exchange: `dlx.<service>` — every queue has a DLX configured.
- Message TTL: 7 days on all queues (configurable).
- Do not put sensitive data (passwords, tokens) in message payloads.
- Routing keys and payload structs: see [`docs/EVENTS.md`](docs/EVENTS.md).

### Service Discovery

- Kubernetes: services discover each other via K8s DNS (`<service>.<namespace>.svc.cluster.local`).
- Docker Compose: service names are DNS hostnames.
- All gRPC target addresses configured via environment variables, never hardcoded.

---

## 7. Authentication & Security

### mTLS Between Services

Every gRPC client/server pair uses mutual TLS.

**Certificate management:**
- Development: self-signed certs generated by `make dev-certs` (uses `cfssl`). `gen-dev-certs.sh` emits subjectAltName for Go 1.15+ hostname verification.
- Production (K8s): cert-manager with internal CA issuer.
- Cert rotation: automated via cert-manager. Services reload certs without restart (use `tls.Config.GetCertificate`).

**Rules:**
- Every gRPC server sets `tls.RequireAndVerifyClientCert`.
- Client cert CN must match expected service name (enforce in server-side interceptor).
- CA cert loaded from `MTLS_CA_CERT_PATH` env var. No defaults.
- Certificate validity: maximum 90 days.
- Cert key file permissions: `chmod 600` (SEC-024).

**mTLS config builder (from `libs/auth/mtls`):**

```go
// ServerTLSConfig returns a tls.Config for gRPC servers requiring client certs
func ServerTLSConfig(caCertPath, certPath, keyPath string) (*tls.Config, error)

// ClientTLSConfig returns a tls.Config for gRPC clients presenting a cert
func ClientTLSConfig(caCertPath, certPath, keyPath string, serverName string) (*tls.Config, error)
```

Dev fallback: when cert paths are unset, services log `slog.Warn` and use `insecure.NewCredentials()`. Never allow this in production — config validation in `main.go` must reject empty cert paths when `OTEL_ENVIRONMENT=production`.

### JWT Validation

- Every gRPC server validates Bearer tokens via `registry-auth` gRPC call.
- Cache validation results in Redis: key `jwt:valid:<jti>`, TTL = `time.Until(claims.ExpiresAt.Time)` (REM-002).
- The cached value must serialise the full `Access` list as JSON — the cache must not drop claim fields.
- On cache miss: call `registry-auth.ValidateToken` gRPC.
- If `registry-auth` is unreachable: fail closed (deny all), log error, increment metric.

### HTTP Bearer Auth — JWT and API-key forms (FUT-006)

`registry-auth`'s `requireAuth` HTTP helper accepts **two** Bearer-token shapes and dispatches internally:

| Form | When | Routes |
|---|---|---|
| `Bearer <RS256 jwt>` (3-segment base64url, starts with `eyJ`) | Browsers / FE clients after `POST /api/v1/login` or `/auth/token` exchange | All authenticated routes |
| `Bearer key.<uuid>.<64-hex-secret>` (FUT-006, 2026-06-23) | CI bots / `curl` scripts wanting to introspect themselves directly | `/api/v1/users/me`, `/api/v1/access/activity`, anything that doesn't require a role claim |

The discriminator is the literal `key.` prefix. API-key validation flows through `ValidateAPIKey` (argon2 verify + expiry/disabled/SA-allowlist checks) and synthesises a `*Claims` with `Subject = vk.UserID` (shadow user id for SA keys), `TenantID`, `Access` (intersected scopes), and **empty `Roles`** — raw API keys don't carry RBAC roles, so any handler that gates on `Roles` (e.g. admin-only endpoints) must continue to require a JWT and will surface a clean 403 rather than 401. Full per-route contract + auth dispatch flow lives in [`docs/SERVICES.md` §2](docs/SERVICES.md#2-registry-auth).

### Environment Variables — Security Rules

- **Never** commit `.env` files. Only `.env.example` with placeholder values.
- All secrets (DB passwords, JWT keys, API credentials) must come from environment variables or a secrets manager.
- In production K8s: use Kubernetes Secrets (base64) mounted as env vars, or External Secrets Operator pointing to Vault/AWS Secrets Manager.
- Every service must fail to start if a required secret env var is empty or missing. Use a `config.Validate()` call in `main.go` before any server starts.
- Never log environment variable values at startup, even at DEBUG level.

### Input Validation

- All user-supplied strings validated against allowlists at the handler layer.
- Repository name: `^[a-z0-9]+([._-][a-z0-9]+)*$`, max 128 chars.
- Tag name: `^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`.
- Digest: `^sha256:[a-f0-9]{64}$`.
- Username: `^[a-zA-Z0-9_-]{3,64}$`.
- Org name: `^[a-z0-9-]{2,64}$`.
- Never pass unvalidated strings to SQL (use parameterised queries only — see §11).
- Never pass unvalidated strings to shell commands (no `exec.Command` with user input).

### Client IP Trust (SEC-009)

- Audit / rate-limit IP extracted from `X-Forwarded-For` only if request came through a CIDR listed in `TRUSTED_PROXY_CIDRS`.
- Empty `TRUSTED_PROXY_CIDRS` ⇒ always use direct TCP peer.
- Malformed CIDR entries are logged + skipped; service still starts.

---

## 8. Storage Layer

### Driver Selection

`STORAGE_DRIVER` selects the backend. Valid values: `minio`, `s3`, `gcs`, `azure`, `filesystem`. Unset or invalid: fail to start with a clear error message.

Per-driver env var tables: see [`docs/SERVICES.md` §4](docs/SERVICES.md#4-registry-storage).

### Encryption at Rest

- S3: enforce `x-amz-server-side-encryption: AES256` on all PutObject calls.
- GCS: CMEK key configured on bucket (Terraform-managed).
- Azure: SSE enabled on storage account.
- MinIO: enable MinIO server-side encryption (SSE-S3 or SSE-KMS) — see `infra/runbooks/minio-encryption.md`.
- Filesystem: filesystem encryption (LUKS/dm-crypt) must be configured at OS level.

---

## 9. Multi-Tenancy & Custom Domains

### Tenant Isolation

- All database rows include `tenant_id UUID NOT NULL`.
- All queries in `registry-metadata` must filter by `tenant_id` — never query across tenants.
- PostgreSQL Row Security Policy (RLS) enabled as a second layer of defence:

  ```sql
  ALTER TABLE repositories ENABLE ROW LEVEL SECURITY;
  CREATE POLICY tenant_isolation ON repositories
    USING (tenant_id = current_setting('app.tenant_id')::uuid);
  ```

- Application sets `SET LOCAL app.tenant_id = '<id>'` in each transaction.
- Storage keys are prefixed with `tenant_id` (see [`docs/SERVICES.md` §4 storage key layout](docs/SERVICES.md#4-registry-storage)).
- RabbitMQ messages include `tenant_id` in payload and as a message header.

### Custom Domain Resolution

```
Incoming request Host: registry.acme.com
  → Gateway looks up in Redis: domain:registry.acme.com → tenant_id: <uuid>
  → Cache miss → query registry-tenant gRPC → cache result (TTL 60s)
  → Inject X-Tenant-ID header
  → Route to registry-core
```

- Wildcard platform domain: `*.registry.example.com` → tenant resolved from subdomain.
- Custom domain: verified and stored in `registry-tenant` DB.
- If domain not found: return 404 with no tenant information exposed.

---

## 10. Observability

### OpenTelemetry Setup

All services instrument with OpenTelemetry Go SDK. Exporter is pluggable via environment variable.

**`OTEL_EXPORTER`** controls the backend: `jaeger` | `tempo` | `datadog` | `stdout`.

**Common env vars (all services):**

```
OTEL_EXPORTER=                   # required: jaeger|tempo|datadog|stdout
OTEL_ENDPOINT=                   # OTLP endpoint URL
OTEL_SERVICE_NAME=               # set per-service in Dockerfile
OTEL_ENVIRONMENT=                # production|staging|development
OTEL_SAMPLING_RATE=1.0           # 0.0 to 1.0, default 1.0
OTEL_INSECURE=                   # true only in local dev with no TLS on collector
```

**Bootstrap pattern (from `libs/observability/otel`):**

```go
func Bootstrap(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error)
```

Call in `main.go` **before** starting any server. Always call `shutdown` on process exit to flush spans/metrics. Missing this call is the root cause of "no traces in Jaeger" — every service entrypoint must include it.

**HTTP tracing:** Wrap the HTTP handler tree with `otelhttp.NewHandler(...)` so HTTP requests create root spans. gRPC tracing is wired via `grpcmw.OTELServerHandler()` as a `StatsHandler`.

### Metrics

Every service exposes Prometheus metrics at `GET /metrics` on a **dedicated port `:9090`** (SEC-025) — separated from the business port so NetworkPolicy can grant Prometheus access without exposing the OCI API.

**Standard metrics (from `libs/observability/metrics`):**
- `registry_http_request_duration_seconds` — histogram, labels: service, method, path, status
- `registry_grpc_request_duration_seconds` — histogram, labels: service, method, status
- `registry_rabbitmq_messages_consumed_total` — counter, labels: service, queue, status
- `registry_storage_operation_duration_seconds` — histogram, labels: driver, operation, status
- `registry_active_uploads_total` — gauge

The local stack uses otel-collector → Prometheus → Jaeger SPM. The otel-collector requires:
- An `otlp` receiver pipeline for `metrics` (not only `traces`), otherwise SDK metric pushes fail with `Unimplemented MetricsService`.
- The prometheus exporter `namespace: registry` is reflected in Jaeger's `PROMETHEUS_QUERY_NAMESPACE=registry` env var so SPM queries the right metric names.

### Structured Logging

- Logger: `log/slog` (Go 1.21+ standard library).
- Format: JSON in production, text in development (`LOG_FORMAT=json|text`).
- Level: `LOG_LEVEL=debug|info|warn|error`.
- Every log entry must include: `trace_id`, `span_id`, `tenant_id` (where available), `service`.
- Never log: passwords, tokens, API keys, private key material, full request bodies.

---

## 11. Database Conventions

### General Rules

- ORM: **none**. Use `pgx/v5` directly with `pgxpool`. Raw SQL only.
- All queries parameterised. Never use `fmt.Sprintf` to build SQL.
- Migrations: `pressly/goose` with SQL migrations embedded via `embed.FS` (`migrations/migrations.go`).
- Only `registry-metadata` has direct PostgreSQL access for metadata; `registry-auth`, `registry-tenant`, `registry-proxy`, `registry-webhook`, `registry-audit` have their own separate databases.
- Connection pool: built from `libs/config/loader.DBConfig.PoolConfig()` which sets `ConnectTimeout: 5s`, `MaxConnLifetime: 30m`, `MaxConnIdleTime: 5m`. `MaxConns` is read from `DB_MAX_CONNS` (default 20).
- Every query must use the request context for cancellation.
- Transactions: always use `defer tx.Rollback(ctx)` — only commit explicitly on success.
- Connection-pool exhaustion is mapped to `codes.ResourceExhausted` via `libs/errors/codes.MapDBError` (SEC-006).

### Migration Rules

- Never drop a column in a migration — add a new column and migrate data in a separate step.
- Every migration must be reversible (down migration required).
- Migration naming: `YYYYMMDDHHMMSS_<description>.sql`.
- Run migrations at startup in a separate step before serving traffic (use `goose up`).

### Connection String

```
DB_DSN=postgres://<user>:<password>@<host>:<port>/<database>?sslmode=require
```

`sslmode=require` is mandatory in production; `sslmode=disable` is rejected at startup (SEC-022). `sslmode=prefer` is permitted only in the local dev Compose stack.

### Read Replica Routing (REM-008)

- `DB_DSN_REPLICA` (optional) configures a read pool routed by `repository.reader()`.
- `ListRepositories`, `ListTags`, `ListOrphanedBlobs` use the replica.
- Without a replica DSN, all queries fall through to the primary pool.

---

## 12. gRPC Conventions

### Proto File Rules (in `proto/`)

```
proto/
├── auth/v1/auth.proto
├── storage/v1/storage.proto
├── metadata/v1/metadata.proto
├── proxy/v1/proxy.proto
├── scanner/v1/scanner.proto
├── signer/v1/signer.proto
├── tenant/v1/tenant.proto
├── webhook/v1/webhook.proto
├── audit/v1/audit.proto
├── gen/go/                    # Generated stubs — committed, not gitignored
└── buf.yaml
```

- Package naming: `registry.<service>.v1`.
- Go package option: `option go_package = "github.com/steveokay/oci-janus/proto/gen/go/<service>/v1;<service>v1";`.
- All fields use `snake_case`.
- All RPCs return errors using `google.rpc.Status` (import `google/rpc/status.proto`).
- Pagination: use `page_token` (string) + `page_size` (int32) pattern, not offset.
- All timestamps: `google.protobuf.Timestamp`.
- UUIDs: `string` (not bytes).
- Breaking changes: never modify existing field numbers. Add new fields only.

### Interceptors (applied to every gRPC server via `libs/middleware/grpc`)

**Server-side, in this order (outermost first):**
1. Recovery (panic → gRPC Internal error)
2. Request ID injection
3. mTLS peer verification (CN check)
4. Auth token validation (for external-facing services)
5. Tenant ID extraction + context injection
6. OpenTelemetry tracing (via `OTELServerHandler` `StatsHandler`)
7. Structured logging
8. Metrics
9. Server-side gRPC cache interceptor on `registry-metadata` (REM-007)

**Client-side:**
1. mTLS credential attachment
2. OpenTelemetry trace propagation
3. Deadline injection
4. Retry (UNAVAILABLE, DEADLINE_EXCEEDED only)

---

## 13. Security Hardening Rules

These rules apply to **every service** without exception.

### Go Code

- [ ] No `unsafe` package usage without a documented, reviewed justification
- [ ] No `exec.Command` with any part of user-supplied input
- [ ] No `os.Getenv` for secrets inside handlers — load at startup into a typed config struct
- [ ] All file paths sanitised with `filepath.Clean` and checked against an allowed prefix
- [ ] HTTP clients: always set timeouts (`Timeout`, `TLSHandshakeTimeout`, `ResponseHeaderTimeout`)
- [ ] No default HTTP client (`http.DefaultClient`) — always create a configured client
- [ ] `context.Background()` never used inside request handlers — always propagate request context (SEC-028)
- [ ] Randomness: use `crypto/rand`, never `math/rand` for security-sensitive values

### HTTP

- [ ] `Content-Security-Policy` header on all HTML responses (via `libs/middleware/http.SecureHeaders`)
- [ ] `X-Content-Type-Options: nosniff` on all responses
- [ ] `X-Frame-Options: DENY` on all responses
- [ ] HSTS on all HTTPS responses
- [ ] No sensitive data in URL query parameters (use POST body or headers)
- [ ] CORS: explicitly configured allowlist, never `*`
- [ ] Request body size limits set on all HTTP servers
- [ ] `ReadHeaderTimeout: 10s` (Slowloris protection), `ReadTimeout` + `WriteTimeout` set per service (SEC-019/020)

### Dependencies

- [ ] `govulncheck` run in CI on every PR
- [ ] `go mod verify` run in CI
- [ ] Dependabot or Renovate configured for automated dependency PRs
- [ ] No indirect dependency pinned in `go.mod` without a comment explaining why
- [ ] License check in CI (reject GPL/AGPL dependencies unless reviewed)

### Secrets

- [ ] No secrets in Git history (pre-commit hook: `gitleaks`)
- [ ] No secrets in Docker image layers (`docker history` checked in CI)
- [ ] No secrets in Helm values files
- [ ] Secret rotation procedure documented in `infra/runbooks/secret-rotation.md`

Numbered SEC items (SEC-001..SEC-036) and their resolution notes live in `security.md`.

---

## 14. Decision Log

| # | Decision | Rationale | Date |
|---|---|---|---|
| 1 | gRPC for sync, RabbitMQ for async | gRPC gives strong contracts + mTLS; RabbitMQ gives durable async with DLQ | Initial |
| 2 | RabbitMQ over Kafka | Lower operational complexity; Quorum Queues give durability without Kafka's broker count requirements | Initial |
| 3 | JWT RS256 + API keys + mTLS | Defence in depth: mTLS for network layer, JWT for identity, API keys for machine accounts | Initial |
| 4 | Multi-tenant with custom domains | Required for white-label / enterprise use cases | Initial |
| 5 | Pluggable scanner interface (external process only, no Go `.so`) | Avoids locking into one scanner; rules out the unsafe Go plugin path | 2026-06-09 |
| 6 | Cosign + Notary v2 | Both are actively maintained and address different use cases; Cosign for keyless, Notary v2 for TUF | Initial |
| 7 | OTEL with pluggable exporter | Avoids vendor lock-in; same instrumentation code works with Jaeger, Tempo, or Datadog | Initial |
| 8 | Monorepo with Go workspaces | Atomic cross-service changes; eliminates version-bump overhead for proto + libs; `go.work` keeps per-service `go.mod` files self-contained for Docker builds | 2026-06-09 |
| 9 | pgx/v5 with raw SQL, no ORM | Full query control; ORM abstraction leaks at scale; parameterised queries enforced by pgx | Initial |
| 10 | Distroless final Docker image | Minimal attack surface; no shell means no RCE via shell injection even if exploited | Initial |
| 11 | No presigned URLs to clients | Prevents storage credential exposure; all blob traffic proxied for audit and rate limiting | Initial |
| 12 | PostgreSQL RLS as second layer | Defence in depth for tenant isolation; application bug cannot leak cross-tenant data | Initial |
| 13 | Trivy as default scanner plugin | Active maintenance + good CVE coverage + permissive license | 2026-06-09 |
| 14 | Vault dev mode as local KMS | Same `SIGNER_KEY_BACKEND=vault` path as production; no special dev-only code path. **Full doc: [`docs/SIGNING.md`](docs/SIGNING.md)** | 2026-06-09 |
| 15 | Audit table FORCE RLS + low-privilege `registry_audit_app` role | Application bug cannot tamper with audit records | 2026-06-09 |
| 16 | GC advisory locks via `pg_try_advisory_lock` (FNV-64a key) | Non-blocking GC across multiple workers; clean lock release via deferred unlock | 2026-06-09 |
| 17 | `services/scanner` gets its own DB for scan policies + compliance reports (FE-API-018/019) | Previously DB-less; scan policies belong with the scanner (not metadata) so policy edits stay close to enforcement; compliance reports are async + multi-replica so `FOR UPDATE SKIP LOCKED` job claim needs Postgres | 2026-06-20 |
| 18 | `services/gc` gets its own DB for GC run status (FE-API-032) | Previously DB-less; `gc_runs` table makes status visibility + `RunNow` async — `RunNow` INSERTs a queued row + non-blocking channel send, drained by the cron loop via `ClaimNextQueued` (`FOR UPDATE SKIP LOCKED`) so the legacy in-process runner still works when `DB_DSN` is unset | 2026-06-21 |
| 19 | Per-tenant SSO model (FE-API-034) — OAuth (PKCE S256) and SAML 2.0 SP | OAuth and SAML both live on `services/auth` since `auth_providers` + `auth_login_sessions` belong with the user table. Hand-rolled OAuth (no `x/oauth2`) for explicit PKCE control; SAML wraps `crewjam/saml` bare `ServiceProvider` (bypasses `samlsp.Middleware` because JWT issuer covers session). `client_secret` AES-256-GCM-encrypted before persistence | 2026-06-21 |
| 20 | Custom-domain primary mutex on `tenant_domains.is_primary` (FE-API-007/027) | Partial unique index `WHERE is_primary`; `MarkDomainVerified` auto-promotes the first verified domain; primary swap is one atomic tx (`SELECT verified → demote-all → promote-target RETURNING`) so no observable state has two primaries | 2026-06-20 |
| 21 | Generic `GetAnalytics` RPC over `services/audit` with BFF-supplied bucket origin (FE-API-030) | Audit is already the system of record for `push.image` etc.; PG14 `date_bin` aligns buckets across replicas; BFF owns the range→bucket mapping (24h→1h×24, 7d→6h×28, 30d→1d×30) and pre-allocates empty buckets so quiet periods report `count=0` rather than gaps | 2026-06-21 |
| 22 | Service-account principal pattern: shadow users (FE-API-048) | Each service account auto-provisions a `users.kind='service_account'` row. `ValidateAPIKey`/`ValidateToken` return that id in `user_id`; downstream services treat it as an opaque actor. RBAC/audit/RLS/JWT machinery unchanged. Distinguishing principal kind is a read-path concern (`LEFT JOIN users ON kind`), not a write-path one. | 2026-06-22 |
| 23 | Two-layer tag immutability — `repositories.immutable_tags` + `tags.immutable` (futures.md Tier 1 #2) | Repo-wide flag is the table-stakes posture; per-tag pin is the lighter alternative for repos that mix mutable dev tags + a small set of pinned releases. `services/core.checkTagImmutable` short-circuits on idempotent same-digest re-pushes (not a "move") and fails OPEN on metadata reachability failures (warn + continue) so a transient DB blip doesn't reject every push. Per-tag pin wins precedence — repo flag is the second RPC only when the same-digest fast path didn't fire | 2026-06-23 |
| 24 | Unified Bearer dispatch in `requireAuth` — JWT + `key.<id>.<secret>` (FUT-006) | Picked option (a) over a parallel `/principal/me` route. One auth surface keeps the mental model simple; the `key.` literal prefix is a cheap structural discriminator that can't collide with a JWT (JWT segment 0 starts with `eyJ` after base64-encoding `{`). Synthesised `*Claims` set `Roles: []` deliberately — raw API keys aren't expected to carry RBAC, so role-gated handlers return a legible 403 instead of misrouting to a 401 | 2026-06-23 |

---

## 15. Workflow Gates

These rules cover what you must run **before pushing a branch**. They exist so PR CI never surfaces a failure that a single local command would have caught.

### 15.1 Frontend — run all 4 CI equivalents before push

CI (`.github/workflows/ci-ui.yml`) runs four jobs: `lint`, `typecheck`, `test`, `build`. Every one of them must be green locally before pushing a frontend branch. The `cd frontend && tsc --noEmit && vitest run` shorthand is **not enough** — it skips lint and skips the route-tree generation that `build` relies on.

```
cd frontend
npm run lint        # eslint, 0 errors required (warnings OK)
npm run typecheck   # tsc -b --noEmit, 0 errors
npm run test        # vitest run, all pass
npm run build       # vite build + tsc -b, builds cleanly
```

**Why this exists:** Established 2026-06-28 after PR #152 failed every frontend CI stage despite local `tsc` + `vitest` coming back green. Two latent issues:

- `frontend/src/routeTree.gen.ts` is gitignored — only Vite generates it. `tsc --noEmit` doesn't run Vite, so CI's typecheck/test jobs had no routeTree to import. Fixed by `npm run routes:generate` + `prelint`/`pretypecheck`/`pretest` hooks that produce the file from `@tanstack/router-generator`'s `Generator` class. The hooks run automatically on every `npm run lint`/`typecheck`/`test`.
- Two pre-existing lint errors had been masked by never running `npm run lint`: `'_e' is defined but never used` in a catch clause, and `React.useMemo` called after an early-return.

**How to apply:** Run all 4 commands listed above. If lint reports an error in code you didn't touch, fix it inline — the rule is "before push, CI is green," not "before push, my diff is clean."

### 15.2 Backend — run the per-service Makefile target before push

Each Go service has its own CI workflow (`.github/workflows/ci-<service>.yml`) that runs `go vet`, `golangci-lint`, `go test ./...`, and `go build ./...`. Run the matching service's `Makefile` target (or the root `Makefile` aggregate `make build && make test && make lint`) before pushing.

If your branch touches `proto/`, also run `make proto` to regenerate the committed stubs in `proto/gen/go/`.

### 15.3 Tracker hygiene

Every redesign PR (REDESIGN-001 phases) must, in the same PR or in a `chore/...` follow-up:

- tick the row in `.claude/plans/2026-06-26-single-tenant-redesign.md` Progress dashboard
- prepend a row in `status.md`
- move the entry out of `status-tracker.md`'s OPEN list and into the shipped table

Pattern proven through Phases 4.1–4.6: bundling the tracker commit into the feature branch (rather than a separate chore branch) keeps the documentation and the diff that motivates it in lock-step.

---

> **Last updated:** see Git log.
> **Questions?** Open an issue with the label `architecture`.
> **This file is the source of truth for rules. Service detail lives in `docs/`.**
