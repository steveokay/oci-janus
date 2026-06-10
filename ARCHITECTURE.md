# OCI-Janus — System Architecture

A production-grade, multi-tenant OCI-compliant Docker registry platform built in Go.
Equivalent in scope to Docker Hub, AWS ECR, or Nexus — designed for self-hosted deployment.

---

## Table of Contents

1. [High-Level Architecture](#1-high-level-architecture)
2. [Service Map](#2-service-map)
3. [Request Flows](#3-request-flows)
   - [docker login](#31-docker-login-auth-flow)
   - [docker push](#32-docker-push-flow)
   - [docker pull](#33-docker-pull-flow)
   - [Vulnerability scan](#34-post-push-async-pipeline)
4. [Service Reference](#4-service-reference)
5. [Infrastructure Components](#5-infrastructure-components)
6. [Communication Patterns](#6-communication-patterns)
7. [Security Model](#7-security-model)
8. [Multi-Tenancy](#8-multi-tenancy)
9. [Storage Backends](#9-storage-backends)
10. [Monorepo Layout](#10-monorepo-layout)

---

## 1. High-Level Architecture

```
 ╔══════════════════════════════════════════════════════════════════════╗
 ║                         EXTERNAL CLIENTS                             ║
 ║   Docker CLI · CI/CD (GitHub Actions, GitLab) · Helm · skopeo        ║
 ╚════════════════════════════╤═════════════════════════════════════════╝
                              │ HTTPS / HTTP (dev)
                              │
 ╔════════════════════════════▼═════════════════════════════════════════╗
 ║                       registry-gateway                               ║
 ║  • TLS termination (Let's Encrypt + wildcard)                        ║
 ║  • Tenant resolution: Host header → X-Tenant-ID                      ║
 ║  • Rate limiting (Redis sliding window)                              ║
 ║  • Injects X-Request-ID                                              ║
 ║  Tech: Traefik v3                                                     ║
 ╚══════╤═══════════════════╤══════════════════════╤════════════════════╝
        │ /v2/*             │ /auth/*              │ /api/v1/*
        │                   │                      │
 ╔══════▼══════╗    ╔════════▼════════╗   ╔════════▼════════╗
 ║ registry-   ║    ║  registry-auth  ║   ║  registry-      ║
 ║   core      ║    ║                 ║   ║  tenant / proxy  ║
 ║ OCI API     ║    ║  JWT issuance   ║   ║  / signer / etc. ║
 ╚══════╤══════╝    ║  API key mgmt   ║   ╚═════════════════╝
        │           ║  Token validate ║
        │ gRPC/mTLS ╚═════════════════╝
        │
 ╔══════▼═══════════════════════════════════════════╗
 ║              Internal Service Mesh (gRPC + mTLS) ║
 ║                                                  ║
 ║  ┌─────────────────┐     ┌─────────────────────┐ ║
 ║  │ registry-       │     │ registry-storage    │ ║
 ║  │ metadata        │     │                     │ ║
 ║  │                 │     │ Driver abstraction:  │ ║
 ║  │ PostgreSQL      │     │  MinIO / S3 / GCS   │ ║
 ║  │ Repos, tags,    │     │  Azure / Filesystem  │ ║
 ║  │ manifests,      │     │                     │ ║
 ║  │ scan results,   │     └─────────────────────┘ ║
 ║  │ quota           │                             ║
 ║  └─────────────────┘                             ║
 ╚══════════════════════════════════════════════════╝
        │
        │ Async (RabbitMQ topic exchange)
        │
 ╔══════▼═══════════════════════════════════════════╗
 ║            Async Worker Services                  ║
 ║                                                  ║
 ║  registry-scanner   registry-webhook              ║
 ║  registry-audit     registry-gc                   ║
 ║  registry-signer                                  ║
 ╚══════════════════════════════════════════════════╝
```

---

## 2. Service Map

```
                            ┌──────────────────────────────────────────┐
                            │            registry-gateway              │
                            │  (single ingress, TLS, tenant routing)   │
                            └──────┬──────────────┬────────────────────┘
                                   │              │
                     ┌─────────────▼──┐    ┌──────▼──────────┐
                     │ registry-core  │    │  registry-auth  │
                     │ OCI /v2/ API   │    │  /auth/token    │
                     └───┬────────────┘    └────────┬────────┘
                         │ gRPC (mTLS)               │ gRPC (mTLS)
         ┌───────────────┼───────────────┐           │
         │               │               │           │
 ┌───────▼──────┐ ┌──────▼──────┐ ┌─────▼──────────▼──┐
 │ registry-    │ │ registry-   │ │   registry-storage  │
 │ metadata     │ │ proxy       │ │   (blob I/O)        │
 │ (PostgreSQL) │ │ (cache)     │ │   MinIO/S3/GCS/     │
 └──────────────┘ └─────────────┘ │   Azure/Filesystem  │
                                   └─────────────────────┘

 Async consumers (RabbitMQ):
 ┌──────────────────────────────────────────────────────────────────┐
 │  push.completed  ──► registry-scanner  (vulnerability scan)      │
 │                  ──► registry-audit    (immutable audit log)      │
 │                  ──► registry-webhook  (external notifications)   │
 │                                                                  │
 │  scan.completed  ──► registry-metadata (update scan status)      │
 │                  ──► registry-webhook                            │
 │                                                                  │
 │  gc.run.started  ──► registry-storage  (delete orphaned blobs)   │
 └──────────────────────────────────────────────────────────────────┘

 Standalone services:
 ┌──────────────────────────────────────────────────────────────────┐
 │  registry-signer   Image signing (Cosign / Notary v2)            │
 │  registry-tenant   Tenant lifecycle + custom domain ACME         │
 │  registry-gc       Nightly garbage collection worker             │
 └──────────────────────────────────────────────────────────────────┘
```

---

## 3. Request Flows

### 3.1 `docker login` Auth Flow

```
Docker CLI                  registry-gateway          registry-auth
    │                              │                        │
    │  GET /v2/                    │                        │
    │─────────────────────────────►│                        │
    │                              │  forward (no token)    │
    │                              │───────────────────────►│
    │◄─────────────────────────────│────────────────────────│
    │  401 WWW-Authenticate:       │                        │
    │  Bearer realm="…/auth/token" │                        │
    │                              │                        │
    │  GET /auth/token             │                        │
    │    ?service=registry-core    │                        │
    │    Authorization: Basic      │                        │
    │─────────────────────────────►│                        │
    │                              │  forward Basic creds   │
    │                              │───────────────────────►│
    │                              │        validate user   │
    │                              │        argon2id hash   │
    │                              │        build JWT       │
    │◄─────────────────────────────│────────────────────────│
    │  200 { "token": "eyJ…" }     │                        │
    │                              │                        │
    │  GET /v2/ with Bearer token  │                        │
    │─────────────────────────────►│                        │
    │                              │  ValidateToken (gRPC)  │
    │                              │───────────────────────►│
    │                              │◄───────────────────────│
    │                              │  valid: true           │
    │◄─────────────────────────────│                        │
    │  200 OK — Login Succeeded    │                        │
```

**JWT claims stored in Redis** (`jwt:valid:<token>` key, TTL = token remaining lifetime) so subsequent requests skip the gRPC round-trip to auth.

---

### 3.2 `docker push` Flow

```
Docker CLI         gateway     registry-core    registry-auth  registry-metadata  registry-storage  RabbitMQ
    │                 │               │                │                │                  │              │
    │ HEAD /v2/<name>/blobs/<digest>  │                │                │                  │              │
    │────────────────►│──────────────►│                │                │                  │              │
    │                 │               │  no/bad token  │                │                  │              │
    │◄────────────────│───────────────│                │                │                  │              │
    │ 401 + WWW-Auth  │               │                │                │                  │              │
    │                 │               │                │                │                  │              │
    │ GET /auth/token?scope=repository:<name>:push,pull                 │                  │              │
    │────────────────►│──────────────────────────────►│                 │                  │              │
    │◄────────────────│──────────────────────────────│                 │                  │              │
    │ JWT { access: [{push,pull}] }   │                │                │                  │              │
    │                 │               │                │                │                  │              │
    │ HEAD /v2/<name>/blobs/<digest>  │                │                │                  │              │
    │  Authorization: Bearer <jwt>    │                │                │                  │              │
    │────────────────►│──────────────►│                │                │                  │              │
    │                 │               │ ValidateToken   │                │                  │              │
    │                 │               │───────────────►│                │                  │              │
    │                 │               │◄───────────────│                │                  │              │
    │                 │               │ BlobExists(key) │                │                  │              │
    │                 │               │────────────────────────────────────────────────────►│              │
    │                 │               │◄───────────────────────────────────────────────────│              │
    │◄────────────────│───────────────│                │                │                  │              │
    │ 404 BLOB_UNKNOWN (not found)    │                │                │                  │              │
    │                 │               │                │                │                  │              │
    │ POST /v2/<name>/blobs/uploads/  │                │                │                  │              │
    │────────────────►│──────────────►│                │                │                  │              │
    │                 │               │ GetOrCreate repo│                │                  │              │
    │                 │               │────────────────────────────────►│                  │              │
    │                 │               │◄───────────────────────────────│                  │              │
    │◄────────────────│───────────────│                │                │                  │              │
    │ 202 Location: /uploads/<uuid>   │                │                │                  │              │
    │                 │               │                │                │                  │              │
    │ PATCH /uploads/<uuid> (data)    │                │                │                  │              │
    │────────────────►│──────────────►│                │                │                  │              │
    │                 │               │  stream chunk  │                │                  │              │
    │                 │               │────────────────────────────────────────────────────►│              │
    │                 │               │◄───────────────────────────────────────────────────│              │
    │◄────────────────│───────────────│                │                │                  │              │
    │ 202 Range: 0-<n>│               │                │                │                  │              │
    │                 │               │                │                │                  │              │
    │ PUT /uploads/<uuid>?digest=sha256:...            │                │                  │              │
    │────────────────►│──────────────►│                │                │                  │              │
    │                 │               │  CompleteUpload│                │                  │              │
    │                 │               │────────────────────────────────────────────────────►│              │
    │◄────────────────│───────────────│                │                │                  │              │
    │ 201 Created     │               │                │                │                  │              │
    │                 │               │                │                │                  │              │
    │ PUT /v2/<name>/manifests/<tag>  │                │                │                  │              │
    │────────────────►│──────────────►│                │                │                  │              │
    │                 │               │  PutManifest   │                │                  │              │
    │                 │               │────────────────────────────────►│                  │              │
    │                 │               │  LinkBlob      │                │                  │              │
    │                 │               │────────────────────────────────►│                  │              │
    │                 │               │  PutTag        │                │                  │              │
    │                 │               │────────────────────────────────►│                  │              │
    │◄────────────────│───────────────│                │                │                  │              │
    │ 201 Created     │               │                │                │                  │              │
    │                 │               │  push.completed event           │                  │              │
    │                 │               │──────────────────────────────────────────────────────────────────►│
```

**After the push** the `push.completed` RabbitMQ event fans out to the scanner, audit, and webhook workers (see §3.4).

---

### 3.3 `docker pull` Flow

```
Docker CLI         gateway      registry-core    registry-auth  registry-metadata  registry-storage
    │                 │                │                │                │                  │
    │ GET /v2/<name>/manifests/<tag>   │                │                │                  │
    │ Authorization: Bearer <jwt>      │                │                │                  │
    │────────────────►│───────────────►│                │                │                  │
    │                 │                │ ValidateToken   │                │                  │
    │                 │                │───────────────►│                │                  │
    │                 │                │◄───────────────│                │                  │
    │                 │                │ GetManifest (by tag or digest)  │                  │
    │                 │                │────────────────────────────────►│                  │
    │                 │                │◄───────────────────────────────│                  │
    │◄────────────────│────────────────│                │                │                  │
    │ 200 manifest JSON               │                │                │                  │
    │   Content-Type: application/vnd.oci…              │                │                  │
    │   Docker-Content-Digest: sha256:… │               │                │                  │
    │                 │                │                │                │                  │
    │ (for each layer in manifest)     │                │                │                  │
    │ GET /v2/<name>/blobs/<digest>    │                │                │                  │
    │────────────────►│───────────────►│                │                │                  │
    │                 │                │ GetBlob(key)   │                │                  │
    │                 │                │──────────────────────────────────────────────────►│
    │                 │                │ stream 256KiB chunks            │                  │
    │◄════════════════│════════════════│                │                │                  │
    │ 200 blob data   │                │                │                │                  │
```

**No presigned URLs** — all blob traffic is proxied through core. This ensures audit logging and rate limiting apply to every byte.

---

### 3.4 Post-Push Async Pipeline

```
registry-core publishes:
  push.completed {repo, tag, digest, pushed_by, size_bytes}
         │
         │  RabbitMQ topic exchange: registry.events
         │
    ┌────┴─────────────────────────────────┐
    │              Fan-out                  │
    │                                      │
    ▼                    ▼                  ▼
registry-scanner   registry-audit    registry-webhook
    │               (append-only      (HMAC-signed
    │                audit log)        HTTP delivery)
    │
    │ 1. Create scan record (pending)
    │ 2. Fetch manifest layers from storage
    │ 3. Invoke scanner plugin (Trivy/Grype/…)
    │ 4. Update scan result in metadata
    │ 5. Publish scan.completed
    │         │
    │    ┌────┴──────────────────┐
    │    │                       │
    ▼    ▼                       ▼
registry-metadata          registry-webhook
(update scan status,        (notify: scan done,
 block tag if policy         policy violation)
 violation)
```

---

### 3.5 Custom Domain Resolution

```
Request: https://registry.acme.com/v2/…

registry-gateway
    │
    │ 1. Extract host: registry.acme.com
    │ 2. Redis lookup: domain:registry.acme.com → tenant_id? (TTL 60s)
    │     cache miss ──► gRPC → registry-tenant
    │                            query DB
    │                            cache result (TTL 60s)
    │                            return tenant_id
    │ 3. Inject X-Tenant-ID: <uuid> header
    │ 4. Forward to registry-core
    │
    ▼
registry-core uses X-Tenant-ID for all DB/storage operations
    (all queries are scoped to that tenant_id)
```

---

## 4. Service Reference

### registry-gateway
**Role:** Single ingress point for all external traffic.

- Terminates TLS (Let's Encrypt wildcard cert for `*.registry.example.com`; per-tenant cert for custom domains via ACME)
- Resolves tenant from `Host` header (cached in Redis)
- Injects `X-Tenant-ID` and `X-Request-ID` headers
- Rate limits by tenant + IP (Redis sliding window)
- Routes: `/v2/*` → core, `/auth/*` → auth, `/api/v1/*` → internal services
- Hard-rejects HTTP (no redirect)
- Tech: **Traefik v3** (dynamic config, ACME built-in)

---

### registry-auth
**Role:** Identity and token service.

- Issues Docker Bearer tokens (JWT RS256, 5-minute TTL) scoped to specific repository actions
- Validates tokens on demand via gRPC (called by every other service)
- Manages user accounts and API keys (robot accounts for CI/CD)
- Argon2id password hashing, account lockout (5 attempts → 15 min lock)
- JWKS endpoint (`/.well-known/jwks.json`) for out-of-band token verification
- Token revocation via Redis (`jti` blocklist)

**JWT payload:**
```json
{
  "iss": "registry-auth",
  "sub": "<user_id>",
  "aud": "registry-core",
  "tenant_id": "<uuid>",
  "access": [
    { "type": "repository", "name": "myorg/myimage", "actions": ["push","pull"] }
  ],
  "exp": "<now+300s>",
  "jti": "<uuid>"
}
```

**Own database:** PostgreSQL (users, API keys, sessions — separate DB from metadata).

---

### registry-core
**Role:** OCI Distribution Spec v1.1 implementation. The surface Docker talks to.

- Handles all `/v2/` endpoints: push, pull, delete, list tags, blob uploads
- Authenticates every request (Bearer JWT or Basic API key) via auth gRPC
- Enforces tenant isolation: JWT `tenant_id` must match `X-Tenant-ID` from gateway
- Streams blobs directly to/from storage (never buffers in memory)
- Tracks upload sessions in Redis (UUID, offset, tenant, repo — 1h TTL)
- Checks quota before accepting uploads; rejects with 403 if exceeded
- Publishes `push.completed` event to RabbitMQ after manifest PUT
- Scoped token challenge: returns 401 with `WWW-Authenticate` on auth failure so Docker knows to re-request with the right scope

**Calls:** auth (validate), metadata (repos/tags/manifests), storage (blobs).

---

### registry-metadata
**Role:** Source of truth for all registry metadata.

- Owns the PostgreSQL schema (repositories, organizations, tags, manifests, blobs, scan results, quota)
- All other services query metadata via gRPC — no direct DB access elsewhere
- Enforces tenant isolation at the query layer (all queries filter by `tenant_id`)
- PostgreSQL Row-Level Security (RLS) as a second layer: `SET LOCAL app.tenant_id` per transaction
- Maintains blob deduplication (`blob_links` table: many repos can share one blob)
- Tracks quota usage per tenant; updated on every push/delete

**Schema highlights:**
```
tenants → organizations → repositories → manifests
                                       → tags
                                       → blob_links → blobs (deduplicated)
                       ← scan_results (per manifest)
```

---

### registry-storage
**Role:** Blob I/O abstraction. All object storage goes through here.

- Single gRPC service in front of the actual storage backend
- Drivers: `minio`, `s3`, `gcs`, `azure`, `filesystem` (dev only)
- Selected at startup via `STORAGE_DRIVER` env var
- Streaming gRPC: `PutBlob` and `GetBlob` stream in 256 KiB chunks
- Supports multipart upload for large layers (> MinIO/S3 part threshold)
- Storage key layout: `blobs/<tenant_id>/sha256/<first2>/<digest>`
- No presigned URLs — core proxies all traffic through this service

**Why a separate service?** Isolates storage credentials from all other services; enables backend swap without touching core.

---

### registry-proxy
**Role:** Pull-through proxy cache for upstream registries.

- Routes `<host>/cache/<upstream>/<image>:<tag>` to configured upstream registries
- Cache hit (not expired): serve from local storage
- Cache miss: fetch from upstream, stream to client, store locally in background goroutine (non-blocking)
- Per-tenant upstream registry configuration (URL, auth, TTL, enabled flag)
- Upstream credentials encrypted at rest (AES-256-GCM)
- SSRF-safe: validates upstream response Content-Type, caps response size (default 20 GiB/layer)
- Verifies `Content-Digest` from upstream before caching

---

### registry-scanner
**Role:** Vulnerability scan orchestration.

- Consumes `push.completed` events from RabbitMQ
- Manages a configurable worker pool (default: 4 concurrent scans)
- Loads scanner via plugin interface: Go plugin (`.so`) or external process (JSON-RPC over stdin/stdout)
- Plugin checksum verified before loading (SHA256 vs `SCANNER_PLUGIN_CHECKSUM` env var)
- Per-tenant scan policy: block pulls on CRITICAL/HIGH, allow-unscanned flag
- Updates scan results in metadata; publishes `scan.completed` event
- Dead-letter queue after 3 retries; 600s job timeout

**Plugin contract:**
```go
type Scanner interface {
    Name() string
    Version() string
    Scan(ctx context.Context, req ScanRequest) (*ScanResult, error)
}
```

---

### registry-signer
**Role:** Image signing and verification.

- Supports Cosign (Sigstore keyless + keyed) and Notary v2 (TUF)
- Cosign: signatures stored as OCI artifacts in core (standard Cosign behavior)
- Key backends: `env` (dev), `vault` (HashiCorp), `awskms`, `gcpkms`, `azurekms`
- Key material never leaves this service; signing is a remote operation for CI/CD
- Verification does not require this service — clients can use the public key directly
- Notary v2: TUF metadata stored in storage, root key ceremony documented in `infra/runbooks/`

---

### registry-webhook
**Role:** Reliable webhook delivery to external URLs.

- Consumes events: `image.pushed`, `image.deleted`, `scan.completed`, `scan.policy_blocked`, `image.signed`
- At-least-once delivery with exponential backoff: 5s → 30s → 5m → 30m → 2h (5 attempts)
- HMAC-SHA256 payload signature in `X-Registry-Signature` header
- SSRF protection: blocks private IP ranges (RFC 1918, loopback, 169.254.169.254)
- HTTPS-only endpoints; HTTP webhook URLs are rejected
- Dead-letter queue after exhausting retries; notifies tenant admin

---

### registry-audit
**Role:** Immutable audit trail.

- Writes audit events published by all other services
- Append-only: no UPDATE or DELETE on audit table (enforced by PostgreSQL policy)
- Records: login/logout/lockout, push/pull/delete, webhook triggers, policy violations, RBAC changes, tenant config changes
- Actor IP extracted from trusted proxy IP only (not raw X-Forwarded-For)
- 365-day default retention (configurable per tenant)
- Query API for compliance and incident response

---

### registry-gc
**Role:** Garbage collection of orphaned blobs and untagged manifests.

Runs nightly (configurable cron). Four modes: `dry-run`, `manifests`, `blobs`, `full`.

**Algorithm:**
```
Phase 1 — Mark (read-only):
  advisory lock on repository in metadata
  collect all live manifests (referenced by any tag)
  collect all live blobs (referenced by any live manifest)

Phase 2 — Sweep:
  for each blob in storage not in live set AND older than GC_BLOB_MIN_AGE (1h):
    delete from storage + metadata + emit gc.blob_deleted
  for each untagged manifest older than GC_MANIFEST_MIN_AGE (24h):
    delete manifest + emit gc.manifest_deleted

Phase 3 — Update quota:
  recompute storage_used per repository in metadata
```

**Safety:** Never deletes blobs younger than 1h (in-flight pushes write blobs before manifests). Always idempotent.

---

### registry-tenant
**Role:** Tenant lifecycle and custom domain management.

- CRUD for tenants (super-admin API, not exposed to end users)
- Custom domain flow: tenant submits domain → DNS TXT verification → Let's Encrypt cert → gateway routing update
- Per-tenant config: quota, scan policy, signing requirements, proxy cache enabled flag
- Provisions tenant isolation on creation: seed org in metadata, create storage prefix policy

---

## 5. Infrastructure Components

```
┌──────────────────────────────────────────────────────────────┐
│                    Infrastructure Layer                       │
│                                                              │
│  PostgreSQL 16      Redis 7         RabbitMQ 3.13            │
│  ┌──────────┐      ┌──────────┐    ┌────────────────────┐   │
│  │ auth DB  │      │ JWT cache│    │ registry.events    │   │
│  │ users    │      │ rate lim │    │ topic exchange     │   │
│  │ api_keys │      │ sessions │    │ quorum queues      │   │
│  ├──────────┤      │ domain   │    │ dead-letter (dlx.) │   │
│  │ metadata │      │ cache    │    └────────────────────┘   │
│  │ DB       │      │ upload   │                             │
│  │ repos    │      │ state    │    MinIO / S3 / GCS / Azure  │
│  │ tags     │      └──────────┘    ┌────────────────────┐   │
│  │ manifests│                      │ blobs/<tenant>/    │   │
│  │ blobs    │                      │ manifests/<tenant>/│   │
│  │ scan_res │                      │ uploads/<tenant>/  │   │
│  ├──────────┤                      │ tuf/<tenant>/      │   │
│  │ tenant   │                      └────────────────────┘   │
│  │ DB       │                                               │
│  └──────────┘                                               │
└──────────────────────────────────────────────────────────────┘
```

| Component | Purpose | Default (dev) |
|---|---|---|
| PostgreSQL 16 | Persistent metadata, auth, tenant data | `postgres:16` |
| Redis 7 | JWT cache, upload state, rate limiting, domain cache | `redis:7` |
| RabbitMQ 3.13 | Async event bus (quorum queues) | `rabbitmq:3.13-management` |
| MinIO | Object storage (dev; S3/GCS/Azure in prod) | `minio/minio` |
| Jaeger | Distributed tracing (dev) | `jaegertracing/all-in-one` |

---

## 6. Communication Patterns

### Synchronous: gRPC over mTLS

All service-to-service calls use gRPC with mutual TLS:

```
Service A                              Service B
   │                                       │
   │── ClientHello (TLS 1.3) ─────────────►│
   │◄─ ServerHello + cert (CN=registry-B) ─│
   │── client cert (CN=registry-A) ────────►│
   │   (both certs signed by internal CA)   │
   │── gRPC call over encrypted tunnel ────►│
```

- Dev: self-signed certs generated by `scripts/gen-dev-certs.sh` with SANs
- Prod: cert-manager + internal CA issuer; 90-day max validity; auto-rotation
- Every gRPC call has a 5-second deadline; retried on `UNAVAILABLE`/`DEADLINE_EXCEEDED` (max 3 attempts)

### Asynchronous: RabbitMQ topic exchange

```
Exchange: registry.events  (topic, durable)
Exchange: registry.dlx     (dead-letter, topic, durable)

Routing keys: push.completed, scan.completed, manifest.deleted,
              webhook.queued, webhook.delivered, gc.run.started, …

Message envelope:
{
  "id":          "uuid-v4",
  "type":        "push.completed",
  "tenant_id":   "…",
  "occurred_at": "2026-06-10T…",
  "version":     "1.0",
  "payload":     { … }
}
```

- Publishers use **confirm mode** — wait for broker ACK before returning
- Consumers use **manual ACK** — only ACK after successful processing
- Quorum queues only (no classic queues in production)
- After 3 NACKs: message routed to `dlx.<service>` dead-letter exchange

---

## 7. Security Model

### Defence in Depth

```
Layer 1: Network
  registry-gateway enforces TLS 1.2+, HSTS, rejects HTTP

Layer 2: Transport (service mesh)
  All gRPC connections use mTLS
  Peer certificate CN verified by server interceptor

Layer 3: Identity
  JWT RS256 tokens, 5-minute TTL, jti revocation list in Redis
  API keys: argon2id hash, shown once at creation

Layer 4: Authorisation
  Token access claims scoped to repository + actions
  RBAC enforced in core on every endpoint

Layer 5: Data isolation (multi-tenant)
  All DB queries filter by tenant_id
  PostgreSQL RLS as second layer (SET LOCAL app.tenant_id per transaction)
  Storage keys prefixed with tenant_id

Layer 6: Secrets
  No secrets in code or Docker images
  Env vars only; Kubernetes Secrets / External Secrets Operator in prod
  No static cloud credentials in production (IMDSv2 / Workload Identity)
```

### Token Flow

```
docker push  →  core challenges 401
             →  Docker fetches token from auth with scope=repository:<name>:push,pull
             →  auth validates credentials, builds JWT with access claims
             →  Docker sends Bearer token on every subsequent request
             →  core validates JWT via auth gRPC (cached in Redis for token lifetime)
             →  access claim checked per-endpoint before any operation
```

---

## 8. Multi-Tenancy

```
Tenant: acme-corp
  │
  ├── Custom domain: registry.acme-corp.com
  │     ↳ Verified via DNS TXT _registry-verify.registry.acme-corp.com
  │     ↳ Let's Encrypt cert auto-provisioned
  │     ↳ Gateway routes Host: registry.acme-corp.com → X-Tenant-ID: <uuid>
  │
  ├── Organizations (within tenant)
  │     ├── backend-team
  │     │     ├── repo: backend-team/api  (storage_quota: 50 GiB)
  │     │     └── repo: backend-team/worker
  │     └── frontend-team
  │           └── repo: frontend-team/webapp
  │
  ├── Users + API keys (in auth DB, scoped by tenant_id)
  │
  ├── Scan policy: block on HIGH, allow unscanned = false
  │
  └── Quota: 500 GiB total storage
```

Every row in every table carries `tenant_id`. Cross-tenant queries are architecturally impossible: the application sets `SET LOCAL app.tenant_id` on each transaction and PostgreSQL RLS blocks anything that doesn't match.

---

## 9. Storage Backends

All blob I/O is routed through `registry-storage`. No other service has storage credentials.

```
registry-core
    │
    │  gRPC PutBlob / GetBlob (streaming, 256 KiB chunks)
    ▼
registry-storage
    │
    │  STORAGE_DRIVER env var selects backend at startup
    │
    ├── minio    → MinIO / any S3-compatible endpoint
    ├── s3       → AWS S3 (IMDSv2 preferred, static keys fallback)
    ├── gcs      → Google Cloud Storage (Workload Identity preferred)
    ├── azure    → Azure Blob Storage (Managed Identity preferred)
    └── filesystem → Local disk (dev/test ONLY)
```

**Storage key layout:**
```
blobs/<tenant_id>/sha256/<first2hex>/<full_digest_hex>
manifests/<tenant_id>/<repo_encoded>/<reference>
uploads/<tenant_id>/<upload_uuid>/parts/<part_num>
tuf/<tenant_id>/…  (Notary v2 TUF metadata)
```

Blobs are **content-addressed** and **deduplicated across repositories**. Two repos in the same tenant sharing a base layer store one blob and two `blob_links` rows.

---

## 10. Monorepo Layout

```
oci-janus/
├── go.work                 ← Go workspace (links all modules for local dev)
├── go.work.sum
│
├── proto/                  ← All .proto files (source of truth)
│   ├── auth/v1/
│   ├── storage/v1/
│   ├── metadata/v1/
│   ├── …
│   └── gen/go/             ← Generated stubs (committed)
│
├── libs/                   ← Shared Go libraries (no business logic)
│   ├── auth/jwt/           ← JWT validation helpers
│   ├── auth/mtls/          ← mTLS config builders
│   ├── rabbitmq/           ← Publisher + consumer wrappers
│   ├── observability/otel/ ← OpenTelemetry bootstrap
│   ├── middleware/grpc/    ← Server/client interceptors
│   ├── crypto/argon2/      ← Password hashing
│   └── testutil/           ← Testcontainers helpers
│
├── services/
│   ├── gateway/            ← Traefik config (not a Go service)
│   ├── auth/               ← go.mod, cmd/, internal/
│   ├── core/               ← go.mod, cmd/, internal/
│   ├── storage/            ← go.mod, cmd/, internal/
│   ├── metadata/           ← go.mod, cmd/, internal/
│   ├── proxy/              ← go.mod, cmd/, internal/
│   ├── scanner/            ← go.mod, cmd/, internal/
│   ├── signer/             ← go.mod, cmd/, internal/
│   ├── webhook/            ← go.mod, cmd/, internal/
│   ├── audit/              ← go.mod, cmd/, internal/
│   ├── gc/                 ← go.mod, cmd/, internal/
│   └── tenant/             ← go.mod, cmd/, internal/
│
├── infra/
│   ├── docker-compose/     ← Local dev stack
│   ├── helm/               ← Kubernetes umbrella chart
│   └── runbooks/           ← Operational procedures
│
└── .github/workflows/      ← CI — path-filtered per service
    ← lint → test → security → build → conformance → integration → deploy
```

Each service has an identical internal layout:
```
<service>/
├── cmd/server/main.go      ← Entrypoint only; calls server.Run()
├── internal/
│   ├── config/             ← Viper env-var config
│   ├── server/             ← Wire dependencies, start gRPC + HTTP
│   ├── handler/            ← Request handling (gRPC or HTTP)
│   ├── service/            ← Business logic
│   └── repository/         ← All SQL (no SQL outside this package)
├── migrations/             ← Goose SQL migrations
├── Dockerfile
└── go.mod
```

---

## Key Design Decisions

| Decision | Rationale |
|---|---|
| gRPC for sync calls | Strong contracts, streaming, mTLS built-in, code generation |
| RabbitMQ for async | Lower ops cost than Kafka; quorum queues give durability |
| JWT RS256 + mTLS | Defence in depth — network layer + identity layer independent |
| No ORM (raw pgx) | Full query control; ORM abstraction leaks at scale |
| No presigned URLs | All blob traffic proxied → unified audit, rate limiting |
| Monorepo + go.work | Atomic cross-service changes; no version-bump dance for proto/libs |
| Distroless Docker images | No shell = no RCE via shell injection; minimal CVE surface |
| PostgreSQL RLS | Second layer of tenant isolation; application bug can't leak data |
| Pluggable scanner | Avoids vendor lock-in; supports Trivy, Grype, and commercial scanners |
