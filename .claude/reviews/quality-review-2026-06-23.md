# Code Quality + Testing Review — 2026-06-23

> Reviewer: code-review subagent
> Scope: backend services + libs + frontend + proto
> Total findings: 28

## Top-5 high-impact findings

1. **QA-001 (P0)** — `signatures` table + `services/signer` carry no `tenant_id`. Global `(manifest_digest, signer_id)` namespace lets one tenant see another tenant's signature for the same public-image digest.
2. **QA-002 (P0)** — `libs/rabbitmq/publisher.Publisher.Publish` is not goroutine-safe; the single `confirms` channel correlates ACKs to the wrong publisher under concurrent load.
3. **QA-003 (P0)** — `services/webhook.PollDueDeliveries` uses `FOR UPDATE SKIP LOCKED` on a pooled `Query` (no explicit tx); locks release on `rows.Close()`. Overlapping ticks dispatch the same delivery twice.
4. **QA-004 (P1)** — `services/core` + `services/proxy` cache JWT validation under Redis key `jwt:valid:<raw token>` (CLAUDE.md §7 specifies `<jti>`). Any Redis read leak hands the attacker every live bearer token verbatim.
5. **QA-005 (P1)** — `services/scanner.Store` never GCs scan records — long-running workers leak one map entry per scan forever.

## Detailed findings

### QA-001 — Signer carries no tenant isolation
- **Where:** `services/signer/migrations/000001_create_signatures.sql`; `services/signer/internal/repository/repository.go:33-115`; `services/signer/internal/sigstore/store.go:16-193`; `services/signer/internal/handler/grpc.go:30-127`
- **Issue:** No `tenant_id` column on `signatures`. `Store`/`List`/`FindRec` operate on global `(manifest_digest, signer_id)` keys. gRPC handler accepts `tenant_id` but never propagates.
- **Fix:** Add `tenant_id UUID NOT NULL` to migration; update unique constraint to `(tenant_id, manifest_digest, signer_id)`; thread through every store/repo method; bake `tenant_id` into Cosign payload critical claims.
- **Effort:** M · **Priority:** P0

### QA-002 — RabbitMQ publisher not safe for concurrent use
- **Where:** `libs/rabbitmq/publisher/publisher.go:59-98`
- **Fix:** `sync.Mutex` around publish-then-read sequence, or refactor to outbox-correlation keyed on `DeliveryTag`.
- **Effort:** M · **Priority:** P0

### QA-003 — `PollDueDeliveries` lock meaningless without open transaction
- **Where:** `services/webhook/internal/repository/repository.go:152-179`
- **Fix:** Wrap SELECT in `pool.BeginTx`, `UPDATE ... SET status='in_flight'` in same tx, COMMIT before dispatching.
- **Effort:** S · **Priority:** P0

### QA-004 — JWT cached under raw token as Redis key
- **Where:** `services/core/internal/service/auth.go:46`; `services/proxy/internal/handler/http.go:565`
- **Fix:** Parse JWT once for `jti` claim, key on `jwt:valid:<jti>` per CLAUDE.md §7.
- **Effort:** S · **Priority:** P1

### QA-005 — `services/scanner` in-memory `Store` unbounded
- **Where:** `services/scanner/internal/store/store.go:30-96`
- **Fix:** Periodic sweep deleting terminal-status records >24h, or drop in-memory store (metadata is system of record).
- **Effort:** S · **Priority:** P1

### QA-006 — `init()` reads env var bypassing config loader
- **Where:** `services/auth/internal/handler/http.go:39-59` reads `TRUSTED_PROXY_CIDRS`
- **Fix:** Move into typed config struct; parse to `[]*net.IPNet` in handler constructor.
- **Effort:** S · **Priority:** P2

### QA-007 — SSRF check has TOCTOU race in webhook dialer
- **Where:** `services/webhook/internal/delivery/dispatcher.go:80-105`
- **Issue:** Custom dialer resolves hostname, validates IPs, then dials by **hostname** triggering fresh DNS resolution. DNS rebinding hits private IPs.
- **Fix:** Pass resolved IP literally to `DialContext`. Validate every returned IP, dial by IP.
- **Effort:** S · **Priority:** P1

### QA-008 — Webhook delivery dispatch spawns unbounded goroutines
- **Where:** `services/webhook/internal/worker/worker.go:112-121`
- **Fix:** Bounded worker pool or `errgroup.SetLimit(N)`.
- **Effort:** S · **Priority:** P2

