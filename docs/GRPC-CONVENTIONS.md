# gRPC Conventions

> **Source of truth for gRPC mechanics.** CLAUDE.md §12 holds only the
> rules; this file holds the proto layout, package-naming convention,
> and the server/client interceptor chains. When code disagrees with
> this file, the code is wrong — but `proto/` and
> `libs/middleware/grpc` are the authoritative implementations.

## Table of Contents

1. [Proto File Rules](#1-proto-file-rules)
2. [Server-side Interceptors](#2-server-side-interceptors)
3. [Client-side Interceptors](#3-client-side-interceptors)

---

## 1. Proto File Rules

### Layout

```
proto/
├── auth/v1/auth.proto
├── storage/v1/storage.proto
├── metadata/v1/metadata.proto
├── proxy/v1/proxy.proto
├── scanner/v1/scanner.proto
├── signer/v1/signer.proto
├── tenant/v1/tenant.proto
├── webhook/v1/webhook.proto
├── audit/v1/audit.proto
├── gen/go/                    # Generated stubs — committed, not gitignored
└── buf.yaml
```

### Conventions

- **Package naming:** `registry.<service>.v1`.
- **Go package option:** `option go_package = "github.com/steveokay/oci-janus/proto/gen/go/<service>/v1;<service>v1";`
- **All fields use `snake_case`.**
- **Errors:** all RPCs return errors using `google.rpc.Status` (import `google/rpc/status.proto`).
- **Pagination:** use `page_token` (string) + `page_size` (int32) pattern, not offset.
- **Timestamps:** `google.protobuf.Timestamp`.
- **UUIDs:** `string` (not bytes).
- **Breaking changes:** never modify existing field numbers. Add new fields only. The `breaking` CI job enforces this against the previous main commit.

### Regenerating stubs

- Run `buf generate` from `proto/`.
- The generated `proto/gen/go/**` output IS committed. `buf.gen.yaml` and the pinned `buf` version are the source of truth for stub shape.

---

## 2. Server-side Interceptors

Applied to every gRPC server via `libs/middleware/grpc`, in this order (outermost first):

1. **Recovery** (panic → gRPC Internal error)
2. **Request ID injection**
3. **mTLS peer verification** (CN check — see `docs/AUTH.md`)
4. **Auth token validation** (for external-facing services)
5. **Tenant ID extraction + context injection**
6. **OpenTelemetry tracing** (via `OTELServerHandler` as a `StatsHandler`, not a UnaryInterceptor)
7. **Structured logging**
8. **Metrics**
9. **Server-side gRPC cache interceptor** on `registry-metadata` only (REM-007)

Each interceptor is a small helper in `libs/middleware/grpc`; the composition happens in the `ServerInterceptors()` / `StreamServerInterceptors()` builders.

---

## 3. Client-side Interceptors

Applied to every outbound gRPC client:

1. **mTLS credential attachment** (via `loader.BaseConfig.MTLSClientCreds(serverName)` — see `docs/AUTH.md`)
2. **OpenTelemetry trace propagation**
3. **Deadline injection**
4. **Retry** — 3 attempts, exponential backoff, only on `UNAVAILABLE` and `DEADLINE_EXCEEDED`

Use `grpc.NewClient` (not the deprecated `grpc.Dial`) with a timeout context — never silently hang. Trigger eager connection establishment via `conn.Connect()` at startup so the first inbound request does not stall during the TLS/HTTP-2 handshake.

---

> **Last updated:** see Git log.
