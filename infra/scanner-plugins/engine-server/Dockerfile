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
# grype v0.116.0: v0.93.0's DB importer chokes on the current upstream vuln DB
# ("UNIQUE constraint failed: operating_systems … (2067)" on hydration), so the
# engine can't load a DB. Bumping the tag here (and rebuilding only this small
# grype-engine image — never registry-scanner) is exactly the decoupling payoff.
FROM anchore/grype:v0.116.0 AS engine-grype

# ── Stage 3: final, selected by ENGINE build-arg ──────────────────────
FROM engine-${ENGINE} AS final
# Re-declare after FROM: a stage only inherits global ARGs it explicitly
# re-declares; without this line, later ${ENGINE} references in this stage
# would resolve empty.
ARG ENGINE=trivy

# Numeric UID 0, NOT `USER root`: grype's distroless base has no /etc/passwd
# entry for the name "root", so `USER root` fails at container start with
# "unable to find user root". Numeric UIDs need no passwd lookup. Root is
# needed to write /engine-cache and read the scanner's 0700 rootfs on the
# shared volume.
USER 0
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
