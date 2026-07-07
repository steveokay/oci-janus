# FUT-019 Phase 3 — Email notification channel (design)

> **Status:** design approved 2026-07-07. Completes FUT-019 Phase 3 by wiring the
> **Email** column of the Settings › Notifications matrix live. The Webhook
> column stays locked for a separate follow-up.

**Goal:** Deliver the platform's per-category notifications by email — Resend as
the default transport, pluggable SMTP/Gmail — configured from a transport panel
on Settings › Notifications, with a per-user email delivery log surfaced by a new
topbar mail icon.

**Posture:** single-tenant (`DEPLOYMENT_MODE=single`).

---

## 1. Scope

**In scope:**
- A pluggable email transport (Resend default + SMTP/Gmail) configured per tenant.
- Reliable per-user email delivery of scheduled notifications, gated by the
  existing per-category `email_enabled` preference.
- A transport config panel (provider, credentials, from-address, enable toggle,
  test-send) above the existing category matrix.
- Unlocking the **Email** column of the notification matrix.
- A topbar **✉️ mail icon** (before the 🔔 bell) opening the current user's email
  **delivery log**.

**Out of scope (deferred):**
- The **Webhook** channel — the third matrix column stays locked
  (`hint="Wired in Phase 3+"`). Separate follow-up.
- Gmail OAuth2 — Gmail is supported as an SMTP preset with an app-password, not a
  full OAuth flow (YAGNI for a first cut).
- Arbitrary per-user transport overrides — the transport is a single tenant-level
  config (single-tenant posture).
- Email verification gating — we send to the stored address regardless of
  `email_verified` (see §5).

---

## 2. Current state (what exists)

`services/audit` owns the entire notification pipeline (verified 2026-07-07):

- **Preference matrix** — `user_notification_preferences(user_id, tenant_id,
  category, bell_enabled, email_enabled, webhook_enabled)`
  (`services/audit/migrations/20260626000001_scheduled_notifications.sql`).
  Missing rows default to `bell=TRUE, email=FALSE, webhook=FALSE`. The BFF
  **already persists** `email_enabled` writes
  (`services/management/internal/handler/notification_preferences.go`) — nothing
  consumes the flag yet. (The futures.md "BFF drops the writes" note is stale.)
- **Scheduler loop** (hourly) enqueues `scheduled_notifications` rows per
  `(tenant, category)` when a cadence elapses.
- **Dispatcher loop** (per-minute) claims due rows `FOR UPDATE SKIP LOCKED`,
  calls `cat.Render(payload)` → title/summary/link, and inserts **one
  `audit_events` row** (`action="notification.scheduled"`) that the bell feed
  reads. Per-user filtering happens on the **read** path via
  `IsBellEnabledForCategory()` — there is no push side today.
  (`services/audit/internal/scheduler/loops.go`.)
- **Bell feed** — `GetNotifications()` projects `audit_events` rows to the topbar
  (`services/audit/internal/handler/notifications.go`).

Reusable platform patterns:
- **AES-256-GCM at rest** — `libs/crypto/aes` (`Encrypt(plaintext, key32)`,
  versioned layout `0x01||nonce||ct||tag`). Per-purpose KEK env var +
  `kek_version SMALLINT` column is the established pattern (proxy
  `CREDENTIAL_KEY_HEX`, webhook `CREDENTIAL_KEY_HEX`, audit
  `AUDIT_EXPORT_SECRETS_KEY_HEX`, auth `MFA_SECRET_KEY_HEX`). `rotate-kek`
  (RED-FU-015) sweeps these.
- **Reliable outbound delivery** — `services/webhook/internal/delivery/dispatcher.go`:
  5-attempt exponential backoff (5s→30s→5m→30m→2h), error redaction
  (`sanitizeURLForError`), capped response reads. The email send loop mirrors
  this shape.
- **Optional-service BFF routes** — a handler 404s `"route disabled"` when its
  gRPC client is nil (`services/management/internal/handler/webhooks.go` etc.);
  the client is nil when the `*_GRPC_ADDR` env var is unset.

---

## 3. Architecture

