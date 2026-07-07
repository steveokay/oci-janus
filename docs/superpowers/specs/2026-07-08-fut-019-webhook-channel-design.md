# FUT-019 Phase 3 (cont.) — Webhook Notification Channel Design

> **Status:** approved 2026-07-08. Finishes FUT-019 by unlocking the third
> (**Webhook**) column of the Settings › Notifications matrix.
>
> **Builds on:** the shipped Email channel (PR #288, `069651c`) + its grant
> hotfix (PR #290, `bc7e23b`). This design mirrors the email channel's
> architecture almost verbatim, substituting an admin-configured **org
> webhook** for the per-user email transport.

---

## 1. Goal

Deliver scheduled notifications to a single **admin-configured org webhook**
(e.g. an ops Slack/PagerDuty/custom relay). Every scheduled notification whose
category is enabled fires **one** signed HTTP POST to that endpoint. This
unlocks the Webhook column that has been shown-but-locked (`hint="Wired in
Phase 3+"`) since Phase 2.

**Explicitly a tenant-level shared webhook** (design decision 2026-07-08): the
destination is one URL, not a per-user URL. One POST per scheduled
notification, not per opted-in user.

## 2. Non-goals / deferred

- **Per-user webhook URLs.** Rejected in favor of a shared org endpoint.
- **Slack/Teams/Discord-native payload formatting.** The POST body is a
  generic signed JSON envelope. Provider-native bodies (`{"text": …}` for
  Slack) are a follow-up; a custom receiver/relay handles formatting today,
  consistent with how `registry-webhook` already works.
- **Reusing `registry-webhook`.** That service does event-driven, per-endpoint
  subscriptions off the RabbitMQ `registry.events` exchange — a different
  concept from scheduled per-category notifications. We build a parallel
  audit-side channel instead. Its HMAC-signing + SSRF-block-list *patterns* are
  reused (copied), not its tables or worker.

## 3. Current state (from recon)

- The `webhook_enabled` per-(user,category) flag already exists in
  `user_notification_preferences` and is persisted end-to-end (proto → BFF →
  repo). **This per-user storage is retired for webhook** — replaced by a
  tenant-level category set (§4). Bell + Email keep their per-user semantics.
- The matrix Webhook column is locked in
  `frontend/src/routes/_authenticated.settings.notifications.tsx` (the
  `ChannelToggleCell` disables the checkbox when `hint` is set).
- The dispatcher (`services/audit/internal/scheduler/loops.go`) fans out to
  bell (an `audit_events` row) + email (`enqueueEmail`). **No webhook arm.**

## 4. Data model — audit schema

**One migration** creates both tables **and their grants** (learning from the
#290 bug where the email migration forgot to `GRANT` to the low-privilege
`registry_audit_app` runtime role → every query 500'd).

### 4.1 `notification_webhook_config` — the org webhook "transport"

One row per tenant. Mirrors `email_transport_config`.

| Column | Type | Notes |
|---|---|---|
| `tenant_id` | `UUID PRIMARY KEY` | |
| `url` | `TEXT` | HTTPS-only (validated on write) |
| `secret_enc` | `BYTEA` | HMAC key, AES-256-GCM-sealed under `NOTIFY_WEBHOOK_KEY_HEX` |
| `enabled` | `BOOLEAN NOT NULL DEFAULT false` | master on/off |
| `enabled_categories` | `TEXT[] NOT NULL DEFAULT '{}'` | tenant-level per-category selection the matrix drives |
| `kek_version` | `SMALLINT NOT NULL DEFAULT 1` | for `rotate-kek` |
| `last_test_at` | `TIMESTAMPTZ` | |
| `last_test_ok` | `BOOLEAN` | |
| `last_test_error` | `TEXT` | redacted |
| `updated_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` | |
| `updated_by` | `UUID` | |

### 4.2 `notification_webhook_deliveries` — send queue + log

Mirrors `email_deliveries`. **One delivery per scheduled notification** (shared
endpoint), so idempotency keys on `source_scheduled_id` alone.

| Column | Type | Notes |
|---|---|---|
| `id` | `UUID PRIMARY KEY DEFAULT gen_random_uuid()` | |
| `tenant_id` | `UUID NOT NULL` | |
| `category` | `TEXT NOT NULL` | |
| `subject` | `TEXT NOT NULL` | |
| `body_summary` | `TEXT NOT NULL` | |
| `link` | `TEXT` | relative deep-link |
| `source_scheduled_id` | `UUID NOT NULL` | |
| `status` | `TEXT NOT NULL DEFAULT 'pending'` | `CHECK IN ('pending','delivered','failed')` |
| `attempts` | `INT NOT NULL DEFAULT 0` | |
| `next_attempt_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` | |
| `last_error` | `TEXT` | secret-redacted |
| `response_status` | `INT` | last HTTP status seen |
| `created_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` | |
| `delivered_at` | `TIMESTAMPTZ` | |

Constraints/indexes: `UNIQUE (source_scheduled_id)`; partial claim index on
`(next_attempt_at) WHERE status = 'pending'`.

### 4.3 Grants (same migration)

```sql
GRANT INSERT, SELECT, UPDATE ON notification_webhook_config     TO registry_audit_app;
GRANT INSERT, SELECT, UPDATE ON notification_webhook_deliveries TO registry_audit_app;
```

(No DELETE path — matches the email tables.) Down migration `DROP`s both tables
(revokes implied).

## 5. Dispatch — `internal/scheduler/loops.go`

In `dispatchOne`, after the existing `r.enqueueEmail(ctx, sn, rendered)`, add
best-effort `r.enqueueWebhook(ctx, sn, rendered)`:

1. Load the tenant's webhook config (cached per tick like the email transport).
2. If `config == nil || !config.Enabled` → return (nothing enqueued).
3. If `sn.Category ∉ config.EnabledCategories` → return.
4. Insert one `notification_webhook_deliveries` row
   (`ON CONFLICT (source_scheduled_id) DO NOTHING`).

Errors log + continue — **never fail the bell write** (identical posture to
`enqueueEmail`). Gated behind a nil check so the whole channel disables cleanly
when unconfigured.

## 6. Send loop — `internal/webhook/{sender,transport}.go` (new package)

Parallel to `internal/email/`. `email → repository` acyclicity preserved
(`webhook` package imports `repository`, never the reverse).

### 6.1 `Sender` (`sender.go`)
- Ticker (~20s) + batch (20), mirroring the email sender cadence.
- Idles (no DB work) when the KEK is unset → channel fully disabled.
- Per tick: `ClaimPendingWebhookDeliveries(now, batch)` via `FOR UPDATE SKIP
  LOCKED`; for each row, resolve the tenant transport (cached per tick),
  render the payload, send, then `MarkWebhookDelivered` / `fail`.
- `fail` bumps `attempts`, applies `Backoff(attempts)`, flips to `'failed'` at
  `MaxAttempts = 5`. A disabled/absent config on a claimed row is aged toward
  terminal state via `fail` (never a bare `continue` — avoids the unbounded
  re-claim churn the email FIX-2 addressed).

### 6.2 `transport.go`
- `buildPayload(d) []byte` → canonical JSON:
  `{"event":"notification","category":…,"subject":…,"summary":…,"link":…,"tenant_id":…,"timestamp":…}`.
- `sign(body, secret)` → `sha256=<hex>` HMAC-SHA256, set as
  `X-Registry-Signature` (matches `registry-webhook`).
- `Send(ctx, url, body, secret)` → **HTTPS-only** + **SSRF block-list check**
  (private/loopback/link-local/metadata ranges) **before** dialing; POST with a
  15s client timeout; non-2xx → error carrying `response_status`.
- `redact` strips the secret from any error string.
- **DRY note:** the SSRF + HMAC helpers are copied from
  `services/webhook/internal/delivery/dispatcher.go` into this package to avoid
  a cross-service import. Extracting a shared `libs/webhook/{ssrf,hmac}` used by
  both services is filed as a follow-up (touches `registry-webhook`, so out of
  scope for this PR).
- `Backoff`, `MaxAttempts` constants mirror the email package
  (`5s→30s→5m→30m→2h`, 5).

## 7. API

### 7.1 Proto (additive to `proto/audit/v1/audit.proto`)
- `GetNotificationWebhookConfig(tenant_id) → NotificationWebhookConfig`
  { `url`, `enabled`, `has_secret` (bool — secret never returned),
  `enabled_categories[]`, `last_test_at/ok/error` }.
- `PutNotificationWebhookConfig(…)` — full replace incl `enabled_categories`;
  `secret` field is **write-only** (empty = keep stored ciphertext, non-empty =
  re-seal, exactly like the email transport). `updated_by` stamped.
- `SendTestNotificationWebhook(tenant_id) → { ok, error }` — one canned POST to
  the configured URL; a disabled/unconfigured transport reports inline
  (`ok=false`) rather than as an RPC error.
- `GetUserNotificationPreferences` handler additionally loads the tenant
  webhook config's `enabled_categories` and sets `webhook_enabled` per category
  from it (the matrix column now reflects org config, not the per-user column).
- `UpdateUserNotificationPreferences` (per-user PATCH) **ignores** the
  `webhook_enabled` field going forward — webhook enablement is tenant-level and
  admin-gated, so a stale/hostile client can't flip it via the non-admin
  per-user path. The proto field is retained (no breaking change); it is simply
  a no-op on write and populated from the tenant config on read.

No field numbers reused; additive only (breaking-check safe).

### 7.2 BFF (registry-management)
- `GET  /api/v1/notifications/webhook-config` (admin) → `GetNotificationWebhookConfig`
- `PUT  /api/v1/notifications/webhook-config` (admin) → `PutNotificationWebhookConfig`
- `POST /api/v1/notifications/webhook-config/test` (admin) → `SendTestNotificationWebhook`
- `FailedPrecondition → 409`; admin gate via `h.effectiveGlobalAdmin(r)` +
  deny service-account principals (mirrors the email-transport handler).
- Bell + Email still flow through the existing per-user
  `PATCH /api/v1/users/me/notification-preferences`.
- The matrix Webhook toggle (admin only) issues a read-modify-write `PUT` of the
  config with the updated `enabled_categories` set.

## 8. Config — `NOTIFY_WEBHOOK_KEY_HEX`

New 32-byte hex KEK, parallel to `NOTIFY_EMAIL_KEY_HEX`:
- Unset → webhook channel disabled (config can't seal a secret; the sender
  idles). Audit still boots.
- Set-but-wrong-length → **fail closed at startup** (`config.Validate`).
- Swept by `rotate-kek` (new `webhookSpecs()`, separate from `emailSpecs()` /
  `mfaSpecs()`).

Server wiring (`internal/server/server.go`): decode the KEK, construct the
`Sender`, start its goroutine alongside the email sender + scheduler runner.

## 9. Frontend

- **`NotificationWebhookPanel`** (`components/settings/`) — admin-only, mounted
  above the matrix beside `EmailTransportPanel`: URL field, enable toggle,
  generate/reveal-secret (write-only; shows `has_secret` state), **Send test**
  button surfacing `ok/error`. `lib/api/notification-webhook.ts` hooks
  (`useNotificationWebhook`, `useUpdateNotificationWebhook`,
  `useSendTestNotificationWebhook`).
- **Unlock the Webhook column** — remove `hint="Wired in Phase 3+"` from the
  Webhook `ChannelToggleCell`. Bind it to the tenant config: admins toggle
  (RMW `PUT`), non-admins render read-only (reuse the locked visual, driven by
  `useAbilities`/admin rather than a hardcoded hint). When no webhook is
  configured, show the "Configure a webhook above…" note (mirrors the email
  empty-transport note).
- Update `settings.notifications.channel-toggle.test.tsx` for the new
  admin-editable / non-admin-read-only Webhook semantics.
- FE-API-058.

## 10. Testing

- **Unit (audit):** transport sign/redact/SSRF-reject/HTTPS-only/backoff;
  sender loop with a fake transport + fake repo (delivered, fail→backoff,
  disabled-config→age-to-terminal); `enqueueWebhook` gate (enabled +
  category-in-set matrix); config hex validation (unset/short/ok); handler
  seal/mask/has_secret + `SendTestNotificationWebhook` disabled-inline.
- **Integration (audit):** repo CRUD + claim/mark against real PG16
  (testcontainers), incl. the grant (a query as `registry_audit_app` must
  succeed — a direct regression guard for the #290 class of bug).
- **Unit (management):** the 3 BFF handlers (admin gate, 409 mapping,
  service-account deny).
- **FE:** panel render/mutations + matrix admin-editable/read-only + the
  column-unlock test. All **4 gates** (lint/typecheck/test/build).

## 11. Security

- HTTPS-only + SSRF block-list on every send (incl. the test POST).
- Secret AES-256-GCM under `NOTIFY_WEBHOOK_KEY_HEX`; never returned
  (`has_secret` bool); redacted from `last_error` + test errors.
- Admin-gated writes; service-account principals denied.
- Fail-closed KEK length; fail-open disable when unset (channel is opt-in, not a
  security boundary — matches email).
- HMAC-SHA256 body signature (`X-Registry-Signature`) so the receiver can
  verify authenticity.

## 12. Docs

`docs/SERVICES.md` (audit RPCs + BFF routes), `services/audit/.env.example`
(`NOTIFY_WEBHOOK_KEY_HEX`), root `.env.example` if present, `CLAUDE.md` audit
service row, `FE-STATUS.md` (FE-API-058), `futures.md` (retire the deferred
"Webhook channel" note), `docs/AUTH.md` only if the KEK warrants a line.

## 13. Deploy prerequisites (for the live stack, post-merge)

- Set audit `NOTIFY_WEBHOOK_KEY_HEX` (32-byte hex) to enable the channel.
- No new mTLS peer edge (unlike email's audit→auth `ResolveUserEmails`): the
  shared webhook needs no per-user email resolution, so **no
  `MTLS_PEER_CN_ALLOWLIST` change** is required.
- Rebuild + restart `registry-audit` + `registry-management`; the grant lands
  via the new migration on boot.
