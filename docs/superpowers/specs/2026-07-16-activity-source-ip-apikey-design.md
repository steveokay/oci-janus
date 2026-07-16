# Design — source_ip + api_key_id on the principal activity feed

> **Status:** Approved design (2026-07-16). Ready for implementation planning.
> **Scope:** Populate the activity feed's `source_ip` and `api_key_id` columns for
> the registry-auth–published `service_account.*` lifecycle events. Entirely within
> registry-auth + registry-audit. **No proto change, no DB migration.**
> **Depends on:** the `outcome` fix (PR #377, branch `fix/activity-status-outcome`),
> which this builds on — both extend `notificationMetadataMap`. The implementation
> PR must land after #377 (or rebase onto it).

---

## 1. Problem

The principal / API-key activity feed (`GET /access/activity`, rendered at
`/api-keys/activity` and `/activity`) defines three enrichment fields per row —
`status`, `source_ip`, `api_key_id` — but only `status` is (now, post-#377)
populated. `source_ip` and `api_key_id` are always empty because the audit events
never carry them:

- `services/auth/internal/service/activity.go` `trimNotifications` reads
  `meta["source_ip"]` and `meta["api_key_id"]` from the audit `NotificationEvent`
  metadata map (already coded, already unit-tested).
- `services/audit/internal/handler/notifications.go` `notificationMetadataMap` never
  emits those keys, and the upstream event payloads never captured the values.

Empirically (30-day window, admin principal): all 24 events return
`source_ip: ""` and `api_key_id: ""`.

## 2. Scope & non-goals

**In scope:** capture `source_ip` and the authenticating `api_key_id` on the
`service_account.*` lifecycle events that registry-auth publishes (created / updated
/ disabled / enabled / deleted / key_issued / key_revoked / scopes_updated) — the
events this feed actually shows today.

**Non-goals (explicitly deferred):**
- `source_ip` / `api_key_id` on the **core** push/pull/delete events. Those require a
  proto change (`ValidateAPIKey` returning the key id), a new `TokenClaims.APIKeyID`,
  and event-payload/consumer plumbing across `services/core` + `services/audit`. Out
  of scope; a possible Phase 2.
- No new `audit_events` column for `api_key_id` (it lives in metadata JSON).
- No FE change (the columns already exist in the table and the reader).

**`api_key_id` semantics:** the API key that **authenticated the request** (the actor
key), consistent across rows. Populated when the admin acted via an API key; blank
for browser/JWT sessions. The *issued* key on `key_issued` remains in the
action/summary, not in this column.

## 3. Capture — cross-cutting via request context

The SA lifecycle events are emitted from registry-auth's own HTTP handlers (e.g.
`createSAAPIKey` → `saService.IssueKey` → `s.audit.Emit`; the SA CRUD handlers →
`CreateServiceAccount` / `Disable` / … → `s.audit.Emit`). These handlers run behind
the gateway (a trusted proxy), so the existing trusted-proxy helper `remoteIP(r)`
(`services/auth/internal/handler/http.go:1059`) already yields the **real client
IP**, and the authenticating key id is already present as the synthesized
`Claims.KeyID` (set on the `Bearer key.` / Basic-auth paths; `uuid.Nil` for JWT).

To avoid changing every SA service method signature, capture once and stamp centrally:

1. **Auth HTTP middleware / handler entry** computes `remoteIP(r)` and stores it in
   the request `context.Context` under a package-private key. (The API-key id is
   already reachable from the request context via the auth claims.)
2. **The audit emitter** — `rabbitMQAuditEmitter.publishSALifecycle`
   (`services/auth/internal/server/server.go:697`), which already receives `ctx` —
   reads `source_ip` and `api_key_id` from that context and stamps them onto the
   outgoing `ServiceAccountLifecyclePayload`. No SA service method signatures change;
   every auth-emitted lifecycle event is covered uniformly.

**Context helpers:** add a small pair, e.g. `withRequestSourceIP(ctx, ip)` /
`requestSourceIP(ctx)` and (if not already exposed) `requestAPIKeyID(ctx)` derived
from the claims already in context. Missing values resolve to the empty string —
never a panic, never a bogus value.

## 4. Storage & surfacing — two appropriate homes, no migration

**Event payload** (`libs/rabbitmq/events/events.go`): add two fields to
`ServiceAccountLifecyclePayload`:

```go
SourceIP string `json:"source_ip"`
APIKeyID string `json:"api_key_id"`
```

These are Go structs (RabbitMQ payloads), not protobuf — no `buf breaking` concern.
`libs` is a shared module: per CLAUDE.md §5 the change lands in one PR that keeps
every affected service (auth + audit) building.

**source_ip → the existing `audit_events.actor_ip` column** (currently always
`""`). The audit consumer's `RoutingServiceAccountLifecycle` case
(`services/audit/internal/eventconsumer/consumer.go:663`) sets
`AuditEvent.ActorIP = payload.SourceIP`. This is the semantic home and also enriches
audit exports.

**api_key_id → the event metadata JSON** (no column). It rides in the payload; the
read path extracts it by adding `APIKeyID string json:"api_key_id"` to the curated
`rawNotificationPayload` struct (`notifications.go:226`).

**Surfacing** (`services/audit/internal/handler/notifications.go`
`notificationMetadataMap`, the same function the `outcome` fix extended): set

```go
if row.ActorIP != "" { m["source_ip"] = row.ActorIP }   // needs outcome/row context
if p.APIKeyID != "" { m["api_key_id"] = p.APIKeyID }
```

Note `source_ip` comes from the **row** (`ActorIP` column) while `api_key_id` comes
from the **payload** (`p`). `notificationMetadataMap` already takes `(p, outcome)`
after #377; it gains the `ActorIP` value the same way (either pass `row.ActorIP` in,
or set `source_ip` in `notificationFromRow` where both `row` and `p` are in scope).
The auth `trimNotifications` reader already consumes `meta["source_ip"]` and
`meta["api_key_id"]` — its extraction is already unit-tested — so the FE columns
populate with no auth-reader or frontend change.

