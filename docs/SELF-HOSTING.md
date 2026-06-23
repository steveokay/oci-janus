# SELF-HOSTING.md — Run OCI-Janus in your own infrastructure

> **Audience:** anyone forking this repo to deploy their own
> instance — a homelab user with a single VM, a startup running on
> Docker Compose, or an enterprise team deploying to Kubernetes.
>
> OCI-Janus is built to be self-hosted under Apache 2.0. There's no
> proprietary "cloud" version with hidden features. Running it
> yourself gives you the full feature set: multi-tenancy, custom
> domains, signed-image admission, audit-log streaming, vulnerability
> scanning, RBAC, retention, the works.

---

## 1. Choose a deployment path

| Path | Best for | Time to first push | Operational complexity |
|---|---|---|---|
| **Docker Compose** (§3) | Single-host deployments, homelab, dev environments, small teams (≤50 users) | ~10 min | Low |
| **Kubernetes via Helm** (§4) | Production deployments, multi-replica, cloud providers, regulated workloads | ~30 min – 2 hours | Medium-High |
| **Fork + customise** (§5) | You want to vendor, brand, or modify before deploying | varies | Varies |

Pick the simplest path that fits — you can always migrate later.

---

## 2. Fork and clone

```bash
# 1. Fork on GitHub (button in the top-right of the repo page)
# 2. Clone YOUR fork (replace <your-org>)
git clone https://github.com/<your-org>/oci-janus.git
cd oci-janus

# 3. Set up the Go workspace
go work sync
```

You're now working on a copy you fully control. Push your customisations to your fork; pull from upstream (`git remote add upstream https://github.com/steveokay/oci-janus.git`) when you want to track new features or security fixes.

---

## 3. Docker Compose path

### Prerequisites

