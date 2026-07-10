# Deploying OCI-Janus

There are two supported ways to run the platform:

- **Docker Compose** — a single host. Great for evaluation, dev, and small
  self-hosted deployments.
- **Kubernetes (Helm)** — the production path, with per-service scaling,
  NetworkPolicies, and externalised secrets.

Both run the same images and the same 14 services. This page is the guided
walkthrough; [Deployment reference](DEPLOYMENT.md) is the terse checklist, and
[Environment reference](env-reference.md) lists every variable.

## Infrastructure dependencies

Whichever path you pick, the platform needs:

| Dependency | Purpose |
|---|---|
| **PostgreSQL 16** | Per-service databases (metadata, auth, tenant, proxy, webhook, audit, scanner) |
| **Redis 7** (or Valkey) | Token/JTI revocation, API-key cache, rate limiting |
| **RabbitMQ 3.13** (quorum queues) | The `registry.events` topic exchange |
| **Object store** | Blobs — MinIO / S3 / GCS / Azure, or filesystem for dev (see [Storage backends](integrations/storage.md)) |
| **Vault or a cloud KMS** | Cosign signing keys (dev Vault is fine locally; see [Image signing](SIGNING.md)) |

## Docker Compose

Everything lives under `infra/docker-compose/`:

```
docker-compose.yml           # all services + infra, dev configuration
docker-compose.override.yml  # local developer overrides (gitignored)
otel-collector.yml           # OTEL collector wiring
prometheus.yml               # Prometheus scrape config
.env.example                 # required variables, no secret values
```

The Compose stack bundles the infrastructure for you — Postgres, Redis,
RabbitMQ, MinIO, Vault (dev mode), and the Jaeger + otel-collector + Prometheus
observability trio.

### 1. Configure secrets

Copy `.env.example` to `.env` and fill in the AES-256-GCM key-encryption keys
(KEKs). Generate each with:

```bash
openssl rand -hex 32
```

The KEKs (all 64 hex chars, swept by the per-service `rotate-kek` tool) are
listed in [Self-hosting](SELF-HOSTING.md) and
[Deployment reference](DEPLOYMENT.md#secrets). A channel whose KEK is unset stays
disabled — it fails closed rather than starting insecure.

### 2. Bring it up

```bash
docker compose -f infra/docker-compose/docker-compose.yml up -d
make dev-bootstrap    # create the first tenant + admin
```

Optional capabilities ship behind Compose **profiles** — enable only what you
need:

```bash
# Real vulnerability scanning (Trivy adapter):
docker compose -f infra/docker-compose/docker-compose.yml --profile scanner up -d

# Clair as the scan backend instead:
docker compose -f infra/docker-compose/docker-compose.yml --profile clair up -d

# The read-only MCP server for AI assistants (see the MCP guide):
docker compose -f infra/docker-compose/docker-compose.yml --profile mcp up -d
```

Local-only overrides (ports, mounts) go in the gitignored
`docker-compose.override.yml`, which Compose merges automatically.

## Kubernetes (Helm)

The umbrella chart lives at `infra/helm/registry` (`registry`, chart version
`0.1.0`). It bundles a subchart per service under `charts/`, sharing a common
`global` block. Images are pulled from `ghcr.io/steveokay` by default.

```
infra/helm/registry/
├── Chart.yaml          # umbrella + per-service subchart dependencies
├── values.yaml         # defaults, no secrets
├── values.prod.yaml    # production overrides, no secrets
└── charts/             # one subchart per service
```

### Values you set

`values.yaml` is the shape; the load-bearing keys:

```yaml
global:
  image:
    registry: ghcr.io/steveokay   # where images are pulled from
    tag: latest                   # pin to a release tag in production
    pullPolicy: IfNotPresent
  mtls:
    enabled: true
    caCertSecret: registry-mtls-ca   # cert-manager-issued CA (see below)
  otel:
    exporter: jaeger
    endpoint: jaeger-collector:4317
    environment: production

auth:   { replicaCount: 2, resources: { … } }
core:   { replicaCount: 3, resources: { … } }
metadata: { replicaCount: 2, resources: { … } }
storage:  { replicaCount: 2, driver: minio, resources: { … } }
# …one block per service
```

`values.prod.yaml` layers production overrides (replica counts, resources,
endpoints) on top. Keep **all** secrets out of both — they come from an external
store (below).

### Secrets & mTLS

- **App secrets** (DB DSNs, KEKs, upstream creds) are supplied via a
  `SecretProviderClass` / **External Secrets Operator** pointing at Vault or a
  cloud secrets manager — never in `values.yaml`.
- **mTLS certs** are issued by **cert-manager** against an internal CA; the CA
  bundle is referenced by `global.mtls.caCertSecret`. Certs hot-reload on
  renewal without a restart.

### Install

```bash
cd infra/helm/registry
helm dependency build                                   # resolve subcharts
helm upgrade --install registry . \
  --namespace registry --create-namespace \
  --values values.prod.yaml
```

### Per-service chart requirements

Every service chart ships a `Deployment` with gRPC readiness/liveness probes, a
`PodDisruptionBudget` (`minAvailable: 1`), a `HorizontalPodAutoscaler` (CPU +
queue-depth), a minimal-RBAC `ServiceAccount`, a default-deny `NetworkPolicy`,
the `SecretProviderClass`, and resource requests/limits on every container. The
full checklist is in [Deployment reference](DEPLOYMENT.md#per-service-helm-chart-requirements).

### Scaling

Adjust `replicaCount` per service (data-plane `core` defaults highest at 3) and
let the HPAs scale on CPU and RabbitMQ queue depth. NetworkPolicies are
allowlist-only — for example `registry-core` accepts ingress from
`registry-gateway` only, and only `registry-proxy` (upstream fetches) and
`registry-webhook` (external callbacks) have unrestricted egress.

## Health & observability

Every service exposes:

- `grpc.health.v1.Health/Check` — Kubernetes readiness/liveness.
- `GET /healthz` (internal HTTP port) — load balancers and the Compose
  healthcheck.
- `GET /metrics` on the dedicated port **`:9090`** — Prometheus scrape, kept off
  the business port so a NetworkPolicy can grant Prometheus access without
  exposing the API (SEC-025).

Traces and metrics ship via OpenTelemetry; `global.otel.exporter` selects the
backend (`jaeger` / `tempo` / `datadog` / `stdout`). See
[Observability](OBSERVABILITY.md).

## Operations runbooks

Step-by-step operator procedures live under
[`infra/runbooks/`](https://github.com/steveokay/oci-janus/tree/main/infra/runbooks):

- [Bootstrap the first admin](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/bootstrap-first-admin.md)
- [Secret rotation](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/secret-rotation.md)
  · [KEK rotation](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/kek-rotation.md)
- [Disaster recovery](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/disaster-recovery.md)
- [MinIO encryption](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/minio-encryption.md)
  · [Scanner isolation](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/scanner-isolation.md)
- [Notary root-key ceremony](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/notary-root-key-ceremony.md)

For a broader production checklist (TLS, encryption at rest, hardening), see
[Self-hosting](SELF-HOSTING.md) and the
[Hardening checklist](HARDENING-CHECKLIST.md).
