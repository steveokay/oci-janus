# syntax=docker/dockerfile:1.7

# ── Stage 1: Build Go binary ──────────────────────────────────────
FROM golang:1.25-bookworm AS builder

WORKDIR /build
COPY go.work go.work.sum ./
COPY libs/ libs/
COPY proto/gen/go/ proto/gen/go/
COPY services/scanner/ services/scanner/

RUN cd /build/libs && CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/healthcheck ./cmd/healthcheck

WORKDIR /build/services/scanner
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/server ./cmd/server

# ── Stage 2: Trivy binary ─────────────────────────────────────────
FROM aquasec/trivy:0.52.0 AS trivy

# ── Stage 3: Final image ──────────────────────────────────────────
# Uses debian-slim instead of distroless: the scanner must exec Trivy as
# a subprocess, and Trivy requires shared libs absent in distroless/static.
FROM debian:12-slim AS final

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*

# Copy Go server, healthcheck, and Trivy binary
COPY --from=builder /bin/healthcheck /healthcheck
COPY --from=builder /bin/server /server
COPY --from=trivy /usr/local/bin/trivy /usr/local/bin/trivy

# Trivy vulnerability DB cache — mount as a volume in production
# so the DB persists across container restarts and doesn't re-download on every start.
#
# PENTEST-019: this cache is writable by the scanner process. A subverted
# Trivy binary could write malicious DB files here. Mitigations in place:
#   1. The Trivy binary itself is SHA256-checksum-verified at scanner startup
#      (SCANNER_PLUGIN_CHECKSUM env var — SEC-003).
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

# SCANNER_PLUGIN_PATH tells the scanner service where to find the Trivy binary.
# SCANNER_PLUGIN_CHECKSUM must be set at deploy time to the SHA256 of the Trivy binary.
ENV SCANNER_PLUGIN_PATH=/usr/local/bin/trivy

VOLUME ["/trivy-cache"]

EXPOSE 50051 8080

USER 65532:65532

ENTRYPOINT ["/server"]