### QA-009 — `RetryInterceptor` retries DEADLINE_EXCEEDED on same ctx
- **Where:** `libs/middleware/grpc/client.go:57-88`
- **Fix:** Drop `DeadlineExceeded` from retry-eligible set, or build fresh per-attempt deadline.
- **Effort:** S · **Priority:** P2

### QA-010 — `domainworker` uses uncancellable `net.LookupTXT`
- **Where:** `services/tenant/internal/domainworker/worker.go:114`
- **Fix:** `net.DefaultResolver.LookupTXT(ctx, target)`.
- **Effort:** S · **Priority:** P2

### QA-011 — `LoggingStreamInterceptor` drops `request_id`
- **Where:** `libs/middleware/grpc/server.go:163-174`
- **Fix:** Add `RequestIDStreamInterceptor` wrapping `grpc.ServerStream` with ctx-overridden stream.
- **Effort:** S · **Priority:** P2

### QA-012 — `Pool.TriggerScanJob` swallows `Enqueue` error
- **Where:** `services/scanner/internal/worker/worker.go:759-770`
- **Fix:** Return `(scanID, err)`; handler deletes pending row + returns `codes.ResourceExhausted` on err.
- **Effort:** S · **Priority:** P2

### QA-013 — Cross-tenant upload-state check missing in core upload handler
- **Where:** `services/core/internal/handler/http.go:701-774`; `services/core/internal/service/upload.go:33-74`
- **Issue:** `UploadStore` keys on `upload:<uuid>` with no tenant prefix. Handler validates push access on repo but never asserts `claims.TenantID == st.TenantID`.
- **Fix:** Add `tenant_id` to Redis key (`upload:<tenant_id>:<uuid>`) + constant-time tenant check.
- **Effort:** S · **Priority:** P1

### QA-014 — `gateway` service is a stub
- **Where:** `services/gateway/internal/server/server.go`
- **Issue:** CLAUDE.md §4 says gateway does TLS + tenant resolution + rate limit. Binary registers only health + `/metrics`.
- **Fix:** Remove the Go gateway service or add top-of-file comment that Traefik does the heavy lifting + this is a health-only sidecar.
- **Effort:** S · **Priority:** P2

### QA-015 — `Signer.SignPayload` accepts tenant ID but no impl uses it
- **Where:** `services/signer/internal/signing/signer.go:21-29, 74-86`; `vault.go:97-113`
- **Fix:** Either drop `tenantID` from interface OR include in `cosignOptional`. Coupled with QA-001.
- **Effort:** S (drop) / M (include + verify-side change) · **Priority:** P1

### QA-016 — `vaultSigner.SignPayload` creates its own context
- **Where:** `services/signer/internal/signing/vault.go:97-113`
- **Fix:** Add `ctx context.Context` first parameter to `Signer.SignPayload` + `VerifyPayload`; forward from handler.
- **Effort:** S · **Priority:** P2

### QA-017 — `storage_used` SUM subquery in hot list path
- **Where:** `services/metadata/internal/repository/repository.go:63-67, 174-252`
- **Fix:** Maintain `repositories.storage_used` via trigger on manifest insert/delete (wire existing `IncrementRepoStorage`).
- **Effort:** M · **Priority:** P2

### QA-018 — `ListRepositories` has no LIMIT / no pagination
- **Where:** `services/metadata/internal/repository/repository.go:174-252`
- **Fix:** Add `page_token + page_size` per CLAUDE.md §12 (default 100, max 500).
- **Effort:** M · **Priority:** P2

### QA-019 — Frontend has no top-level ErrorBoundary
- **Where:** `frontend/src/routes/__root.tsx`; `frontend/src/main.tsx`
- **Fix:** Add `react-error-boundary` wrapping `<Outlet />` in `__root.tsx`.
- **Effort:** S · **Priority:** P2

### QA-020 — Frontend test coverage near-zero
- **Where:** 3 test files for ~140 components
- **Fix:** Prioritise `lib/api/client.ts` (refresh+retry stampede), `lib/auth/store.ts`, `lib/auth/jwt.ts`, plus auth route + role-gate snapshots.
- **Effort:** L · **Priority:** P1