```
Scheduler (hourly) ─enqueue─▶ scheduled_notifications
                                     │
Dispatcher (per-min) ─claim─────────┘
   │ 1. insert audit_events row  (bell — unchanged)
   │ 2. if email transport enabled:
   │      a. prefs: user_ids WHERE email_enabled=true for (tenant,category)
   │      b. auth.ResolveUserEmails(user_ids) ──gRPC──▶ services/auth
   │      c. cat.Render(payload) → subject/summary/link
   │      d. INSERT email_deliveries (status=pending)   [idempotent]
   ▼
email_deliveries (queue + log)
   ▲
Email send loop (~20s) ─claim pending FOR UPDATE SKIP LOCKED─┐
   │ transport.Send(msg) ──HTTP/SMTP──▶ Resend / SMTP server │
   │ ok → sent ; fail → attempts++ + backoff ; 5th → failed  │
   └─────────────────────────────────────────────────────────┘

BFF (registry-management):
  GET/PUT /notifications/email-transport   (admin)  ─▶ audit config RPCs
  POST    /notifications/email-transport/test (admin) ─▶ audit SendTestEmail
  GET     /notifications/email-deliveries   (user)  ─▶ audit ListEmailDeliveries

FE (Settings › Notifications):  EmailTransportPanel + unlocked Email column
FE (topbar):                    ✉️ EmailActivityMenu (delivery log)  before  🔔
```

No new service. `services/audit` gains the transport, the send loop, two tables,
four RPCs, and one outbound client (to auth). `services/auth` gains one RPC.

---

## 4. Data model

New migration in `services/audit/migrations/` (goose,
`YYYYMMDDHHMMSS_email_channel.sql`, with a `-- +goose Down`).

### 4.1 `email_transport_config` — tenant-scoped, one row per tenant

| column | type | notes |
|---|---|---|
| `tenant_id` | `UUID PRIMARY KEY` | one config per tenant |
| `provider` | `TEXT NOT NULL` | `resend` \| `smtp` |
| `enabled` | `BOOLEAN NOT NULL DEFAULT false` | master switch; email inert until true |
| `from_address` | `TEXT` | envelope from (required before enable) |
| `from_name` | `TEXT` | display name |
| `resend_api_key_enc` | `BYTEA` | AES-256-GCM |
| `smtp_host` | `TEXT` | |
| `smtp_port` | `INT` | |
| `smtp_username` | `TEXT` | |
| `smtp_password_enc` | `BYTEA` | AES-256-GCM |
| `smtp_tls_mode` | `TEXT` | `starttls` \| `implicit` \| `none` |
| `kek_version` | `SMALLINT NOT NULL DEFAULT 1` | `rotate-kek` target |
| `last_test_at` | `TIMESTAMPTZ` | last test-send time |
| `last_test_ok` | `BOOLEAN` | last test outcome |
| `last_test_error` | `TEXT` | redacted error from last test |
| `updated_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` | |
| `updated_by` | `UUID` | who last saved |

### 4.2 `email_deliveries` — per-send log **and** send queue

| column | type | notes |
|---|---|---|
| `id` | `UUID PRIMARY KEY DEFAULT gen_random_uuid()` | |
| `tenant_id` | `UUID NOT NULL` | |
| `user_id` | `UUID NOT NULL` | recipient |
| `to_address` | `TEXT NOT NULL` | email snapshot at enqueue time |
| `category` | `TEXT NOT NULL` | |
| `subject` | `TEXT NOT NULL` | rendered title |
| `body_summary` | `TEXT NOT NULL` | rendered summary (shown in the mail-icon log) |
| `link` | `TEXT` | app-relative deep link |
| `source_event_id` | `UUID` | the `audit_events` row this came from |
| `status` | `TEXT NOT NULL DEFAULT 'pending'` | `pending` \| `sent` \| `failed` |
| `attempts` | `INT NOT NULL DEFAULT 0` | |
| `next_attempt_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` | backoff gate |
| `last_error` | `TEXT` | redacted |
| `provider` | `TEXT` | transport that sent it (snapshot) |
| `created_at` | `TIMESTAMPTZ NOT NULL DEFAULT now()` | |
| `sent_at` | `TIMESTAMPTZ` | set on success |

Constraints / indexes:
- `UNIQUE (source_event_id, user_id)` — idempotency: reprocessing a due
  notification cannot double-enqueue a recipient.
- partial index `(status, next_attempt_at) WHERE status='pending'` — claim query.
- index `(tenant_id, user_id, created_at DESC)` — mail-icon per-user read.

