# System Architecture Review — 2026-06-23

> Reviewer: architecture review agent (Opus 4.7 / 1M context)
> Scope: services, contracts, deployment, ops
> Total findings: 22

## Top-5 high-impact findings (architecture-level)

1. **ARCH-001** — PostgreSQL RLS is documented as a second-layer defence but is implemented only on `audit_events`. Metadata/auth/webhook/proxy/tenant DBs all have `tenant_id` columns but no `ENABLE ROW LEVEL SECURITY`. The documented "application bug can't leak cross-tenant data" property does not hold today.
2. **ARCH-002** — No transactional outbox for RabbitMQ. `push.completed`, `scan.completed`, `image.signed`, `rbac.*`, `service_account.lifecycle` all published after a DB commit. A broker outage or crash between commit and publish silently loses the event.
3. **ARCH-003** — Migrations run at service startup with no coordination, init-container, or Job. With `replicaCount: 3-5` in prod, all replicas race on goose's advisory lock. A bad migration takes down every replica simultaneously, no rollback path.
4. **ARCH-004** — Helm charts ship without graceful-shutdown wiring. No `terminationGracePeriodSeconds`, no `preStop`. Rolling restart on `registry-core` mid multi-GB layer push severs the upload at the 30s default.
5. **ARCH-005** — Configuration validation does not enforce production-mode invariants. CLAUDE.md §7 says "config validation must reject empty cert paths when `OTEL_ENVIRONMENT=production`" — this check exists nowhere.

## Detailed findings

### ARCH-001 — Documented RLS layer mostly unimplemented
- **Layer:** Data model / Security
- **Where:** All non-audit service migrations. Only `services/audit/migrations/20240101000002_audit_rls_role.sql` enables RLS.
- **Fix:**
  1. Per-service migration enabling RLS on every tenant-scoped table.
  2. Common middleware in `libs/middleware/grpc`: extract `tenant_id` from auth interceptor's context + `SET LOCAL app.tenant_id = $1` at tx start.
  3. Low-privilege application role per service (mirror `registry_audit_app`).
  4. CI integration test: as app role without `SET LOCAL`, assert `SELECT *` returns zero rows.
- **Migration path:** Per service, audit-first. Each rollout is one migration + one server.go diff.
- **Effort:** M · **Priority:** P0

### ARCH-002 — No transactional outbox for event publication
- **Layer:** Async messaging / Data integrity
- **Where:** Every publisher follows: PG commit → `publisher.Publish(...)`. Broker outage / process kill between = event lost.
- **Fix:** Per-service `outbox_events` table (id, occurred_at, routing_key, tenant_id, payload, published_at, attempts); handlers write outbox row inside same tx as business state; background worker drains via `FOR UPDATE SKIP LOCKED`.
- **Migration path:** Roll out service by service starting with `registry-core`. `libs/outbox` shared. Existing direct publishers keep working during transition.
- **Effort:** M · **Priority:** P0

### ARCH-003 — Schema migration doesn't survive multi-replica rollouts
- **Layer:** Ops / Deployment
- **Where:** `services/*/internal/server/server.go` (every service calls `goose.Up()` at boot — verified at `services/auth/internal/server/server.go:304`)
- **Fix:**
  1. Helm `pre-install`/`pre-upgrade` Job named `<svc>-migrate` running goose up.
  2. `Deployment.initContainer: wait-for-migration` polling `schema_migrations`.
  3. Gate on-boot `goose.Up()` behind `RUN_MIGRATIONS_ON_BOOT=true` (default off in Helm, on in Compose).
  4. `docs/SCHEMA-EVOLUTION.md` (see ARCH-018).
- **Effort:** M · **Priority:** P1

### ARCH-004 — No graceful-shutdown wiring in Helm
- **Layer:** Ops
- **Where:** Every `charts/*/templates/deployment.yaml`. No `preStop` or `terminationGracePeriodSeconds`.
- **Fix:**
  ```yaml
  terminationGracePeriodSeconds: 120
  lifecycle:
    preStop:
      exec:
        command: ["/bin/sleep", "10"]
  ```
- **Effort:** S · **Priority:** P1

### ARCH-005 — `validate()` doesn't enforce production-mode invariants
- **Layer:** Configuration / Security
- **Fix:** `libs/config/loader.RequireMTLSInProd(b BaseConfig, ca, cert, key string) error`. Same for `DB_DSN sslmode=require`, non-empty `JWT_KEY_ID`, empty `DEV_DEFAULT_TENANT_ID`.
- **Effort:** S · **Priority:** P0

### ARCH-006 — `libs/rabbitmq/publisher` has no reconnection / channel-recovery
- **Layer:** Async messaging / Resilience
- **Fix:** Supervisor goroutine on `NotifyClose` → reconnect, redeclare exchange, re-enable confirms. ~50 lines.
- **Effort:** S · **Priority:** P1

