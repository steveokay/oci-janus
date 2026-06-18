# syntax=docker/dockerfile:1.7
#
# backup-tools — small alpine image used by the DR CronJobs in
# infra/helm/registry/charts/backup. It bundles:
#
#   * postgresql16-client : pg_dump / pg_restore matching the platform's PG 16
#   * aws-cli             : S3 / MinIO uploads (we set AWS_ENDPOINT_URL for MinIO)
#   * curl + jq           : RabbitMQ HTTP API exports + JSON parsing
#   * bash                : the backup/restore scripts need bash features (set -o pipefail)
#
# We deliberately keep this image OUT of the per-service distroless pipeline:
# distroless has no shell, no awscli, no pg_dump. The CronJobs need a real
# userspace to be debuggable in a DR (which is precisely when you'll be ssh'd
# in trying to figure out what went wrong).
#
# Vault snapshot does NOT bundle the vault CLI here — we hit /v1/sys/storage/raft/snapshot
# directly via curl so we don't tie the image to a Vault release.
#
# The image is built/pushed by CI under the usual ghcr.io/steveokay/<name> path.

FROM alpine:3.20

# Pin the postgres-client version explicitly so a future alpine bump cannot
# silently shift our pg_dump version (which would create dumps that the
# running cluster's pg_restore cannot read).
RUN apk add --no-cache \
        bash \
        ca-certificates \
        curl \
        jq \
        coreutils \
        tzdata \
        aws-cli \
        postgresql16-client=~16 \
    && rm -rf /var/cache/apk/*

# Non-root user mirroring the other service images (USER 65532:65532 from
# CLAUDE.md distroless convention). The scripts only need to read mounted
# configmaps / write to /tmp and stdout, so no extra perms required.
RUN addgroup -g 65532 -S backup \
    && adduser -u 65532 -S -G backup -H -s /sbin/nologin backup

USER 65532:65532
WORKDIR /work

# No ENTRYPOINT — the CronJobs invoke `bash -c '...'` directly, mounting the
# scripts ConfigMap at /scripts. Leaving the entrypoint empty means the same
# image is also usable for ad-hoc `kubectl run --image=...-backup-tools sh`
# debugging during a real incident.
