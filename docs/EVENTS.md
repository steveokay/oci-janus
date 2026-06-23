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
  pull.image                     # FE-API-042 (publisher: registry-core after a successful GetManifest; sampled via PULL_EVENT_SAMPLE_RATE; powers /activity pull events + max_idle_days retention)
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
  retention.evaluated       # FE-API-038/043 (publisher: registry-metadata retention evaluator; one event per repo per evaluation)
  retention.applied         # FE-API-040 (publisher: registry-metadata retention executor; emitted when a tag is soft-deleted into grace)
  retention.grace_completed # FE-API-041 (publisher: registry-metadata retention executor; emitted when a graced tag is hard-deleted)
  service_account.lifecycle # FE-API-048 (publisher: registry-auth; one routing key with embedded Action ∈ {created, updated, disabled, deleted, key_issued, key_revoked})
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
    RepoID         string `json:"repo_id"`        // canonical UUID identifier
    RepositoryName string `json:"repository_name"`
    Tag            string `json:"tag"`
    ManifestDigest string `json:"manifest_digest"`
    PushedBy       string `json:"pushed_by"`      // actor user ID
    SizeBytes      int64  `json:"size_bytes"`
    ArtifactType   string `json:"artifact_type"`  // S-MAINT-1 P6: "image" | "helm" | "signature" | "sbom" | "other"
                                                  // used by registry-scanner to skip non-image artifacts
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

### `pull.image` (FE-API-042)

```go
type PullImagePayload struct {
    RepoID         string `json:"repo_id"`
    RepositoryName string `json:"repository_name"`
    Tag            string `json:"tag"`
    ManifestDigest string `json:"manifest_digest"`
    PulledBy       string `json:"pulled_by"`      // actor user ID; "" for anonymous pulls
    UserAgent      string `json:"user_agent"`     // docker/helm/oras client string
}
```

**Publishers:** `registry-core` after each successful `GetManifest`,
gated by `PULL_EVENT_SAMPLE_RATE` (default 1.0 — every pull publishes).
Lower the rate to reduce event volume on hot repos; the FE-API-043
`max_idle_days` retention rule still works as long as the rate is > 0
because services/metadata debounces `last_pulled_at` updates to ~24h.

**Consumers:** `registry-audit` (drives `/activity` pull events),
`registry-metadata` (updates `manifests.last_pulled_at` with the
24h debounce).

---

### `retention.*` (FE-API-038 / 040 / 041 / 043)

```go
type RetentionEvaluatedPayload struct {
    RepoID         string `json:"repo_id"`
    RepositoryName string `json:"repository_name"`
    RuleKind       string `json:"rule_kind"`         // "max_tags" | "max_age_days" | "max_idle_days"
    Matched        int32  `json:"matched"`           // tags identified for grace
    Pending        int32  `json:"pending_delete"`    // tags currently in grace
    DryRun         bool   `json:"dry_run"`
}

type RetentionAppliedPayload struct {
    RepoID         string    `json:"repo_id"`
    Tag            string    `json:"tag"`
    ManifestDigest string    `json:"manifest_digest"`
    GraceUntil     time.Time `json:"grace_until"`    // hard-delete cut-over
    RuleKind       string    `json:"rule_kind"`
}

type RetentionGraceCompletedPayload struct {
    RepoID         string `json:"repo_id"`
    Tag            string `json:"tag"`
    ManifestDigest string `json:"manifest_digest"`   // empty if the manifest also got GCed
}
```

**Publishers:** `registry-metadata`'s retention evaluator + executor
(both run as background workers on a cron schedule). `retention.evaluated`
is one event per repo per evaluation; `retention.applied` is one per
tag entering grace; `retention.grace_completed` is one per graced tag
that hits hard-delete.

**Consumers:** `registry-audit` (drives the Activity tab's retention
entries), `registry-webhook` (operators can subscribe to retention
deletions for downstream cleanup).

---

### `service_account.lifecycle` (FE-API-048)

```go
type ServiceAccountLifecyclePayload struct {
    Action           string `json:"action"`        // "created" | "updated" | "disabled" | "deleted" | "key_issued" | "key_revoked"
    ServiceAccountID string `json:"service_account_id"`
    ShadowUserID     string `json:"shadow_user_id"`
    ActorID          string `json:"actor_id"`      // the human (or SA) that performed the action
    KeyID            string `json:"key_id,omitempty"` // populated for key_issued / key_revoked
}
```

One routing key handles every SA mutation — the discriminator is the
embedded `Action` field. The audit consumer rehydrates each action
into a distinct audit event so the dashboard /activity feed renders
"alice@acme.com issued key for ci-prod-bot" rather than a generic
"service_account.lifecycle" line.

**Publishers:** `registry-auth` (every `ServiceAccountService` write
path emits this; durable persistence rides on top of the slog-only
stand-in tracked as FUT-007 — already shipped).

**Consumers:** `registry-audit`.

---

> Other payload types (`PushFailedPayload`, `ManifestDeletedPayload`, `TagDeletedPayload`, `WebhookDeliveredPayload`, `TenantDeletedPayload`, etc.) follow the same shape: a small struct with the resource identifier, tenant ID, and actor. Read the source file rather than re-documenting here — the struct is the contract.

---

### Tag immutability — no dedicated event yet (futures.md Tier 1 #2)

Tag immutability transitions currently ride on the existing repository / tag mutation event paths rather than emitting dedicated routing keys:

- **Repo-wide flag flip** (`PATCH /api/v1/repositories/{org}/{repo}` with `{"immutable_tags": ...}`) — captured as part of the generic repository-update audit row. The diff between old and new `immutable_tags` shows up in the audit trail's `fields` map.
- **Per-tag pin / unpin** (`POST /pin` and `DELETE /pin`) — currently NOT emitting a dedicated `tag.pinned` / `tag.unpinned` routing key. The mutation lands in `tags.immutable` and is observable via `registry-audit`'s actor-trail join against the repo update timestamp, but webhook subscribers can't subscribe to "tag pinned" directly.
- **Push rejection** (`MANIFEST_INVALID` on an immutable tag) is logged at `slog.Warn` by `services/core.checkTagImmutable` with `repo_id, tag, existing_digest, new_digest`. No event published — the push was rejected, so `push.completed` doesn't fire either; consumers see the absence rather than a positive "rejected" signal.

**Planned follow-up:** add `tag.pinned`, `tag.unpinned`, and `repository.immutability_changed` routing keys so webhook subscribers can wire dedicated notifications (e.g. Slack alert when a production repo's immutability flag is turned off). Tracked in `status.md` Tag immutability row.
