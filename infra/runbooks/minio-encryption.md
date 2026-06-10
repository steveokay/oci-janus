# MinIO Server-Side Encryption Setup

> **Applies to:** Self-hosted MinIO used as the `registry-storage` backend.
> For AWS S3, GCS, or Azure Blob, encryption is configured at the bucket/storage-account level via provider console or Terraform.

---

## Overview

MinIO supports two SSE modes:

| Mode | Key source | Recommended for |
|---|---|---|
| SSE-S3 | MinIO-managed AES-256 per-object key, wrapped by a master key | Dev / staging |
| SSE-KMS | External KMS (Vault Transit, AWS KMS) via MinIO KES | Production |

All objects stored by `registry-storage` must be encrypted at rest per CLAUDE.md §8.

---

## 1. SSE-S3 (Dev / Staging)

### Configure master key

```bash
# Generate a 32-byte hex master key
MASTER_KEY=$(openssl rand -hex 32)

# Set on MinIO server via environment variable (before starting MinIO)
export MINIO_KMS_SECRET_KEY="registry-master-key:${MASTER_KEY}"
```

In Docker Compose, add to the `minio` service:

```yaml
environment:
  MINIO_KMS_SECRET_KEY: "registry-master-key:${MINIO_KMS_MASTER_KEY}"
```

Add `MINIO_KMS_MASTER_KEY` to `.env` (generate with `openssl rand -hex 32`).

### Apply default bucket encryption

```bash
mc alias set local http://localhost:9000 minioadmin minioadmin
mc mb local/registry 2>/dev/null || true
mc encrypt set SSE-S3 local/registry
mc encrypt info local/registry
# Expected: Auto encryption 'sse-s3' set
```

---

## 2. SSE-KMS with HashiCorp Vault Transit (Production)

### Prerequisites

- Vault Transit secrets engine enabled (`vault secrets enable transit`)
- MinIO KES (Key Encryption Service) deployed as a sidecar or separate service

### Deploy KES

```bash
# Generate KES server TLS cert
kes identity new --key /etc/kes/server.key --cert /etc/kes/server.cert

# Get MinIO's identity hash (run on MinIO host)
kes identity of /etc/minio/kes-client.cert

# /etc/kes/config.yaml
cat > /etc/kes/config.yaml <<'EOF'
address: 0.0.0.0:7373

tls:
  key:  /etc/kes/server.key
  cert: /etc/kes/server.cert

policy:
  minio:
    allow:
      - /v1/key/create/*
      - /v1/key/generate/*
      - /v1/key/decrypt/*
    identities:
      - <minio-identity-hash>

keystore:
  vault:
    endpoint: http://vault:8200
    transit:
      engine_path: transit
      key_prefix: kes-
    approle:
      id:     <vault-approle-role-id>
      secret: <vault-approle-secret-id>
      retry:  15s
EOF

kes server --config /etc/kes/config.yaml &
```

### Configure MinIO to use KES

```bash
export MINIO_KMS_KES_ENDPOINT=https://kes:7373
export MINIO_KMS_KES_CERT_FILE=/etc/minio/kes-client.cert
export MINIO_KMS_KES_KEY_FILE=/etc/minio/kes-client.key
export MINIO_KMS_KES_CA_PATH=/etc/kes/server.cert
export MINIO_KMS_KES_KEY_NAME=registry-default-key

# Create the default encryption key
mc admin kms key create myminio registry-default-key

# Apply KMS encryption to bucket
mc encrypt set SSE-KMS registry-default-key myminio/registry
mc encrypt info myminio/registry
```

---

## 3. Kubernetes Deployment

Reference the KMS config as secrets in the `registry-storage` Helm chart:

```yaml
# values.prod.yaml
storage:
  extraEnv:
    - name: MINIO_KMS_KES_ENDPOINT
      value: "https://kes.registry.svc.cluster.local:7373"
    - name: MINIO_KMS_KES_CERT_FILE
      value: "/etc/kes/client.cert"
    - name: MINIO_KMS_KES_KEY_FILE
      value: "/etc/kes/client.key"
    - name: MINIO_KMS_KES_CA_PATH
      value: "/etc/kes/server.cert"
    - name: MINIO_KMS_KES_KEY_NAME
      value: "registry-default-key"
  extraVolumeMounts:
    - name: kes-certs
      mountPath: /etc/kes
      readOnly: true
  extraVolumes:
    - name: kes-certs
      secret:
        secretName: registry-kes-certs
```

---

## 4. Verification Checklist

After enabling encryption, verify:

- [ ] `mc encrypt info myminio/registry` shows encryption enabled
- [ ] Push a test image and inspect the object: `mc stat myminio/registry/blobs/...` shows `X-Amz-Server-Side-Encryption`
- [ ] KES health check passes: `curl -sk https://kes:7373/v1/status | jq .`
- [ ] MinIO server logs show no KMS errors on startup
- [ ] Existing objects (before encryption was enabled) are migrated or documented as unencrypted

---

## 5. Key Rotation

### SSE-S3

```bash
# Generate new master key and update MinIO config
NEW_KEY=$(openssl rand -hex 32)
# Update MINIO_KMS_SECRET_KEY, restart MinIO
# MinIO transparently re-wraps DEKs on next object access
```

### SSE-KMS

```bash
# Rotate the KES key in Vault (creates new key version)
mc admin kms key rotate myminio registry-default-key

# Optional: force immediate re-encryption of all objects
mc find myminio/registry --recursive | \
  xargs -I{} mc cp --enc-kms "registry-default-key" {} {}
```