### ARCH-007 — `services/management` BFF accumulating business logic across 13 boundaries
- **Layer:** Service boundaries / Coupling
- **Fix:** (a) RBAC authoritative only in backing services' auth interceptors; management's checks become UX hints, canonical 403 comes from downstream. (b) Use proto-generated gRPC-Gateway for straight CRUD; keep hand-rolled BFF only for composing surfaces (tag detail).
- **Effort:** L · **Priority:** P2

### ARCH-008 — Tenant policy knobs split across 4 services with no canonical view
- **Layer:** Data model / Service boundaries
- **Fix:** `TenantPolicyService` gRPC on `services/tenant` that fans out + returns normalised view. Don't move source-of-truth — provide read facade.
- **Effort:** M · **Priority:** P2

### ARCH-009 — gRPC client retry amplifies thundering-herd on slow auth
- **Layer:** Resilience / Scaling
- **Fix:** Circuit breaker (sony/gobreaker); `singleflight` per-JTI on cache miss; jittered backoff.
- **Effort:** S · **Priority:** P1

### ARCH-010 — Cross-DB FKs absent by design; orphan-row tracking isn't
- **Layer:** Data model
- **Fix:** Every service subscribes to `tenant.deleted` with idempotent cascade; nightly reconciliation job; document per-service "what gets deleted" in `docs/SERVICES.md`.
- **Effort:** M · **Priority:** P1

### ARCH-011 — Storage key layout is operator-friendly but tenant export tooling missing
- **Layer:** Storage / Ops
- **Fix:** `tools/tenant-export` CLI walking storage + dumping per-tenant PG slices into tarball.
- **Effort:** S · **Priority:** P2

### ARCH-012 — Helm exposes no ServiceMonitor / PodMonitor
- **Layer:** Observability / Ops
- **Issue:** None of the helm charts ship `ServiceMonitor` CRs. `/metrics` on `:9090` but Prometheus must be told to scrape. Fresh self-hoster installs chart, sees nothing in Grafana.
- **Fix:**
  - `templates/servicemonitor.yaml` per chart, gated by `.Values.metrics.serviceMonitor.enabled`.
  - `templates/prometheusrule.yaml` with starter alerts (5xx rate, gRPC retry-budget, RabbitMQ depth, pgxpool exhaustion, JWT cache hit ratio).
  - Starter Grafana dashboard JSON in `infra/grafana/dashboards/`.
- **Effort:** M · **Priority:** P1

### ARCH-013 — Read-replica routing only `services/metadata` uses it
- **Layer:** Performance / Scaling
- **Fix:** Adopt the metadata reader/writer pattern in `services/audit` + `services/auth`. Document in `prod-flow.md` §15.
- **Effort:** M · **Priority:** P2

### ARCH-014 — Single shared Postgres in Compose vs separate DBs in prod
- **Layer:** Dev-vs-prod parity
- **Fix:** Compose profile `per-service-db` spinning up 6 PG containers mirroring prod. CI integration suite runs this profile.
- **Effort:** S · **Priority:** P2

### ARCH-015 — Compose stack assumes MinIO; no storage-backend smoke profiles
- **Layer:** Self-hostability / Testing
- **Fix:** Compose profiles `storage-fs`, `storage-fake-gcs` (fsouza/fake-gcs-server), `storage-azurite`.
- **Effort:** S · **Priority:** P2

### ARCH-016 — No first-run bootstrap CLI for self-hosters
- **Layer:** Self-hostability
- **Where:** `SELF-HOSTING.md` §3 step 1-9 is 9 manual openssl+base64+compose steps.
- **Fix:** `make bootstrap` / `tools/bootstrap` binary: generate keys, write `.env`, bring up stack, wait for health, prompt admin email/password, create via auth, print `docker login` hint.
- **Effort:** S · **Priority:** P0 (for self-hoster persona)

### ARCH-017 — `registry-gc` documented as CronJob, Helm is Deployment
- **Layer:** Ops / Deployment
- **Issue:** Decision #18 changed GC to need BOTH async queue worker AND cron. Helm wasn't updated.
- **Fix:** `Deployment` (queue worker) + `CronJob` (scheduled trigger) in `charts/gc/templates/`.
- **Effort:** S · **Priority:** P2

### ARCH-018 — No schema-evolution discipline documented
- **Layer:** Contributor experience
- **Fix:** `docs/SCHEMA-EVOLUTION.md` covering expand/contract, `CREATE INDEX CONCURRENTLY`, default-value choice, online ALTER patterns.
- **Effort:** S · **Priority:** P2

### ARCH-019 — Webhook delivery + audit-export delivery duplicate retry+DLX machinery
- **Layer:** Async messaging
- **Fix:** Promote to `libs/delivery/`: `Deliver(ctx, Target, payload)`, `Worker` handling retry+DLX+rate-limit+SSRF+HMAC.
- **Effort:** M · **Priority:** P2

