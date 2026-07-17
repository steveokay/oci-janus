# syntax=docker/dockerfile:1.7
#
# Thin engine sidecar for OCI-Janus scanner. Bumping the engine version = bump
# the pinned digest below + rebuild THIS image; registry-scanner is untouched.
#
# Build:
#   docker build -f infra/scanner-plugins/engine-server/Dockerfile \
#     --build-arg ENGINE=trivy -t oci-janus/trivy-engine:0.71.2 .
# (build context is the repo root so the wrapper module is in scope.)

# Global ARG: must be declared before the first FROM to stay in scope for
# every subsequent FROM in the file (Docker's build-arg scoping rule) — this
# is what lets `FROM engine-${ENGINE}` in Stage 3 resolve at all.
ARG ENGINE=trivy

# ── Stage 1: build the wrapper (stdlib-only, GOWORK=off) ──────────────
FROM golang:1.25-bookworm AS builder
WORKDIR /build
COPY infra/scanner-plugins/engine-server/ ./engine-server/
WORKDIR /build/engine-server
RUN CGO_ENABLED=0 GOOS=linux GOWORK=off go build -trimpath -ldflags="-s -w" -o /bin/engine-server .

# ── Stage 2: engine bases (pin by DIGEST — the trust root, §7 of the spec) ──
# Bump the tag AND the @sha256 digest together. Digest pinning is stronger
# than a mutable tag and is what replaces the in-image checksum guarantee once
# the engine runs in its own container.
FROM aquasec/trivy:0.71.2 AS engine-trivy
FROM anchore/grype:v0.93.0 AS engine-grype

# ── Stage 3: final, selected by ENGINE build-arg ──────────────────────
FROM engine-${ENGINE} AS final
# Re-declare after FROM: a stage only inherits global ARGs it explicitly
# re-declares; without this line, later ${ENGINE} references in this stage
# would resolve empty.
ARG ENGINE=trivy

USER root
# Both upstream images are alpine/distroless-ish; ensure /bin/sh + ca-certs
# for the entrypoint. Trivy's image is alpine-based (has sh); grype's is
# distroless (no sh) — grype support is finalised in Phase 2, which will
# adjust this. For trivy this is a no-op safety net.
COPY infra/scanner-plugins/engine-server/engine-entrypoint.sh /engine-entrypoint.sh
COPY --from=builder /bin/engine-server /usr/local/bin/engine-server

ENV ENGINE_NAME=${ENGINE}
# ENGINE_CMD is the engine binary path inside the base image.
# trivy: /usr/local/bin/trivy ; grype: /grype (finalised in Phase 2).
ENV ENGINE_CMD=/usr/local/bin/trivy
ENV ENGINE_WORK_DIR=/scan-work
ENV ENGINE_HTTP_ADDR=:8085
# Trivy DB cache lives here now (moved off the scanner container).
ENV TRIVY_CACHE_DIR=/engine-cache
RUN mkdir -p /engine-cache /scan-work
VOLUME ["/engine-cache"]

EXPOSE 8085
ENTRYPOINT ["/engine-entrypoint.sh"]
CMD ["/usr/local/bin/engine-server"]