- Docker 24+ and Docker Compose v2
- Go 1.23+ (only needed if you're going to modify code; not needed to just run it)
- 8 GB RAM minimum (10 GB recommended) — the dev stack runs ~17 containers
- Ports 8080-8091, 50051-50060, 5432 (Postgres), 6379 (Redis), 5672 + 15672 (RabbitMQ), 9000 + 9001 (MinIO), 8200 (Vault), 16686 (Jaeger UI) free on the host

### Step-by-step

```bash
# 1. Generate the JWT signing key (RSA 4096)
openssl genrsa -out /tmp/jwt.pem 4096
openssl rsa -in /tmp/jwt.pem -pubout -out /tmp/jwt.pub
JWT_PRIVATE=$(base64 -w0 < /tmp/jwt.pem)
JWT_PUBLIC=$(base64 -w0 < /tmp/jwt.pub)

# 2. Generate the AES-256-GCM keys (32 bytes = 64 hex chars)
CREDENTIAL_KEY=$(openssl rand -hex 32)   # registry-proxy upstream creds
SSO_KEY=$(openssl rand -hex 32)          # registry-auth OAuth client secrets
AUDIT_EXPORT_KEY=$(openssl rand -hex 32) # registry-audit SIEM streaming secrets

# 3. Set up the .env file
cd infra/docker-compose
cp .env.example .env
# Edit .env — change every POSTGRES_PASSWORD, RABBITMQ_DEFAULT_PASS,
# MINIO_ROOT_PASSWORD, etc. Paste your generated keys into the
# corresponding env vars. **DO NOT** keep the defaults.

# 4. (Optional) Set your platform domain
# If you want pretty hostnames like "<tenant>.registry.example.com",
# edit PLATFORM_BASE_DOMAIN in .env. Default: registry.localhost
# DNS for *.registry.localhost resolves to 127.0.0.1 if you add it to
# /etc/hosts (or use dnsmasq).

# 5. Bring up the stack
docker compose up -d

# 6. Wait for everything to be healthy (~30s)
docker compose ps
# All "(healthy)" → ready

# 7. Verify
curl http://localhost:8081/v2/                # 401 + WWW-Authenticate = correct
curl http://localhost:8080/.well-known/jwks.json | jq .

# 8. Log in to the dashboard at http://localhost:5173
# (Start the frontend dev server separately for now —
#  see step 9 below to bake the FE into a container.)

# 9. Push your first image
docker login localhost:8081 -u admin -p <your-admin-password>
docker tag alpine:3.20 localhost:8081/dev/alpine:3.20
docker push localhost:8081/dev/alpine:3.20
```

### Frontend in production

The dev stack runs the dashboard via `npm run dev`. For your own deployment you'd:

```bash
cd frontend
npm install
npm run build           # produces frontend/dist/
# Serve frontend/dist/ via nginx, Caddy, the gateway, or a static-file CDN
```

A future PR will likely add a `registry-ui` Dockerfile + compose service so you don't have to wire this yourself. Until then, treat the frontend as a static bundle you serve however you'd serve any SPA.

### Persisting data

The default compose file uses named Docker volumes for Postgres, MinIO, and RabbitMQ. They survive `docker compose down`. For real deployments:

| Service | Volume | What it stores |
|---|---|---|
| `postgres` | `postgres_data` | All metadata, auth, audit, scan, retention, webhook, tenant DBs |
| `minio` | `minio_data` | Image blobs (in dev — use real S3 in prod, see §3 storage backend) |
| `rabbitmq` | `rabbitmq_data` | Quorum queues, DLX messages, message metadata |
| `vault` | `vault_data` | Signing keys (in dev — use prod Vault in prod, see [`docs/SIGNING.md`](SIGNING.md)) |

Back these volumes up like any other database. The Postgres DBs are the most important — they hold all your operational state.

### Storage backend

The default is MinIO (S3-compatible, runs locally). For real deployments, point `services/storage` at AWS S3, GCS, or Azure Blob:

```bash
# .env additions for AWS S3
STORAGE_DRIVER=s3
STORAGE_S3_BUCKET=mycompany-registry
STORAGE_S3_REGION=us-east-1
AWS_ACCESS_KEY_ID=<your-access-key>
AWS_SECRET_ACCESS_KEY=<your-secret>
# IAM role-based auth is preferred — use the AWS-IAM-role env vars instead
```

Full driver matrix in [`docs/SERVICES.md`](SERVICES.md) §4.

### Reverse proxy + TLS

For anything past localhost dev, put a reverse proxy in front of `registry-gateway`:

| Proxy | Config example |
|---|---|
| **Caddy** | `registry.example.com { reverse_proxy localhost:8443 }` — auto-HTTPS via Let's Encrypt |
| **nginx** | Standard `proxy_pass http://localhost:8443` with manual cert management |
| **Traefik** | Already used internally as `registry-gateway`; can be a customer-facing proxy too |
| **Cloudflare Tunnel** | Zero-trust front-door; good for homelab without static IP |

The gateway itself listens on `:8443` (HTTP). TLS termination is normally upstream of it.

### Customising the dashboard branding

OCI-Janus is Apache 2.0 — you can rebrand it. The bits that matter:

| What | Where |
|---|---|
| Dashboard logo + name | `frontend/src/components/brand/` (logo SVG + the `Brand` component) |
| Email name in audit-export | `services/audit/internal/export/export.go` — `Hostname` / `AppName` constants |
| Syslog SD-PARAM enterprise number | `services/audit/internal/export/export.go` — `PEN 53430` is a placeholder; replace with your real IANA PEN if you have one |
| PRs comment author | Per-developer; no central config |

The Apache 2.0 license does require you to preserve the original `LICENSE` file and any `NOTICE` file in distributions, but you can otherwise rebrand freely.

---

## 4. Kubernetes path

The Helm chart lives at `infra/helm/registry/`. It's an umbrella chart with per-service sub-charts.

### Prerequisites

- Kubernetes 1.27+
- Helm 3.14+
- `cert-manager` for internal mTLS rotation (optional but recommended)
- External Secrets Operator + Vault / AWS Secrets Manager / GCP Secret Manager for production-grade secret rotation (optional but recommended)
- Persistent storage class for Postgres, RabbitMQ, MinIO StatefulSets

### Install

```bash
# 1. Create the namespace + secrets (production)
kubectl create namespace registry
# Populate secrets via ExternalSecret CRs pointing at your secret backend.
# See infra/helm/registry/values.prod.yaml.example for the secret-name layout.

# 2. Install
helm upgrade --install registry ./infra/helm/registry \
  -f infra/helm/registry/values.prod.yaml \
  --namespace registry

# 3. Check rollout
kubectl -n registry get pods
kubectl -n registry rollout status deployment/registry-auth
kubectl -n registry rollout status deployment/registry-core
# ... etc for each service

# 4. Run migrations (one-time, per service)
kubectl -n registry exec deployment/registry-auth -- goose -dir /migrations up
# ... etc for each DB-owning service
# (The compose path runs migrations at startup; the Helm chart can do the
#  same via initContainers if you set the values flag — see DEPLOYMENT.md.)

# 5. Verify
kubectl -n registry port-forward svc/registry-gateway 8443:8443 &
curl https://localhost:8443/v2/ -k
```

Full chart layout + values reference in [`docs/DEPLOYMENT.md`](DEPLOYMENT.md). Production wiring (cert-manager, External Secrets, TLS, NetworkPolicies) is documented in [`prod-flow.md`](../prod-flow.md).

---

## 5. Fork + customise

You can fork OCI-Janus and modify before deploying. Common reasons:

| You want to | Where to look |
|---|---|
| Disable a feature you don't need (e.g. SAML, scanner) | Each service's `internal/server/server.go` — most features are wired conditionally on env vars being set. The simplest path is to just NOT set the env var and the feature stays dormant. |
| Add a custom scanner adapter | `infra/scanner-plugins/<your-adapter>/` — follow the dev-stub / trivy-adapter pattern. The JSON-RPC contract is in [`docs/SCANNER.md`](SCANNER.md). |
| Add a new wire format for audit streaming | `services/audit/internal/export/export.go` — add a renderer function + a case in `render()`. Update the format-enum CHECK constraint in `services/audit/migrations/20260623100000_audit_export_configs.sql`. |
| Customise RBAC roles | `services/auth/migrations/20260614000001_create_rbac.sql` seeds the four canonical roles. Add new roles via a new migration; teach `services/management` to gate on the new role IDs. |
| Add a new dashboard surface | `frontend/src/routes/_authenticated.<your-route>.tsx` — TanStack Router file-based. Backend route in the BFF. See [`CLAUDE.md`](../CLAUDE.md) §2 for the per-service layout. |
| Replace the gateway (e.g. use Envoy) | `registry-gateway` is a thin Traefik wrapper. Replace with whatever you want; the only contract is "look up Host header → tenant_id, inject `X-Tenant-ID`, forward to the right service." |
| Embed in a larger product | The Go modules are import-able; you could in principle compose `services/core` into your own binary. Not the typical path — usually easier to run as a separate stack. |

### Staying in sync with upstream

```bash
# Add the upstream remote (do this once)
git remote add upstream https://github.com/steveokay/oci-janus.git

# Periodically pull upstream changes into your fork
git fetch upstream
git checkout main
git merge upstream/main           # or rebase, depending on your preference
git push origin main
```

For deeper customisations, consider keeping your changes on a long-lived branch (`my-org/main`) and cherry-picking upstream commits. The codebase is small enough that this stays manageable.

---

## 6. Operating the deployment

Day-2 operations docs:

| What | Where |
|---|---|
| Logs | Every service emits structured JSON to stdout. `slog` with `trace_id`, `span_id`, `tenant_id`, `service` fields. Ship to Loki / Datadog / Splunk via your usual log shipper. |
| Metrics | `/metrics` on port `:9090` per service (separate from business port — see CLAUDE.md §10). Scrape with Prometheus; dashboards in `infra/docker-compose/prometheus/` cover the common signals. |
| Traces | OpenTelemetry → Jaeger / Tempo / Datadog via `OTEL_EXPORTER` env var. |
| Backups | Postgres: `pg_dump` per DB (auth, metadata, audit, etc.) on a schedule. Object storage: native backup features (S3 versioning + replication, MinIO mirror). |
| Upgrades | Pull upstream changes, run migrations (`goose up` per service), restart services one at a time. The proto contract is backward-compatible across minor versions; major versions get a migration guide. |
| Scaling | Stateless services (auth, core, management, scanner, signer, webhook, gc, gateway) scale horizontally. Stateful (Postgres, MinIO, RabbitMQ) scale via their own tooling. |

The platform is designed to run unattended. The default dev compose stack has run for weeks at a time during development without intervention; production deployments with cert-manager + External Secrets + a real Postgres + a real S3 should be similarly low-touch.

---

## 7. Common pitfalls

| Symptom | Likely cause |
|---|---|
| `docker compose up -d` fails on cert-init | The cert-init container needs to run successfully BEFORE other services can mount the certs. Check `docker logs docker-compose-cert-init-1`. |
| "Couldn't register, try again" on first custom domain | Dev tenant row missing in `services/tenant`'s DB. Fixed by migration `20260623120000_seed_dev_tenant.sql`; if you removed that migration in your fork, re-add or insert the dev tenant manually. |
| Vault dev mode loses signing keys on restart | Dev Vault is in-memory by default. For production, use a real Vault with persistent storage — see [`docs/SIGNING.md`](SIGNING.md). |
| Webhook deliveries failing in dev | The dev compose stack's webhook test endpoint is on the host network; from inside a container, use `http://host.docker.internal:<port>` (Linux requires `--add-host=host.docker.internal:host-gateway`). |
| Cross-origin browser errors | The CORS allowlist in `services/management` only allows the configured `CORS_ALLOWED_ORIGINS`. Add your dashboard origin to that env var. |

---

## 8. Where to get help

- **Documentation:** start with the [docs map](../README.md#documentation-map) in the README.
- **Bugs:** [GitHub Issues](https://github.com/steveokay/oci-janus/issues).
- **Questions:** [GitHub Discussions](https://github.com/steveokay/oci-janus/discussions).
- **Security:** [private vulnerability reporting](https://github.com/steveokay/oci-janus/security/advisories/new). Don't open a public issue for security issues.

If you've stood up a deployment and want to share what you built, please open a Discussion. Hearing about real-world use makes the maintenance work feel worth doing.

---

> **Last updated:** 2026-06-23.
> **Maintainer:** see `git log -- docs/SELF-HOSTING.md`.