### QA-021 — axios interceptor uses substring match for refresh-exempt paths
- **Where:** `frontend/src/lib/api/client.ts:81-87` (`original.url?.includes("/login")`)
- **Fix:** Exact path matching against `const NO_REFRESH_PATHS = new Set([...])`.
- **Effort:** S · **Priority:** P2

### QA-022 — `time.Sleep` in integration tests
- **Where:** `services/metadata/internal/testutil/integration/retention_test.go:75,101,133,158`; `retention_org_test.go:98`; `scanner/internal/testutil/integration/scanner_test.go:361`; `scanner/internal/worker/swap_test.go:95,105`
- **Fix:** Channel signals, `assert.Eventually`, or testcontainers readiness probes.
- **Effort:** M · **Priority:** P2

### QA-023 — `RequireAuth` makes gRPC call per request, no caching
- **Where:** `services/management/internal/middleware/auth.go:33-56`
- **Fix:** Apply REM-002 caching pattern (with QA-004 fix on key format).
- **Effort:** S · **Priority:** P2

### QA-024 — `MapDBError` doesn't handle serialization/deadlock codes
- **Where:** `libs/errors/codes/codes.go`
- **Fix:** Expand for 40xxx + 08xxx classes. Overlaps with REM-016.
- **Effort:** S · **Priority:** P2 (folded into REM-016)

### QA-025 — Scanner policy resolver errors silently disable enforcement
- **Where:** `services/scanner/internal/worker/worker.go:327-346, 720-731`
- **Fix:** Either fail-closed (NACK; broker retries), or emit `metric_policy_resolver_failed_total` + audit event.
- **Effort:** S · **Priority:** P2

### QA-026 — Long `handler.go` in services/management
- **Where:** `services/management/internal/handler/handler.go` (2000+ lines)
- **Fix:** Continue extraction — repo, tag, scan, webhook handlers each into own file.
- **Effort:** M · **Priority:** P2

### QA-027 — `worker.Pool.persistScanStatus` swallows errors
- **Where:** `services/scanner/internal/worker/worker.go:544-577`
- **Fix:** Order so RabbitMQ publish happens AFTER metadata write succeeds.
- **Effort:** S · **Priority:** P2

### QA-028 — `context.Background()` fire-and-forget idiom inconsistent
- **Where:** `services/core/internal/service/registry.go:389,403`; `services/signer/internal/sigstore/store.go:96`; `services/auth/internal/service/auth.go:553`; `services/audit/internal/eventconsumer/consumer.go:148`; `services/proxy/internal/handler/http.go:452`
- **Fix:** `libs/` helper `DetachContext(parent, timeout)` using `context.WithoutCancel` (Go 1.21+).
- **Effort:** S · **Priority:** P2

## Test coverage summary

- **Strong:** `services/auth` (24 test files incl integration), `services/core` (10 tests + OCI conformance), `services/scanner`.
- **Weak:** `services/gateway` (stub), `services/storage` (handler+integration only; no S3/GCS/Azure driver coverage), `services/signer` (tenant_id missing-by-design has zero coverage).
- **Untested critical paths:** publisher concurrency (QA-002), webhook overlap (QA-003), JWT key collision (QA-004), FE axios refresh stampede (QA-020), SSRF DNS rebinding (QA-007).
- **Flaky:** `time.Sleep` in 7 test files (QA-022); `<-time.After(5ms)` in `scanner/worker/swap_test.go:95`.

---

## Summary

**28 findings** across 13 services + libs + frontend + proto. Three systemic themes:

1. **Concurrency primitives quietly wrong** — publisher (QA-002), webhook poller (QA-003), unbounded goroutines (QA-008), DEADLINE_EXCEEDED retry (QA-009). Invisible at low load, fire at scale.
2. **Tenant isolation half-baked outside metadata** — signer ignores tenant_id (QA-001, QA-015), upload Redis keys lack tenant prefix (QA-013), JWT cache key uses raw token (QA-004).
3. **Context propagation idioms inconsistent** — 5 different "detached context" patterns with 3 different timeouts (QA-028); `os.Getenv` in `init()` (QA-006); `LookupTXT` without ctx (QA-010); signer invents own ctx (QA-016).

**If you fix nothing else, fix QA-002 (RabbitMQ publisher concurrency).** Silent corruption of "publisher-confirms = durable" guarantee across audit, scan, push, signing, retention, and webhook event flows. One-line `sync.Mutex` change with outsized correctness impact.
