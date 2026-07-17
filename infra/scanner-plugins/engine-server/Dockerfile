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
# grype's base image is distroless (no /bin/sh, no mkdir) — pre-create the
# runtime directories here, in a stage that has a shell, and COPY them into
# the final stage below instead of RUN mkdir there.
RUN mkdir -p /engine-runtime-dirs/engine-cache /engine-runtime-dirs/scan-work

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
# Both upstream images are alpine/distroless-ish. grype's base is distroless
# (no /bin/sh, no mkdir/coreutils at all), so the wrapper binary itself is the
# entrypoint and handles DB pre-warm + healthchecks in-process — see
# main.go's warm()/-health. Runtime dirs are COPYed in pre-built (see builder
# stage) rather than created with `RUN mkdir`, which needs a shell trivy has
# but grype does not.
COPY --from=builder /bin/engine-server /usr/local/bin/engine-server
COPY --from=builder /engine-runtime-dirs/engine-cache /engine-cache
COPY --from=builder /engine-runtime-dirs/scan-work /scan-work

ENV ENGINE_NAME=${ENGINE}
# ENGINE_CMD is left unset here — the wrapper defaults it per ENGINE_NAME
# (trivy: /usr/local/bin/trivy ; grype: /grype). Set explicitly to override.
ENV ENGINE_WORK_DIR=/scan-work
ENV ENGINE_HTTP_ADDR=:8085
# DB cache dir for both engines (harmless to set both regardless of which
# engine this image actually bakes).
ENV TRIVY_CACHE_DIR=/engine-cache
ENV GRYPE_DB_CACHE_DIR=/engine-cache
VOLUME ["/engine-cache"]

EXPOSE 8085
ENTRYPOINT ["/usr/local/bin/engine-server"]