`user_notification_preferences` is reused unchanged.

---

## 5. Recipient resolution — new auth gRPC

`services/audit` owns the preference matrix but not user email addresses (those
live in `services/auth.users.email`). At send time the dispatcher resolves them:

**New RPC on `services/auth`** (additive to `proto/auth/v1/auth.proto`):

```proto
rpc ResolveUserEmails(ResolveUserEmailsRequest) returns (ResolveUserEmailsResponse);

message ResolveUserEmailsRequest {
  string tenant_id = 1;
  repeated string user_ids = 2;
}
message ResolveUserEmailsResponse {
  repeated ResolvedEmail emails = 1;
}
message ResolvedEmail {
  string user_id = 1;
  string email = 2;
  bool email_verified = 3;
}
```

- Tenant-scoped: `WHERE tenant_id = $1 AND id = ANY($2) AND email <> ''`. Users
  with no email are omitted from the response (skipped as recipients).
- **Verified gate:** in single-tenant we send to the stored address regardless of
  `email_verified` — local/bootstrap users are frequently unverified and the
  operator is trusted. `email_verified` is returned for *future* gating but does
  not block delivery now. **This is a deliberate posture decision, not an
  oversight.**

**mTLS wiring (new peer edge audit → auth):**
- `services/auth`'s `MTLS_PEER_CN_ALLOWLIST` gains `services/audit`'s client CN.
- `services/audit` config gains `AUTH_GRPC_ADDR`; the client is built with the
  shared `libs/middleware/grpc` client interceptors and `loader.BaseConfig.
  MTLSClientCreds("registry-auth")`, with an eager `conn.Connect()` at startup.
- If `AUTH_GRPC_ADDR` is unset the email feature is disabled the same way an
  unset KEK disables it (see §8) — audit still boots for the bell channel.

---

## 6. Transport layer

New package `services/audit/internal/email/`.

```go
// Message is one rendered email ready to send.
type Message struct {
    To       string
    ToName   string
    Subject  string
    HTMLBody string
    TextBody string
}

// Transport sends a single message via a concrete provider. Send returns a
// redacted, retryable error; Name identifies the provider for the delivery log.
type Transport interface {
    Send(ctx context.Context, msg Message) error
    Name() string
}

// DecryptedConfig is the transport config with secrets already decrypted, built
// by the caller from an email_transport_config row.
type DecryptedConfig struct {
    Provider    string
    FromAddress string
    FromName    string
    ResendAPIKey string
    SMTPHost    string
    SMTPPort    int
    SMTPUsername string
    SMTPPassword string
    SMTPTLSMode string
}

// NewTransport builds the concrete Transport for cfg.Provider.
func NewTransport(cfg DecryptedConfig) (Transport, error)
```

- **`ResendTransport`** — `POST https://api.resend.com/emails`,
  `Authorization: Bearer <key>`, JSON body `{from, to, subject, html, text}`.
  Fixed host (no SSRF surface). 2xx = ok; on non-2xx, read the (capped) body and
  return an error **with the API key redacted**. Uses an `http.Client` with a
  sane timeout (~15s).
- **`SMTPTransport`** — `net/smtp` with three TLS modes: `starttls` (submit on
  587 + `STARTTLS`), `implicit` (TLS on 465), `none` (plaintext, dev only). Auth
  via `smtp.PlainAuth`. **Gmail** is not a separate adapter — it is an SMTP
  config preset (`smtp.gmail.com:587` + `starttls` + an app-password) offered by
  the UI.
- **Secrets** are encrypted with `libs/crypto/aes` under a **dedicated
  `NOTIFY_EMAIL_KEY_HEX`** (32-byte hex) — a separate secret domain from
  `AUDIT_EXPORT_SECRETS_KEY_HEX`, mirroring how auth's MFA KEK is separate from
  its SSO KEK. `kek_version` is stamped on write for `rotate-kek`.
- **SMTP-host SSRF:** the SMTP host is *admin-only* config (like the DB DSN), not
  user-supplied, so the webhook SSRF block-list is **deliberately not applied**.
  Documented here so security review reads it as a decision.

### 6.1 Email body

