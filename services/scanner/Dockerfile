# syntax=docker/dockerfile:1.7

# ── Stage 1: Build Go binaries ────────────────────────────────────
# Builds the scanner server, the healthcheck helper, and three adapter
# binaries (dev-stub + trivy-adapter + grype-adapter) that satisfy the
# JSON-RPC plugin contract in services/scanner/internal/plugin.
#
# Adapters live under infra/scanner-plugins/ and are intentionally NOT
# part of the go.work workspace — they're standalone single-file
# programs whose only purpose is bridging the contract to a concrete
# scanner engine (dev-stub: hardcoded findings for UI testing; trivy +
# grype: real CVE detection from different upstreams). Keeping them
# out of the workspace means a breaking change to libs/ never
# accidentally breaks an adapter build.
FROM golang:1.25-bookworm AS builder

WORKDIR /build
COPY go.work go.work.sum ./
COPY libs/ libs/
COPY proto/gen/go/ proto/gen/go/
COPY services/scanner/ services/scanner/
COPY infra/scanner-plugins/ infra/scanner-plugins/

RUN cd /build/libs && CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/healthcheck ./cmd/healthcheck

WORKDIR /build/services/scanner
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/server ./cmd/server

# Each adapter is built in its own GOWORK=off invocation. They're outside
# the go.work workspace so the build cannot pick up any sibling module.
WORKDIR /build/infra/scanner-plugins/dev-stub
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/dev-stub .

WORKDIR /build/infra/scanner-plugins/trivy-adapter
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/trivy-adapter .

WORKDIR /build/infra/scanner-plugins/grype-adapter
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/grype-adapter .

# REM-014 — Clair adapter. Stdlib-only binary (no Clair Go client dep)
# that talks to the Clair v4 indexer + matcher over HTTP. The scanner
# container picks this up via the /usr/local/bin/scanner-* glob, same
# as the other adapters.
WORKDIR /build/infra/scanner-plugins/clair-adapter
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/clair-adapter .

# ── Stage 2b: Grype binary ────────────────────────────────────────
# Anchore publishes grype as a single static binary inside the
# anchore/grype image. Mirrors the same shape as the Trivy stage —
# pulling from upstream pins us to a known release without us hosting
# the binary ourselves.
#
# Version pin note: grype < v0.81 uses DB schema v5, which Anchore
# stopped publishing fresh updates for in mid-2024. The grype binary's
# own age check (max 5 days old by default) rejects the stale v5 DBs
# with "the vulnerability database was built N weeks ago". Pinning to
# v0.93.0 (or any post-v0.81 release) gets schema v6, which is
# actively maintained upstream.
FROM anchore/grype:v0.93.0 AS grype

