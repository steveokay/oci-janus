# Architecture overview

OCI-Janus is a set of **14 Go services** behind a gateway, talking to each other
over gRPC with mutual TLS, and coordinating asynchronously over RabbitMQ. This
page is the map; per-service detail (endpoints, schemas, env vars) lives in
[Services](SERVICES.md).

```
                        ┌─────────────────────────────────────────┐
                        │           registry-gateway               │
                        │   (Traefik/Nginx + TLS termination)      │
                        │   Routes by host header (custom domains) │
                        └────────────┬────────────────────────────┘
                                     │ HTTPS
               ┌─────────────────────┼──────────────────────┐
               │                     │                      │
        ┌──────▼──────┐      ┌───────▼──────┐      ┌───────▼──────┐
        │ registry-   │      │ registry-    │      │ registry-    │
        │   auth      │      │   core       │      │   proxy      │
        │ (JWT/API    │      │ (OCI API)    │      │ (pull-thru)  │
        │  key issue) │      │              │      │              │
        └──────┬──────┘      └───────┬──────┘      └──────┬───────┘
               │ gRPC                │ gRPC                │ gRPC
               │              ┌──────▼──────┐             │
               │              │  registry-  │             │
               │              │  metadata   │             │
               │              │ (PostgreSQL)│             │
               │              └──────┬──────┘             │
               │                     │ gRPC               │
               │              ┌──────▼──────┐             │
               └─────────────►│  registry-  │◄────────────┘
                              │   storage   │
                              │(MinIO/S3/   │
                              │  GCS/Azure) │
                              └─────────────┘
```

## Three planes

The services fall into three groups:

- **Edge** — [`registry-gateway`](SERVICES.md) (Traefik): TLS termination,
  host-based routing, rate limiting.
- **Data plane** — bytes on the OCI API: `registry-core` (the `/v2/` spec
  implementation), `registry-storage` (pluggable blob backends), `registry-proxy`
  (pull-through cache).
- **Control plane** — identity, metadata, and lifecycle: `registry-auth`
  (JWT + API keys + SSO + RBAC), `registry-metadata` (repos/tags/manifests source
  of truth), `registry-tenant` (tenant CRUD + deployment metadata),
  `registry-scanner` (vuln scans + compliance reports), `registry-signer`
  (Cosign), `registry-webhook`, `registry-audit` (tamper-evident log),
  `registry-gc` (mark-sweep), `registry-management` (the REST BFF), and
  `registry-mcp` (read-only MCP server for AI assistants).

## Communication patterns

**Synchronous (internal):** all service-to-service calls are **gRPC over mTLS**.
Every server requires and verifies a client cert whose CN is on its allowlist;
certs hot-reload on renewal. Proto contracts live in `proto/` with generated
stubs committed. See [gRPC conventions](GRPC-CONVENTIONS.md).

**Asynchronous (events):** services publish and consume typed events on the
`registry.events` RabbitMQ **topic exchange** (quorum queues, publisher confirms,
manual consumer ACK, per-queue dead-letter exchange). For example:

```
registry-core  ──push.completed──►  registry-scanner / audit / webhook
registry-scanner ──scan.completed──► registry-metadata / webhook
registry-gc     ──gc.run──►          registry-storage (delete blobs)
registry-auth   ──rbac.role_granted──► registry-audit
```

The full routing-key + payload catalogue is in [Events](EVENTS.md).

## Data ownership

Each stateful service owns its **own database** — `registry-metadata` holds
repo/tag/manifest metadata; `registry-auth`, `registry-tenant`, `registry-proxy`,
`registry-webhook`, `registry-audit`, and `registry-scanner` each own a separate
schema. No service reaches into another's tables; they cross boundaries only over
gRPC. Persistence conventions (pgx, migrations, RLS) are in
[Database](DATABASE.md).

## Security posture

- **mTLS everywhere** between services, not just at the edge.
- **Multi-key RS256 JWT** signing with a JWKS ring + rotation, plus API keys.
- **Tamper-evident audit** — Postgres `FORCE ROW LEVEL SECURITY`, a low-privilege
  runtime role, and a per-tenant SHA-256 hash chain.
- **Secrets sealed at rest** with per-purpose AES-256-GCM KEKs (see the
  [integrations catalogue](integrations/index.md) and
  [Self-hosting](SELF-HOSTING.md)).

Details: [Authentication](AUTH.md) · [Observability](OBSERVABILITY.md) ·
[Hardening checklist](HARDENING-CHECKLIST.md).

## Deployment posture

The platform is single-tenant: one deployment serves one bootstrap tenant. The
`tenant_id` columns stay frozen in the schema and wire format, but the
`DEPLOYMENT_MODE` toggle and the SaaS/`multi` surface were removed in v3
([ADR-0031](adr/0031-retire-multi-tenant-posture.md)). See
[Migrating v1 → v2](MIGRATION-v1-to-v2.md) and the ADRs for rationale.
