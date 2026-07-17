#!/bin/sh
# entrypoint.sh — auto-fill SCANNER_PLUGIN_PATH + SCANNER_PLUGIN_CHECKSUM
# when the operator hasn't supplied them, then exec the scanner server.
#
# Rationale: the scanner refuses to start unless SCANNER_PLUGIN_CHECKSUM
# matches sha256sum($SCANNER_PLUGIN_PATH). This protects against an
# attacker swapping the binary inside a running container — the
# operator-supplied hash is the out-of-band attestation.
#
# For local dev and zero-config Compose runs, requiring an out-of-band
# hash is friction that buys little (the operator just rebuilt the
# image; they trust its contents). So when the env var is empty we
# compute the hash from the image-baked binary and proceed. Operator
# overrides always win; this only fills in the empty case.
#
# This is a defense-in-depth degradation, not a regression: the check
# still catches "binary swapped after image build" inside a running
# container — the only thing it stops catching is "image was built
# with a tampered binary," which is a supply-chain concern handled by
# image signing (Cosign — see services/signer).

set -eu

# Compose's ${VAR:-} syntax sets the env var to an empty string when no
# override is supplied, which overrides the Dockerfile's ENV default
# back to empty. Re-apply the Dockerfile default ourselves when path
# came through empty — the dev-stub adapter is always present.
if [ -z "${SCANNER_PLUGIN_PATH:-}" ]; then
    SCANNER_PLUGIN_PATH=/usr/local/bin/scanner-dev-stub
    export SCANNER_PLUGIN_PATH
fi

# Auto-fill the checksum from the binary when the operator left it
# empty. Operator-supplied values are never overwritten.
if [ -z "${SCANNER_PLUGIN_CHECKSUM:-}" ]; then
    if [ ! -x "$SCANNER_PLUGIN_PATH" ]; then
        echo "entrypoint: $SCANNER_PLUGIN_PATH is not an executable file" >&2
        exit 1
    fi
    SCANNER_PLUGIN_CHECKSUM=$(sha256sum "$SCANNER_PLUGIN_PATH" | awk '{print $1}')
    export SCANNER_PLUGIN_CHECKSUM
    echo "entrypoint: auto-computed SCANNER_PLUGIN_CHECKSUM=$SCANNER_PLUGIN_CHECKSUM for $SCANNER_PLUGIN_PATH" >&2
fi

# REM-014: Pre-warm the Grype vulnerability DB at container start so
# the first scan doesn't pay the ~2 GB download cost (which can blow
# past the scanner job timeout and produce the "exit status 1 with
# empty stderr" symptom we hit during initial integration). Best-
# effort — failure logs but doesn't block startup, because the dev-
# stub + Trivy paths still work without Grype's DB.
#
# Skips the warm if:
#   - grype isn't in the image (defensive — tests with a different
#     image layout still work)
#   - SCANNER_SKIP_GRYPE_WARM=1 (operator override for fast CI runs
#     where the DB isn't needed)
if [ -z "${SCANNER_SKIP_GRYPE_WARM:-}" ] && [ -x /usr/local/bin/grype ]; then
    echo "entrypoint: pre-warming Grype vulnerability DB (one-time per cache volume)..." >&2
    if /usr/local/bin/grype db update >/tmp/grype-warm.log 2>&1; then
        echo "entrypoint: Grype DB ready." >&2
    else
        echo "entrypoint: Grype DB warm failed (see /tmp/grype-warm.log) — continuing; first scan with Grype active will retry." >&2
    fi
fi

# REM-019 Phase 2: Pre-warm the Trivy vulnerability DB at container start for
# the SAME reason as Grype above — Trivy is the default active adapter, but its
# DB was never pre-warmed, so every scan against a stale/absent DB paid a live
# ~100MB download from mirror.gcr.io on the hot path. That download made scans
# take ~30s and fail intermittently on transient registry errors, producing the
# "failed" scan_results with scanner_name="unknown" this phase set out to fix.
# With the DB warmed here, the trivy-adapter runs `trivy rootfs --skip-db-update`
# (see infra/scanner-plugins/trivy-adapter/main.go) so the scan hot path does no
# network I/O. Best-effort — a failed warm logs but never blocks startup, and
# the adapter falls back to a one-time online fetch for a cold cache.
#
# Skips the warm if:
#   - trivy isn't in the image (defensive — dev-stub adapter needs no DB)
#   - SCANNER_SKIP_TRIVY_WARM=1 (operator override for fast CI runs)
if [ -z "${SCANNER_SKIP_TRIVY_WARM:-}" ] && [ -x /usr/local/bin/trivy ]; then
    echo "entrypoint: pre-warming Trivy vulnerability DB (one-time per cache volume)..." >&2
    if /usr/local/bin/trivy image --download-db-only >/tmp/trivy-warm.log 2>&1; then
        echo "entrypoint: Trivy DB ready." >&2
    else
        echo "entrypoint: Trivy DB warm failed (see /tmp/trivy-warm.log) — continuing; first scan will fall back to an online DB fetch." >&2
    fi
fi

exec "$@"
