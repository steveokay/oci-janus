# CLAUDE.md — OCI-Compliant Docker Registry Platform

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
7. [Authentication & Security](#7-authentication--security) — rules only; mechanics in [`docs/AUTH.md`](docs/AUTH.md)
8. [Storage Layer](#8-storage-layer)
9. [Multi-Tenancy](#9-multi-tenancy)
10. [Observability](#10-observability) — rules only; mechanics in [`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md)
11. [Database Conventions](#11-database-conventions) — rules only; mechanics in [`docs/DATABASE.md`](docs/DATABASE.md)
12. [gRPC Conventions](#12-grpc-conventions) — rules only; mechanics in [`docs/GRPC-CONVENTIONS.md`](docs/GRPC-CONVENTIONS.md)
13. [Security Hardening Rules](#13-security-hardening-rules) — pointer to [`docs/HARDENING-CHECKLIST.md`](docs/HARDENING-CHECKLIST.md)
14. [Decision Log](#14-decision-log) — pointer to [`docs/adr/`](docs/adr/)
15. [Workflow Gates](#15-workflow-gates)

External references:
- [`docs/SERVICES.md`](docs/SERVICES.md) — per-service detail (endpoints, gRPC, schemas, env vars)
- [`docs/AUTH.md`](docs/AUTH.md) — auth mechanics (mTLS hot reload, JWT key ring, peer-CN allowlist, API-key cache, Bearer dispatch)
- [`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md) — OTEL env vars, metrics catalogue, log-field contract
- [`docs/DATABASE.md`](docs/DATABASE.md) — pgx pool config, DSN format, migration mechanics, read-replica routing
- [`docs/GRPC-CONVENTIONS.md`](docs/GRPC-CONVENTIONS.md) — proto layout, server + client interceptor chains
- [`docs/EVENTS.md`](docs/EVENTS.md) — RabbitMQ routing keys + payloads
- [`docs/HARDENING-CHECKLIST.md`](docs/HARDENING-CHECKLIST.md) — per-service security hardening checklist
- [`docs/adr/`](docs/adr/) — Architecture Decision Records (one per decision, indexed in `docs/adr/README.md`)
- [`docs/TESTING.md`](docs/TESTING.md) — coverage targets, integration tests, OCI conformance
- [`docs/CI-CD.md`](docs/CI-CD.md) — pipeline stages, Docker build rules, versioning
- [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) — Compose + Helm chart layout
- `security.md` — SEC-NNN hardening items (status + resolution notes)
- `status-tracker.md` — currently open remediation + security items (lean by design)
- `status.md` — completed-work log + resolution notes (items move here once cleared from `status-tracker.md`)

---

## 1. Project Overview

A production-grade OCI-compliant Docker registry platform built in Go. Equivalent in feature scope to Docker Hub / Nexus / AWS ECR, designed primarily for self-hosted deployment.

### Deployment mode

The platform ships in two postures controlled by `DEPLOYMENT_MODE`:

- **`single` (default, OSS posture)** — self-hosted single-tenant. One bootstrap tenant is provisioned via the `registry-auth bootstrap` CLI; FE chrome hides tenant switcher, plan/billing, custom domains, and tenant-create flows. `services/tenant.CreateTenant` returns `FAILED_PRECONDITION` when a second tenant insert is attempted. (REDESIGN-001 Decision #25.)
- **`multi`** — preserves the SaaS posture for operators who want multi-tenant capability. Same proto contracts; FE re-exposes the multi-tenant surfaces.

Storage schemas keep `tenant_id` columns in both modes — single mode just populates them with the bootstrap tenant id.

### Core Capabilities

- Full OCI Distribution Spec v1.1 (push, pull, delete, list, referrers).
- **Auth:** JWT (RS256, multi-key ring + JWKS rotation) + API keys; mTLS everywhere between services with hot reload and per-server peer-CN allowlist. See §7 + [`docs/AUTH.md`](docs/AUTH.md).
- **SSO:** global OAuth 2.0 + PKCE (Google / GitHub / Microsoft / generic OIDC) and SAML 2.0 SP with auto-provisioning + AES-256-GCM-encrypted secrets. See [`docs/SAML.md`](docs/SAML.md).
- **Storage:** pluggable — MinIO / S3 / GCS / Azure / filesystem.
- **Scanner:** pluggable external-process JSON-RPC interface + per-tenant scan policies + SPDX SBOM & PDF compliance reports.
- **Signing:** Cosign (Sigstore) and Notary v2. See [`docs/SIGNING.md`](docs/SIGNING.md).
- **Pull-through cache** for upstream registries with digest verification.
- **RBAC** at org / repo level (owner / admin / writer / reader); typed `users.is_global_admin` primitive (Decision #26).
- **Webhooks** with retries + HMAC signing.
- **Tamper-evident audit trail:** Postgres FORCE RLS + low-privilege `registry_audit_app` role + per-tenant sha-256 hash-chain so a compromised audit service cannot rewrite or fork the chain (Phase 6.12 + REM-022; see §10).
- **Observability:** OpenTelemetry → Jaeger / Grafana Tempo / Datadog.

### Language & Runtime

| Concern | Choice |
|---|---|
| Backend language | Go 1.23+ (toolchain `go 1.25.11` across all modules) |
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
| 2 | `registry-auth` | JWT issuance (multi-key RS256 ring), API keys (Argon2 verify + 60s Redis cache), RBAC permission checks, global SSO (OAuth + SAML) | Postgres (auth schema — incl. `global_sso_config`, `auth_login_sessions`, `users.sso_subject`, `users.is_global_admin`, `service_accounts`) | RS256 300s TTL, JTI revocation in Redis fail-closed, principal-revocation `revoke:user:<id>`; multi-key kid-stamped JWTs (`JWT_KEY_RING_PATH`); PKCE S256 OAuth; SAML SP via `crewjam/saml` with `SSO_SAML_TRUST_EMAIL` flag |
| 3 | `registry-core` | OCI Distribution Spec v1.1 — `/v2/` API | — | Streams blobs, checkAccess on every handler; tag immutability preflight on `PutManifest` rejects re-pushes with `400 MANIFEST_INVALID` when `repositories.immutable_tags=true` OR `tags.immutable=true` |
| 4 | `registry-storage` | Pluggable blob storage abstraction | Object store backend | MinIO/S3/GCS/Azure/filesystem |
| 5 | `registry-metadata` | Source of truth for repos/tags/manifests/scans/SBOMs | Postgres (metadata schema) | gRPC-only access; Redis cache; read-replica routing; per-tag SBOM columns |
| 6 | `registry-proxy` | Pull-through cache for upstream registries | Postgres (proxy schema) | Upstream creds AES-256-GCM; `store.queued` retry |
| 7 | `registry-scanner` | Vulnerability scan orchestration + plugin host + scan policies + compliance reports | Postgres (scanner schema — `scan_policies`, `compliance_reports`) | External-process JSON-RPC; checksum-validated binary; async report worker with `FOR UPDATE SKIP LOCKED`; SPDX JSON 2.3 + hand-crafted PDF renderer |
| 8 | `registry-signer` | Cosign + Notary v2 signing/verification | Postgres (signatures table) | Vault dev mode locally; KMS in prod |
| 9 | `registry-webhook` | Reliable webhook delivery with retries + HMAC + delivery payload retrieval | Postgres (webhook schema) | SSRF block-list; HTTPS-only; `GetDelivery` for FE inspection |
| 10 | `registry-audit` | Immutable audit log + analytics + notifications | Postgres (audit schema) | `FORCE ROW LEVEL SECURITY`, `registry_audit_app` role; PG14 `date_bin` time-series; email notification transport (Resend/SMTP) + per-user delivery log (FUT-019 Phase 3, KEK `NOTIFY_EMAIL_KEY_HEX`) |
| 11 | `registry-gc` | Mark-sweep garbage collection + GC status visibility | Postgres (`gc_runs` table) | `pg_try_advisory_lock`; FNV-64a key per tenant; async `RunNow` queues a row + drains via `FOR UPDATE SKIP LOCKED` |
| 12 | `registry-tenant` | Tenant CRUD (single-mode rejects 2nd insert; multi-mode allows full CRUD) + `deployment_metadata` source for `bootstrap_tenant_id` | Postgres (tenant schema — `tenants.slug`, `deployment_metadata`) | `services/tenant.CreateTenant` returns `FAILED_PRECONDITION` in single mode; `GetDeploymentMetadata` RPC feeds every other service's SingleTenantInjector (REDESIGN-001 Phase 3.4). Custom domain CRUD removed (RM-001) |
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

> **Mechanics + implementation detail** live in [`docs/AUTH.md`](docs/AUTH.md)
> (mTLS hot reload, peer-CN allowlist, JWT key ring + JWKS, API-key
> Argon2 cache, HTTP Bearer dispatch). This section holds the rules
> only.

### mTLS between services

Every gRPC client/server pair uses mutual TLS.

- Every gRPC server sets `tls.RequireAndVerifyClientCert`.
- Client cert CN must match the expected service name — enforced via
  `MTLS_PEER_CN_ALLOWLIST` (per-server opt-in; see `docs/AUTH.md`).
- Every outbound dial uses `loader.BaseConfig.MTLSClientCreds(serverName)`
  for serverName pinning — no per-service helpers (SEC-038/039).
- CA cert loaded from `MTLS_CA_CERT_PATH` env var. No defaults.
- Certificate validity: maximum 90 days.
- Cert key file permissions: `chmod 600` (SEC-024).
- Dev: `make dev-certs` (cfssl); prod: cert-manager + internal CA.
- Hot reload via `ReloadingServerTLSConfig` / `ReloadingClientTLSConfig`
  — cert-manager renewals pick up at the next handshake without a
  restart. **Insecure dev fallback** (`insecure.NewCredentials()`) is
  rejected at startup when `OTEL_ENVIRONMENT=production`.

### JWT validation

- `services/auth` signs RS256 tokens from a multi-key ring
  (`JWT_KEY_RING_PATH`); JWKS at `/.well-known/jwks.json`. Mixing the
  ring with the legacy single-key trio is rejected at startup.
- Every gRPC server validates Bearer tokens via the
  `registry-auth.ValidateToken` gRPC call.
- **Fail-closed on `registry-auth` unreachable** — deny all, log,
  increment metric. Same posture applies to the principal-revocation
  Redis check (`revoke:user:<id>`) — Redis unreachable triggers a deny,
  not a silent allow.
- API-key Argon2 verify is Redis-cached (60s TTL); HIT path still
  re-loads the live DB row + re-runs every state gate, so a stale
  cache cannot outlive a revocation. Redis-down on the API-key cache
  is fail-**open** (cache is an optimisation, not a security boundary).

### HTTP Bearer dispatch — JWT and API-key forms (FUT-006)

`registry-auth`'s `requireAuth` HTTP helper accepts two Bearer forms,
discriminated by the literal `key.` prefix:

- `Bearer <RS256 jwt>` — browsers, FE clients, all authenticated routes.
- `Bearer key.<uuid>.<64-hex-secret>` — CI bots, self-introspection
  routes only. Synthesised claims have **empty `Roles`**, so admin-only
  handlers cleanly return 403 (not 401).

Full per-route contract: [`docs/SERVICES.md` §2](docs/SERVICES.md#2-registry-auth).
Dispatch mechanics: [`docs/AUTH.md`](docs/AUTH.md).

### Environment variables — security rules

- **Never** commit `.env` files. Only `.env.example` with placeholder values.
- All secrets (DB passwords, JWT keys, API credentials) must come from environment variables or a secrets manager.
- In production K8s: use Kubernetes Secrets (base64) mounted as env vars, or External Secrets Operator pointing to Vault/AWS Secrets Manager.
- Every service must fail to start if a required secret env var is empty or missing. Use a `config.Validate()` call in `main.go` before any server starts.
- Never log environment variable values at startup, even at DEBUG level.

### Input validation

All user-supplied strings validated against allowlists at the handler layer:

- Repository name: `^[a-z0-9]+([._-][a-z0-9]+)*$`, max 128 chars.
- Tag name: `^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`.
- Digest: `^sha256:[a-f0-9]{64}$`.
- Username: `^[a-zA-Z0-9_-]{3,64}$`.
- Org name: `^[a-z0-9-]{2,64}$`.
- Never pass unvalidated strings to SQL (parameterised queries only — see §11).
- Never pass unvalidated strings to shell commands (no `exec.Command` with user input).

### Client IP trust (SEC-009)

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

## 9. Multi-Tenancy

### Deployment modes

The platform supports two `DEPLOYMENT_MODE` values (REDESIGN-001 Decision #25):

- **`single` (default)** — self-hosted OSS posture. One bootstrap tenant is provisioned by the `registry-auth bootstrap` CLI; its id is recorded in `tenant.deployment_metadata` under the `bootstrap_tenant_id` key. Every gRPC server wires `libs/middleware/grpc.SingleTenantInjector` to inject `bootstrap_tenant_id` into requests missing tenant metadata and to reject mismatched tenant ids with `InvalidArgument`. `services/tenant.CreateTenant` returns `FAILED_PRECONDITION` when a second tenant insert is attempted. Custom domain CRUD has been removed (REDESIGN-001 RM-001).
- **`multi`** — preserves the SaaS posture. Tenant create/delete + tenant switcher + plan badge UI are re-exposed; the single-tenant guards are bypassed.

The wire format and schema are mode-agnostic — `tenant_id UUID NOT NULL` columns persist in both modes. Single mode just populates them with the bootstrap tenant id.

### Tenant Isolation

- All database rows include `tenant_id UUID NOT NULL`.
- All queries in `registry-metadata` must filter by `tenant_id` — never query across tenants.
- Storage keys are prefixed with `tenant_id` (see [`docs/SERVICES.md` §4 storage key layout](docs/SERVICES.md#4-registry-storage)).
- RabbitMQ messages include `tenant_id` in payload and as a message header.
- `libs/middleware/grpc.SingleTenantInjector` enforces the single-mode invariant at the interceptor layer (REDESIGN-001 Phase 3.4).

### Row-Level Security (RLS)

> **Status (Phase 7.1):** RLS as a second layer of defence remains *partial*. The `audit_events` table has `FORCE ROW LEVEL SECURITY` + the `registry_audit_app` low-privilege role (Decision #15). Other tables rely on application-layer `tenant_id` filtering + the single-tenant injector for now. Universal RLS coverage was deferred per Phase 0 D4 decision (Top-5 #1 remains open) and is tracked separately.

When RLS is enabled on a table the pattern is:

```sql
ALTER TABLE repositories ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON repositories
  USING (tenant_id = current_setting('app.tenant_id')::uuid);
```

Application sets `SET LOCAL app.tenant_id = '<id>'` in each transaction. Treat this section as the target state for future RLS rollouts; current truth is in the migrations.

---

## 10. Observability

> **Mechanics + implementation detail** live in
> [`docs/OBSERVABILITY.md`](docs/OBSERVABILITY.md) (OTEL env vars,
> Bootstrap contract, metrics catalogue, otel-collector wiring,
> structured-log field contract). This section holds the rules only.

### Rules

- All services instrument with OpenTelemetry via `libs/observability/otel.Bootstrap`. Call in `main.go` **before** starting any server; always call the returned `shutdown` on process exit or spans/metrics are lost.
- `OTEL_EXPORTER` selects the backend: `jaeger` | `tempo` | `datadog` | `stdout`.
- Every service exposes Prometheus metrics at `GET /metrics` on a **dedicated port `:9090`** (SEC-025) — separated from the business port so NetworkPolicy can grant Prometheus access without exposing the OCI API.
- The full metric catalogue lives in `libs/observability/metrics/metrics.go`. A spec-lint rule enforces that every metric documented in `docs/OBSERVABILITY.md` is declared there — adding a new metric requires updating both.
- Logger: `log/slog` (JSON in production, text in dev; `LOG_LEVEL=debug|info|warn|error`). Every entry must include `trace_id`, `span_id`, `tenant_id` (where available), `service`.
- **Never log:** passwords, tokens, API keys, private key material, full request bodies.

### Audit Trail (security-critical rules)

- `services/audit` consumes typed events from RabbitMQ (`registry.events` topic exchange) and persists them to `audit_events` via the `eventconsumer` package. Every event type registered in `libs/rabbitmq/events` must either map to a row in `audit_events` (via a case in `mapEvent`) OR carry an explicit `// audit: skip` annotation in `events.go`. A spec-lint rule enforces this invariant.
- Storage posture (Decision #15): `audit_events` has `FORCE ROW LEVEL SECURITY` so even the table owner cannot bypass the policy; the runtime role `registry_audit_app` is INSERT-only on `audit_events` (no UPDATE, no DELETE). The pgx connection pool authenticates as that role.
- **Hash chain** (Decision #30, REDESIGN-001 Phase 6.12): each row carries `prev_hash` + `row_hash` (`sha256(prev_hash || canonical_row_bytes)`) and a `chain_seq BIGINT` populated via a per-table sequence (REM-022 replaced the original `GENERATED ALWAYS AS IDENTITY` form to work on the partitioned parent). Per-tenant chain serialised by `pg_advisory_xact_lock(tenant_id)`. The tip is derived from `audit_events` itself (`SELECT row_hash FROM audit_events WHERE tenant_id = $1 ORDER BY chain_seq DESC LIMIT 1`) so the runtime role cannot rewrite it. `Repository.VerifyChain(ctx, tenantID)` walks the linked list and returns a `ChainVerification{FirstBadID, FirstBadAt, Unverifiable}` — the first tampered/forked/orphaned row (or `uuid.Nil` when intact) plus a count of rows that predate the chain migration (backfilled with the `0x00` sentinel row_hash) and therefore lie outside the tamper-evidence guarantee (SEC-051).

---

## 11. Database Conventions

> **Mechanics + implementation detail** live in
> [`docs/DATABASE.md`](docs/DATABASE.md) (pgx pool config, DSN format,
> migration mechanics, read-replica routing). This section holds the
> rules only.

- **ORM: none.** Use `pgx/v5` directly with `pgxpool`. Raw SQL only. All queries parameterised — never use `fmt.Sprintf` to build SQL.
- **Migrations:** `pressly/goose` with SQL migrations embedded via `embed.FS`. Naming: `YYYYMMDDHHMMSS_<description>.sql`. Never drop a column in a single migration — add a new column and migrate data in a separate step. Every migration must have a down migration.
- **Per-service databases:** only `registry-metadata` holds metadata; `registry-auth`, `registry-tenant`, `registry-proxy`, `registry-webhook`, `registry-audit` each own a separate database.
- **Connection pool** is built from `libs/config/loader.DBConfig.PoolConfig()` — see `docs/DATABASE.md` §2 for the concrete timeouts.
- **Context + transactions:** every query uses the request context for cancellation. Transactions always use `defer tx.Rollback(ctx)` and only commit explicitly on success.
- **DSN format:** `postgres://…?sslmode=require`. `sslmode=disable` is rejected at startup (SEC-022); `sslmode=prefer` is permitted only in local dev.
- **Connection-pool exhaustion** is mapped to `codes.ResourceExhausted` via `libs/errors/codes.MapDBError` (SEC-006).
- **Read replicas (REM-008):** `DB_DSN_REPLICA` opts a service into a read pool routed by `repository.reader()`. When unset, `reader()` returns the primary pool as a passthrough.

---

## 12. gRPC Conventions

> **Mechanics + implementation detail** live in
> [`docs/GRPC-CONVENTIONS.md`](docs/GRPC-CONVENTIONS.md) (proto file
> layout, full package-naming convention, ordered server/client
> interceptor chains). This section holds the rules only.

- **Proto packages:** `registry.<service>.v1`; Go package option `github.com/steveokay/oci-janus/proto/gen/go/<service>/v1;<service>v1`. Fields are `snake_case`; timestamps use `google.protobuf.Timestamp`; UUIDs are `string`.
- **Pagination:** always `page_token` (string) + `page_size` (int32) — never offset.
- **Errors:** RPCs return `google.rpc.Status` (import `google/rpc/status.proto`).
- **Breaking changes:** never modify existing field numbers — add new fields only. The `breaking` CI job enforces this against the previous main commit.
- **Generated stubs:** `proto/gen/go/**` is **committed**, not gitignored. Regenerate with `buf generate` from `proto/`.
- **Server interceptors** applied via `libs/middleware/grpc.ServerInterceptors()` — the ordered chain (Recovery → RequestID → mTLS peer verify → Auth → Tenant → OTEL → logging → metrics → optional REM-007 cache) is documented in `docs/GRPC-CONVENTIONS.md` §2. Do not add interceptors ad-hoc in a service's `main.go`; extend the shared builder.
- **Client interceptors** via the same package attach mTLS creds, propagate OTEL trace context, inject deadlines, and retry (3 attempts, exponential backoff, only on `UNAVAILABLE` and `DEADLINE_EXCEEDED`).
- Use `grpc.NewClient` (not the deprecated `grpc.Dial`) with a timeout context — never silently hang. Trigger `conn.Connect()` at startup so the first inbound request does not stall during the TLS/HTTP-2 handshake.

---

## 13. Security Hardening Rules

The per-service hardening checklist (Go code rules, HTTP headers,
dependency hygiene, secret handling) lives in
[`docs/HARDENING-CHECKLIST.md`](docs/HARDENING-CHECKLIST.md). Every
service must satisfy every box or document the exception with a
SEC-NNN reference.

Numbered SEC items (SEC-001..SEC-036 and beyond) and their resolution
notes live in [`security.md`](security.md).

---

## 14. Decision Log

Decisions #1–#30 each have a dedicated ADR in [`docs/adr/`](docs/adr/)
(ADR-0001..ADR-0030, indexed in [`docs/adr/README.md`](docs/adr/README.md)).
Read the ADR for the full rationale + alternatives considered; the
files are the source of truth for *why* a decision was made.

When adding a new decision: write `docs/adr/00NN-<slug>.md`, link it
from `docs/adr/README.md`, and reference the ADR number from any rule
in this file that depends on it. Do not re-add the summary table here —
the ADR files are durable, this file should stay slim.

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

The `pre{lint,typecheck,test}` npm hooks regenerate the gitignored `routeTree.gen.ts` from `@tanstack/router-generator` — running the 4 commands above closes the "green locally, red in CI" gap surfaced by PR #152 (2026-06-28).

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
