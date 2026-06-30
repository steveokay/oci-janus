# ADR-0007: OTEL with pluggable exporter

**Status:** ACCEPTED.
**Date:** Initial.
**Phase:** Initial.

## Context

Operators run a mix of Jaeger, Tempo, and Datadog; hardcoding one would force vendor migration when a customer's observability stack changes.

## Decision

Instrument every service with OpenTelemetry SDK and choose the exporter at boot via `OTEL_EXPORTER={jaeger|tempo|datadog|stdout}`. The bootstrap helper lives in `libs/observability/otel`.

## Consequences

Switching backends is a config change, not a code change. `libs/observability/otel.Bootstrap` must be called from every `main.go` before any server starts, or spans are lost.

## Verified by

`libs/observability/otel/otel.go:Bootstrap` — every service entrypoint invokes it; removing the helper would silently drop telemetry across the platform.
