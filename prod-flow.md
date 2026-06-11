# Production Flow

> How the platform works when deployed to Kubernetes — what changes from local Docker Compose
> and what stays the same.

---

## Table of Contents

1. [What stays the same](#1-what-stays-the-same)
2. [What changes in production](#2-what-changes-in-production)
3. [TLS and routing](#3-tls-and-routing)
4. [AUTH_REALM — the critical env var](#4-auth_realm--the-critical-env-var)
5. [Secrets management](#5-secrets-management)
6. [mTLS between services](#6-mtls-between-services)
7. [Storage](#7-storage)
8. [Database](#8-database)
9. [Service discovery](#9-service-discovery)
10. [End-to-end: docker push](#10-end-to-end-docker-push)
11. [End-to-end: docker pull](#11-end-to-end-docker-pull)
12. [End-to-end: pull-through proxy cache](#12-end-to-end-pull-through-proxy-cache)
13. [Async pipeline after a push](#13-async-pipeline-after-a-push)
14. [Custom domain provisioning](#14-custom-domain-provisioning)
15. [Scaling](#15-scaling)
16. [Key production env vars per service](#16-key-production-env-vars-per-service)
17. [What is not yet production-ready](#17-what-is-not-yet-production-ready)

---

## 1. What stays the same

The same 12 Go services run in production as in local Docker Compose. The gRPC communication
topology, RabbitMQ event contracts, PostgreSQL schema, and all business logic are identical.
What changes is the infrastructure layer that sits underneath them:

| Concern | Dev (Docker Compose) | Production (Kubernetes) |
|---|---|---|
| TLS termination | None (plain HTTP inside Compose network) | Traefik + cert-manager + Let's Encrypt |
| Service certs (mTLS) | `cert-init` container, self-signed, shared volume | cert-manager, automatic rotation every 90 days |
| Secrets | `.env` files + Vault dev mode | Kubernetes Secrets via External Secrets Operator → Vault / AWS SM / GCP SM |
| Object storage | MinIO container | AWS S3 / GCP Cloud Storage / Azure Blob (IAM/Workload Identity) |
| PostgreSQL | Local container, `sslmode=prefer`, no cert | Managed DB (RDS / Cloud SQL / Azure DB), `sslmode=require` |
| Service discovery | Docker Compose DNS (`registry-auth:50051`) | Kubernetes DNS (`registry-auth.registry.svc.cluster.local:50051`) |
| Ingress | Services exposed directly on `localhost:808x` | Single Traefik ingress on port 443, routes by Host header |
| Scaling | Single container per service | HPA per service (CPU + RabbitMQ queue depth) |
| Auth realm | `http://localhost:8080/auth/token` | `https://auth.registry.example.com/auth/token` (public URL) |

---

## 2. What changes in production

### The single public entry point

In dev you hit individual service ports directly. In production **Traefik is the only public
surface**. All client traffic — Docker daemon, API calls, browser — enters on port 443. No
internal service port is externally reachable.

```
Internet
    │ HTTPS :443
    ▼
registry-gateway (Traefik)
    │ resolves tenant from Host header
    │ injects X-Tenant-ID + X-Request-ID
    │ rate-limits by tenant + IP
    ├──► registry-auth    (Host: auth.registry.example.com)
    ├──► registry-core    (Host: *.registry.example.com or custom domain)
    └──► registry-proxy   (Host: proxy.registry.example.com or custom domain)
```

### All internal traffic is mTLS

Every gRPC call between services uses mutual TLS. In dev this is opt-in (falls back to
insecure when cert paths are absent). In production the cert paths are always set — the
`clientCreds()` helper in each service's server.go picks up the cert-manager-issued certs
and the insecure fallback path is never taken.

---

## 3. TLS and routing

### Platform domain

Traefik holds a wildcard certificate for `*.registry.example.com` issued by Let's Encrypt
(DNS-01 challenge). Tenant subdomains resolve here:

```
acme.registry.example.com  →  tenant_id: <uuid for "acme">  →  registry-core
```

Traefik looks up the tenant from the subdomain on every request via `registry-tenant` gRPC,
caches the result in Redis (TTL 60 seconds).

### Custom tenant domains

When a tenant registers `registry.acme.com`:

1. `registry-tenant` generates a DNS TXT verification token
2. The tenant adds `_registry-verify.registry.acme.com TXT <token>` to their DNS
3. `registry-tenant`'s domain worker polls DNS until verified (max 48 hours, exponential backoff)
4. On verification: Traefik ACME issues an individual certificate for `registry.acme.com`
5. Traefik routing table is updated (Redis-backed, no TTL)

From that point on, `docker login registry.acme.com` works identically to the platform subdomain.
See [§14](#14-custom-domain-provisioning) for the full flow.

---

## 4. AUTH_REALM — the critical env var

`AUTH_REALM` is the URL baked into every `401 WWW-Authenticate` challenge. Docker clients must
be able to reach it from wherever they are running — a developer's laptop, a CI runner, a
Kubernetes pod pulling images.

| Environment | `AUTH_REALM` value |
|---|---|
| Local dev | `http://localhost:8080/auth/token` (default) |
| Production | `https://auth.registry.example.com/auth/token` |
| Custom domain tenant | `https://auth.registry.example.com/auth/token` (same — auth is a platform service, not per-tenant) |

Both `registry-core` and `registry-proxy` read this variable. If it points to an internal
hostname (e.g. `http://registry-auth:8080/auth/token`) Docker clients on the host or internet
will fail to fetch a token and every push/pull will 401-loop.

**Set this in Helm values, not hardcoded:**

```yaml
# infra/helm/registry/values.prod.yaml
registryCore:
  env:
    AUTH_REALM: "https://auth.registry.example.com/auth/token"

registryProxy:
  env:
    AUTH_REALM: "https://auth.registry.example.com/auth/token"
```

---

## 5. Secrets management

### Dev

Vault runs in dev mode (`-dev -dev-root-token-id=dev-root-token`). Secrets for signer key
material are written by `vault-init` on startup. All other secrets come from `.env` files.

### Production

No `.env` files in production. The flow is:

```
Secret store (Vault / AWS Secrets Manager / GCP Secret Manager)
    │
    ▼
External Secrets Operator (K8s controller)
    │  reads SecretProviderClass for each service
    ▼
Kubernetes Secret (base64-encoded)
    │  mounted as environment variables
    ▼
Service container
```

Each service's Helm chart includes a `SecretProviderClass` that maps secret store paths to
env var names. The service code is unchanged — it reads from environment variables regardless
of how they were populated.

**Secrets that must be in the store:**

| Service | Secret | Notes |
|---|---|---|
| `registry-auth` | `JWT_PRIVATE_KEY_B64`, `JWT_PUBLIC_KEY_B64`, `JWT_KEY_ID` | RSA 4096 key pair for token signing |
| `registry-auth` | `DB_DSN` | Includes DB password |
| `registry-proxy` | `CREDENTIAL_KEY_HEX` | AES-256-GCM key for upstream registry password encryption |
| `registry-signer` | `SIGNER_COSIGN_PRIVATE_KEY` (if `SIGNER_KEY_BACKEND=env`) | Or Vault path if using Vault backend |
| All services | `DB_DSN` (where applicable) | One secret per DB-owning service |
| All services | `REDIS_PASSWORD` | |
| `registry-storage` | `STORAGE_MINIO_SECRET_KEY` / AWS/GCP/Azure credentials | Prefer IAM role / Workload Identity over static keys |

---

## 6. mTLS between services

### Dev

`cert-init` generates a self-signed CA and per-service certs on first boot. Certs are written
to a shared Docker volume (`certs_data`). Services read them via `MTLS_*` env vars.

### Production

cert-manager runs as a cluster controller. Each service's Helm chart includes a `Certificate`
resource pointing at an internal `ClusterIssuer`:

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: registry-core-mtls
spec:
  secretName: registry-core-mtls-certs
  issuerRef:
    name: registry-internal-ca
    kind: ClusterIssuer
  dnsNames:
    - registry-core.registry.svc.cluster.local
  duration: 2160h    # 90 days
  renewBefore: 360h  # renew 15 days before expiry
```

The cert is mounted into the pod as a volume. Services use `tls.Config.GetCertificate` to
reload certs on rotation without restart. `MTLS_CA_CERT_PATH`, `MTLS_CERT_PATH`, and
`MTLS_KEY_PATH` point to the mounted volume paths.

The `clientCreds()` helper in each service's `server.go` calls
`libs/auth/mtls.ClientTLSConfig()` when cert paths are set. With cert-manager in production
they are always set, so the insecure fallback never runs.

---

## 7. Storage

### Dev

MinIO runs as a sidecar container. `STORAGE_DRIVER=minio`, `STORAGE_MINIO_ENDPOINT=minio:9000`.

### Production

Set `STORAGE_DRIVER` to the target backend. Prefer IAM authentication over static credentials.

**AWS S3:**
```
STORAGE_DRIVER=s3
STORAGE_S3_BUCKET=my-registry-blobs
STORAGE_S3_REGION=us-east-1
# No AWS_ACCESS_KEY_ID — use pod IAM role (IRSA)
```

**GCP Cloud Storage:**
```
STORAGE_DRIVER=gcs
STORAGE_GCS_BUCKET=my-registry-blobs
STORAGE_GCS_PROJECT=my-project
# No GOOGLE_APPLICATION_CREDENTIALS — use Workload Identity
```

**Azure Blob:**
```
STORAGE_DRIVER=azure
STORAGE_AZURE_CONTAINER=registry-blobs
STORAGE_AZURE_ACCOUNT=myregistry
# No STORAGE_AZURE_ACCOUNT_KEY — use managed identity
```

Storage key layout is the same regardless of backend:
```
blobs/<tenant_id>/sha256/<first2>/<full_digest>
manifests/<tenant_id>/<repo_encoded>/<reference>
uploads/<tenant_id>/<upload_uuid>/parts/<part_num>
```

Server-side encryption must be enabled on the bucket/container (SSE-S3, CMEK, or SSE-Azure).

---

## 8. Database

### Dev

Single PostgreSQL container shared by all DB-owning services (separate logical databases).
`sslmode=prefer` because the dev container has no TLS cert.

### Production

Each DB-owning service connects to a managed PostgreSQL instance (RDS, Cloud SQL, etc.):

| Service | Database name | Notes |
|---|---|---|
| `registry-auth` | `registry_auth` | Users, API keys, lockout state |
| `registry-metadata` | `registry_metadata` | All registry metadata: repos, tags, manifests, blobs, scan results |
| `registry-proxy` | `registry_proxy` | Upstream configs, cached manifests |
| `registry-audit` | `registry_audit` | Append-only partitioned audit events |
| `registry-tenant` | `registry_tenant` | Tenant records, custom domain state |

All prod DSNs use `sslmode=require`. The config loader rejects `sslmode=disable` at startup
and emits a warning for anything other than `sslmode=require`.

`registry-metadata` optionally connects a second read-replica pool via `DB_DSN_REPLICA` for
list queries (`ListRepositories`, `ListTags`, `ListOrphanedBlobs`).

---

## 9. Service discovery

In Docker Compose, services find each other by container name (`registry-auth:50051`).

In Kubernetes, services find each other by K8s DNS:

```
registry-auth.registry.svc.cluster.local:50051
registry-storage.registry.svc.cluster.local:50051
registry-metadata.registry.svc.cluster.local:50051
```

These values go into the `*_GRPC_ADDR` env vars in each service's Helm values. No hardcoding
in Go code — all target addresses are configured via environment variables.

---

## 10. End-to-end: docker push

```
docker push registry.acme.com/myorg/myimage:v1.0
```

**Step 1 — Version check and auth challenge**
```
Docker  →  GET https://registry.acme.com/v2/
Traefik:   terminates TLS
           looks up registry.acme.com in Redis → tenant_id: <acme-uuid>
           injects X-Tenant-ID: <acme-uuid>
           forwards to registry-core

registry-core  →  401
  WWW-Authenticate: Bearer realm="https://auth.registry.example.com/auth/token",
                           service="registry-core",
                           scope="repository:myorg/myimage:push"
```

**Step 2 — Token fetch**
```
Docker  →  GET https://auth.registry.example.com/auth/token
             ?service=registry-core
             &scope=repository:myorg/myimage:push
           Authorization: Basic <base64(username:password)>

registry-auth:
  - parses X-Tenant-ID (injected by Traefik)
  - validates username + password (Argon2id hash check, lockout check)
  - checks IP rate limit (Redis sliding window)
  - issues JWT RS256, TTL 300s, scope = [push] on myorg/myimage

  →  200 { "token": "<JWT>", "expires_in": 300 }
```

**Step 3 — Blob upload (one per layer)**
```
Docker  →  POST https://registry.acme.com/v2/myorg/myimage/blobs/uploads/
           Authorization: Bearer <JWT>

registry-core:
  - validates JWT via registry-auth gRPC (mTLS); caches result in Redis by JTI
  - verifies tenant_id in JWT matches X-Tenant-ID header
  - checks per-tenant storage quota via registry-metadata gRPC
  - creates upload session (UUID) in Redis, TTL 1 hour
  →  202  Location: /v2/myorg/myimage/blobs/uploads/<uuid>

Docker  →  PATCH .../blobs/uploads/<uuid>  (chunked body)
registry-core  →  streams each chunk to registry-storage gRPC (PutBlob stream, mTLS)
registry-storage  →  writes to S3 PutObject

Docker  →  PUT .../blobs/uploads/<uuid>?digest=sha256:<expected>
registry-core:
  - verifies computed digest == declared digest; rejects mismatch with 400
  - calls registry-storage.StatBlob to confirm write
  - calls registry-metadata.LinkBlob to record the blob reference
  →  201  Docker-Content-Digest: sha256:<digest>
```

**Step 4 — Manifest push**
```
Docker  →  PUT https://registry.acme.com/v2/myorg/myimage/manifests/v1.0
           Content-Type: application/vnd.oci.image.manifest.v1+json

registry-core:
  - validates all layer digests exist in registry-metadata
  - calls registry-metadata.PutManifest + PutTag
  - publishes push.completed event to RabbitMQ (exchange: registry.events)
  →  201  Docker-Content-Digest: sha256:<manifest-digest>
```

---

## 11. End-to-end: docker pull

```
docker pull registry.acme.com/myorg/myimage:v1.0
```

Steps 1 and 2 are identical to the push flow — Docker gets a 401 challenge, fetches a token
(this time with `scope=repository:myorg/myimage:pull`), then re-requests with Bearer auth.

**Step 3 — Manifest fetch**
```
Docker  →  GET https://registry.acme.com/v2/myorg/myimage/manifests/v1.0
           Authorization: Bearer <JWT>
           Accept: application/vnd.oci.image.manifest.v1+json, ...

registry-core:
  - validates JWT
  - calls registry-metadata.GetManifest (Redis-cached, TTL 5m)
  →  200  Content-Type: application/vnd.oci.image.manifest.v1+json
          Docker-Content-Digest: sha256:<digest>
          Body: <manifest JSON>
```

**Step 4 — Blob fetch (one per layer)**
```
Docker  →  GET https://registry.acme.com/v2/myorg/myimage/blobs/sha256:<digest>
           Authorization: Bearer <JWT>

registry-core:
  - calls registry-storage.GetBlob (streaming gRPC)
  - streams response chunks directly to Docker client (never buffered in memory)
  →  200  Content-Type: application/octet-stream
          Docker-Content-Digest: sha256:<digest>
          Body: <layer bytes streamed>
```

---

## 12. End-to-end: pull-through proxy cache

```
docker pull proxy.registry.example.com/cache/dockerhub/library/alpine:3.20
```

The proxy participates in the same Docker token-auth flow as `registry-core`. Docker handles
it automatically after `docker login proxy.registry.example.com`.

**Step 1 — Auth challenge**
```
Docker  →  GET https://proxy.registry.example.com/v2/
registry-proxy  →  401
  WWW-Authenticate: Bearer realm="https://auth.registry.example.com/auth/token",
                           service="registry-proxy"
```

**Step 2 — Token fetch**

Same as above — Docker calls `registry-auth` with the proxy's service name.

**Step 3 — Manifest request (first hit)**
```
Docker  →  GET https://proxy.registry.example.com/v2/cache/dockerhub/library/alpine/manifests/3.20
           Authorization: Bearer <JWT>

registry-proxy:
  - validates JWT via registry-auth gRPC
  - looks up "dockerhub" upstream config for this tenant
  - queries proxy_manifests DB: cache miss (first request)
  - fetches manifest from https://registry-1.docker.io
    (with decrypted upstream credentials if auth_type != "none")
  - streams manifest to Docker client immediately
  - in background goroutine: calls repo.UpsertManifest to cache in proxy_manifests
  →  200  Docker-Content-Digest: sha256:<digest>
```

**Step 3 — Manifest request (subsequent hits)**
```
registry-proxy:
  - queries proxy_manifests DB: cache hit, not expired
  - serves from DB without touching Docker Hub
  →  200  (served locally, no upstream network call)
```

**Step 4 — Blob fetch**
```
Docker  →  GET https://proxy.registry.example.com/v2/cache/dockerhub/library/alpine/blobs/sha256:<digest>

registry-proxy:
  - calls registry-storage.BlobExists: present (already cached from previous pull)
  - streams directly from registry-storage to Docker client
  →  200  (served from S3/MinIO, no upstream network call)
```

On first blob fetch, the proxy uses `io.TeeReader` to simultaneously stream to the client
and store to `registry-storage` in a background goroutine. If the background store fails,
a `store.queued` RabbitMQ event is published for durable retry (dead-lettered after 3 attempts).

---

## 13. Async pipeline after a push

After `registry-core` publishes `push.completed` to RabbitMQ, three consumers fire in parallel:

```
RabbitMQ exchange: registry.events  (topic, durable, Quorum Queues)
routing key: push.completed

├── registry-scanner  (queue: scanner.push.completed)
│     1. Creates scan record in registry-metadata (status: pending)
│     2. Fetches manifest → extracts layer digests
│     3. Worker pool (SCANNER_WORKER_COUNT, default 4) invokes Trivy plugin via stdin/stdout JSON-RPC
│     4. Trivy fetches layers from registry-storage
│     5. Updates scan result in registry-metadata
│     6. Publishes scan.completed event
│          ├── registry-metadata: updates scan status
│          └── registry-webhook: triggers scan.completed webhook delivery
│
│     If CRITICAL findings + tenant policy requires blocking:
│          → updates tag status to "blocked" in registry-metadata
│          → publishes scan.policy_blocked event
│
├── registry-audit  (queue: audit.push.completed)
│     Writes immutable audit record (append-only, FORCE RLS, registry_audit_app role)
│
└── registry-webhook  (queue: webhook.push.completed)
      For each registered webhook endpoint for this tenant:
        - Signs payload with HMAC-SHA256 (X-Registry-Signature header)
        - Validates destination URL is not a private IP (SSRF protection)
        - Delivers via HTTPS only
        - Retries with exponential backoff: 5s → 30s → 5m → 30m → 2h
        - After 5 failures: dead-letters, notifies tenant admin
```

---

## 14. Custom domain provisioning

```
Tenant submits: registry.acme.com
        │
        ▼
registry-tenant.RegisterDomain()
  - validates RFC-1123 hostname format
  - generates DNS TXT token: _registry-verify.registry.acme.com TXT <token>
  - stores domain record: status=pending
        │
        ▼
Tenant adds TXT record to their DNS
        │
        ▼
registry-tenant domain worker (background goroutine, per-domain poll)
  - exponential backoff: <1h → 5min, 1-12h → 10min, >12h → 20min
  - polls net.LookupTXT("_registry-verify.registry.acme.com")
  - at 24h: sends "pending" notification (logged)
  - at 47h: sends "failed" notification
  - at 48h: marks domain as expired
        │ (on verified)
        ▼
  - marks domain: status=verified
  - triggers cert-manager ACME certificate issuance for registry.acme.com
  - updates Traefik routing table in Redis:
      key:   domain:registry.acme.com
      value: tenant_id:<uuid>  (no TTL — permanent until domain removed)
        │
        ▼
docker login registry.acme.com  ← works
```

---

## 15. Scaling

Each service has an HPA in its Helm chart. Scale-out triggers:

| Service | Primary scale signal | Notes |
|---|---|---|
| `registry-core` | CPU + active uploads gauge | Stateless; scales freely |
| `registry-auth` | CPU + request rate | Stateless; scales freely |
| `registry-proxy` | CPU | Stateless; upstream creds in DB |
| `registry-metadata` | CPU | Scales with read replica for list queries |
| `registry-storage` | CPU | Stateless; all state in S3/GCS/Azure |
| `registry-scanner` | RabbitMQ queue depth (scanner.push.completed) | Each pod runs a worker pool |
| `registry-webhook` | RabbitMQ queue depth (webhook.*) | Each pod is a consumer |
| `registry-audit` | RabbitMQ queue depth (audit.*) | Append-only writes; scales freely |
| `registry-gc` | — | Runs as a scheduled CronJob, not a long-running Deployment |
| `registry-tenant` | CPU | Low traffic; typically 1-2 replicas |

`registry-gc` is the only service that runs as a `CronJob` rather than a `Deployment`.
Nightly by default. GC uses PostgreSQL advisory locks (`pg_try_advisory_lock`) so concurrent
replicas skip tenants already being collected by another pod.

`registry-metadata` is the only service with a read-replica pool. `DB_DSN_REPLICA` points to
the replica endpoint; list queries route there automatically. Write queries always use the
primary.

---

## 16. Key production env vars per service

Only the values that differ from dev defaults are listed. All services also need `MTLS_*`,
`OTEL_*`, `REDIS_*`, and `DB_DSN` set.

### registry-auth
```
AUTH_REALM is not set on auth itself — it's set on core and proxy.
JWT_PRIVATE_KEY_B64=<from secret store>
JWT_PUBLIC_KEY_B64=<from secret store>
JWT_KEY_ID=<from secret store>
TRUSTED_PROXY_CIDRS=10.0.0.0/8          # K8s pod CIDR — enables correct client IP extraction
DEV_DEFAULT_TENANT_ID=                   # unset in production — all requests must have X-Tenant-ID
```

### registry-core
```
AUTH_REALM=https://auth.registry.example.com/auth/token
AUTH_GRPC_ADDR=registry-auth.registry.svc.cluster.local:50051
STORAGE_GRPC_ADDR=registry-storage.registry.svc.cluster.local:50051
METADATA_GRPC_ADDR=registry-metadata.registry.svc.cluster.local:50051
```

### registry-proxy
```
AUTH_REALM=https://auth.registry.example.com/auth/token
AUTH_GRPC_ADDR=registry-auth.registry.svc.cluster.local:50051
STORAGE_GRPC_ADDR=registry-storage.registry.svc.cluster.local:50051
CREDENTIAL_KEY_HEX=<from secret store>
```

### registry-storage
```
STORAGE_DRIVER=s3                        # or gcs / azure
STORAGE_S3_BUCKET=my-registry-blobs
STORAGE_S3_REGION=us-east-1
# No static credentials — pod uses IRSA / Workload Identity
```

### registry-signer
```
SIGNER_KEY_BACKEND=vault                 # or awskms / gcpkms / azurekms
VAULT_ADDR=https://vault.internal:8200
VAULT_COSIGN_PATH=secret/data/signer/cosign
# Vault auth via K8s service account token (not root token)
```

### registry-metadata
```
DB_DSN=postgres://...?sslmode=require
DB_DSN_REPLICA=postgres://...?sslmode=require   # read replica; optional
```

---

## 17. What is not yet production-ready

| Item | Status | Notes |
|---|---|---|
| Helm charts tested against real cluster | Not yet | Charts have correct structure; untested. Sprint 5. |
| Terraform for cloud infra | Not started | `infra/terraform/` is present but empty. Decision #10. |
| OCI conformance suite in CI | Not yet | `make test-conformance` in services/core must pass before release. Sprint 4. |
| Integration tests (testcontainers) | Not yet | Sprint 4. |
| Prometheus metrics wired | Not yet | `/metrics` returns 200 with no data. Sprint 4. |
| `sslmode=require` for dev Postgres | Accepted risk | Dev compose uses `sslmode=prefer`. Never use compose DSNs in prod. |
| RBAC at org / repo / tag level | Scaffold only | Token scopes enforced; per-object ACL not implemented. Post Sprint 4. |
| UI | Not started | Vite + React scaffold exists, no routes or components. Post Sprint 4. |

---

> For the full request/response sequence diagrams including multi-arch manifest index handling,
> see [ARCHITECTURE.md](ARCHITECTURE.md).
>
> For local development setup, see [local-setup.md](local-setup.md).
>
> For open security issues, see [security.md](security.md).