### ARCH-020 — Cosign verification at pull time is RPC-based, not in-process
- **Layer:** Performance / Coupling
- **Fix:** `libs/signing/verify` — pure-Go Cosign verification. Fetch signatures + public keys once per digest (5m cache), verify in-process. Signer service becomes write-path only.
- **Effort:** M · **Priority:** P2

### ARCH-021 — JWT validation cache is RPC+Redis; no local JWKS verification path
- **Layer:** Security / Resilience
- **Issue:** Auth-down ⇒ system fully wedged (fail-closed) even with valid not-yet-expired tokens.
- **Fix:** Local JWKS verifier in `libs/auth/jwt-verify`. Services fetch `/.well-known/jwks.json` once at boot (refresh hourly), verify signature locally. Hit auth only for revocation check (`SISMEMBER jwt:revoked <jti>`).
- **Effort:** M · **Priority:** P1

### ARCH-022 — Storage driver abstraction doesn't expose multipart uniformly
- **Layer:** Performance / Driver design
- **Issue:** Drivers implement multipart internally but gRPC only exposes single streams. Core can't resume interrupted uploads or parallelise large layers.
- **Fix:** Driver interface gains `InitMultipart`, `UploadPart`, `CompleteMultipart`. Surface via Storage proto. Filesystem driver fakes it.
- **Effort:** L · **Priority:** P2

## Systemic observations

**Patterns that work well + should be preserved:**
- Per-service `go.mod` + root `go.work` (Decision #8).
- External-process JSON-RPC scanner plugins (Decision #5).
- Distroless + non-root + read-only-rootfs across all charts.
- Quorum queues + confirm publishes + manual ACK (when complemented by outbox).
- `FORCE ROW LEVEL SECURITY` + low-privilege role on audit (Decision #15) — exemplary. Pattern needs to spread (ARCH-001).
- Same-binary dev/prod for Vault (Decision #14).
- Decision Log in CLAUDE.md — captures rationale at the right grain.

**Patterns that don't generalise:**
- "Feature wired by env-var presence." `FEATURES_ENABLED=...` allow-list would be more auditable.
- "BFF holds business logic" (ARCH-007).
- "Per-service migration runner at boot" (ARCH-003).

**Underused infrastructure:**
- `libs/observability/metrics` — defined; some services emit only subset.
- Read-replica DSN — only `services/metadata`.
- PostgreSQL RLS — only `services/audit`.
- `libs/testutil/containers/auth_with_audit.go` — beautiful cross-service bundle, used in exactly one test.

**Missing infrastructure:**
- Transactional outbox (ARCH-002).
- Helm `ServiceMonitor` + starter `PrometheusRule` (ARCH-012).
- Helm migration `Job` + `initContainer` (ARCH-003).
- `libs/delivery/` for retry+DLX+SSRF+HMAC reuse (ARCH-019).
- Local JWKS verifier `libs/auth/jwt-verify` (ARCH-021).
- First-run bootstrap CLI (ARCH-016).
- Tenant export runbook (ARCH-011).

## Self-hostability assessment

**Frictionless for a new self-hoster:**
- `SELF-HOSTING.md` is current with clear Compose / Helm / fork+customise paths.
- One-command Compose stack with all 17 containers.
- Real OCI conformance (75/75) — `docker push` works against `localhost:8081`.
- Apache 2.0 + clear customisation hooks.
- mTLS-by-default in dev via `cert-init`.

**Still needs work:**
- 9 manual openssl/base64 steps before first push (ARCH-016).
- Helm chart "structure correct, untested" per prod-flow.md §17.
- Migration upgrade path not documented (ARCH-003).
- Observability dashboards not shipped (ARCH-012).
- SaaS-vs-self-hosted deployment-mode docs (DEPLOY-001 already tracked).
- Frontend isn't packaged.

**Recommended next 3 things for self-hoster persona:**
1. Ship `make bootstrap` + `tools/bootstrap` binary (ARCH-016).
2. Add `ServiceMonitor` + `PrometheusRule` + starter Grafana JSON (ARCH-012).
3. Add `registry-ui` Compose service + Helm sub-chart serving prebuilt frontend bundle.

---

**Summary:** 22 findings spanning service boundaries (ARCH-007/-008/-010), data integrity (ARCH-001/-002), ops/deployment (ARCH-003/-004/-012/-017), security (ARCH-005/-021), resilience (ARCH-006/-009), self-hostability (ARCH-011/-015/-016). Dominant theme: **"documented architecture is one step ahead of implemented architecture"** — RLS, production-mode config enforcement, graceful shutdown, multi-mode GC deployment, and second-layer tenant isolation all described in canonical docs but partially missing from code or Helm.

**The project's biggest architectural risk in the next 6 months is silent data integrity loss from the missing transactional outbox (ARCH-002):** a single broker outage between commit and publish drops `push.completed` / `scan.completed` / audit / SIEM-export events on the floor with no replay path. Recently-landed Tier 1 #4 SIEM streaming inherits this exact gap. Closing ARCH-002 + ARCH-001 should be the next sprint's anchor work.
