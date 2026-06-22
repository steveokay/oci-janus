# RabbitMQ Event Contracts

> Canonical reference for all event types, routing keys, and payload schemas published on the `registry.events` topic exchange.
> The Go type definitions are owned by `libs/rabbitmq/events/events.go` — if this file disagrees, prefer the code.

---

## Exchange Layout

```
Exchange: registry.events    (topic, durable)
Exchange: registry.dlx       (topic, durable) — dead-letter target

Routing keys:
  push.completed
  push.failed
  manifest.deleted
  tag.deleted
  scan.queued
  scan.completed
  scan.policy_blocked
  webhook.queued
  webhook.delivered
  webhook.failed
  gc.run.started
  gc.run.completed
  image.signed              # published by registry-management on /sign success (FE-API-026)
  tenant.created
  tenant.deleted
  tenant.renamed            # FE-API-029
  tenant.plan_changed       # FE-API-029
  tenant.domain.verified
  store.queued              # proxy background-store retry
  rbac.role_granted         # published by registry-auth on GrantRole success
  rbac.role_revoked         # published by registry-auth on RevokeRole success
  auth.provider_created     # FE-API-034 (publisher: registry-auth SSO admin handler)
  auth.provider_updated     # FE-API-034
  auth.provider_deleted     # FE-API-034
  auth.user_sso_provisioned # FE-API-034 (publisher: OAuth/SAML callback path)
```

> The `auth.*` routing keys above are not yet typed in
> `libs/rabbitmq/events`; the SSO admin handler declares them locally
> (`services/auth/internal/handler/sso_admin.go`) and the audit consumer
> treats them generically (routing key + payload JSON). A follow-up
> commit can promote them to typed payloads in the shared events
> package.

---

## Event Envelope

Every event is wrapped in the same envelope:

```go
type Event struct {
    ID         string          `json:"id"`          // UUID v4
    Type       string          `json:"type"`        // routing key
    TenantID   string          `json:"tenant_id"`
    OccurredAt time.Time       `json:"occurred_at"`
    Version    string          `json:"version"`     // "1.0"
    Payload    json.RawMessage `json:"payload"`
}
```

Rules:
- Publishers use confirm mode — wait for broker ACK before returning.
- Consumers use manual ACK — ACK only after successful processing.
- Every queue has a DLX (`dlx.<service>`) configured.
- Message TTL: 7 days on all queues (configurable).
- Do not put sensitive data (passwords, tokens, raw API keys) in payloads.
- All payload structs are defined in `libs/rabbitmq/events/events.go`.

---

## Payload Reference

### `push.completed`

```go
type PushCompletedPayload struct {
    RepositoryName string `json:"repository_name"`
    Tag            string `json:"tag"`
    ManifestDigest string `json:"manifest_digest"`
    PushedBy       string `json:"pushed_by"`     // actor user ID
    SizeBytes      int64  `json:"size_bytes"`
}
```

**Publishers:** `registry-core` (after a successful manifest PUT).
**Consumers:** `registry-scanner`, `registry-audit`, `registry-webhook`.

---

### `scan.completed`

```go
type ScanCompletedPayload struct {
    ManifestDigest  string         `json:"manifest_digest"`
    RepositoryName  string         `json:"repository_name"`
    ScannerName     string         `json:"scanner_name"`
    SeverityCounts  map[string]int `json:"severity_counts"`
    PolicyViolation bool           `json:"policy_violation"`
    Blocked         bool           `json:"blocked"`
}
```

**Publishers:** `registry-scanner`.
**Consumers:** `registry-metadata` (updates `scan_results`), `registry-webhook`.

---

### `rbac.role_granted`

Published by `registry-auth` after a successful `GrantRole` gRPC call. Consumed by `registry-audit` to append an audit record without a direct gRPC dependency on auth.

```go
type RoleGrantedPayload struct {
    TenantID   string `json:"tenant_id"`
    UserID     string `json:"user_id"`
    Role       string `json:"role"`        // "owner"|"admin"|"writer"|"reader"
    ScopeType  string `json:"scope_type"`  // "org" or "repo"
    ScopeValue string `json:"scope_value"` // org name or "org/repo"
    GrantedBy  string `json:"granted_by"`  // user_id of the granting actor
}
```

---

### `rbac.role_revoked`

Published by `registry-auth` after a successful `RevokeRole` gRPC call.