A single small HTML template (inline CSS, no external assets) plus a plaintext
alternative, both built from the rendered notification:
- Subject = the category's rendered title.
- Body = the summary paragraph + a CTA button/link to the **absolute** URL
  (`platformHost` + the app-relative `link`) + a footer: *"You're receiving this
  because {category} email is enabled. Manage preferences → /settings/notifications."*
- Plaintext = summary + raw absolute link + footer.

The link is composed only from `platformHost` (deployment config) + a
system-generated app-relative path from the category renderer — no user-supplied
content — so there is no link/header-injection surface.

---

## 7. Delivery pipeline

### 7.1 Dispatcher extension (`services/audit/internal/scheduler/loops.go`)

After the dispatcher inserts the bell `audit_events` row for a claimed due
notification, in the same processing step:
1. If no `email_transport_config` row exists, or `enabled=false`, or the email
   feature is disabled (§8) → **skip** (no `email_deliveries` rows created).
2. Query `user_notification_preferences` for `user_id`s where
   `email_enabled=true` for `(tenant_id, category)`.
3. `auth.ResolveUserEmails(tenant_id, user_ids)` → addresses (skips users with no
   email).
4. `cat.Render(payload)` → subject/summary/link (the same content the bell row
   carries).
5. Insert one `email_deliveries` row per resolved recipient
   (`status='pending'`, `next_attempt_at=now`), relying on
   `UNIQUE (source_event_id, user_id)` to make reprocessing idempotent
   (`ON CONFLICT DO NOTHING`).

The dispatcher does **not** call `transport.Send` — it only enqueues, so mail
latency never blocks notification dispatch.

### 7.2 Email send loop (`services/audit/internal/email/sender.go`)

A new loop started from audit's `main.go` alongside the scheduler/dispatcher,
ticking ~every 20s:
1. Load the `email_transport_config` row; if absent or `enabled=false`, idle this
   tick. Decrypt secrets once and cache with the config's `updated_at` as the
   cache key (rebuild the `Transport` when the row changes).
2. `ClaimPendingDeliveries(limit)` →
   `FOR UPDATE SKIP LOCKED WHERE status='pending' AND next_attempt_at<=now()
   ORDER BY next_attempt_at LIMIT $1`.
3. For each row: build `Message` (§6.1), `transport.Send(ctx, msg)`.
   - **success** → `status='sent'`, `sent_at=now`, `provider=transport.Name()`.
   - **failure** → `attempts=attempts+1`; if `attempts>=5` →
     `status='failed'`, `last_error=<redacted>`; else keep `status='pending'`,
     `next_attempt_at = now + backoff(attempts)`, `last_error=<redacted>`.
4. Backoff schedule reused from the webhook dispatcher: `5s, 30s, 5m, 30m, 2h`.

### 7.3 Test-send

