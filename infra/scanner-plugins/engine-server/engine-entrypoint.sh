#!/bin/sh
# engine-entrypoint.sh — pre-warm the engine vuln DB once per cache volume,
# then exec the wrapper. Relocated from services/scanner/scripts/entrypoint.sh
# (REM-019 Phase 2): the DB now lives with the engine, so the warm belongs here.
#
# Best-effort — a failed warm logs but never blocks startup; the first scan
# self-heals. Skip with SCANNER_SKIP_ENGINE_WARM=1 for fast CI runs.
set -eu

if [ -z "${SCANNER_SKIP_ENGINE_WARM:-}" ]; then
    case "${ENGINE_NAME:-}" in
    trivy)
        echo "engine-entrypoint: pre-warming Trivy DB..." >&2
        if trivy image --download-db-only >/tmp/trivy-warm.log 2>&1; then
            echo "engine-entrypoint: Trivy DB ready." >&2
        else
            echo "engine-entrypoint: Trivy DB warm failed (see /tmp/trivy-warm.log) — first scan will fetch online." >&2
        fi
        ;;
    grype)
        echo "engine-entrypoint: pre-warming Grype DB..." >&2
        if grype db update >/tmp/grype-warm.log 2>&1; then
            echo "engine-entrypoint: Grype DB ready." >&2
        else
            echo "engine-entrypoint: Grype DB warm failed (see /tmp/grype-warm.log) — first scan will retry." >&2
        fi
        ;;
    esac
fi

exec "$@"
