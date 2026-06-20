# Deployment Reference

## Docker Compose (`infra/docker-compose/`)

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
- Jaeger + otel-collector + Prometheus (default OTEL backend for local dev)
- HashiCorp Vault in dev mode (for signer key storage — see [`SIGNING.md`](SIGNING.md) for the full key-lifecycle reference)

## Kubernetes (`infra/helm/`)

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

## Per-service Helm chart requirements

Each service chart must include:

- `Deployment` with `readinessProbe` and `livenessProbe` (gRPC health check).
- `PodDisruptionBudget` (`minAvailable: 1`).
- `HorizontalPodAutoscaler` (CPU + custom metric: queue depth).
- `ServiceAccount` with minimal RBAC.
- `NetworkPolicy` — allowlist only (default deny all).
- `SecretProviderClass` (External Secrets Operator) for secrets.
- Resource requests and limits on every container — no defaults.

## NetworkPolicy rules

- `registry-core` → ingress from `registry-gateway` only.
- `registry-metadata` → ingress from `registry-core`, `registry-proxy`, `registry-scanner`, `registry-gc` only.
- `registry-storage` → ingress from `registry-core`, `registry-proxy`, `registry-scanner`, `registry-gc` only.
- No service has unrestricted egress except `registry-proxy` (fetches from internet) and `registry-webhook` (calls external URLs).

## Health Check Endpoints

Every service implements:

- `grpc.health.v1.Health/Check` — for K8s readiness/liveness.
- `GET /healthz` (HTTP, internal port) — for load balancers and Compose healthcheck.
- `GET /metrics` (HTTP, internal port `:9090`) — Prometheus scrape (SEC-025: dedicated port so NetworkPolicy can allow Prometheus without exposing the business port).