`SendTestEmail(to_address)` is synchronous and bypasses the queue: it builds a
canned message (*"Test email from your registry — the {provider} transport is
working."*), sends it via the current transport, writes `last_test_at/ok/error`
on the config row, and returns `{ok, error}`. The BFF resolves the **caller's own
email** and passes it — the RPC never sends to an arbitrary address (no open
relay).

---

## 8. Configuration & failure posture

New env vars for `services/audit` (documented in `.env.example`):
- `NOTIFY_EMAIL_KEY_HEX` — 32-byte hex KEK for transport secrets.
- `AUTH_GRPC_ADDR` — mTLS target for the recipient-resolution RPC.

Posture (email is optional; the bell channel must never be taken down by it):
- **`NOTIFY_EMAIL_KEY_HEX` unset** → email feature disabled: the four transport
  RPCs return `FAILED_PRECONDITION`, the send loop stays idle, the dispatcher
  skips enqueue. **audit still boots** and the bell channel is unaffected.
- **`NOTIFY_EMAIL_KEY_HEX` set but not 32 bytes** → **fail closed** at startup
  (mirrors the MFA KEK), because a misconfigured KEK would silently corrupt
  secrets.
- **`AUTH_GRPC_ADDR` unset** → email feature disabled the same way (can't resolve
  recipients).

---

## 9. RPCs + BFF routes

### 9.1 audit proto RPCs (additive to `proto/audit/v1/audit.proto`)

- `GetEmailTransportConfig(tenant_id)` → config with **secrets never returned**:
  `provider`, `enabled`, `from_address`, `from_name`, `smtp_host/port/username`,
  `smtp_tls_mode`, `last_test_*`, plus booleans `has_resend_key`,
  `has_smtp_password`.
- `PutEmailTransportConfig(config)` → upsert with **write-only secrets**: an empty
  secret field means "keep existing" (re-saving the form doesn't wipe the key).
  Encrypts any provided secret with `NOTIFY_EMAIL_KEY_HEX`, stamps `kek_version`.
- `SendTestEmail(to_address)` → `{ok, error}`, records `last_test_*`.
- `ListEmailDeliveries(tenant_id, user_id, page_token, page_size)` → the per-user
  delivery log, newest first.

### 9.2 BFF routes (`services/management`, optional-service pattern)

The BFF already mounts an audit gRPC client (it backs the existing
notification-preferences routes), so these routes reuse it — no new client. Nil
audit client (unset `AUDIT_GRPC_ADDR`) → `404 "route disabled"`.

| route | authz | maps to |
|---|---|---|
| `GET /api/v1/notifications/email-transport` | admin | `GetEmailTransportConfig` |
| `PUT /api/v1/notifications/email-transport` | admin | `PutEmailTransportConfig` |
| `POST /api/v1/notifications/email-transport/test` | admin | `SendTestEmail` (caller's email) |
| `GET /api/v1/notifications/email-deliveries` | current user | `ListEmailDeliveries` (own `user_id` from JWT) |

- **Admin** = `is_global_admin` (single-tenant workspace admin). Transport config
  is admin-only; the delivery log is per-user and always scoped to the JWT's
  `user_id` (a user cannot read another user's deliveries).
- The `PUT`/`test` handlers resolve the caller's email via the existing auth path
  the BFF already uses, so test-send has a recipient without trusting client input.

---

## 10. Frontend

### 10.1 Transport panel — Settings › Notifications

`EmailTransportPanel` card rendered **above** the existing category matrix
(`routes/_authenticated.settings.notifications.tsx`):
- Provider select **Resend | SMTP**; a **"Use Gmail"** preset button sets
  provider=SMTP, host=`smtp.gmail.com`, port=`587`, tls=`starttls`.
- Conditional fields: Resend → API key; SMTP → host / port / username / password
  / TLS mode. Secret inputs render `•••• configured` when `has_*` is true;
  submitting them empty keeps the stored secret.
- From address + from name · **Enabled** toggle · **Send test email** button
  (shows the inline `last_test_*` result — green ok / red error) · **Save**.
- Admin-only (hidden / read-only for non-admins).
- `lib/api/email-transport.ts`: `useEmailTransport` (GET), `useUpdateEmailTransport`
  (PUT), `useSendTestEmail` (POST mutation).

### 10.2 Unlock the Email column

In the matrix, drop `hint="Wired in Phase 3+"` from the **email** column so
`ChannelToggleCell` renders a live toggle. When no transport is enabled, show a
subtle inline note — *"Configure an email transport above to receive these."* The
**webhook** column keeps its lock. Update the section description accordingly.

### 10.3 Topbar mail icon

`EmailActivityMenu` (lucide `Mail`) placed immediately **before** the bell button
in the topbar component, mirroring the bell's Popover:
- Lists the current user's recent `email_deliveries`: category label · subject ·
  to-address · status badge (Sent / Failed / Pending) · relative time.
- Empty states: *"No emails yet"* when configured; *"Email isn't set up yet"* with
  a Settings link when transport is disabled.
- Footer link → Settings › Notifications.
- `lib/api/email-deliveries.ts`: `useEmailDeliveries` (GET, stale 30s).

---

## 11. Error handling

| Condition | Result |
|---|---|
| `NOTIFY_EMAIL_KEY_HEX` unset / `AUTH_GRPC_ADDR` unset | email feature disabled; transport RPCs `FAILED_PRECONDITION`; bell unaffected |
| `NOTIFY_EMAIL_KEY_HEX` set, wrong length | audit fails closed at startup |
| `AUDIT_GRPC_ADDR` unset at BFF | email routes `404 "route disabled"` |
| Transport `enabled=false` | dispatcher skips enqueue; send loop idle |
| No recipients (`email_enabled` empty) for a category | nothing enqueued |
| User has no email | omitted by `ResolveUserEmails` |
| `transport.Send` transient failure | `attempts++`, backoff, retry (≤5) |
| 5th failure | `status='failed'`, redacted `last_error`, visible in the log |
| Provider error contains a secret | redacted before persistence + logging |
| Non-admin hits transport config route | `403` |
| User requests another user's deliveries | not possible — `user_id` forced from JWT |

---

## 12. Testing

- **`internal/email`:** Resend adapter against `httptest` (request shape, `Bearer`
  header, 2xx ok, non-2xx error with key redacted); SMTP adapter via an interface
  fake; `NewTransport` factory dispatch; backoff schedule values.
- **repository:** `ClaimPendingDeliveries` (SKIP LOCKED + `next_attempt_at`
  filter + limit), sent/failed state transitions, `(source_event_id,user_id)`
  idempotency (`ON CONFLICT DO NOTHING`), `ListEmailDeliveries` per-user scoping,
  config upsert with empty-secret-preserves-existing.
- **dispatcher:** enqueues only when transport enabled **and** `email_enabled`
  prefs exist; idempotent on reprocess; skips entirely when disabled.
- **send loop:** pending→sent on success; failure increments + sets backoff; 5th
  failure → failed.
- **auth:** `ResolveUserEmails` tenant-scoped, omits empty-email users, returns
  `email_verified`.
- **BFF:** GET masks secrets (only `has_*`); PUT preserves on empty secret;
  test-send records `last_test_*`; delivery-log handler forces JWT `user_id`.
- **FE:** panel renders / saves / shows test result; email column unlocks +
  configure-note when disabled; mail-icon dropdown renders deliveries + empty
  states. All 4 CI gates (lint / typecheck / test / build) green.
- **No OCI impact** — conformance unchanged; audit + auth + management
  build/vet/test/lint must pass.

---

## 13. Files

**Create**
- `services/audit/migrations/<ts>_email_channel.sql`
- `services/audit/internal/email/transport.go` (interface + factory + adapters) + `_test.go`
- `services/audit/internal/email/sender.go` (send loop) + `_test.go`
- `services/audit/internal/repository/email.go` (config + deliveries queries) + `_test.go`
- `services/audit/internal/handler/grpc_email.go` (4 RPCs) + `_test.go`
- `services/management/internal/handler/email_transport.go` (BFF routes) + `_test.go`
- `frontend/src/lib/api/email-transport.ts`, `frontend/src/lib/api/email-deliveries.ts`
- `frontend/src/components/settings/email-transport-panel.tsx` + test
- `frontend/src/components/topbar/email-activity-menu.tsx` + test

**Modify**
- `proto/audit/v1/audit.proto` + `proto/auth/v1/auth.proto` (+ regenerated stubs)
- `services/auth/internal/handler/...` — `ResolveUserEmails` + `services/auth/internal/repository/...`
- `services/audit/internal/scheduler/loops.go` — dispatcher enqueue step
- `services/audit/internal/config/config.go` — `NOTIFY_EMAIL_KEY_HEX`, `AUTH_GRPC_ADDR`
- `services/audit/cmd/server/main.go` — start send loop + auth client
- `services/management/internal/handler/handler.go` / `server/server.go` — mount routes
- `frontend/src/routes/_authenticated.settings.notifications.tsx` — panel + unlock email column
- `frontend/src/components/.../topbar` — mount `EmailActivityMenu` before the bell

**Docs/trackers**
- `docs/SERVICES.md` (audit RPCs + BFF routes + auth `ResolveUserEmails`)
- `docs/AUTH.md` (new peer edge audit→auth)
- `services/audit/.env.example` (`NOTIFY_EMAIL_KEY_HEX`, `AUTH_GRPC_ADDR`)
- `CLAUDE.md` service catalogue (audit gains email transport)
- `FE-STATUS.md`, `futures.md` (FUT-019 Phase 3 email shipped), `status.md`

---

## 14. Out of scope (deferred follow-ups)

- **Webhook channel** — unlock the third matrix column using the
  `services/webhook` delivery machinery (retries + HMAC + SSRF already exist
  there). Separate build.
- **Gmail OAuth2** — refresh-token flow instead of app-password.
- **Per-user / per-org transport overrides** — multiple transports.
- **Email verification gate** — block delivery to `email_verified=false`
  addresses once verification is enforced.
- **Digest batching** — one daily email rolling up N notifications instead of one
  email per notification.
