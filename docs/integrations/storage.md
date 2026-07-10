# Storage backends

`registry-storage` is the blob-storage abstraction every other service reads and
writes through — clients never touch the backend directly. The backend is
pluggable via the `STORAGE_DRIVER` environment variable.

## Choosing a driver

`STORAGE_DRIVER` accepts `minio`, `s3`, `gcs`, `azure`, or `filesystem`. An unset
or unrecognised value fails startup with a clear error.

| Driver | Status | Use it for |
|---|---|---|
| `minio` | **Implemented** | MinIO, and **any S3-compatible** object store (including AWS S3) by setting the endpoint |
| `filesystem` | **Implemented** | Local development and single-node deployments |
| `s3` / `gcs` / `azure` | Recognised, driver on the roadmap | Native cloud SDKs — for now, use the `minio` driver against an S3-compatible endpoint |

!!! tip "AWS S3 today"
    The `minio` driver speaks the S3 API, so you do not need to wait for the
    native `s3` driver — point `STORAGE_MINIO_ENDPOINT` at `s3.amazonaws.com`
    (or your regional/VPC endpoint) with S3 credentials and you are done.

## MinIO / S3-compatible

| Env var | Required | Default | Notes |
|---|---|---|---|
| `STORAGE_DRIVER` | yes | — | `minio` |
| `STORAGE_MINIO_ENDPOINT` | yes | — | `host:port` (no scheme), e.g. `minio:9000` or `s3.amazonaws.com` |
| `STORAGE_MINIO_ACCESS_KEY` | yes | — | Access key / AWS access key id |
| `STORAGE_MINIO_SECRET_KEY` | yes | — | Secret key / AWS secret access key |
| `STORAGE_MINIO_BUCKET` | yes | — | Target bucket (must exist) |
| `STORAGE_MINIO_USE_SSL` | no | `true` | Set `false` only for plaintext local MinIO |
| `STORAGE_MINIO_REGION` | no | — | Region hint for S3-style backends |

```env
STORAGE_DRIVER=minio
STORAGE_MINIO_ENDPOINT=minio:9000
STORAGE_MINIO_ACCESS_KEY=minioadmin
STORAGE_MINIO_SECRET_KEY=minioadmin
STORAGE_MINIO_BUCKET=registry
STORAGE_MINIO_USE_SSL=true
STORAGE_MINIO_REGION=us-east-1
```

!!! warning "Secrets come from the environment"
    Never commit these values. Supply them from your secrets manager (Kubernetes
    Secrets, External Secrets Operator, Vault) — see the [security rules in
    CLAUDE.md](https://github.com/steveokay/oci-janus/blob/main/CLAUDE.md#7-authentication--security).

## Filesystem (development)

| Env var | Required | Default | Notes |
|---|---|---|---|
| `STORAGE_DRIVER` | yes | — | `filesystem` |
| `STORAGE_FILESYSTEM_ROOT` | yes | — | Directory blobs are written under, e.g. `/data` |

```env
STORAGE_DRIVER=filesystem
STORAGE_FILESYSTEM_ROOT=/data
```

Intended for local development and single-node use. For durability and
encryption you are responsible for the underlying volume (see below).

## Cloud drivers (roadmap)

The config layer already recognises `gcs` and `azure` (with
`STORAGE_GCS_BUCKET` / `STORAGE_GCS_PROJECT` and `STORAGE_AZURE_CONTAINER` /
`STORAGE_AZURE_ACCOUNT` placeholders) and `s3`, but the native driver
implementations are not shipped yet. Until they land, use the `minio` driver
against an S3-compatible endpoint. Track progress in
[Services](../SERVICES.md#4-registry-storage).

## Encryption at rest

Enable server-side encryption at the storage layer:

- **S3-compatible:** enforce `x-amz-server-side-encryption: AES256` on the
  bucket, or use SSE-KMS.
- **MinIO:** enable MinIO SSE-S3 or SSE-KMS — see
  `infra/runbooks/minio-encryption.md`.
- **GCS / Azure (when native drivers land):** CMEK on the bucket / SSE on the
  storage account.
- **Filesystem:** configure disk encryption (LUKS/dm-crypt) at the OS level.

## Multi-tenancy

Blob keys are prefixed with the `tenant_id`, so tenants never share a key space
even on a single bucket. In single-tenant deployments this is simply the
bootstrap tenant id. See [Multi-tenancy in
CLAUDE.md](https://github.com/steveokay/oci-janus/blob/main/CLAUDE.md#9-multi-tenancy).

**Related:** [Services → registry-storage](../SERVICES.md#4-registry-storage) for
the storage key layout and the driver interface.
