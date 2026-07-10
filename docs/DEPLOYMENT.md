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

## Secrets

This reference is intentionally env-var-light — each service's `.env.example`
is the authoritative list. The AES-256-GCM KEKs (all 64 hex chars, swept by
the per-service `rotate-kek` tool) are:

- `CREDENTIAL_KEY_HEX` (registry-proxy — upstream creds)
- `SSO_CREDENTIAL_KEY_HEX` (registry-auth — OAuth client secrets)
- `MFA_SECRET_KEY_HEX` (registry-auth — TOTP MFA secrets)
- `AUDIT_EXPORT_SECRETS_KEY_HEX` (registry-audit — SIEM streaming secrets)
- `NOTIFY_EMAIL_KEY_HEX` (registry-audit — email transport creds)
- `NOTIFY_WEBHOOK_KEY_HEX` (registry-audit — notification webhook secret)
- `PR_REGISTRY_KEY_HEX` (registry-metadata — FUT-023 PR-registry webhook secret)

In Compose these come from the `.env` file (generate with `openssl rand -hex 32`
— see [`SELF-HOSTING.md`](SELF-HOSTING.md) §3); in Kubernetes they are supplied
via the `SecretProviderClass` / External Secrets Operator wiring (below).

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
│       ├── registry-tenant/
│       └── registry-management/      # REST BFF for the dashboard (and CLI/Terraform); HTTP-only, no gRPC server
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
