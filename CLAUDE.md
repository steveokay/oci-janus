# CLAUDE.md — OCI-Compliant Docker Registry Platform

> **Purpose:** This file is the canonical reference for AI-assisted development of the Docker Registry Platform.
> Every service, interface, security decision, and convention is defined here.
> When in doubt, re-read this file before writing any code. Never assume — ask.

---

## Table of Contents

1. [Project Overview](#1-project-overview)
2. [Repository Structure](#2-repository-structure)
3. [Architecture Overview](#3-architecture-overview)
4. [Service Catalogue](#4-service-catalogue)
5. [Shared Libraries Repo](#5-shared-libraries-repo)
6. [Communication Patterns](#6-communication-patterns)
7. [Authentication & Security](#7-authentication--security)
8. [Storage Layer](#8-storage-layer)
9. [Multi-Tenancy & Custom Domains](#9-multi-tenancy--custom-domains)
10. [Vulnerability Scanning](#10-vulnerability-scanning)
11. [Image Signing](#11-image-signing)
12. [Observability](#12-observability)
13. [Database Conventions](#13-database-conventions)
14. [RabbitMQ Event Contracts](#14-rabbitmq-event-contracts)
15. [gRPC Conventions](#15-grpc-conventions)
16. [Deployment](#16-deployment)
17. [Security Hardening Rules](#17-security-hardening-rules)
18. [Testing Requirements](#18-testing-requirements)
19. [CI/CD Pipeline](#19-cicd-pipeline)
20. [Decision Log](#20-decision-log)

---

## 1. Project Overview

A production-grade, multi-tenant OCI-compliant Docker registry platform built in Go.
It is equivalent in features to Docker Hub / Nexus / AWS ECR and is designed for self-hosted deployment.

### Core Capabilities

- Full OCI Distribution Spec v1.1 compliance (push, pull, delete, list)
- Multi-tenant with per-tenant custom domains
- JWT (RS256) + API key authentication; mTLS between all internal services
- Pluggable storage: MinIO, AWS S3, GCP Cloud Storage, Azure Blob
- Pluggable vulnerability scanner interface
- Image signing via Cosign (Sigstore) and Notary v2
- Pull-through proxy cache for upstream registries
- RBAC at org / repo / tag level
- Webhook delivery with retries
- Full audit trail
- Pluggable observability: OpenTelemetry → Jaeger, Grafana Tempo, or Datadog

### Language & Runtime

| Concern | Choice |
|---|---|
| Backend language | Go 1.23+ |
| Minimum Go version | 1.23 (use toolchain directive in go.mod) |
| Protobuf/gRPC codegen | `buf` CLI v1.x |
| Database | PostgreSQL 16 |
| Cache / Rate limiting | Redis 7 (Valkey acceptable) |
| Message broker | RabbitMQ 3.13 with Quorum Queues |
| Container runtime (dev) | Docker + Docker Compose v2 |

---

## 2. Repository Structure

All code lives in a **single monorepo** at `github.com/<org>/registry`. Go services each have their own `go.mod` (for isolated Docker builds) and are linked together via a root `go.work` file for local development.

```
github.com/<org>/registry/          # single git repo
├── go.work                         # Go workspace — ties all service modules together
├── go.work.sum
├── proto/                          # All .proto files + generated Go stubs (source of truth)
│   ├── auth/v1/auth.proto
│   ├── storage/v1/storage.proto
│   ├── metadata/v1/metadata.proto
│   ├── ...
│   ├── gen/go/                     # Generated stubs — committed, not gitignored
│   └── buf.yaml
├── libs/                           # Shared Go modules (auth, storage, observability, errors)
├── services/
│   ├── gateway/                    # API gateway / reverse proxy + TLS termination
│   ├── auth/                       # Token service: JWT issuance, API key management
│   ├── core/                       # OCI Distribution Spec implementation (push/pull)
│   ├── storage/                    # Storage abstraction service (MinIO/S3/GCS/Azure)
│   ├── metadata/                   # Repository/tag/manifest metadata (PostgreSQL)
│   ├── proxy/                      # Pull-through proxy cache
│   ├── scanner/                    # Vulnerability scan orchestration + plugin host
│   ├── signer/                     # Image signing: Cosign + Notary v2
│   ├── webhook/                    # Webhook delivery worker
│   ├── audit/                      # Audit log writer + query API
│   ├── gc/                         # Garbage collection worker
│   └── tenant/                     # Tenant + custom domain management
├── ui/                             # React/TypeScript frontend
├── infra/                          # Helm charts, Docker Compose, Terraform, runbooks
├── .github/workflows/              # CI — path-filtered jobs per service
├── Makefile                        # Top-level: make build-all, make test-all, make lint-all
└── .golangci.yml                   # Shared linter config for all Go services
```

### Go Workspace

`go.work` at the root links all service modules and `libs/` together:

```
go 1.23

use (
    ./libs
    ./services/auth
    ./services/core
    ./services/storage
    ./services/metadata
    ./services/proxy
    ./services/scanner
    ./services/signer
    ./services/webhook
    ./services/audit
    ./services/gc
    ./services/tenant
    ./services/gateway
)
```

- `go.work` is used for local development and CI. Each service `go.mod` remains self-contained for Docker builds (`COPY services/auth . && go build` works without the workspace).
- `go.work.sum` is committed alongside `go.work`.
- Never run `go build` for Docker inside the workspace context — use `GOFLAGS=-mod=mod` or build from the service directory directly.

### Per-Service Layout (Go services)

```
<service>/
├── cmd/
│   └── server/
│       └── main.go          # Entrypoint only — no business logic
├── internal/
│   ├── config/              # Viper-based config loading
│   ├── server/              # gRPC and/or HTTP server setup
│   ├── handler/             # gRPC handlers / HTTP handlers
│   ├── service/             # Business logic (pure functions where possible)
│   ├── repository/          # Database access (no raw SQL outside this package)
│   ├── middleware/           # Auth, logging, tracing middleware
│   └── testutil/            # Test helpers, fixtures, mocks
├── api/                     # OpenAPI specs or gRPC client wrappers (if needed)
├── migrations/              # SQL migration files (goose format)
├── Dockerfile
├── docker-compose.dev.yml
├── .env.example             # All env vars documented, no defaults for secrets
├── go.mod
├── go.sum
├── buf.gen.yaml             # Points to ../../proto for codegen
└── Makefile                 # make build, make test, make lint, make proto
```

---

## 3. Architecture Overview

```
                        ┌─────────────────────────────────────────┐
                        │           registry-gateway               │
                        │   (Nginx/Traefik + TLS termination)      │
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

        Async (RabbitMQ exchanges):
        registry-core ──push.completed──► registry-scanner
                      ──push.completed──► registry-audit
                      ──push.completed──► registry-webhook
        registry-scanner ──scan.done──► registry-metadata (update scan status)
        registry-scanner ──scan.done──► registry-webhook
        registry-gc ──gc.run──► registry-storage (delete blobs)
```

---

## 4. Service Catalogue

### 4.1 `registry-gateway`

**Purpose:** Single ingress point. TLS termination. Routes by `Host` header to resolve tenant. Injects `X-Tenant-ID` header downstream. Rate limiting. DDoS protection.

**Tech:** Traefik v3 (preferred for dynamic config + Let's Encrypt) or Nginx with Lua.

**Responsibilities:**
- Terminate TLS (Let's Encrypt via ACME for custom domains, wildcard cert for platform domain)
- Resolve tenant from `Host` header via lookup in `registry-tenant` (cached in Redis, TTL 60s)
- Inject `X-Tenant-ID` and `X-Request-ID` headers on all downstream requests
- Rate limit by tenant + IP (Redis-backed sliding window)
- Block requests with missing or malformed `Host` headers
- Forward `/v2/` prefix to `registry-core`
- Forward `/auth/` prefix to `registry-auth`
- Forward `/api/v1/` to relevant internal services

**Security:**
- TLS 1.2 minimum, TLS 1.3 preferred
- HSTS header on all responses
- Reject HTTP (no redirect to HTTPS — hard fail)
- Strip all `X-Forwarded-*` headers from clients before re-setting them internally
- Log all requests with tenant ID, method, path, status, latency (no auth tokens in logs)

---

### 4.2 `registry-auth`

**Purpose:** Docker token auth service. Issues JWT access tokens. Manages API keys. Validates credentials.

**Endpoints (HTTP, called by Docker clients and gateway):**

```
POST /auth/token          # Docker token endpoint (RFC 7235 flow)
POST /api/v1/users        # Create user
POST /api/v1/login        # Issue long-lived session token
POST /api/v1/apikeys      # Create API key (robot accounts)
DELETE /api/v1/apikeys/:id
GET  /api/v1/apikeys      # List API keys for current user
POST /api/v1/logout
GET  /.well-known/jwks.json  # Public key set for JWT verification
```

**JWT Structure:**
```json
{
  "iss": "registry-auth",
  "sub": "<user_id>",
  "aud": "registry-core",
  "exp": "<now + 300s>",
  "iat": "<now>",
  "jti": "<uuid>",
  "tenant_id": "<tenant_id>",
  "access": [
    {
      "type": "repository",
      "name": "myorg/myimage",
      "actions": ["push", "pull"]
    }
  ]
}
```

**Rules:**
- Sign with RS256. Private key loaded from environment (PEM, base64-encoded). Never hardcoded.
- Token TTL: 300 seconds (5 minutes). Non-configurable — Docker clients re-request automatically.
- API keys: stored as `argon2id` hash in PostgreSQL. Never stored in plaintext. Return raw key only once at creation.
- Enforce account lockout: 5 failed login attempts → lock for 15 minutes. Log lockout event to audit.
- `jti` (JWT ID) stored in Redis for token revocation. Check on every validation.
- Rotate signing key pair without downtime: support multiple public keys in JWKS, tag active key with `kid`.
- Password policy: minimum 12 characters, 1 uppercase, 1 lowercase, 1 number, 1 symbol. Enforce server-side — never rely on client.
- Rate limit: 10 failed auth attempts per IP per minute before returning 429.

**gRPC (internal, mTLS):**

```protobuf
service AuthService {
  rpc ValidateToken(ValidateTokenRequest) returns (ValidateTokenResponse);
  rpc ValidateAPIKey(ValidateAPIKeyRequest) returns (ValidateAPIKeyResponse);
  rpc GetUserPermissions(GetUserPermissionsRequest) returns (GetUserPermissionsResponse);
}
```

---

### 4.3 `registry-core`

**Purpose:** OCI Distribution Spec v1.1 implementation. The primary interface for Docker/OCI clients.

**Endpoints (all under `/v2/`):**

```
GET  /v2/                                           # Version check → 200 or 401
GET  /v2/<name>/tags/list                           # List tags
GET  /v2/<name>/manifests/<reference>               # Pull manifest (tag or digest)
PUT  /v2/<name>/manifests/<reference>               # Push manifest
DELETE /v2/<name>/manifests/<reference>             # Delete manifest
HEAD /v2/<name>/manifests/<reference>               # Manifest exists check
GET  /v2/<name>/blobs/<digest>                      # Pull blob
HEAD /v2/<name>/blobs/<digest>                      # Blob exists check
DELETE /v2/<name>/blobs/<digest>                    # Delete blob
POST /v2/<name>/blobs/uploads/                      # Initiate blob upload
GET  /v2/<name>/blobs/uploads/<uuid>                # Get upload status
PATCH /v2/<name>/blobs/uploads/<uuid>               # Chunked upload
PUT  /v2/<name>/blobs/uploads/<uuid>                # Complete upload
DELETE /v2/<name>/blobs/uploads/<uuid>              # Cancel upload
```

**Rules:**
- Every request must carry a valid Bearer token. Extract `tenant_id` from JWT. Validate via `registry-auth` gRPC.
- Enforce `X-Tenant-ID` from gateway matches `tenant_id` in JWT — reject mismatches with 403.
- Content-addressable blobs: SHA256 digest is the canonical key. Reject uploads where computed digest ≠ declared digest.
- Support both `Docker-Content-Digest` and OCI digest headers.
- Support manifest media types: `application/vnd.docker.distribution.manifest.v2+json`, `application/vnd.oci.image.manifest.v1+json`, `application/vnd.oci.image.index.v1+json` (multi-arch).
- Chunked uploads: store upload state (UUID, offset, tenant, repo) in Redis with 1-hour TTL.
- Never buffer a full blob in memory. Stream blobs directly to `registry-storage` via gRPC streaming.
- On successful manifest push: publish `push.completed` event to RabbitMQ (see §14).
- Enforce per-tenant storage quota (check before accepting upload, fail fast with 403 if exceeded).
- Return `Link` header for paginated tag lists (`?n=` and `?last=` params per spec).
- `name` in all routes = `<org>/<repo>`. Reject single-component names.

**gRPC calls made by this service:**
- `registry-auth`: ValidateToken, ValidateAPIKey
- `registry-metadata`: CreateTag, GetManifest, ListTags, DeleteTag
- `registry-storage`: PutBlob, GetBlob, StatBlob, DeleteBlob, InitiateUpload, AppendChunk, CompleteUpload

---

### 4.4 `registry-storage`

**Purpose:** Storage abstraction. All blob I/O goes through this service. Clients never touch storage directly.

**Storage backends (configured per deployment, not per tenant):**

| Backend | Driver name | Notes |
|---|---|---|
| MinIO | `minio` | Self-hosted S3-compatible |
| AWS S3 | `s3` | Native SDK, supports IMDSv2 |
| GCP Cloud Storage | `gcs` | ADC or service account JSON |
| Azure Blob Storage | `azure` | Managed identity or connection string |
| Local filesystem | `filesystem` | Dev/testing only — never production |

**Backend selection:** `STORAGE_DRIVER` environment variable. Must be explicitly set — no default.

**Driver interface (internal Go interface, all drivers must implement):**

```go
type Driver interface {
    // Blob operations
    PutBlob(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
    GetBlob(ctx context.Context, key string) (io.ReadCloser, int64, error)
    StatBlob(ctx context.Context, key string) (BlobInfo, error)
    DeleteBlob(ctx context.Context, key string) error
    BlobExists(ctx context.Context, key string) (bool, error)

    // Multipart (for large blobs)
    InitiateMultipart(ctx context.Context, key string) (uploadID string, error)
    UploadPart(ctx context.Context, key, uploadID string, partNum int, r io.Reader, size int64) (ETag string, error)
    CompleteMultipart(ctx context.Context, key, uploadID string, parts []CompletedPart) error
    AbortMultipart(ctx context.Context, key, uploadID string) error

    // Listing (for GC)
    ListBlobs(ctx context.Context, prefix string) ([]string, error)

    // Health
    Ping(ctx context.Context) error
}
```

**Storage key layout:**
```
blobs/<tenant_id>/sha256/<first2>/<digest>
manifests/<tenant_id>/<repo_encoded>/<reference>
uploads/<tenant_id>/<upload_uuid>/parts/<part_num>
```

**Security:**
- Credentials for cloud backends loaded from environment only. Never from config files committed to Git.
- For S3/GCS/Azure: use IAM roles / Workload Identity / Managed Identity where available. Avoid static credentials in production.
- Enable bucket versioning on S3/GCS (protects against accidental GC bugs).
- Server-side encryption: enforce SSE-S3 (S3), CMEK (GCS), SSE (Azure) — flag if backend does not support it.
- No presigned URLs exposed to end clients — all blob traffic proxied through `registry-core`.

**gRPC service definition (in `proto/`):**

```protobuf
service StorageService {
  rpc PutBlob(stream PutBlobRequest) returns (PutBlobResponse);
  rpc GetBlob(GetBlobRequest) returns (stream GetBlobResponse);
  rpc StatBlob(StatBlobRequest) returns (StatBlobResponse);
  rpc DeleteBlob(DeleteBlobRequest) returns (DeleteBlobResponse);
  rpc BlobExists(BlobExistsRequest) returns (BlobExistsResponse);
  rpc ListBlobs(ListBlobsRequest) returns (stream ListBlobsResponse);
  rpc InitiateMultipart(InitiateMultipartRequest) returns (InitiateMultipartResponse);
  rpc UploadPart(stream UploadPartRequest) returns (UploadPartResponse);
  rpc CompleteMultipart(CompleteMultipartRequest) returns (CompleteMultipartResponse);
  rpc AbortMultipart(AbortMultipartRequest) returns (AbortMultipartResponse);
}
```

---

### 4.5 `registry-metadata`

**Purpose:** Source of truth for all registry metadata: repositories, tags, manifests, blob references, scan status, quota usage.

**This service owns the PostgreSQL database.** All other services that need metadata go through this service's gRPC API — they do not connect to PostgreSQL directly.

**gRPC service:**

```protobuf
service MetadataService {
  // Repositories
  rpc CreateRepository(CreateRepositoryRequest) returns (Repository);
  rpc GetRepository(GetRepositoryRequest) returns (Repository);
  rpc ListRepositories(ListRepositoriesRequest) returns (stream Repository);
  rpc DeleteRepository(DeleteRepositoryRequest) returns (google.protobuf.Empty);
  rpc UpdateRepositoryQuota(UpdateRepositoryQuotaRequest) returns (Repository);

  // Tags
  rpc PutTag(PutTagRequest) returns (Tag);
  rpc GetTag(GetTagRequest) returns (Tag);
  rpc ListTags(ListTagsRequest) returns (stream Tag);
  rpc DeleteTag(DeleteTagRequest) returns (google.protobuf.Empty);

  // Manifests
  rpc PutManifest(PutManifestRequest) returns (Manifest);
  rpc GetManifest(GetManifestRequest) returns (Manifest);
  rpc DeleteManifest(DeleteManifestRequest) returns (google.protobuf.Empty);
  rpc ListUntaggedManifests(ListUntaggedManifestsRequest) returns (stream Manifest);

  // Blobs
  rpc LinkBlob(LinkBlobRequest) returns (google.protobuf.Empty);
  rpc UnlinkBlob(UnlinkBlobRequest) returns (google.protobuf.Empty);
  rpc ListOrphanedBlobs(ListOrphanedBlobsRequest) returns (stream BlobRef);

  // Quota
  rpc GetTenantQuotaUsage(GetTenantQuotaUsageRequest) returns (QuotaUsage);
  rpc IncrementTenantStorage(IncrementTenantStorageRequest) returns (google.protobuf.Empty);
  rpc DecrementTenantStorage(DecrementTenantStorageRequest) returns (google.protobuf.Empty);

  // Scan status
  rpc UpdateScanStatus(UpdateScanStatusRequest) returns (google.protobuf.Empty);
  rpc GetScanResult(GetScanResultRequest) returns (ScanResult);
}
```

**Database schema (canonical — migrations live in this repo):**

```sql
-- Tenants (owned by registry-tenant, replicated here as FK target)
CREATE TABLE tenants (
    id          UUID PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Organizations within a tenant
CREATE TABLE organizations (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(tenant_id, name)
);

-- Repositories
CREATE TABLE repositories (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id          UUID NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    tenant_id       UUID NOT NULL,
    name            TEXT NOT NULL,
    is_public       BOOLEAN NOT NULL DEFAULT false,
    storage_quota   BIGINT NOT NULL DEFAULT 10737418240, -- 10GB default
    storage_used    BIGINT NOT NULL DEFAULT 0,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(org_id, name)
);

-- Manifests
CREATE TABLE manifests (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id         UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    tenant_id       UUID NOT NULL,
    digest          TEXT NOT NULL,         -- sha256:...
    media_type      TEXT NOT NULL,
    raw_json        BYTEA NOT NULL,
    size_bytes      BIGINT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(repo_id, digest)
);

-- Tags
CREATE TABLE tags (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    repo_id         UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    tenant_id       UUID NOT NULL,
    name            TEXT NOT NULL,
    manifest_digest TEXT NOT NULL,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE(repo_id, name)
);

-- Blob references (deduplication across repos)
CREATE TABLE blobs (
    digest          TEXT PRIMARY KEY,      -- sha256:...
    size_bytes      BIGINT NOT NULL,
    storage_key     TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE blob_links (
    repo_id         UUID NOT NULL REFERENCES repositories(id) ON DELETE CASCADE,
    blob_digest     TEXT NOT NULL REFERENCES blobs(digest),
    PRIMARY KEY (repo_id, blob_digest)
);

-- Scan results
CREATE TABLE scan_results (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    manifest_digest TEXT NOT NULL,
    repo_id         UUID NOT NULL,
    tenant_id       UUID NOT NULL,
    scanner_name    TEXT NOT NULL,
    scanner_version TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN ('pending','running','complete','failed')),
    severity_counts JSONB NOT NULL DEFAULT '{}',
    findings        JSONB NOT NULL DEFAULT '[]',
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Indexes
CREATE INDEX idx_tags_repo_id ON tags(repo_id);
CREATE INDEX idx_manifests_repo_id ON manifests(repo_id);
CREATE INDEX idx_blob_links_digest ON blob_links(blob_digest);
CREATE INDEX idx_scan_results_manifest ON scan_results(manifest_digest);
CREATE INDEX idx_scan_results_tenant ON scan_results(tenant_id);
```

---

### 4.6 `registry-proxy`

**Purpose:** Pull-through proxy cache. Routes `docker pull <registry>/cache/<upstream-prefix>/<image>:<tag>` through to upstream registries, caching locally.

**Upstream registry config (stored in DB, per tenant):**

```go
type UpstreamRegistry struct {
    Name        string        // e.g. "dockerhub", "quay", "gcr"
    URL         string        // e.g. "https://registry-1.docker.io"
    AuthType    string        // "none" | "basic" | "token"
    Username    string        // stored encrypted in DB
    Password    string        // stored encrypted in DB (AES-256-GCM, key from KMS/env)
    TTL         time.Duration // how long to cache manifests
    Enabled     bool
}
```

**Cache flow:**
1. Check `registry-metadata` for cached manifest by digest/tag
2. Cache hit and not expired → serve from `registry-storage`
3. Cache miss or expired → fetch from upstream, stream to client, store in background goroutine
4. Background store: save manifest to `registry-metadata`, blobs to `registry-storage`
5. Never block client response on background store completion

**Security:**
- Upstream credentials encrypted at rest (AES-256-GCM). Key from environment variable.
- Sanitise upstream responses: validate Content-Type, reject unexpected media types.
- Cap upstream response size (configurable, default 20GB per layer).
- Honour upstream `Content-Digest` — verify before caching.
- Do not expose upstream auth credentials in any log or error message.

---

### 4.7 `registry-scanner`

**Purpose:** Orchestrates vulnerability scanning. Hosts the scanner plugin interface. Does not implement scanning itself.

**Plugin interface:**

```go
// Scanner is the interface all scanner plugins must implement.
// Plugins are loaded as Go plugins (*.so) OR as external processes via stdin/stdout JSON-RPC.
type Scanner interface {
    // Name returns the unique scanner identifier, e.g. "trivy", "grype"
    Name() string
    // Version returns the scanner version string
    Version() string
    // Scan performs a vulnerability scan on the given image layers.
    // manifestDigest: sha256:... identifying the image
    // layers: ordered list of layer blob digests to fetch
    // Returns findings or error. Must be idempotent.
    Scan(ctx context.Context, req ScanRequest) (*ScanResult, error)
}

type ScanRequest struct {
    TenantID       string
    RepositoryName string
    ManifestDigest string
    Layers         []LayerRef
    StorageFetcher BlobFetcher // injected by orchestrator to fetch blobs
}

type ScanResult struct {
    ScannerName    string
    ScannerVersion string
    Findings       []Finding
    SeverityCounts map[string]int // "CRITICAL","HIGH","MEDIUM","LOW","NEGLIGIBLE"
    ScannedAt      time.Time
}

type Finding struct {
    CVE         string
    Severity    string
    Package     string
    Version     string
    FixedIn     string
    Description string
    References  []string
}
```

**Plugin loading:**
- `SCANNER_PLUGIN_PATH` env var points to plugin binary or `.so` file
- Validate plugin binary checksum (SHA256) against `SCANNER_PLUGIN_CHECKSUM` env var before loading
- If checksum mismatch: log critical, refuse to start
- External process plugins: communicate over stdin/stdout with newline-delimited JSON. Never shell-exec with user-supplied input.

**Scan job flow:**
1. Consume `push.completed` from RabbitMQ
2. Create scan record in `registry-metadata` (status: `pending`)
3. Fetch manifest from `registry-metadata`, extract layer digests
4. Invoke scanner plugin with layer refs
5. Plugin fetches blobs via `registry-storage` gRPC (authenticated)
6. Update scan result in `registry-metadata`
7. Publish `scan.completed` event to RabbitMQ
8. If findings contain CRITICAL/HIGH and tenant policy requires blocking: update tag status to `blocked`

**Concurrency:**
- Worker pool, size configurable via `SCANNER_WORKER_COUNT` (default: 4)
- Each job has a timeout (`SCANNER_JOB_TIMEOUT_SECONDS`, default: 600)
- Dead-letter queue for failed jobs after 3 retries

---

### 4.8 `registry-signer`

**Purpose:** Image signing and verification using Cosign (Sigstore) and Notary v2.

**Signing backends:**

```go
type Signer interface {
    Sign(ctx context.Context, req SignRequest) (*Signature, error)
    Verify(ctx context.Context, req VerifyRequest) (*VerificationResult, error)
    ListSignatures(ctx context.Context, manifestDigest string) ([]Signature, error)
}
```

**Cosign integration:**
- Signatures stored as OCI artifacts in `registry-core` (standard Cosign behaviour)
- `registry-signer` exposes a signing API for CI/CD pipelines that don't have key material
- Key material stored in: env var (dev), HashiCorp Vault (production), or cloud KMS (AWS KMS / GCP KMS / Azure Key Vault)
- Key backend configured via `SIGNER_KEY_BACKEND` env var: `env` | `vault` | `awskms` | `gcpkms` | `azurekms`
- Never log, print, or include key material in error messages

**Notary v2 integration:**
- TUF (The Update Framework) metadata stored in `registry-storage`
- Delegation keys per tenant
- Root key ceremony documented in `infra/runbooks/notary-root-key-ceremony.md`

**gRPC service:**

```protobuf
service SignerService {
  rpc SignManifest(SignManifestRequest) returns (SignManifestResponse);
  rpc VerifyManifest(VerifyManifestRequest) returns (VerifyManifestResponse);
  rpc ListSignatures(ListSignaturesRequest) returns (ListSignaturesResponse);
}
```

---

### 4.9 `registry-webhook`

**Purpose:** Reliable webhook delivery with retries, dead-lettering, and HMAC signing.

**Events delivered:**
- `image.pushed` — new tag/manifest pushed
- `image.deleted` — tag or manifest deleted
- `scan.completed` — vulnerability scan finished
- `scan.policy_blocked` — image blocked by policy
- `image.signed` — signature added

**Delivery guarantees:**
- At-least-once delivery
- Retry with exponential backoff: 5s, 30s, 5m, 30m, 2h (5 attempts total)
- After 5 failures: move to dead-letter queue, notify tenant admin
- Timeout per delivery attempt: 30 seconds

**Security:**
- HMAC-SHA256 signature on payload, key set per webhook endpoint
- Signature in `X-Registry-Signature: sha256=<hex>` header
- Validate destination URL is not a private IP range (SSRF protection): block 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 127.0.0.0/8, ::1, metadata endpoints (169.254.169.254)
- Enforce HTTPS-only webhook endpoints (reject HTTP)
- Never include auth tokens or credentials in webhook payload

---

### 4.10 `registry-audit`

**Purpose:** Immutable audit log for all significant actions.

**Events logged (minimum):**
- User login / logout / lockout
- Token issued / revoked
- API key created / deleted
- Image pushed / pulled / deleted
- Repository created / deleted
- Webhook created / triggered
- Scan started / completed
- Policy violation
- Tenant config changed
- RBAC changes

**Audit record structure:**
```go
type AuditEvent struct {
    ID         uuid.UUID  `json:"id"`
    TenantID   uuid.UUID  `json:"tenant_id"`
    ActorID    string     `json:"actor_id"`    // user ID or "system"
    ActorType  string     `json:"actor_type"`  // "user" | "robot" | "system"
    ActorIP    string     `json:"actor_ip"`    // IPv4/IPv6, never logged raw from header (use trusted proxy IP)
    Action     string     `json:"action"`      // verb.resource: "push.image"
    Resource   string     `json:"resource"`    // e.g. "myorg/myimage:v1.2.3"
    Outcome    string     `json:"outcome"`     // "success" | "failure"
    Metadata   JSONB      `json:"metadata"`    // additional context, no secrets
    OccurredAt time.Time  `json:"occurred_at"`
}
```

**Rules:**
- Audit records are append-only. No UPDATE or DELETE on audit table — enforce via PostgreSQL row security policy.
- Actor IP extracted from `X-Forwarded-For` only if request came through trusted gateway IP. Otherwise use direct TCP peer.
- Never log passwords, tokens, API keys, or secret values in `metadata`.
- Retain audit logs for minimum 90 days (configurable, default 365 days).

---

### 4.11 `registry-gc`

**Purpose:** Garbage collection worker. Identifies and deletes orphaned blobs and untagged manifests.

**GC modes:**
- `dry-run` — report what would be deleted, no deletions
- `manifests` — delete untagged manifests only
- `blobs` — delete orphaned blobs only (no manifest references)
- `full` — manifests then blobs

**GC algorithm:**
```
Phase 1 — Mark (read-only):
  1. Lock repository in registry-metadata (advisory lock, not table lock)
  2. Walk all tags → collect all referenced manifest digests
  3. Walk all manifests → collect all referenced blob digests
  4. Set of "live blobs" = union of all referenced digests

Phase 2 — Sweep:
  1. List all blobs in registry-storage for tenant
  2. For each blob not in live set AND older than GC_BLOB_MIN_AGE (default 1h):
     - Delete from registry-storage
     - Delete from blobs table in registry-metadata
     - Emit gc.blob_deleted event
  3. For each untagged manifest older than GC_MANIFEST_MIN_AGE (default 24h):
     - Delete manifest
     - Emit gc.manifest_deleted event

Phase 3 — Update quota:
  1. Recompute storage_used per repository
  2. Update registry-metadata
```

**Safety rules:**
- Always run dry-run first in CI before scheduling
- Never delete blobs younger than `GC_BLOB_MIN_AGE` — in-flight pushes write blobs before manifests
- Emit audit event for every deletion
- GC is scheduled, not triggered by push events — run nightly by default (configurable cron)
- GC must be idempotent: safe to run multiple times

---

### 4.12 `registry-tenant`

**Purpose:** Tenant lifecycle management, custom domain provisioning, per-tenant configuration.

**Responsibilities:**
- CRUD for tenants (super-admin API, not exposed to end users)
- Custom domain registration and verification (DNS TXT record or HTTP challenge)
- Per-tenant quota configuration
- Per-tenant feature flags (proxy cache enabled, signing required, scan policy)
- Provision tenant isolation: create org in `registry-metadata`, create S3 prefix/bucket policy

**Custom domain flow:**
1. Tenant submits domain `registry.acme.com`
2. System generates DNS TXT verification record
3. Tenant adds `_registry-verify.<domain>` TXT record
4. Background worker polls DNS until verified (max 48h)
5. On verification: trigger Let's Encrypt certificate issuance via gateway ACME
6. Store cert in Redis (Traefik reads it) or notify Nginx via API
7. Update `registry-gateway` routing table (Redis-backed, TTL-less)

---

## 5. Shared Libraries (`libs/`)

**Module path:** `github.com/<org>/registry/libs`

All shared packages live here. Services import specific sub-packages. Because all code lives in a monorepo with Go workspaces, services import `libs/` directly — there is no version pinning or `go get` required for local development or CI.

```
libs/
├── auth/
│   ├── jwt/          # JWT validation (public key only — no signing logic here)
│   ├── apikey/       # API key hashing + validation helpers
│   └── mtls/         # mTLS client + server config builders
├── storage/
│   └── driver/       # The Driver interface definition (no implementations)
├── scanner/
│   └── plugin/       # Scanner interface + ScanRequest/ScanResult/Finding types
├── observability/
│   ├── otel/         # OTEL setup: tracer, meter, logger provider bootstrap
│   ├── tracing/      # Span helpers, trace ID propagation
│   └── metrics/      # Common metric definitions (request count, latency histograms)
├── errors/
│   ├── codes/        # Canonical error codes (maps to gRPC status + HTTP status)
│   └── types/        # Typed errors with tenant context
├── middleware/
│   ├── grpc/         # Unary + stream interceptors: auth, tracing, logging, recovery
│   └── http/         # HTTP middleware: request ID, tracing, auth, rate limit
├── config/
│   └── loader/       # Viper-based config loader with env var binding
├── crypto/
│   ├── aes/          # AES-256-GCM helpers (for encrypting upstream credentials)
│   └── argon2/       # Argon2id password hashing helpers
├── rabbitmq/
│   ├── publisher/    # Typed event publisher with confirm mode
│   ├── consumer/     # Consumer with dead-letter + retry logic
│   └── events/       # All event type definitions (canonical, shared across services)
├── testutil/
│   ├── containers/   # Testcontainers helpers (postgres, redis, rabbitmq, minio)
│   └── fixtures/     # Common test data builders
├── go.mod
└── .golangci.yml     # Linter config — symlinked (or copied) into each service directory
```

**Rules:**
- No business logic in `libs/`. Only utilities and interfaces.
- No circular imports. Services import from `libs/`. `libs/` never imports from `services/`.
- All public functions must have godoc comments.
- A breaking change to a `libs/` interface requires a PR that updates all affected services in the same commit — no multi-repo version dance.

---

## 6. Communication Patterns

### gRPC (synchronous, internal)

- All internal service-to-service calls use gRPC over mTLS
- Proto files live in `proto/`, generated stubs committed to `proto/gen/go/`
- Each service has a `buf.gen.yaml` pointing to `../../proto` for regeneration
- Use `grpc.WithBlock()` with a timeout on client connections — never silently hang
- Always set deadlines on outgoing gRPC calls: `ctx, cancel := context.WithTimeout(ctx, 5*time.Second)`
- gRPC health check protocol (`grpc.health.v1`) implemented by every service
- Retry policy: 3 attempts, exponential backoff, only on `UNAVAILABLE` and `DEADLINE_EXCEEDED`

### RabbitMQ (asynchronous, event-driven)

- Exchange type: `topic` for all events
- Quorum queues only (no classic queues in production)
- Publishers use confirm mode — wait for broker ACK before returning
- Consumers use manual ACK — ACK only after successful processing
- Every service that publishes events does so via `libs/rabbitmq/publisher`
- Dead-letter exchange: `dlx.<service>` — all queues have a DLX configured
- Message TTL: 7 days on all queues (configurable)
- Do not put sensitive data (passwords, tokens) in message payloads

### Service Discovery

- Kubernetes: services discover each other via K8s DNS (`<service>.<namespace>.svc.cluster.local`)
- Docker Compose: service names are DNS hostnames
- All gRPC target addresses configured via environment variables, never hardcoded

---

## 7. Authentication & Security

### mTLS Between Services

Every gRPC client/server pair uses mutual TLS.

**Certificate management:**
- Development: self-signed certs generated by `make dev-certs` (uses `cfssl`)
- Production (K8s): cert-manager with internal CA issuer
- Cert rotation: automated via cert-manager. Services reload certs without restart (use `tls.Config.GetCertificate`)

**Rules:**
- Every gRPC server sets `tls.RequireAndVerifyClientCert`
- Client cert CN must match expected service name (enforce in server-side interceptor)
- CA cert loaded from `MTLS_CA_CERT_PATH` env var. No defaults.
- Certificate validity: maximum 90 days

**mTLS config builder (from `libs/auth/mtls`):**

```go
// ServerTLSConfig returns a tls.Config for gRPC servers requiring client certs
func ServerTLSConfig(caCertPath, certPath, keyPath string) (*tls.Config, error)

// ClientTLSConfig returns a tls.Config for gRPC clients presenting a cert
func ClientTLSConfig(caCertPath, certPath, keyPath string, serverName string) (*tls.Config, error)
```

### JWT Validation

- Every gRPC server validates Bearer tokens via `registry-auth` gRPC call
- Cache validation results in Redis: key `jwt:valid:<jti>`, TTL = token remaining lifetime
- On cache miss: call `registry-auth.ValidateToken` gRPC
- If `registry-auth` is unreachable: fail closed (deny all), log error, increment metric

### Environment Variables — Security Rules

- **Never** commit `.env` files. Only `.env.example` with placeholder values.
- All secrets (DB passwords, JWT keys, API credentials) must come from environment variables or a secrets manager.
- In production K8s: use Kubernetes Secrets (base64) mounted as env vars, or External Secrets Operator pointing to Vault/AWS Secrets Manager.
- Every service must fail to start if a required secret env var is empty or missing. Use a `config.Validate()` call in `main.go` before any server starts.
- Never log environment variable values at startup, even at DEBUG level.

### Input Validation

- All user-supplied strings (repo names, tag names, usernames) validated against allowlists. Reject at the handler layer before passing to service layer.
- Repository name: `^[a-z0-9]+([._-][a-z0-9]+)*$`, max 128 chars
- Tag name: `^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`
- Digest: `^sha256:[a-f0-9]{64}$`
- Username: `^[a-zA-Z0-9_-]{3,64}$`
- Org name: `^[a-z0-9-]{2,64}$`
- Never pass unvalidated strings to SQL (use parameterised queries only — see §13)
- Never pass unvalidated strings to shell commands (no `exec.Command` with user input)

---

## 8. Storage Layer

### Driver Selection

`STORAGE_DRIVER` environment variable selects the backend. Valid values: `minio`, `s3`, `gcs`, `azure`, `filesystem`.
If unset or invalid: fail to start with a clear error message.

### Configuration per Driver

**MinIO:**
```
STORAGE_MINIO_ENDPOINT=          # e.g. minio:9000
STORAGE_MINIO_ACCESS_KEY=        # required
STORAGE_MINIO_SECRET_KEY=        # required
STORAGE_MINIO_BUCKET=            # required
STORAGE_MINIO_USE_SSL=true       # default true
STORAGE_MINIO_REGION=us-east-1   # optional
```

**AWS S3:**
```
STORAGE_S3_BUCKET=               # required
STORAGE_S3_REGION=               # required
STORAGE_S3_ROLE_ARN=             # optional, for cross-account assume-role
# Credentials: prefer IMDSv2 (no static keys). Static fallback:
AWS_ACCESS_KEY_ID=
AWS_SECRET_ACCESS_KEY=
```

**GCP Cloud Storage:**
```
STORAGE_GCS_BUCKET=              # required
STORAGE_GCS_PROJECT=             # required
GOOGLE_APPLICATION_CREDENTIALS=  # path to service account JSON, or use Workload Identity
```

**Azure Blob:**
```
STORAGE_AZURE_CONTAINER=         # required
STORAGE_AZURE_ACCOUNT=           # required
# Auth: prefer managed identity. Static fallback:
STORAGE_AZURE_ACCOUNT_KEY=
```

**Filesystem (dev only):**
```
STORAGE_FILESYSTEM_ROOT=/data    # required, absolute path
```

### Encryption at Rest

- S3: enforce `x-amz-server-side-encryption: AES256` on all PutObject calls
- GCS: CMEK key configured on bucket (Terraform-managed)
- Azure: SSE enabled on storage account
- MinIO: enable MinIO server-side encryption (SSE-S3 or SSE-KMS) — document setup in `infra/runbooks/minio-encryption.md`
- Filesystem: document that filesystem encryption (LUKS/dm-crypt) must be configured at OS level

---

## 9. Multi-Tenancy & Custom Domains

### Tenant Isolation

- All database rows include `tenant_id UUID NOT NULL`
- All queries in `registry-metadata` must filter by `tenant_id` — never query across tenants
- PostgreSQL Row Security Policy (RLS) enabled as a second layer of defence:
  ```sql
  ALTER TABLE repositories ENABLE ROW LEVEL SECURITY;
  CREATE POLICY tenant_isolation ON repositories
    USING (tenant_id = current_setting('app.tenant_id')::uuid);
  ```
- Application sets `SET LOCAL app.tenant_id = '<id>'` in each transaction
- Storage keys are prefixed with `tenant_id` (see §4.4)
- RabbitMQ messages include `tenant_id` in payload and as a message header

### Custom Domain Resolution

```
Incoming request Host: registry.acme.com
  → Gateway looks up in Redis: domain:registry.acme.com → tenant_id: <uuid>
  → Cache miss → query registry-tenant gRPC → cache result (TTL 60s)
  → Inject X-Tenant-ID header
  → Route to registry-core
```

- Wildcard platform domain: `*.registry.example.com` → tenant resolved from subdomain
- Custom domain: verified and stored in `registry-tenant` DB
- If domain not found: return 404 with no tenant information exposed

---

## 10. Vulnerability Scanning

### Plugin Contract

Scanner plugins must implement the `Scanner` interface defined in `libs/scanner/plugin` (see §4.7).

**Plugin types supported:**
1. **Go plugin** (`.so` file): loaded via `plugin.Open()`. Must export `New() Scanner`.
2. **External process**: spawned as subprocess, communicates via stdin/stdout JSON-RPC.

**JSON-RPC protocol for external process plugins:**

Request (to stdin):
```json
{
  "id": "uuid",
  "method": "scan",
  "params": {
    "tenant_id": "...",
    "manifest_digest": "sha256:...",
    "layers": [{"digest": "sha256:...", "media_type": "..."}]
  }
}
```

Response (from stdout):
```json
{
  "id": "uuid",
  "result": {
    "scanner_name": "trivy",
    "scanner_version": "0.50.0",
    "findings": [...],
    "severity_counts": {"CRITICAL": 2, "HIGH": 5}
  },
  "error": null
}
```

- Process must exit 0 on success, non-zero on failure
- Do not use stderr for structured data — only for human-readable diagnostics

### Scan Policy

Per-tenant policy (stored in `registry-tenant`):

```go
type ScanPolicy struct {
    ScanOnPush          bool
    BlockOnSeverity     string // "CRITICAL" | "HIGH" | "MEDIUM" | "" (disabled)
    AllowUnscanned      bool   // if false, block pull of unscanned images
    ExemptRepositories  []string
}
```

---

## 11. Image Signing

### Key Management

`SIGNER_KEY_BACKEND` controls where keys are loaded from:

| Backend | Config env vars |
|---|---|
| `env` | `SIGNER_COSIGN_PRIVATE_KEY` (PEM, base64), `SIGNER_COSIGN_PUBLIC_KEY` |
| `vault` | `VAULT_ADDR`, `VAULT_TOKEN` (or K8s SA auth), `VAULT_COSIGN_PATH` |
| `awskms` | `SIGNER_KMS_ARN`, standard AWS credential chain |
| `gcpkms` | `SIGNER_KMS_RESOURCE_ID`, standard GCP credential chain |
| `azurekms` | `SIGNER_KMS_VAULT_URL`, `SIGNER_KMS_KEY_NAME`, standard Azure credential chain |

**Rules:**
- Key material never leaves the signing service
- Signing operations are audit-logged
- Public keys are discoverable via `GET /api/v1/signers/cosign/public-key` (per tenant)
- Verification does not require the signing service — clients can verify with the public key directly

### Notary v2 TUF Metadata

- Root keys are generated offline and stored in HSM or secure cold storage
- Documented key ceremony in `infra/runbooks/notary-root-key-ceremony.md`
- TUF metadata stored in `registry-storage` under `tuf/<tenant_id>/`
- Targets signed per push (targets key delegated to per-tenant keys)

---

## 12. Observability

### OpenTelemetry Setup

All services instrument with OpenTelemetry Go SDK. Exporter is pluggable via environment variable.

**`OTEL_EXPORTER`** controls the backend:
- `jaeger` → OTLP/gRPC to Jaeger
- `tempo` → OTLP/gRPC to Grafana Tempo
- `datadog` → OTLP/HTTP to Datadog Agent
- `stdout` → dev/debug only

**Common env vars (all services):**
```
OTEL_EXPORTER=                   # required: jaeger|tempo|datadog|stdout
OTEL_ENDPOINT=                   # OTLP endpoint URL
OTEL_SERVICE_NAME=               # set per-service in Dockerfile
OTEL_ENVIRONMENT=                # production|staging|development
OTEL_SAMPLING_RATE=1.0           # 0.0 to 1.0, default 1.0
```

**Datadog-specific:**
```
DD_AGENT_HOST=
DD_API_KEY=                      # stored as secret, never logged
```

**Bootstrap pattern (in `libs/observability/otel`):**

```go
func Bootstrap(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error)
```

Call in `main.go` before starting servers. Always call `shutdown` on process exit.

### Metrics

Every service exposes Prometheus metrics at `GET /metrics` (internal port only — not exposed via gateway).

**Standard metrics (defined in `libs/observability/metrics`):**
- `registry_http_request_duration_seconds` — histogram, labels: service, method, path, status
- `registry_grpc_request_duration_seconds` — histogram, labels: service, method, status
- `registry_rabbitmq_messages_consumed_total` — counter, labels: service, queue, status
- `registry_storage_operation_duration_seconds` — histogram, labels: driver, operation, status
- `registry_active_uploads_total` — gauge

### Structured Logging

- Logger: `log/slog` (Go 1.21+ standard library)
- Format: JSON in production, text in development (`LOG_FORMAT=json|text`)
- Level: `LOG_LEVEL=debug|info|warn|error`
- Every log entry must include: `trace_id`, `span_id`, `tenant_id` (where available), `service`
- Never log: passwords, tokens, API keys, private key material, full request bodies

---

## 13. Database Conventions

### General Rules

- ORM: **none**. Use `pgx/v5` directly with `pgxpool`. Raw SQL only.
- All queries parameterised. Never use `fmt.Sprintf` to build SQL.
- Migrations: `pressly/goose` with SQL migrations. Migration files in `migrations/` of each service that owns a schema.
- Only `registry-metadata` has direct PostgreSQL access. All other services call its gRPC API.
- `registry-auth` and `registry-tenant` have their own separate PostgreSQL databases (logical separation).
- Connection pool: `pgxpool.New()` with `MaxConns` set from `DB_MAX_CONNS` env var (default 20).
- Every query must use the request context for cancellation.
- Transactions: always use `defer tx.Rollback(ctx)` — only committed explicitly on success.

### Migration Rules

- Never drop a column in a migration — add a new column and migrate data in a separate step
- Every migration must be reversible (down migration required)
- Migration naming: `YYYYMMDDHHMMSS_<description>.sql`
- Run migrations at startup in a separate step before serving traffic (use `goose up`)

### Connection String

```
DB_DSN=postgres://<user>:<password>@<host>:<port>/<database>?sslmode=require
```

`sslmode=require` is mandatory. `disable` is rejected at startup.

---

## 14. RabbitMQ Event Contracts

All event types defined in `libs/rabbitmq/events`. No service defines its own event types.

### Exchange Layout

```
Exchange: registry.events    (topic, durable)
Exchange: registry.dlx       (topic, durable) — dead-letter target

Routing keys:
  push.completed
  push.failed
  manifest.deleted
  tag.deleted
  scan.queued
  scan.completed
  scan.policy_blocked
  webhook.queued
  webhook.delivered
  webhook.failed
  gc.run.started
  gc.run.completed
  image.signed
  tenant.created
  tenant.domain.verified
```

### Event Envelope

```go
type Event struct {
    ID         string          `json:"id"`          // UUID v4
    Type       string          `json:"type"`        // routing key
    TenantID   string          `json:"tenant_id"`
    OccurredAt time.Time       `json:"occurred_at"`
    Version    string          `json:"version"`     // "1.0"
    Payload    json.RawMessage `json:"payload"`
}
```

### `push.completed` Payload

```go
type PushCompletedPayload struct {
    RepositoryName string `json:"repository_name"`
    Tag            string `json:"tag"`
    ManifestDigest string `json:"manifest_digest"`
    PushedBy       string `json:"pushed_by"`     // actor user ID
    SizeBytes      int64  `json:"size_bytes"`
}
```

### `scan.completed` Payload

```go
type ScanCompletedPayload struct {
    ManifestDigest  string         `json:"manifest_digest"`
    RepositoryName  string         `json:"repository_name"`
    ScannerName     string         `json:"scanner_name"`
    SeverityCounts  map[string]int `json:"severity_counts"`
    PolicyViolation bool           `json:"policy_violation"`
    Blocked         bool           `json:"blocked"`
}
```

---

## 15. gRPC Conventions

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
├── gen/go/                    # Generated stubs — committed, not gitignored
└── buf.yaml
```

- Package naming: `registry.<service>.v1`
- Go package option: `option go_package = "github.com/<org>/registry/proto/gen/go/<service>/v1;<service>v1";`
- All fields use `snake_case`
- All RPCs return errors using `google.rpc.Status` (import `google/rpc/status.proto`)
- Pagination: use `page_token` (string) + `page_size` (int32) pattern, not offset
- All timestamps: `google.protobuf.Timestamp`
- UUIDs: `string` (not bytes)
- Breaking changes: never modify existing field numbers. Add new fields only.

### Interceptors (applied to every gRPC server via `libs/middleware/grpc`)

**Server-side:**
1. Recovery (panic → gRPC Internal error)
2. Request ID injection
3. mTLS peer verification (CN check)
4. Auth token validation (for external-facing services)
5. Tenant ID extraction + context injection
6. OpenTelemetry tracing
7. Structured logging
8. Metrics

**Client-side:**
1. mTLS credential attachment
2. OpenTelemetry trace propagation
3. Deadline injection
4. Retry (UNAVAILABLE, DEADLINE_EXCEEDED only)

---

## 16. Deployment

### Docker Compose (`infra/docker-compose/`)

```
docker-compose.yml           # All services, dev configuration
docker-compose.override.yml  # Local developer overrides (gitignored)
docker-compose.test.yml      # Integration test environment
.env.example                 # All required variables, no secret values
```

**Required infrastructure services in Compose:**
- PostgreSQL 16
- Redis 7
- RabbitMQ 3.13 (management plugin enabled)
- MinIO (default storage driver for local dev)
- Jaeger (default OTEL backend for local dev)

### Kubernetes (`infra/helm/`)

```
helm/
├── registry/                # Umbrella chart
│   ├── Chart.yaml
│   ├── values.yaml          # Default values, no secrets
│   ├── values.prod.yaml     # Production overrides (no secrets)
│   └── charts/
│       ├── registry-gateway/
│       ├── registry-auth/
│       ├── registry-core/
│       ├── registry-storage/
│       ├── registry-metadata/
│       ├── registry-proxy/
│       ├── registry-scanner/
│       ├── registry-signer/
│       ├── registry-webhook/
│       ├── registry-audit/
│       ├── registry-gc/
│       └── registry-tenant/
```

**Each service Helm chart must include:**
- `Deployment` with `readinessProbe` and `livenessProbe` (gRPC health check)
- `PodDisruptionBudget` (minAvailable: 1)
- `HorizontalPodAutoscaler` (CPU + custom metric: queue depth)
- `ServiceAccount` with minimal RBAC
- `NetworkPolicy` — allowlist only (default deny all)
- `SecretProviderClass` (External Secrets Operator) for secrets
- Resource requests and limits on every container — no defaults

**NetworkPolicy rules:**
- `registry-core` → ingress from `registry-gateway` only
- `registry-metadata` → ingress from `registry-core`, `registry-proxy`, `registry-scanner`, `registry-gc` only
- `registry-storage` → ingress from `registry-core`, `registry-proxy`, `registry-scanner`, `registry-gc` only
- No service has unrestricted egress except `registry-proxy` (fetches from internet) and `registry-webhook` (calls external URLs)

### Health Check Endpoints

Every service implements:
- `grpc.health.v1.Health/Check` — for K8s readiness/liveness
- `GET /healthz` (HTTP, internal port) — for load balancers and Compose healthcheck
- `GET /metrics` (HTTP, internal port) — Prometheus scrape

---

## 17. Security Hardening Rules

These rules apply to **every service** without exception.

### Go Code

- [ ] No `unsafe` package usage without a documented, reviewed justification
- [ ] No `exec.Command` with any part of user-supplied input
- [ ] No `os.Getenv` for secrets inside handlers — load at startup into a typed config struct
- [ ] All file paths sanitised with `filepath.Clean` and checked against an allowed prefix
- [ ] HTTP clients: always set timeouts (`Timeout`, `TLSHandshakeTimeout`, `ResponseHeaderTimeout`)
- [ ] No default HTTP client (`http.DefaultClient`) — always create a configured client
- [ ] `context.Background()` never used inside request handlers — always propagate request context
- [ ] Randomness: use `crypto/rand`, never `math/rand` for security-sensitive values

### HTTP

- [ ] `Content-Security-Policy` header on all HTML responses
- [ ] `X-Content-Type-Options: nosniff` on all responses
- [ ] `X-Frame-Options: DENY` on all responses
- [ ] No sensitive data in URL query parameters (use POST body or headers)
- [ ] CORS: explicitly configured allowlist, never `*`
- [ ] Request body size limits set on all HTTP servers

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

---

## 18. Testing Requirements

### Unit Tests

- Location: `_test.go` files alongside source
- Coverage target: 80% minimum per service (enforced in CI)
- Test naming: `Test<FunctionName>_<scenario>_<expectedOutcome>`
- Use table-driven tests for validation logic
- No real network calls in unit tests — use interfaces and mocks
- Mocks generated with `mockery` (config in `.mockery.yaml` per repo)

### Integration Tests

- Location: `internal/testutil/integration/`
- Use `testcontainers-go` (helpers in `libs/testutil/containers`)
- Spin up real PostgreSQL, Redis, RabbitMQ, MinIO per test suite
- Integration tests tagged with `//go:build integration` — excluded from default `go test ./...`
- Run with `make test-integration`

### OCI Spec Conformance

- `registry-core` must pass the OCI Distribution Spec conformance test suite
- Run with `make test-conformance` in `registry-core`
- Conformance tests run in CI on every PR to `main`
- Reference: https://github.com/opencontainers/distribution-spec/tree/main/conformance

### Security Tests

- SAST: `gosec` run in CI on every PR
- Dependency audit: `govulncheck` in CI
- Integration: OWASP ZAP baseline scan against staging environment (weekly)

---

## 19. CI/CD Pipeline

The monorepo has a single `.github/workflows/` directory. Jobs are **path-filtered** — a change under `services/core/` only triggers the `core` pipeline; a change under `libs/` triggers all service pipelines. Each service pipeline runs the same stages:

```
Stages (per service, triggered by path filter):
1. lint          → golangci-lint (config in .golangci.yml at repo root)
2. test          → go test -race ./...
3. security      → govulncheck, gosec, gitleaks
4. build         → docker build (multi-stage, distroless base)
5. conformance   → (services/core only) OCI conformance suite
6. integration   → make test-integration
7. publish       → push image to registry (semver tag on release)
8. deploy-staging → helm upgrade to staging namespace
9. deploy-prod   → manual approval gate → helm upgrade to prod

libs/ change triggers:
  → lint + test for libs/
  → then fan-out: stages 1-4 for every service (parallel)
```

### Docker Build Rules

- Multi-stage builds: builder stage (`golang:1.23-bookworm`), final stage (`gcr.io/distroless/static-debian12`)
- Final image must contain only the compiled binary and TLS CA certs
- No shell in final image
- Run as non-root user (`USER 65532:65532`)
- Image tagged with: `git SHA` (every build) + semver tag (releases)
- `docker scout` or `trivy image` scan in CI — fail build on CRITICAL CVEs

### Release Versioning

- Semantic versioning: `v<major>.<minor>.<patch>` — single version tag for the entire monorepo
- `proto/` and `libs/` follow the same release cadence as services — no independent versioning
- Breaking proto changes: major version bump of the monorepo tag; maintain backward-compatible stubs for one release cycle
- Changelog: conventional commits enforced via `commitlint`; scoped commits preferred (e.g. `feat(core):`, `fix(libs/auth):`)

---

## 20. Decision Log

| # | Decision | Rationale | Date |
|---|---|---|---|
| 1 | gRPC for sync, RabbitMQ for async | gRPC gives strong contracts + mTLS; RabbitMQ gives durable async with DLQ | Initial |
| 2 | RabbitMQ over Kafka | Lower operational complexity; Quorum Queues give durability without Kafka's broker count requirements | Initial |
| 3 | JWT RS256 + API keys + mTLS | Defence in depth: mTLS for network layer, JWT for identity, API keys for machine accounts | Initial |
| 4 | Multi-tenant with custom domains | Required for white-label / enterprise use cases | Initial |
| 5 | Pluggable scanner interface | Avoids locking into one scanner; allows BYO commercial scanners | Initial |
| 6 | Cosign + Notary v2 | Both are actively maintained and address different use cases; Cosign for keyless, Notary v2 for TUF | Initial |
| 7 | OTEL with pluggable exporter | Avoids vendor lock-in; same instrumentation code works with Jaeger, Tempo, or Datadog | Initial |
| 8 | Monorepo with Go workspaces | Atomic cross-service changes; eliminates version-bump overhead for proto + libs; `go.work` keeps per-service `go.mod` files self-contained for Docker builds | 2026-06-09 |
| 9 | pgx/v5 with raw SQL, no ORM | Full query control; ORM abstraction leaks at scale; parameterised queries enforced by pgx | Initial |
| 10 | Distroless final Docker image | Minimal attack surface; no shell means no RCE via shell injection even if exploited | Initial |
| 11 | No presigned URLs to clients | Prevents storage credential exposure; all blob traffic proxied for audit and rate limiting | Initial |
| 12 | PostgreSQL RLS as second layer | Defence in depth for tenant isolation; application bug cannot leak cross-tenant data | Initial |

---

> **Last updated:** See Git log.
> **Questions?** Open an issue in this repository with the label `architecture`.
> **This file is the source of truth. If code contradicts this file, the code is wrong.**