```go
type RoleRevokedPayload struct {
    TenantID     string `json:"tenant_id"`
    AssignmentID string `json:"assignment_id"` // UUID of the deleted role_assignments row
    RevokedBy    string `json:"revoked_by"`    // user_id of the revoking actor
}
```

---

### Service account lifecycle (FE-API-048)

Emitted by `services/auth/internal/service/service_account.ServiceAccountService` on every SA mutation. The current implementation uses a `slogAuditEmitter` stand-in (events go to slog INFO), so they are visible in container logs but not yet persisted to `audit_events`. Durable RabbitMQ-based emission is a follow-up (FUT-007); routing keys + payload shapes below are the planned contract.

All payloads carry a common `actor_id` (the human admin who initiated the action) and `resource` (the SA id). The `fields` map carries action-specific extras.

| Action code | Emitted when | Notable fields |
|---|---|---|
| `service_account.created`        | `POST /api/v1/service-accounts` succeeds | `service_account_id`, `name`, `description`, `allowed_scopes`, `creator_email`, `creator_display_name` (creator snapshot so attribution survives admin offboarding per spec §4.2) |
| `service_account.updated`        | `PATCH /api/v1/service-accounts/:id` (name/desc/allowed_scopes) | `changed` (diff map) |
| `service_account.disabled`       | `PATCH … {disabled: true}` | `reason` (free text, future) |
| `service_account.enabled`        | `PATCH … {disabled: false}` | — |
| `service_account.deleted`        | `DELETE /api/v1/service-accounts/:id` | `name` (snapshot — the row is gone after cascade) |
| `service_account.key_issued`     | `POST /api/v1/service-accounts/:id/api-keys` | `key_id`, `key_prefix` (never the raw secret) |
| `service_account.key_revoked`    | `DELETE …/api-keys/:keyID`          | `key_id`, `key_prefix` |
| `service_account.scopes_updated` | PATCH with `set_allowed_scopes`     | `before` / `after` lists |
| `rbac.role_granted_to_service_account` | A shadow user receives a role grant | Same payload shape as `rbac.role_granted` plus `service_account_id` so admin "list users with role X" surfaces can render SAs distinctly. Distinct routing key so future filters can separate human vs machine grants. |

**Security tripwire:** `ValidateAPIKey` emits `pentest.cross_tenant_attempt` (best-effort, not via the structured payloads above) when an SA-owned key is presented with an `X-Tenant-ID` that disagrees with the SA's owner tenant. Action body carries `service_account_id`, `key_id`, `claimed_tenant`, `actual_tenant`. See spec §5.4 + security finding H1.

---

### `store.queued`

Published by `registry-proxy` when a background blob-store goroutine fails. The proxy itself consumes this event to retry the store (3 attempts, DLQ after).

See `libs/rabbitmq/events/events.go` for `StoreQueuedPayload`.

---

### `image.signed`

Published by `registry-management` after a successful `signer.SignManifest` call from the dashboard sign-from-UI route (FE-API-026). The signer service itself does not yet publish this event — when it does, drop the management-side publisher.

```go
type ImageSignedPayload struct {
    TenantID       string `json:"tenant_id"`
    RepositoryName string `json:"repository_name"`
    Tag            string `json:"tag"`
    ManifestDigest string `json:"manifest_digest"`
    SignerID       string `json:"signer_id"`
    SignedBy       string `json:"signed_by"` // user_id of the signing actor
}
```

**Consumers:** `registry-audit`, `registry-webhook`.

---

### `tenant.renamed` / `tenant.plan_changed`

Published by `registry-tenant` after `UpdateTenant` (FE-API-029). Per-field events — patching both `name` and `plan` fires two events.

```go
type TenantRenamedPayload struct {
    TenantID string `json:"tenant_id"`
    OldName  string `json:"old_name"`
    NewName  string `json:"new_name"`
    OldSlug  string `json:"old_slug"`
    NewSlug  string `json:"new_slug"`
    ActorID  string `json:"actor_id"`
}

type TenantPlanChangedPayload struct {
    TenantID string `json:"tenant_id"`
    OldPlan  string `json:"old_plan"`
    NewPlan  string `json:"new_plan"`
    ActorID  string `json:"actor_id"`
}
```

---

> Other payload types (`PushFailedPayload`, `ManifestDeletedPayload`, `TagDeletedPayload`, `WebhookDeliveredPayload`, `TenantDeletedPayload`, etc.) follow the same shape: a small struct with the resource identifier, tenant ID, and actor. Read the source file rather than re-documenting here — the struct is the contract.
