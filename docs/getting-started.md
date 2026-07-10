# Quick start

Bring up the full stack locally with Docker Compose, create the first admin, and
push your first image. This mirrors the quick start in the repository
[`README`](https://github.com/steveokay/oci-janus/blob/main/README.md); for
production deployment see [Self-hosting](SELF-HOSTING.md).

## Prerequisites

- Docker + Docker Compose v2
- `make` (the dev targets wrap Compose and the bootstrap CLI)
- Roughly 4 GB of free RAM for the full stack (Postgres, Redis, RabbitMQ, MinIO,
  Vault, Jaeger, and the registry services)

## Bring up the stack

```bash
git clone https://github.com/steveokay/oci-janus.git
cd oci-janus
make dev-certs              # generate self-signed mTLS certs for local services
docker compose -f infra/docker-compose/docker-compose.yml up -d
make dev-bootstrap          # create the first admin (admin / Admin1234!)
```

What just happened:

1. **`make dev-certs`** writes a local CA + per-service certs to `certs/`.
   Kubernetes deployments use cert-manager instead — see [Deployment](DEPLOYMENT.md).
2. **`docker compose up -d`** brings up Postgres, Redis, RabbitMQ, MinIO, Vault,
   Jaeger, and all 14 registry services on the `registry.events` topic exchange.
3. **`make dev-bootstrap`** runs `registry-auth bootstrap` inside the auth
   container to create the first tenant + admin user. It is idempotent — safe to
   re-run.

!!! warning "Dev credentials are for local use only"
    `admin / Admin1234!` is the default local bootstrap credential. Never use it
    outside a throwaway dev stack. Production bootstrap reads the password from
    stdin — see the [bootstrap runbook](https://github.com/steveokay/oci-janus/blob/main/infra/runbooks/bootstrap-first-admin.md).

## Push and pull an image

```bash
docker login localhost:8081 -u admin -p Admin1234!
docker tag alpine:latest localhost:8081/library/alpine:latest
docker push localhost:8081/library/alpine:latest
docker pull localhost:8081/library/alpine:latest
```

The OCI `/v2/` API is served at `http://localhost:8081`. The dashboard is at
`http://localhost:5173` (Vite dev server) — sign in with the same admin
credentials to see the image you just pushed, browse tags, and review audit
events.

## Next steps

- **[Self-hosting](SELF-HOSTING.md)** — production env vars, secrets, and the
  key-encryption keys (KEKs) each service needs.
- **[Vulnerability scanning](SCANNER.md)** — wire up Trivy/Grype/Clair and
  per-tenant scan policies.
- **[Image signing](SIGNING.md)** — Cosign signing and verification.
- **[Authentication](AUTH.md)** — JWT, API keys, SSO (OAuth + SAML), and RBAC.
- **[Migrating v1 → v2](MIGRATION-v1-to-v2.md)** — if you are upgrading an
  existing deployment.
