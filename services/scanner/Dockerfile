# syntax=docker/dockerfile:1.7

# ── Stage 1: Build Go binaries ────────────────────────────────────
# Builds three artifacts: the scanner server, the healthcheck helper,
# and the two adapter binaries (dev-stub + trivy-adapter) that satisfy
# the JSON-RPC plugin contract in services/scanner/internal/plugin.
#
# Adapters live under infra/scanner-plugins/ and are intentionally NOT
# part of the go.work workspace — they're standalone single-file
# programs whose only purpose is bridging the contract to a concrete
# scanner engine (dev-stub: hardcoded findings for UI testing; trivy:
# real CVE detection). Keeping them out of the workspace means a
# breaking change to libs/ never accidentally breaks an adapter build.
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

# ── Stage 2: Trivy binary ─────────────────────────────────────────
FROM aquasec/trivy:0.52.0 AS trivy

# ── Stage 3: Final image ──────────────────────────────────────────
# Uses debian-slim instead of distroless: the scanner must exec adapter
# binaries as subprocesses, and the trivy-adapter in turn execs Trivy
# itself, which links against shared libs absent in distroless/static.
FROM debian:12-slim AS final

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates coreutils \
 && rm -rf /var/lib/apt/lists/*

# Copy Go server, healthcheck, both adapters, and the Trivy binary.
COPY --from=builder /bin/healthcheck /healthcheck
COPY --from=builder /bin/server /server
COPY --from=builder /bin/dev-stub /usr/local/bin/scanner-dev-stub
COPY --from=builder /bin/trivy-adapter /usr/local/bin/scanner-trivy-adapter
COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy

# Trivy vulnerability DB cache — mount as a volume in production
# so the DB persists across container restarts and doesn't re-download on every start.
#
# PENTEST-019: this cache is writable by the scanner process. A subverted
# Trivy binary could write malicious DB files here. Mitigations in place:
#   1. The active adapter binary is SHA256-checksum-verified at scanner
#      startup (SCANNER_PLUGIN_CHECKSUM env var — SEC-003). The entrypoint
#      below auto-computes the checksum from the image-baked binary when
#      the operator hasn't set it explicitly — image integrity is the
#      trust root in that case, and any in-container binary swap by an
#      attacker would still fail the check.
#   2. The scanner runs as non-root (USER 65532:65532 below).
#   3. Container filesystem is read-only outside /trivy-cache and /tmp.
# Operators wanting hard cache integrity should mount /trivy-cache as a
# tmpfs-backed overlay so each pod sees a fresh cache, OR mount the upstream
# Trivy DB read-only and disable in-place DB updates via TRIVY_NO_PROGRESS +
# pre-baked cache layers. See infra/runbooks/scanner-cache-hardening.md.
RUN mkdir -p /trivy-cache \
 && groupadd -g 65532 nonroot \
 && useradd -u 65532 -g nonroot -s /sbin/nologin -M nonroot \
 && chown nonroot:nonroot /trivy-cache

ENV TRIVY_CACHE_DIR=/trivy-cache

# Default to the dev-stub adapter so `docker compose --profile scanner
# up -d` works with zero operator configuration — useful for local UI
# work and CI smoke tests. Operators wanting real CVE detection should
# override with SCANNER_PLUGIN_PATH=/usr/local/bin/scanner-trivy-adapter
# at deploy time (the scanner image already contains both adapters and
# the Trivy binary; only the env var flips).
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

VOLUME ["/trivy-cache"]

EXPOSE 50051 8080

USER 65532:65532

ENTRYPOINT ["/entrypoint.sh"]
CMD ["/server"]
