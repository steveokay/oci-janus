# Local Setup Guide

> This file is tracked in git. Do not commit real secret values — keep placeholders only.

## Prerequisites

- Docker Desktop (includes Compose v2)
- `openssl` in your terminal

---

## Step 1 — Generate secrets

Run from the **repo root**. Copy each output into the files in Step 2.

```bash
# AES keys for proxy + webhook (one command each)
openssl rand -hex 32   # → PROXY_CREDENTIAL_KEY_HEX
openssl rand -hex 32   # → WEBHOOK_CREDENTIAL_KEY_HEX

# Signer ECDSA P-256 key pair
openssl ecparam -name prime256v1 -genkey -noout \
  | openssl pkcs8 -topk8 -nocrypt \
  | base64 -w0          # → SIGNER_COSIGN_PRIVATE_KEY

# Derive public key from the private key you just set:
echo "<paste SIGNER_COSIGN_PRIVATE_KEY here>" | base64 -d \
  | openssl pkey -pubout \
  | base64 -w0          # → SIGNER_COSIGN_PUBLIC_KEY

# Auth JWT RS256 key pair
openssl genrsa -out /tmp/jwt.pem 4096
openssl rsa -in /tmp/jwt.pem -pubout -out /tmp/jwt.pub
base64 -w0 /tmp/jwt.pem    # → JWT_PRIVATE_KEY_B64
base64 -w0 /tmp/jwt.pub    # → JWT_PUBLIC_KEY_B64
uuidgen | tr '[:upper:]' '[:lower:]'   # → JWT_KEY_ID
```

---

## Step 2 — Fill in the `.env` files

**`infra/docker-compose/.env`** (copy from `.env.example`):
```
PROXY_CREDENTIAL_KEY_HEX=<from above>
WEBHOOK_CREDENTIAL_KEY_HEX=<from above>
SIGNER_COSIGN_PRIVATE_KEY=<from above>
SIGNER_COSIGN_PUBLIC_KEY=<from above>
# leave the rest as-is (registry/registry, minioadmin/minioadmin, etc.)
```

**`services/auth/.env`** (copy from `services/auth/.env.example`):
```
JWT_PRIVATE_KEY_B64=<from above>
JWT_PUBLIC_KEY_B64=<from above>
JWT_KEY_ID=<from above>
# leave everything else — compose overrides DB_DSN, REDIS_ADDR, MTLS_*, OTEL_*
```

**`services/core/.env`** (no changes needed — compose overrides all values):
```bash
cp services/core/.env.example services/core/.env
```

---

## Step 3 — Start the stack

```bash
cd infra/docker-compose
docker compose up -d
```

On first boot `cert-init` runs first and generates dev mTLS certs (world-readable, uid 65532-compatible) into the `certs_data` volume. Then infra starts (postgres, redis, rabbitmq, minio, jaeger, vault), then all 12 services in dependency order. Allow ~90 seconds for all containers to reach healthy state.

**Notes:**
- Dev postgres has no TLS cert, so DSNs use `sslmode=prefer` (falls back to plaintext). Production injects its own `DB_DSN` with `sslmode=require` via K8s Secret.
- OTEL traces go to Jaeger at `jaeger:4317` (no `http://` prefix — gRPC endpoint).
- All service images contain a compiled `/healthcheck` binary (no shell available — distroless base).

---

## Step 4 — Verify everything is healthy

```bash
docker compose ps          # all 16 containers should show "healthy" or "running"

# Healthz check for every service
for port in 8080 8081 8082 8083 8084 8086 8087 8088 8089 8090; do
  echo -n "port $port: "; curl -sf http://localhost:$port/healthz && echo OK || echo FAIL
done
```

Expected: 10 OK responses (auth, core, storage, metadata, proxy, signer, webhook, audit, gc, tenant). Scanner is optional (requires plugin binary). Gateway is Traefik and doesn't serve `/healthz` on a standard port.

---

## Step 5 — Smoke-test the OCI flow

### 5a — Allow the insecure local registry in Docker Desktop

Docker Desktop must trust `localhost:8081` before any push/pull. Do this **once**:

1. Open Docker Desktop → Settings → Docker Engine
2. Add to the JSON:
   ```json
   "insecure-registries": ["localhost:8081"]
   ```
3. Click **Apply & Restart**

Without this, Docker attempts TLS and the push fails before it even reaches the auth step.

### 5b — Log in to the local registry

```bash
docker login localhost:8081 -u admin -p password
```

This stores a credential so Docker can exchange it for a bearer token via
`http://localhost:8080/auth/token` (the `AUTH_REALM` configured in docker-compose).

> **Why `localhost:8080`?** The `WWW-Authenticate` header returned by `registry-core`
> on a 401 tells Docker where to fetch a token. It must point to a URL reachable from
> your host machine, not an internal Compose hostname. `AUTH_REALM` controls this value;
> it defaults to `http://localhost:8080/auth/token` in the Compose stack.

### 5c — Push and pull a test image

```bash
# 1. Check the OCI version endpoint (should return {} HTTP 200)
curl -sf http://localhost:8081/v2/
# → 401 Unauthorized (expected — confirms auth challenge is working)

TOKEN=$(curl -sf "http://localhost:8080/auth/token?service=registry-core&scope=repository:myorg/myimage:push,pull" \
  -u admin:password | jq -r .token)
curl -sf -H "Authorization: Bearer $TOKEN" http://localhost:8081/v2/
# → {}  (HTTP 200)

# 2. Push a test image
docker pull alpine:3.20
docker tag alpine:3.20 localhost:8081/myorg/alpine:3.20
docker push localhost:8081/myorg/alpine:3.20

# 3. Pull it back
docker pull localhost:8081/myorg/alpine:3.20
```

---

## Useful UIs

| UI | URL | Credentials |
|---|---|---|
| Jaeger traces | http://localhost:16686 | — |
| RabbitMQ | http://localhost:15672 | registry / registry |
| MinIO console | http://localhost:9001 | minioadmin / minioadmin |
| Vault | http://localhost:8200 | token: `dev-root-token` |

---

## Scanner (optional)

Scanner is not started by default — it requires an external plugin binary.

```bash
# Once you have a plugin binary and its SHA-256 checksum:
# Add to infra/docker-compose/.env:
#   SCANNER_PLUGIN_PATH=/plugins/trivy-wrapper
#   SCANNER_PLUGIN_CHECKSUM=<sha256sum output>
# Mount the binary into the container, then:
docker compose --profile scanner up -d registry-scanner
```

---

## Teardown

```bash
docker compose down          # stop, keep volumes
docker compose down -v       # stop + wipe all data (certs, postgres, minio, etc.)
```