## 5. Data flow (end to end)

```
auth HTTP handler (remoteIP(r) → ctx; Claims.KeyID in ctx)
  → s.audit.Emit(ctx, AuditEvent{...})           # SA service, unchanged
    → publishSALifecycle(ctx, ev)                # reads ctx, stamps payload
      → ServiceAccountLifecyclePayload{SourceIP, APIKeyID, ...}  (RabbitMQ)
        → audit consumer RoutingServiceAccountLifecycle case
            AuditEvent.ActorIP = payload.SourceIP           # → actor_ip column
            metadata.raw carries api_key_id                 # payload JSON
          → notificationFromRow
              meta["source_ip"]  = row.ActorIP
              meta["api_key_id"] = p.APIKeyID
            → auth ActivityService.trimNotifications (already reads both)
              → GET /access/activity → FE ActivityTable columns
```

## 6. Testing

- **auth:** unit test that `publishSALifecycle` stamps `source_ip` / `api_key_id`
  from context onto the published payload — API-key request → both set; JWT request →
  `api_key_id` empty, `source_ip` set. (Use the existing publisher/emitter test
  seams.)
- **audit:** extend the consumer test so the `RoutingServiceAccountLifecycle` case
  asserts `payload.SourceIP → AuditEvent.ActorIP`; extend the notification tests
  (`notifications_outcome_test.go`, added by #377, or a sibling) to assert
  `meta["source_ip"]` (from `ActorIP`) and `meta["api_key_id"]` (from payload) are
  surfaced.
- **live-verify:** rebuild auth + audit; create an SA / issue a key via an
  API-key-authenticated request (`Bearer key.<id>.<secret>`) and via a browser/JWT
  session; confirm the feed shows the client IP on both rows and the key id only on
  the API-key one.
- **gates:** `services/auth` and `services/audit` per-service targets (build / test /
  vet / golangci-lint). The `libs/rabbitmq/events` change means both service modules
  must still build+test in the same PR (CLAUDE.md §5).

## 7. Docs & tracker

- Update the activity-feed docs (auth service section in `docs/SERVICES.md` and/or the
  FE-API-048 notes) to record that `source_ip` / `api_key_id` are now populated for
  auth-published access events, and that core push/pull enrichment is a deferred
  Phase 2.
- `status.md` row; this also closes the "out of scope" caveat noted in PR #377.

## 8. Risks / open questions

- **Some SA mutations might arrive via the management BFF → auth gRPC** rather than
  auth's HTTP handlers. On that path auth's `remoteIP` would see the BFF, not the
  client. The plan must verify per-endpoint that the instrumented SA-lifecycle
  triggers run through auth's HTTP handlers (where `remoteIP(r)` is the real client);
  any that come via gRPC either need the client IP forwarded in gRPC metadata or are
  documented as capturing the BFF hop. Resolve during planning by tracing each
  `s.audit.Emit` caller to its entry point.
- **`api_key_id` will be sparse** until the deferred core-path Phase 2 — admin SA
  mutations done in the browser show a blank key id (correct: no API key
  authenticated them). This is expected and honest, not a defect.
