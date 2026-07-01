# Observability — OpenTelemetry, Metrics, Logging

> **Source of truth for observability mechanics.** CLAUDE.md §10 holds
> only the rules; this file holds the env-var tables, metric names,
> collector wiring, and log-field contract. When code disagrees with
> this file, the code is wrong — but `libs/observability/otel` and
> `libs/observability/metrics` are the authoritative implementations.

## Table of Contents

1. [OpenTelemetry Setup](#1-opentelemetry-setup)
2. [Metrics Catalogue](#2-metrics-catalogue)
3. [otel-collector wiring](#3-otel-collector-wiring)
4. [Structured Logging](#4-structured-logging)

---

## 1. OpenTelemetry Setup

All services instrument with the OpenTelemetry Go SDK. Exporter is pluggable via environment variable.

**`OTEL_EXPORTER`** controls the backend: `jaeger` | `tempo` | `datadog` | `stdout`.

### Common env vars (all services)

```
OTEL_EXPORTER=                   # required: jaeger|tempo|datadog|stdout
OTEL_ENDPOINT=                   # OTLP endpoint URL
OTEL_SERVICE_NAME=               # set per-service in Dockerfile
OTEL_ENVIRONMENT=                # production|staging|development
OTEL_SAMPLING_RATE=1.0           # 0.0 to 1.0, default 1.0
OTEL_INSECURE=                   # true only in local dev with no TLS on collector
```

### Bootstrap pattern (from `libs/observability/otel`)

```go
func Bootstrap(ctx context.Context, cfg Config) (shutdown func(context.Context) error, err error)
```

Call in `main.go` **before** starting any server. Always call `shutdown` on process exit to flush spans/metrics. Missing this call is the root cause of "no traces in Jaeger" — every service entrypoint must include it.

### Instrumentation

- **HTTP tracing:** Wrap the HTTP handler tree with `otelhttp.NewHandler(...)` so HTTP requests create root spans.
- **gRPC tracing:** Wired via `grpcmw.OTELServerHandler()` as a `StatsHandler` in `libs/middleware/grpc`.

---

## 2. Metrics Catalogue

Every service exposes Prometheus metrics at `GET /metrics` on a **dedicated port `:9090`** (SEC-025) — separated from the business port so NetworkPolicy can grant Prometheus access without exposing the OCI API.

### Standard metrics (from `libs/observability/metrics`)

| Metric | Type | Labels | Notes |
|---|---|---|---|
| `registry_http_request_duration_seconds` | histogram | `service`, `method`, `path`, `status` | |
| `registry_grpc_request_duration_seconds` | histogram | `service`, `method`, `status` | |
| `registry_rabbitmq_messages_consumed_total` | counter | `service`, `queue`, `status` | |
| `registry_storage_operation_duration_seconds` | histogram | `driver`, `operation`, `status` | |
| `registry_active_uploads_total` | gauge | — | |
| `registry_grpc_peer_cn_denied_total` | counter | `method`, `reason` (`missing_cn` / `cn_not_allowed`) | REDESIGN-001 Phase 6.10 |
| `registry_grpc_peer_cn_allowlist_enabled` | gauge | — | `1` when `MTLS_PEER_CN_ALLOWLIST` is non-empty for the local server; `0` = no enforcement — REDESIGN-001 Phase 6.10 |
| `registry_auth_jwt_kid_fallback_total` | counter | `reason` (`missing_kid` / `unknown_kid`) | REDESIGN-001 Phase 6.5 |

Adding a new metric: define it in `libs/observability/metrics/metrics.go`. The spec-lint rule "every metric promised in §10 must exist" scans this file — if you add a metric here that isn't in `metrics.go`, the rule fires.

---

## 3. otel-collector wiring

The local stack uses `otel-collector → Prometheus → Jaeger SPM`. The collector requires:

- An `otlp` receiver pipeline for **metrics** (not only `traces`), otherwise SDK metric pushes fail with `Unimplemented MetricsService`.
- The prometheus exporter `namespace: registry` is reflected in Jaeger's `PROMETHEUS_QUERY_NAMESPACE=registry` env var so SPM queries the right metric names.

---

## 4. Structured Logging

- Logger: `log/slog` (Go 1.21+ standard library).
- Format: JSON in production, text in development (`LOG_FORMAT=json|text`).
- Level: `LOG_LEVEL=debug|info|warn|error`.
- Every log entry must include: `trace_id`, `span_id`, `tenant_id` (where available), `service`.
- **Never log:** passwords, tokens, API keys, private key material, full request bodies.

---

> **Last updated:** see Git log.