# ── Stage 3: Final image ──────────────────────────────────────────
# Uses debian-slim instead of distroless: the scanner must exec adapter
# binaries as subprocesses, and the trivy-adapter in turn execs Trivy
# itself, which links against shared libs absent in distroless/static.
FROM debian:12-slim AS final

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates coreutils \
 && rm -rf /var/lib/apt/lists/*

# Copy Go server, healthcheck, all four adapters, and the Grype binary.
# The adapter registry auto-discovers via the /usr/local/bin/scanner-*
# glob at boot, so adding a new adapter only needs (a) the binary baked
# here and (b) the engine CLI it shells out to. The Trivy engine binary
# no longer lives in this image — the trivy-adapter now shells out to
# the trivy-engine sidecar over HTTP instead of execing a local `trivy`
# (scanner engine decoupling, 2026-07-17).
COPY --from=builder /bin/healthcheck /healthcheck
COPY --from=builder /bin/server /server
COPY --from=builder /bin/dev-stub /usr/local/bin/scanner-dev-stub
COPY --from=builder /bin/trivy-adapter /usr/local/bin/scanner-trivy-adapter
COPY --from=builder /bin/grype-adapter /usr/local/bin/scanner-grype-adapter
COPY --from=builder /bin/clair-adapter /usr/local/bin/scanner-clair-adapter
# Grype upstream packages the binary at /grype inside its image.
COPY --from=grype /grype /usr/local/bin/grype

# /grype-cache holds the Anchore vulnerability DB.
#
# PENTEST-019: this cache is writable by the scanner process. A subverted
# Grype binary could write malicious DB files here. Mitigations in place:
#   1. The active adapter binary is SHA256-checksum-verified at scanner
#      startup (SCANNER_PLUGIN_CHECKSUM env var — SEC-003). The entrypoint
#      below auto-computes the checksum from the image-baked binary when
#      the operator hasn't set it explicitly — image integrity is the
#      trust root in that case, and any in-container binary swap by an
#      attacker would still fail the check.
#   2. The scanner runs as non-root (USER 65532:65532 below).
#   3. Container filesystem is read-only outside /grype-cache and /tmp.
# Operators wanting hard cache integrity should mount /grype-cache as a
# tmpfs-backed overlay so each pod sees a fresh cache, OR mount the upstream
# Grype DB read-only and disable in-place DB updates. See
# infra/runbooks/scanner-cache-hardening.md.
#
# /scan-work is the shared work directory used to hand blob/layer data
# to sidecar scan engines (trivy-engine and, eventually, other decoupled
# engines) without shelling out to a locally-baked binary.
RUN mkdir -p /grype-cache /scan-work /tmp/reports \
 && groupadd -g 65532 nonroot \
 && useradd -u 65532 -g nonroot -s /sbin/nologin -M nonroot \
 && chown nonroot:nonroot /grype-cache /scan-work /tmp/reports
# /tmp/reports holds rendered compliance reports (FE-API-019). Both this
# scanner container (writer) and the registry-management container (reader,
# via the scanner_reports named volume in infra/docker-compose) share this
# path. We chown at image-build time so the named volume picks up the
# nonroot:nonroot ownership on first attach — Docker copies image content
# into a fresh volume, which means the volume inherits these permissions
# rather than the default root:root that Docker creates volumes with.
# Tracked properly as REM-012 (streaming RPC replacement).

# GRYPE_DB_CACHE_DIR controls where Grype downloads its vuln DB. Mount
# as a volume in production so the DB persists across container
# restarts and doesn't re-download on every pod cycle.
ENV GRYPE_DB_CACHE_DIR=/grype-cache

# Default to the dev-stub adapter so `docker compose --profile scanner
# up -d` works with zero operator configuration — useful for local UI
# work and CI smoke tests. Operators wanting real CVE detection should
# override with SCANNER_PLUGIN_PATH=/usr/local/bin/scanner-trivy-adapter
# at deploy time (the scanner image already contains all adapters;
# only the env var flips — the trivy-adapter talks to the trivy-engine
# sidecar over HTTP rather than execing a local binary).
#
# SCANNER_PLUGIN_CHECKSUM is intentionally NOT set here — the entrypoint
# auto-computes the SHA-256 of $SCANNER_PLUGIN_PATH at startup so the
# zero-config path works without the operator having to copy a hash
# around. In production deployments the operator SHOULD set
# SCANNER_PLUGIN_CHECKSUM explicitly so the integrity check has an
# out-of-band reference rather than trusting the image's own contents.
ENV SCANNER_PLUGIN_PATH=/usr/local/bin/scanner-dev-stub

# Entrypoint wrapper: auto-fills SCANNER_PLUGIN_CHECKSUM from the
# selected adapter's SHA-256 when the operator hasn't set it. Operator
# overrides win; this only kicks in when the env var is empty.
COPY services/scanner/scripts/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh

VOLUME ["/grype-cache", "/scan-work"]

EXPOSE 50051 8080
# REM-014 — Clair adapter's embedded HTTP server. Clair fetches staged
# layer blobs from the scanner container via http://scanner:9099/<digest>
# on the `registry` Docker network. Not mapped to a host port; only
# reachable from sibling containers.
EXPOSE 9099

USER 65532:65532

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/server"]
