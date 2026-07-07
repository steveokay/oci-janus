# FUT-019 Phase 3 — Email Notification Channel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver per-category notifications by email (Resend default + pluggable SMTP/Gmail), configured from a transport panel on Settings › Notifications, with a per-user email delivery log surfaced by a new topbar mail icon.

**Architecture:** `services/audit` owns the pipeline. The existing per-minute dispatcher, after writing the bell `audit_events` row, best-effort enqueues one `email_deliveries` row per opted-in recipient (recipient emails resolved via a new `services/auth.ResolveUserEmails` gRPC). A new send loop drains `email_deliveries` with `FOR UPDATE SKIP LOCKED` + webhook-style backoff, sending via a pluggable `email.Transport` (Resend HTTP / SMTP). Transport config + secrets (AES-256-GCM under a dedicated `NOTIFY_EMAIL_KEY_HEX`) live in a new `email_transport_config` table. The BFF exposes admin config CRUD + test-send + a per-user delivery log; the FE adds a transport panel, unlocks the Email matrix column, and adds an ✉️ delivery-log dropdown before the 🔔 bell.

**Tech Stack:** Go 1.25 (pgx/v5, `net/smtp`, `libs/crypto/aes`, gRPC/buf), React + TanStack Query/Router, PostgreSQL 16 (goose migrations).

**Spec:** `docs/superpowers/specs/2026-07-07-fut-019-email-channel-design.md`

**Build note:** all Go build/test/vet run with `GOWORK=off` from the service dir (matches Docker/CI). Proto regen is `cd proto && buf generate` (module-scoped). Frontend gates: `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`.

**Two refinements over the spec (both simplifications, applied throughout this plan):**
1. `email_deliveries` idempotency anchors on **`source_scheduled_id`** (the `scheduled_notifications.id`, available in the dispatcher) rather than the audit-event id — equally correct for "one email per occurrence per user", and no need to thread the audit row id back.
2. The BFF audit client is a **required** dependency (constructed in `handler.New`), so the four email routes are **always mounted** — there is no optional-service 404. The "email not configured" state surfaces as gRPC `FailedPrecondition` from audit, which the BFF maps to HTTP `409`.

---

## File Structure

**services/audit (create):**
- `migrations/20260707120000_email_channel.sql` — the two tables.
- `internal/email/transport.go` — `Transport` iface, `Message`, `DecryptedConfig`, `NewTransport`, Resend + SMTP adapters, `Backoff`.
- `internal/email/transport_test.go`
- `internal/email/sender.go` — the send loop (`Sender`).
- `internal/email/sender_test.go`
- `internal/repository/email.go` — config + deliveries + recipients queries.
- `internal/repository/email_test.go`
- `internal/handler/grpc_email.go` — the 4 audit RPCs.
- `internal/handler/grpc_email_test.go`

**services/audit (modify):**
- `internal/config/config.go` — `NOTIFY_EMAIL_KEY_HEX`, `AUTH_GRPC_ADDR` + fail-closed validation.
- `internal/scheduler/loops.go` — best-effort email enqueue in `dispatchOne`.
- `internal/server/server.go` — decode the email KEK, dial the auth client, start the `Sender`, register the email gRPC handler.
- `.env.example` — document the two new vars.

**services/auth (modify):**
- `internal/repository/user.go` — `ResolveEmails` batch query.
- `internal/service/*.go` — `ResolveUserEmails` service method.
- `internal/handler/grpc.go` — `ResolveUserEmails` RPC.

**proto (modify + regen):**
- `proto/auth/v1/auth.proto` — `ResolveUserEmails`.
- `proto/audit/v1/audit.proto` — 4 email RPCs + messages.

**services/management (create + modify):**
- `internal/handler/email_transport.go` — 4 route handlers + `requireEmailAdmin`.
- `internal/handler/email_transport_test.go`
- `internal/handler/handler.go` — register 4 routes.

**frontend (create + modify):**
- `src/lib/api/email-transport.ts`, `src/lib/api/email-deliveries.ts`
- `src/components/settings/email-transport-panel.tsx` (+ test)
- `src/components/shell/email-activity-menu.tsx` (+ test)
- `src/routes/_authenticated.settings.notifications.tsx` — mount panel + unlock email column.
- `src/components/shell/topbar.tsx` — mount menu before the bell.

**docs/trackers (modify):** `docs/SERVICES.md`, `docs/AUTH.md`, `CLAUDE.md`, `FE-STATUS.md`, `futures.md`, `status.md`.

---

## Task 1: Migration — `email_transport_config` + `email_deliveries`

**Files:**
- Create: `services/audit/migrations/20260707120000_email_channel.sql`

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- FUT-019 Phase 3 — email notification channel.
-- email_transport_config: one row per tenant. Secrets are AES-256-GCM
-- sealed under NOTIFY_EMAIL_KEY_HEX (see services/audit config); kek_version
-- tracks the KEK generation for rotate-kek (RED-FU-015).
CREATE TABLE email_transport_config (
    tenant_id          UUID PRIMARY KEY,
    provider           TEXT        NOT NULL DEFAULT 'resend',
    enabled            BOOLEAN     NOT NULL DEFAULT false,
    from_address       TEXT,
    from_name          TEXT,
    resend_api_key_enc BYTEA,
    smtp_host          TEXT,
    smtp_port          INT,
    smtp_username      TEXT,
    smtp_password_enc  BYTEA,
    smtp_tls_mode      TEXT        NOT NULL DEFAULT 'starttls',
    kek_version        SMALLINT    NOT NULL DEFAULT 1,
    last_test_at       TIMESTAMPTZ,
    last_test_ok       BOOLEAN,
    last_test_error    TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by         UUID,
    CONSTRAINT email_transport_provider_chk CHECK (provider IN ('resend','smtp')),
    CONSTRAINT email_transport_tls_chk CHECK (smtp_tls_mode IN ('starttls','implicit','none'))
);

-- email_deliveries: per-send log AND send queue. The dispatcher inserts
-- pending rows (one per opted-in recipient); the send loop drains them.
CREATE TABLE email_deliveries (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL,
    user_id             UUID        NOT NULL,
    to_address          TEXT        NOT NULL,
    category            TEXT        NOT NULL,
    subject             TEXT        NOT NULL,
    body_summary        TEXT        NOT NULL,
    link                TEXT,
    source_scheduled_id UUID        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'pending',
    attempts            INT         NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error          TEXT,
    provider            TEXT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at             TIMESTAMPTZ,
    CONSTRAINT email_delivery_status_chk CHECK (status IN ('pending','sent','failed')),
    CONSTRAINT email_delivery_idem UNIQUE (source_scheduled_id, user_id)
);

CREATE INDEX idx_email_deliveries_claim
    ON email_deliveries (next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX idx_email_deliveries_user
    ON email_deliveries (tenant_id, user_id, created_at DESC);

-- +goose Down
DROP TABLE email_deliveries;
DROP TABLE email_transport_config;
```

- [ ] **Step 2: Verify the migration is well-formed**

Run: `cd services/audit && GOWORK=off go build ./... && grep -c "goose" migrations/20260707120000_email_channel.sql`
Expected: build clean; grep returns `2` (Up + Down markers present).

- [ ] **Step 3: Commit**

```bash
git add services/audit/migrations/20260707120000_email_channel.sql
git commit -m "feat(audit): email channel migration (transport config + deliveries)"
```

---

## Task 2: audit config — `NOTIFY_EMAIL_KEY_HEX` + `AUTH_GRPC_ADDR`

**Files:**
- Modify: `services/audit/internal/config/config.go`
- Test: `services/audit/internal/config/config_test.go` (create if absent)

**Context:** the existing `ExportSecretsKeyHex` field + `decodeHexKey` (in `server.go`) are the pattern. `NOTIFY_EMAIL_KEY_HEX` differs in one way: **set-but-wrong-length must fail closed at startup** (the export key does the same via `decodeHexKey` returning an error). Unset is allowed (disables email).

- [ ] **Step 1: Write the failing test**

Create `services/audit/internal/config/config_test.go`:

```go
package config

import (
	"testing"

	"github.com/steveokay/oci-janus/libs/config/loader"
)

func TestValidate_notifyEmailKey_wrongLengthFailsClosed(t *testing.T) {
	cfg := &Config{
		BaseConfig: loader.BaseConfig{
			MTLSCACertPath: "ca", MTLSCertPath: "c", MTLSKeyPath: "k",
		},
		DBDSN: "postgres://x", RabbitMQURL: "amqp://x",
		NotifyEmailKeyHex: "abcd", // not 64 hex chars
	}
	if err := validate(cfg); err == nil {
		t.Fatal("expected validate to reject a set-but-short NOTIFY_EMAIL_KEY_HEX")
	}
}

func TestValidate_notifyEmailKey_unsetIsAllowed(t *testing.T) {
	cfg := &Config{
		BaseConfig: loader.BaseConfig{
			MTLSCACertPath: "ca", MTLSCertPath: "c", MTLSKeyPath: "k",
		},
		DBDSN: "postgres://x", RabbitMQURL: "amqp://x",
		NotifyEmailKeyHex: "", // unset → email disabled, not an error
	}
	if err := validate(cfg); err != nil {
		t.Fatalf("unset NOTIFY_EMAIL_KEY_HEX must be allowed, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/config/ -run TestValidate_notifyEmailKey -v`
Expected: FAIL — `NotifyEmailKeyHex` field undefined / no length check.

- [ ] **Step 3: Add the fields + validation**

In `config.go`, add to the `Config` struct (after `ExportSecretsKeyHex`):

```go
	// NotifyEmailKeyHex (FUT-019 Phase 3) is the 64-char hex AES-256-GCM key
	// sealing email_transport_config secrets (resend_api_key / smtp_password).
	// Empty disables the email channel: transport RPCs writing a secret return
	// FailedPrecondition and the send loop idles. Set-but-not-32-bytes fails
	// closed at startup (a bad KEK would silently corrupt secrets).
	NotifyEmailKeyHex string `mapstructure:"NOTIFY_EMAIL_KEY_HEX"`

	// AuthGRPCAddr (FUT-019 Phase 3) is the mTLS target for
	// registry-auth.ResolveUserEmails, used by the dispatcher to resolve
	// recipient email addresses. Empty disables email fan-out.
	AuthGRPCAddr string `mapstructure:"AUTH_GRPC_ADDR"`
```

Add to `validate`, after the required-map loop (before `return nil`):

```go
	// FUT-019 Phase 3 — email KEK is optional (unset disables email), but a
	// set-but-malformed key must fail closed rather than silently corrupt rows.
	if cfg.NotifyEmailKeyHex != "" {
		if _, err := hex.DecodeString(cfg.NotifyEmailKeyHex); err != nil {
			return fmt.Errorf("NOTIFY_EMAIL_KEY_HEX: not valid hex: %w", err)
		}
		if len(cfg.NotifyEmailKeyHex) != 64 {
			return fmt.Errorf("NOTIFY_EMAIL_KEY_HEX: expected 64 hex chars (32 bytes), got %d", len(cfg.NotifyEmailKeyHex))
		}
	}
```

Add `"encoding/hex"` to the imports.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd services/audit && GOWORK=off go test ./internal/config/ -run TestValidate_notifyEmailKey -v`
Expected: PASS (both cases).

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/config/
git commit -m "feat(audit): NOTIFY_EMAIL_KEY_HEX + AUTH_GRPC_ADDR config (fail-closed on bad KEK)"
```

---

## Task 3: audit repository — email config + deliveries + recipients

**Files:**
- Create: `services/audit/internal/repository/email.go`
- Test: `services/audit/internal/repository/email_test.go`

**Context:** `Repository` holds `r.pool` (a `*pgxpool.Pool`). Mirror the SQL style in `scheduled_notifications.go` (parameterised, `fmt.Errorf` wrap, `pgx.ErrNoRows` handling). Secrets are stored as `BYTEA`; the repository takes/returns raw `[]byte` ciphertext — encryption/decryption happens in the **handler** (Task 7), matching how `audit_export.go` splits `resolveSecret`/`openSecret` from the repo.

Types + methods to add:

```go
// EmailTransportConfig mirrors an email_transport_config row. Secret columns
// hold ciphertext (BYTEA) — the handler seals/opens them with the email KEK.
type EmailTransportConfig struct {
	TenantID        uuid.UUID
	Provider        string
	Enabled         bool
	FromAddress     string
	FromName        string
	ResendAPIKeyEnc []byte
	SMTPHost        string
	SMTPPort        int
	SMTPUsername    string
	SMTPPasswordEnc []byte
	SMTPTLSMode     string
	KEKVersion      int16
	LastTestAt      *time.Time
	LastTestOK      *bool
	LastTestError   string
	UpdatedAt       time.Time
	UpdatedBy       *uuid.UUID
}

// EmailDelivery mirrors an email_deliveries row.
type EmailDelivery struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	UserID            uuid.UUID
	ToAddress         string
	Category          string
	Subject           string
	BodySummary       string
	Link              string
	SourceScheduledID uuid.UUID
	Status            string
	Attempts          int
	NextAttemptAt     time.Time
	LastError         string
	Provider          string
	CreatedAt         time.Time
	SentAt            *time.Time
}
```

Methods (full SQL):

- `GetEmailTransportConfig(ctx, tenantID) (*EmailTransportConfig, error)` — `SELECT ... WHERE tenant_id=$1`; return `(nil, nil)` on `pgx.ErrNoRows` (no config yet).
- `UpsertEmailTransportConfig(ctx, cfg) error` — `INSERT ... ON CONFLICT (tenant_id) DO UPDATE SET ...` for every column except `tenant_id`.
- `UpdateEmailTestResult(ctx, tenantID, ok bool, errMsg string) error` — `UPDATE ... SET last_test_at=now(), last_test_ok=$2, last_test_error=$3`.
- `EnqueueEmailDelivery(ctx, d EmailDelivery) error` — `INSERT ... ON CONFLICT (source_scheduled_id, user_id) DO NOTHING` (idempotent fan-out).
- `ListEmailRecipients(ctx, tenantID uuid.UUID, category string) ([]uuid.UUID, error)` — `SELECT user_id FROM user_notification_preferences WHERE tenant_id=$1 AND category=$2 AND email_enabled=true`.
- `ClaimPendingEmailDeliveries(ctx, now time.Time, limit int) ([]*EmailDelivery, error)` — `FOR UPDATE SKIP LOCKED WHERE status='pending' AND next_attempt_at<=$1 ORDER BY next_attempt_at LIMIT $2`, flipping nothing (claim-by-select; the send loop updates status per-row after Send). **Note:** unlike the scheduled-notification claim, do NOT flip to an intermediate state — set `next_attempt_at = now() + interval '1 minute'` in the same UPDATE-RETURNING so a crashed sender's rows become claimable again after a minute (lease). SQL:

```sql
WITH claimed AS (
    SELECT id FROM email_deliveries
     WHERE status='pending' AND next_attempt_at <= $1
     ORDER BY next_attempt_at
       FOR UPDATE SKIP LOCKED
     LIMIT $2
)
UPDATE email_deliveries d
   SET next_attempt_at = now() + interval '1 minute'
  FROM claimed
 WHERE d.id = claimed.id
RETURNING d.id, d.tenant_id, d.user_id, d.to_address, d.category, d.subject,
          d.body_summary, COALESCE(d.link,''), d.source_scheduled_id, d.status,
          d.attempts, d.next_attempt_at, COALESCE(d.last_error,''),
          COALESCE(d.provider,''), d.created_at, d.sent_at
```

- `MarkEmailSent(ctx, id uuid.UUID, provider string) error` — `UPDATE ... SET status='sent', sent_at=now(), provider=$2, last_error=NULL WHERE id=$1`.
- `MarkEmailFailed(ctx, id uuid.UUID, attempts int, nextAttempt time.Time, failed bool, errMsg string) error` — `UPDATE ... SET attempts=$2, next_attempt_at=$3, status=CASE WHEN $4 THEN 'failed' ELSE 'pending' END, last_error=$5 WHERE id=$1`.
- `ListEmailDeliveries(ctx, tenantID, userID uuid.UUID, limit int) ([]*EmailDelivery, error)` — `WHERE tenant_id=$1 AND user_id=$2 ORDER BY created_at DESC LIMIT $3`.

- [ ] **Step 1: Write the failing test (SQL-shape unit test)**

The audit repo tests are integration-gated (testcontainers). Follow the package's existing convention — check whether `scheduled_notifications` has a `_test.go` using `testutil` Postgres. If integration tests are Docker-gated, write `email_test.go` guarded the same way (mirror the build tag / `testutil.NewPostgres` helper used by the existing repo tests). Cover: upsert→get round-trip (incl. ciphertext bytes), `EnqueueEmailDelivery` idempotency (second insert with same `(source_scheduled_id,user_id)` is a no-op), `ClaimPendingEmailDeliveries` respects `next_attempt_at`, `ListEmailRecipients` filters `email_enabled=true`, `MarkEmailSent`/`MarkEmailFailed` transitions, `ListEmailDeliveries` scoping.

```go
//go:build integration
// (match the build tag used by the existing audit repository integration tests)

func TestEmailTransportConfig_upsertRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newTestRepo(t) // mirror the helper used by other *_test.go in this pkg
	tid := uuid.New()
	in := EmailTransportConfig{
		TenantID: tid, Provider: "resend", Enabled: true,
		FromAddress: "n@example.com", FromName: "Reg",
		ResendAPIKeyEnc: []byte{0x01, 0x02, 0x03}, SMTPTLSMode: "starttls", KEKVersion: 1,
	}
	if err := repo.UpsertEmailTransportConfig(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetEmailTransportConfig(ctx, tid)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Provider != "resend" || !got.Enabled || string(got.ResendAPIKeyEnc) != "\x01\x02\x03" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/repository/ -run TestEmailTransportConfig -tags integration -v`
Expected: FAIL — methods undefined (or, without Docker, a compile error referencing the missing methods, which is the red state).

- [ ] **Step 3: Implement `email.go`** with all methods above (full SQL as specified).

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/audit && GOWORK=off go build ./... && GOWORK=off go test ./internal/repository/ -run TestEmail -tags integration -v` (integration needs Docker; if unavailable in-loop, at minimum `go build ./...` and `go vet ./internal/repository/` must pass — note that in the commit).
Expected: build + vet clean; integration green where Docker is available.

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/repository/email.go services/audit/internal/repository/email_test.go
git commit -m "feat(audit): email repository (config, deliveries queue, recipients)"
```

---

## Task 4: audit email transport package (Resend + SMTP + backoff)

**Files:**
- Create: `services/audit/internal/email/transport.go`, `services/audit/internal/email/transport_test.go`

- [ ] **Step 1: Write the failing test**

`transport_test.go`:

```go
package email

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestResendTransport_send_postsExpectedShape(t *testing.T) {
	var gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"abc"}`))
	}))
	defer srv.Close()

	tr := &resendTransport{
		apiKey: "re_secret", from: "Reg <n@example.com>",
		endpoint: srv.URL, client: srv.Client(),
	}
	err := tr.Send(context.Background(), Message{
		To: "u@example.com", Subject: "Hi", HTMLBody: "<b>x</b>", TextBody: "x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer re_secret" {
		t.Fatalf("auth header = %q", gotAuth)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["subject"] != "Hi" || payload["from"] != "Reg <n@example.com>" {
		t.Fatalf("body = %s", gotBody)
	}
}

func TestResendTransport_send_redactsKeyOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"bad key re_secret"}`))
	}))
	defer srv.Close()
	tr := &resendTransport{apiKey: "re_secret", from: "f", endpoint: srv.URL, client: srv.Client()}
	err := tr.Send(context.Background(), Message{To: "u@example.com", Subject: "s"})
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if strings.Contains(err.Error(), "re_secret") {
		t.Fatalf("error leaked the API key: %v", err)
	}
}

func TestBackoff_schedule(t *testing.T) {
	want := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d)=%v want %v", i+1, got, w)
		}
	}
	if Backoff(99) != 2*time.Hour {
		t.Error("Backoff clamps to last bucket")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/email/ -run 'TestResend|TestBackoff' -v`
Expected: FAIL — package/types undefined.

- [ ] **Step 3: Implement `transport.go`**

```go
// Package email implements the FUT-019 Phase 3 email notification channel:
// a pluggable transport (Resend HTTP API or SMTP) plus a send loop that drains
// the email_deliveries queue.
package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

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

// DecryptedConfig is the transport config with secrets already decrypted,
// built by the caller (send loop) from an email_transport_config row.
type DecryptedConfig struct {
	Provider     string
	FromAddress  string
	FromName     string
	ResendAPIKey string
	SMTPHost     string
	SMTPPort     int
	SMTPUsername string
	SMTPPassword string
	SMTPTLSMode  string
}

// fromHeader renders the RFC5322 From value: "Name <addr>" or "addr".
func (c DecryptedConfig) fromHeader() string {
	if c.FromName != "" {
		return fmt.Sprintf("%s <%s>", c.FromName, c.FromAddress)
	}
	return c.FromAddress
}

// NewTransport builds the concrete Transport for cfg.Provider.
func NewTransport(cfg DecryptedConfig) (Transport, error) {
	switch cfg.Provider {
	case "resend":
		if cfg.ResendAPIKey == "" {
			return nil, fmt.Errorf("resend transport: api key not set")
		}
		return &resendTransport{
			apiKey:   cfg.ResendAPIKey,
			from:     cfg.fromHeader(),
			endpoint: "https://api.resend.com/emails",
			client:   &http.Client{Timeout: 15 * time.Second},
		}, nil
	case "smtp":
		if cfg.SMTPHost == "" {
			return nil, fmt.Errorf("smtp transport: host not set")
		}
		return &smtpTransport{cfg: cfg}, nil
	default:
		return nil, fmt.Errorf("unknown email provider %q", cfg.Provider)
	}
}

// Backoff returns the retry delay for a given (1-based) attempt number, clamped
// to the last bucket. Mirrors the webhook dispatcher schedule.
func Backoff(attempt int) time.Duration {
	sched := []time.Duration{5 * time.Second, 30 * time.Second, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(sched) {
		return sched[len(sched)-1]
	}
	return sched[attempt-1]
}

// MaxAttempts is the retry ceiling; on the MaxAttempts-th failure the delivery
// flips to 'failed'.
const MaxAttempts = 5

// ── Resend ───────────────────────────────────────────────────────────

type resendTransport struct {
	apiKey   string
	from     string
	endpoint string
	client   *http.Client
}

func (t *resendTransport) Name() string { return "resend" }

func (t *resendTransport) Send(ctx context.Context, msg Message) error {
	body, err := json.Marshal(map[string]any{
		"from":    t.from,
		"to":      []string{msg.To},
		"subject": msg.Subject,
		"html":    msg.HTMLBody,
		"text":    msg.TextBody,
	})
	if err != nil {
		return fmt.Errorf("marshal resend body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build resend request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+t.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		// net.Error may embed the URL but never the key; still, redact defensively.
		return fmt.Errorf("resend send failed: %s", redact(err.Error(), t.apiKey))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("resend returned %d: %s", resp.StatusCode, redact(string(snippet), t.apiKey))
}

// redact removes any occurrence of secret from s so provider errors can't leak
// credentials into logs / last_error.
func redact(s, secret string) string {
	if secret == "" {
		return s
	}
	return strings.ReplaceAll(s, secret, "[redacted]")
}

// ── SMTP ─────────────────────────────────────────────────────────────

type smtpTransport struct {
	cfg DecryptedConfig
}

func (t *smtpTransport) Name() string { return "smtp" }

func (t *smtpTransport) Send(ctx context.Context, msg Message) error {
	addr := fmt.Sprintf("%s:%d", t.cfg.SMTPHost, t.cfg.SMTPPort)
	auth := smtp.PlainAuth("", t.cfg.SMTPUsername, t.cfg.SMTPPassword, t.cfg.SMTPHost)
	raw := buildMIME(t.cfg.fromHeader(), msg)

	send := func() error {
		switch t.cfg.SMTPTLSMode {
		case "implicit":
			return t.sendImplicitTLS(addr, auth, msg.To, raw)
		default: // starttls / none — smtp.SendMail negotiates STARTTLS when offered.
			return smtp.SendMail(addr, auth, t.cfg.FromAddress, []string{msg.To}, raw)
		}
	}
	if err := send(); err != nil {
		return fmt.Errorf("smtp send failed: %s", redact(err.Error(), t.cfg.SMTPPassword))
	}
	return nil
}

// sendImplicitTLS dials a TLS socket first (port 465 style) then speaks SMTP.
func (t *smtpTransport) sendImplicitTLS(addr string, auth smtp.Auth, to string, raw []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: t.cfg.SMTPHost, MinVersion: tls.VersionTLS12})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, t.cfg.SMTPHost)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Auth(auth); err != nil {
		return err
	}
	if err := c.Mail(t.cfg.FromAddress); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(raw); err != nil {
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// buildMIME renders a minimal multipart/alternative message (text + html).
func buildMIME(from string, msg Message) []byte {
	const boundary = "janus-mime-boundary-8f2c"
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", msg.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", msg.Subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", boundary, msg.TextBody)
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n", boundary, msg.HTMLBody)
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return []byte(b.String())
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/audit && GOWORK=off go test ./internal/email/ -run 'TestResend|TestBackoff' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/email/transport.go services/audit/internal/email/transport_test.go
git commit -m "feat(audit): pluggable email transport (Resend + SMTP) with redaction"
```

---

## Task 5: auth `ResolveUserEmails` RPC

**Files:**
- Modify: `proto/auth/v1/auth.proto` (+ regen `proto/gen/go/auth/v1/*`)
- Modify: `services/auth/internal/repository/user.go`
- Modify: `services/auth/internal/service/*.go` (the service that fronts the user repo — mirror `LookupUsernames`)
- Modify: `services/auth/internal/handler/grpc.go`
- Test: `services/auth/internal/handler/grpc_test.go` (or the file holding `LookupUsernames` tests)

**Context:** this is a near-clone of the existing `LookupUsernames` RPC (batch, dedupe, cap). Reuse its constant style.

- [ ] **Step 1: Add the proto**

In `proto/auth/v1/auth.proto`, add to `service AuthService`:

```proto
  // ResolveUserEmails maps a batch of user ids to their email addresses within
  // a tenant. Used by registry-audit to resolve email-notification recipients.
  // Users with no email are omitted from the response.
  rpc ResolveUserEmails(ResolveUserEmailsRequest) returns (ResolveUserEmailsResponse);
```

And the messages (near `LookupUsernames*`):

```proto
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

- [ ] **Step 2: Regenerate stubs**

Run: `cd proto && buf generate`
Expected: `proto/gen/go/auth/v1/auth.pb.go` + `auth_grpc.pb.go` updated; `git status` shows them modified.

- [ ] **Step 3: Write the failing handler test**

Mirror the `LookupUsernames` test in the auth handler test file. Cover: invalid tenant_id → `InvalidArgument`; empty `user_ids` → empty response; a fake service returning two emails (one with empty email omitted). Use the existing fake-service pattern in that test file.

Run: `cd services/auth && GOWORK=off go test ./internal/handler/ -run ResolveUserEmails -v`
Expected: FAIL — method undefined.

- [ ] **Step 4: Implement repo + service + handler**

Repo (`user.go`) — batch email lookup:

```go
// ResolveEmails returns (user_id, email, email_verified) for the given ids
// within a tenant, skipping users with no email. Human accounts only.
func (r *UserRepository) ResolveEmails(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) ([]EmailLookup, error) {
	const q = `
		SELECT id, COALESCE(email, ''), COALESCE(email_verified, false)
		FROM   users
		WHERE  tenant_id = $1 AND id = ANY($2) AND kind = 'human' AND COALESCE(email,'') <> ''`
	rows, err := r.pool.Query(ctx, q, tenantID, ids)
	if err != nil {
		return nil, fmt.Errorf("resolve emails: %w", err)
	}
	defer rows.Close()
	out := make([]EmailLookup, 0, len(ids))
	for rows.Next() {
		var e EmailLookup
		if err := rows.Scan(&e.ID, &e.Email, &e.EmailVerified); err != nil {
			return nil, fmt.Errorf("scan email lookup: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// EmailLookup is one resolved recipient.
type EmailLookup struct {
	ID            uuid.UUID
	Email         string
	EmailVerified bool
}
```

> **Verify before coding:** confirm the `users` table has an `email_verified` column. Run `grep -rn "email_verified" services/auth/migrations/`. If absent, drop the `email_verified` selection + proto field to `false` (the spec treats it as informational only and does not gate on it).

Service method (mirror `LookupUsernames` in the service layer) — parse/dedupe/cap `user_ids`, call `repo.ResolveEmails`, return `[]EmailLookup`.

Handler (`grpc.go`) — mirror `LookupUsernames` exactly:

```go
func (h *GRPCHandler) ResolveUserEmails(ctx context.Context, req *authv1.ResolveUserEmailsRequest) (*authv1.ResolveUserEmailsResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	raw := req.GetUserIds()
	if len(raw) == 0 {
		return &authv1.ResolveUserEmailsResponse{}, nil
	}
	if len(raw) > lookupUsernamesMaxBatch { // reuse the existing batch cap constant
		return nil, status.Errorf(codes.InvalidArgument, "user_ids exceeds batch cap of %d", lookupUsernamesMaxBatch)
	}
	seen := make(map[uuid.UUID]struct{}, len(raw))
	parsed := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, perr := uuid.Parse(s)
		if perr != nil {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		parsed = append(parsed, id)
	}
	if len(parsed) == 0 {
		return &authv1.ResolveUserEmailsResponse{}, nil
	}
	emails, err := h.svc.ResolveUserEmails(ctx, tenantID, parsed)
	if err != nil {
		return nil, errcodes.MapDBError(err, "resolve user emails")
	}
	out := make([]*authv1.ResolvedEmail, len(emails))
	for i, e := range emails {
		out[i] = &authv1.ResolvedEmail{UserId: e.ID.String(), Email: e.Email, EmailVerified: e.EmailVerified}
	}
	return &authv1.ResolveUserEmailsResponse{Emails: out}, nil
}
```

- [ ] **Step 5: Run tests to verify pass**

Run: `cd services/auth && GOWORK=off go build ./... && GOWORK=off go test ./internal/handler/ -run ResolveUserEmails -v`
Expected: build clean; PASS.

- [ ] **Step 6: Commit**

```bash
git add proto/auth proto/gen/go/auth services/auth/internal
git commit -m "feat(auth): ResolveUserEmails RPC for email-notification recipients"
```

---

## Task 6: audit email RPCs (proto)

**Files:**
- Modify: `proto/audit/v1/audit.proto` (+ regen)

- [ ] **Step 1: Add the proto**

Add to `service AuditService`:

```proto
  // FUT-019 Phase 3 — email transport config + delivery log.
  rpc GetEmailTransportConfig(GetEmailTransportConfigRequest) returns (EmailTransportConfig);
  rpc PutEmailTransportConfig(PutEmailTransportConfigRequest) returns (EmailTransportConfig);
  rpc SendTestEmail(SendTestEmailRequest) returns (SendTestEmailResponse);
  rpc ListEmailDeliveries(ListEmailDeliveriesRequest) returns (ListEmailDeliveriesResponse);
```

Messages (secrets are **write-only in Put, never in the returned config**):

```proto
message GetEmailTransportConfigRequest { string tenant_id = 1; }

message EmailTransportConfig {
  string provider = 1;              // resend | smtp
  bool   enabled = 2;
  string from_address = 3;
  string from_name = 4;
  string smtp_host = 5;
  int32  smtp_port = 6;
  string smtp_username = 7;
  string smtp_tls_mode = 8;
  bool   has_resend_key = 9;        // true when a key is stored (never the value)
  bool   has_smtp_password = 10;
  google.protobuf.Timestamp last_test_at = 11;
  bool   last_test_ok = 12;
  string last_test_error = 13;
}

message PutEmailTransportConfigRequest {
  string tenant_id = 1;
  string updated_by = 2;
  string provider = 3;
  bool   enabled = 4;
  string from_address = 5;
  string from_name = 6;
  string smtp_host = 7;
  int32  smtp_port = 8;
  string smtp_username = 9;
  string smtp_tls_mode = 10;
  // Secrets: empty means "keep existing"; a value replaces it. Never echoed back.
  string resend_api_key = 11;
  string smtp_password = 12;
}

message SendTestEmailRequest {
  string tenant_id = 1;
  string to_address = 2;   // resolved by the BFF to the caller's own email
}
message SendTestEmailResponse {
  bool   ok = 1;
  string error = 2;
}

message ListEmailDeliveriesRequest {
  string tenant_id = 1;
  string user_id = 2;
  int32  page_size = 3;
}
message ListEmailDeliveriesResponse {
  repeated EmailDelivery deliveries = 1;
}
message EmailDelivery {
  string id = 1;
  string category = 2;
  string subject = 3;
  string to_address = 4;
  string status = 5;       // pending | sent | failed
  string last_error = 6;
  google.protobuf.Timestamp created_at = 7;
  google.protobuf.Timestamp sent_at = 8;
}
```

- [ ] **Step 2: Regenerate + build**

Run: `cd proto && buf generate && cd ../services/audit && GOWORK=off go build ./...`
Expected: stubs regenerated; audit still builds (handler not yet implemented — build passes because generated code is standalone).

- [ ] **Step 3: Commit**

```bash
git add proto/audit proto/gen/go/audit
git commit -m "feat(audit): email transport + delivery-log gRPC contract"
```

---

## Task 7: audit email gRPC handler (4 RPCs + secret sealing)

**Files:**
- Create: `services/audit/internal/handler/grpc_email.go`, `services/audit/internal/handler/grpc_email_test.go`

**Context:** mirror `audit_export.go`'s `resolveSecret`/`openSecret` split + the `*Set`-boolean masking. The four RPCs are methods on the **existing** audit gRPC server struct (the one implementing `AuditServiceServer`) — do NOT create a second server type or registration.

- [ ] **Step 0: Extend the existing server struct**

Find it: `grep -rn "AuditServiceServer\|repo \*repository.Repository" services/audit/internal/handler`. Add two fields (and import `"github.com/steveokay/oci-janus/services/audit/internal/email"`):

```go
	// FUT-019 Phase 3 — email channel. emailKEK may be empty (email disabled):
	// a Put carrying a secret then returns FailedPrecondition, matching the
	// audit-export posture. newEmailTransport is injected so tests can fake the
	// transport for SendTestEmail.
	emailKEK          []byte
	newEmailTransport func(email.DecryptedConfig) (email.Transport, error)
```

Task 8 sets these in the constructor (`emailKEK` + `email.NewTransport`). All four methods below use this struct as their receiver.

Method logic:

- `GetEmailTransportConfig`: load row (`repo.GetEmailTransportConfig`). If nil, return a zero-value `EmailTransportConfig{Provider:"resend", SmtpTlsMode:"starttls"}` (sensible defaults for a fresh form). Map to proto with `HasResendKey = len(row.ResendAPIKeyEnc)>0`, `HasSmtpPassword = len(row.SMTPPasswordEnc)>0`. **Never** decrypt/return secrets.
- `PutEmailTransportConfig`: load existing row (for keep-on-empty). Build `EmailTransportConfig` from the request; seal secrets via `sealSecret(kek, existingCipher, plaintext)`:

```go
func sealSecret(kek, existing []byte, plaintext string) ([]byte, error) {
	if plaintext == "" {
		return existing, nil // keep existing
	}
	if len(kek) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "email transport secrets key (NOTIFY_EMAIL_KEY_HEX) not configured")
	}
	ct, err := aes.Encrypt([]byte(plaintext), kek)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt secret: %v", err)
	}
	return ct, nil
}
```

  Stamp `KEKVersion = 1`. Upsert. Return the freshly-masked config (re-run the Get mapping).
- `SendTestEmail`: load row; if `!enabled` or provider empty → return `{Ok:false, Error:"email transport not enabled"}` (not an RPC error — the panel shows it inline). Decrypt secrets (`openSecret`), build `DecryptedConfig`, `newTransport(cfg)`, `Send` a canned test message to `req.ToAddress`. Record via `repo.UpdateEmailTestResult(ok, errStr)`. Return `{Ok, Error: redactedErr}`.
- `ListEmailDeliveries`: parse ids, `repo.ListEmailDeliveries(tenant, user, pageSize)` (default/cap pageSize to 50), map to proto.

`openSecret` (mirror audit_export.go):

```go
func openSecret(kek, ct []byte) (string, error) {
	if len(ct) == 0 {
		return "", nil
	}
	if len(kek) == 0 {
		return "", fmt.Errorf("email secrets key not configured")
	}
	pt, err := aes.Decrypt(ct, kek)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(pt), nil
}
```

- [ ] **Step 1: Write the failing test**

`grpc_email_test.go` — table tests with a fake repo (or the pkg's existing test harness). Cover:
- `GetEmailTransportConfig` on a stored row returns `HasResendKey=true`, `provider`, `enabled`, and **no** secret field populated.
- `PutEmailTransportConfig` with empty `resend_api_key` preserves the existing ciphertext; with a value, re-encrypts (assert ciphertext changed + decrypts back via the test KEK).
- `PutEmailTransportConfig` with a secret **and empty KEK** → `FailedPrecondition`.
- `SendTestEmail` with a fake transport records `last_test_ok=true`; a failing transport records `ok=false` + redacted error.
- `ListEmailDeliveries` maps rows + enforces the pageSize cap.

Run: `cd services/audit && GOWORK=off go test ./internal/handler/ -run Email -v`
Expected: FAIL — methods undefined.

- [ ] **Step 2: Implement `grpc_email.go`** per the logic above.

- [ ] **Step 3: Run to verify pass**

Run: `cd services/audit && GOWORK=off go test ./internal/handler/ -run Email -v && GOWORK=off go vet ./...`
Expected: PASS + vet clean.

- [ ] **Step 4: Commit**

```bash
git add services/audit/internal/handler/grpc_email.go services/audit/internal/handler/grpc_email_test.go
git commit -m "feat(audit): email transport + delivery-log gRPC handlers (write-only secrets)"
```

---

## Task 8: audit wiring — email KEK, auth client, register handler

**Files:**
- Modify: `services/audit/internal/server/server.go`
- Modify: `services/audit/cmd/server/main.go` (only if the Runner/loop start lives there)

**Context:** decode the email KEK the same way `ExportSecretsKeyHex` is decoded; dial registry-auth mirroring the registry-tenant dial; give the gRPC server struct its `emailKEK` + `newEmailTransport` + an `*authv1.AuthServiceClient`.

- [ ] **Step 1: Decode the email KEK**

Near the existing `secretsKey` decode block:

```go
var emailKEK []byte
if keyHex := cfg.NotifyEmailKeyHex; keyHex != "" {
	k, err := decodeHexKey(keyHex) // reuse the existing helper (32-byte check)
	if err != nil {
		return fmt.Errorf("NOTIFY_EMAIL_KEY_HEX: %w", err)
	}
	emailKEK = k
}
```

- [ ] **Step 2: Dial registry-auth (optional)**

```go
var authClient authv1.AuthServiceClient
if cfg.AuthGRPCAddr != "" {
	authCreds, err := cfg.MTLSClientCreds("registry-auth")
	if err != nil {
		return fmt.Errorf("build auth gRPC creds: %w", err)
	}
	authConn, err := grpc.NewClient(cfg.AuthGRPCAddr, grpc.WithTransportCredentials(authCreds))
	if err != nil {
		return fmt.Errorf("dial auth gRPC: %w", err)
	}
	defer func() { _ = authConn.Close() }()
	authConn.Connect() // eager, per CLAUDE.md §6
	authClient = authv1.NewAuthServiceClient(authConn)
}
```

Add `authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"` to imports.

- [ ] **Step 3: Attach KEK + transport factory to the gRPC server struct**

Set the new fields on the audit gRPC server struct when it's constructed: `emailKEK: emailKEK`, `newEmailTransport: email.NewTransport`. (No separate registration — the four methods are on the existing `AuditServiceServer`.)

- [ ] **Step 4: Build**

Run: `cd services/audit && GOWORK=off go build ./... && GOWORK=off go vet ./...`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/server/server.go services/audit/cmd/server/main.go
git commit -m "feat(audit): wire email KEK + registry-auth client into the gRPC server"
```

---

## Task 9: dispatcher enqueue — fan out email deliveries

**Files:**
- Modify: `services/audit/internal/scheduler/loops.go`
- Test: `services/audit/internal/scheduler/loops_test.go` (extend)

**Context:** `dispatchOne` currently inserts the bell `audit_events` row. After that succeeds, best-effort enqueue email rows. The `Runner` needs new collaborators: the `*repository.Repository` (already has it) + an email-recipient resolver interface (so tests can fake auth) + a "is email enabled" gate. Add an interface to keep auth out of the scheduler's imports:

```go
// EmailRecipientResolver resolves user ids to email addresses. Implemented by a
// thin adapter over authv1.AuthServiceClient (wired in server.go); nil disables
// email fan-out (AUTH_GRPC_ADDR unset).
type EmailRecipientResolver interface {
	ResolveEmails(ctx context.Context, tenantID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID]string, error)
}
```

Add `resolver EmailRecipientResolver` to `Runner` + a `RunnerConfig`/`New` param (or a `WithEmailResolver` setter). When `resolver == nil`, skip enqueue entirely.

New best-effort method:

```go
// enqueueEmail fans a rendered notification out to the email_deliveries queue,
// one row per user who has email_enabled for this category. Best-effort: every
// failure logs + returns nil so a mail problem never fails bell delivery.
func (r *Runner) enqueueEmail(ctx context.Context, sn *repository.ScheduledNotification, rendered RenderedNotification) {
	if r.resolver == nil {
		return
	}
	recipients, err := r.repo.ListEmailRecipients(ctx, sn.TenantID, sn.Category)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 email: list recipients failed", "err", err, "category", sn.Category)
		return
	}
	if len(recipients) == 0 {
		return
	}
	emails, err := r.resolver.ResolveEmails(ctx, sn.TenantID, recipients)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 email: resolve emails failed", "err", err)
		return
	}
	for uid, addr := range emails {
		if addr == "" {
			continue
		}
		if err := r.repo.EnqueueEmailDelivery(ctx, repository.EmailDelivery{
			TenantID:          sn.TenantID,
			UserID:            uid,
			ToAddress:         addr,
			Category:          sn.Category,
			Subject:           rendered.Title,
			BodySummary:       rendered.Summary,
			Link:              rendered.Link,
			SourceScheduledID: sn.ID,
		}); err != nil {
			slog.WarnContext(ctx, "FUT-019 email: enqueue failed", "err", err, "user_id", uid)
		}
	}
}
```

Call it in `dispatchOne` right after the `r.repo.Insert(...)` bell write succeeds:

```go
	// FUT-019 Phase 3 — best-effort email fan-out. Never fails the bell write.
	r.enqueueEmail(ctx, sn, rendered)
	return nil
```

> `RenderedNotification` is the type `cat.Render` returns (the recon shows `rendered.Title/Summary/Link/Metadata`); use its real name (`grep -n "func.*Render(" services/audit/internal/scheduler/categories.go`).

- [ ] **Step 1: Write the failing test**

In `loops_test.go`, add a test with a fake `EmailRecipientResolver` + a real/fake repo asserting: when two users have `email_enabled` for a category, `dispatchOne` enqueues two `email_deliveries` rows; when `resolver==nil`, none; a resolver error does not fail `dispatchOne`.

Run: `cd services/audit && GOWORK=off go test ./internal/scheduler/ -run Email -v`
Expected: FAIL.

- [ ] **Step 2: Implement** the interface, `Runner` field, `enqueueEmail`, and the `dispatchOne` call.

- [ ] **Step 3: Run to verify pass**

Run: `cd services/audit && GOWORK=off go test ./internal/scheduler/ -v`
Expected: PASS (existing dispatcher tests still green).

- [ ] **Step 4: Commit**

```bash
git add services/audit/internal/scheduler/
git commit -m "feat(audit): dispatcher fans notifications out to the email queue (best-effort)"
```

---

## Task 10: email send loop + wiring

**Files:**
- Create: `services/audit/internal/email/sender.go`, `services/audit/internal/email/sender_test.go`
- Modify: `services/audit/internal/server/server.go` (build the resolver adapter, start the Sender, pass the resolver to the Runner)

**Context:** the Sender ticks ~20s, loads+caches the transport, claims pending rows, sends, updates status with `Backoff`.

- [ ] **Step 1: Write the failing test**

`sender_test.go` — a fake repo returning one pending delivery + a fake transport. Assert: on success the row is marked sent; on failure `attempts` increments and `next_attempt_at` uses `Backoff`; on the `MaxAttempts`-th failure status flips to failed. Use a repo interface the Sender depends on (so the test needn't hit Postgres):

```go
type senderRepo interface {
	GetEmailTransportConfig(ctx context.Context, tenantID uuid.UUID) (*repository.EmailTransportConfig, error)
	ClaimPendingEmailDeliveries(ctx context.Context, now time.Time, limit int) ([]*repository.EmailDelivery, error)
	MarkEmailSent(ctx context.Context, id uuid.UUID, provider string) error
	MarkEmailFailed(ctx context.Context, id uuid.UUID, attempts int, next time.Time, failed bool, errMsg string) error
}
```

Run: `cd services/audit && GOWORK=off go test ./internal/email/ -run Sender -v`
Expected: FAIL.

- [ ] **Step 2: Implement `sender.go`**

```go
// Sender drains the email_deliveries queue and sends via the configured
// transport. Constructed in server.go and started in a goroutine alongside the
// scheduler runner. Disabled (idle) when the KEK is unset or config is disabled.
type Sender struct {
	repo     senderRepo
	kek      []byte
	interval time.Duration
	batch    int
	platformHost string // absolute-link base for CTA URLs
	buildTransport func(email DecryptedConfig) (Transport, error)
}

func NewSender(repo senderRepo, kek []byte, platformHost string) *Sender {
	return &Sender{repo: repo, kek: kek, interval: 20 * time.Second, batch: 20,
		platformHost: platformHost, buildTransport: NewTransport}
}

func (s *Sender) Start(ctx context.Context) {
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.runTick(ctx)
		}
	}
}

func (s *Sender) runTick(ctx context.Context) {
	if len(s.kek) == 0 {
		return // email disabled
	}
	// One tenant in single mode; iterate configs by claiming rows (rows carry
	// tenant_id). Load the config lazily per tenant seen in the batch.
	now := time.Now().UTC()
	rows, err := s.repo.ClaimPendingEmailDeliveries(ctx, now, s.batch)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 sender: claim failed", "err", err)
		return
	}
	transportByTenant := map[uuid.UUID]Transport{}
	for _, d := range rows {
		tr, err := s.transportFor(ctx, d.TenantID, transportByTenant)
		if err != nil {
			s.fail(ctx, d, err)
			continue
		}
		if tr == nil {
			continue // config disabled — leave pending (lease expires, retried)
		}
		msg := renderMessage(s.platformHost, d)
		if err := tr.Send(ctx, msg); err != nil {
			s.fail(ctx, d, err)
			continue
		}
		if err := s.repo.MarkEmailSent(ctx, d.ID, tr.Name()); err != nil {
			slog.ErrorContext(ctx, "FUT-019 sender: mark sent failed", "err", err, "id", d.ID)
		}
	}
}

// transportFor loads + caches a tenant's transport, or nil if disabled.
func (s *Sender) transportFor(ctx context.Context, tenantID uuid.UUID, cache map[uuid.UUID]Transport) (Transport, error) {
	if tr, ok := cache[tenantID]; ok {
		return tr, nil
	}
	cfg, err := s.repo.GetEmailTransportConfig(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	if cfg == nil || !cfg.Enabled {
		cache[tenantID] = nil
		return nil, nil
	}
	dec, err := decryptConfig(s.kek, cfg)
	if err != nil {
		return nil, err
	}
	tr, err := s.buildTransport(dec)
	if err != nil {
		return nil, err
	}
	cache[tenantID] = tr
	return tr, nil
}

func (s *Sender) fail(ctx context.Context, d *repository.EmailDelivery, sendErr error) {
	attempts := d.Attempts + 1
	failed := attempts >= MaxAttempts
	next := time.Now().UTC().Add(Backoff(attempts))
	if err := s.repo.MarkEmailFailed(ctx, d.ID, attempts, next, failed, sendErr.Error()); err != nil {
		slog.ErrorContext(ctx, "FUT-019 sender: mark failed errored", "err", err, "id", d.ID)
	}
}
```

Add `decryptConfig(kek, *repository.EmailTransportConfig) (DecryptedConfig, error)` (uses `openSecret`-style decrypt for the two secret columns) and `renderMessage(platformHost string, d *repository.EmailDelivery) Message` (builds the HTML+text body per spec §6.1, absolute link = `platformHost + d.Link`).

- [ ] **Step 3: Run to verify pass**

Run: `cd services/audit && GOWORK=off go test ./internal/email/ -v`
Expected: PASS.

- [ ] **Step 4: Wire the Sender + resolver in `server.go`**

- Build a resolver adapter over `authClient`:

```go
type authEmailResolver struct{ c authv1.AuthServiceClient }

func (a authEmailResolver) ResolveEmails(ctx context.Context, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	strIDs := make([]string, len(ids))
	for i, id := range ids {
		strIDs[i] = id.String()
	}
	resp, err := a.c.ResolveUserEmails(ctx, &authv1.ResolveUserEmailsRequest{TenantId: tenantID.String(), UserIds: strIDs})
	if err != nil {
		return nil, err
	}
	out := make(map[uuid.UUID]string, len(resp.GetEmails()))
	for _, e := range resp.GetEmails() {
		if id, perr := uuid.Parse(e.GetUserId()); perr == nil {
			out[id] = e.GetEmail()
		}
	}
	return out, nil
}
```

- Pass the resolver into `scheduler.New(...)` (or `runner.WithEmailResolver(...)`) — only when `authClient != nil`.
- Start the Sender in a goroutine next to the runner:

```go
go func() {
	slog.Info("FUT-019: starting email sender loop")
	email.NewSender(repo, emailKEK, cfg.PlatformHost).Start(ctx)
	slog.Info("FUT-019: email sender stopped")
}()
```

> Confirm audit config exposes a platform-host / public base URL. If not, add `PLATFORM_HOST` to audit config (mirror management's `cfg.PlatformHost`); links then fall back to relative if empty.

- [ ] **Step 5: Build + vet + commit**

Run: `cd services/audit && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...`
Expected: clean (integration-gated repo tests may skip without Docker).

```bash
git add services/audit/internal/email/ services/audit/internal/server/server.go
git commit -m "feat(audit): email send loop + resolver wiring (drains queue, webhook-style backoff)"
```

---

## Task 11: BFF routes — transport config CRUD + test + delivery log

**Files:**
- Create: `services/management/internal/handler/email_transport.go`, `services/management/internal/handler/email_transport_test.go`
- Modify: `services/management/internal/handler/handler.go` (register 4 routes)

**Context:** `h.audit` is always non-nil (required). Admin gate = `requireEmailAdmin` (mirror `requireScannerAdmin`, minus the scanner-nil branch). User/tenant from `middleware.TenantIDFromContext` / `middleware.UserIDFromContext`. Map audit `FailedPrecondition` → HTTP `409`.

Handlers:

```go
// requireEmailAdmin gates the transport-config routes to platform admins and
// blocks service-account bearers (a platform-wide config change).
func (h *Handler) requireEmailAdmin(w http.ResponseWriter, r *http.Request) bool {
	if middleware.PrincipalKindFromContext(r.Context()) == middleware.PrincipalKindServiceAccount {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	if !h.effectiveGlobalAdmin(r) {
		writeError(w, http.StatusForbidden, "platform-admin role required")
		return false
	}
	return true
}

func (h *Handler) handleGetEmailTransport(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	resp, err := h.audit.GetEmailTransportConfig(r.Context(), &auditv1.GetEmailTransportConfigRequest{TenantId: tenantID})
	if err != nil {
		writeGRPCError(w, err) // helper that maps FailedPrecondition→409, else 500
		return
	}
	writeJSON(w, http.StatusOK, emailTransportToJSON(resp))
}

func (h *Handler) handlePutEmailTransport(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	var body emailTransportPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	resp, err := h.audit.PutEmailTransportConfig(r.Context(), &auditv1.PutEmailTransportConfigRequest{
		TenantId: tenantID, UpdatedBy: userID,
		Provider: body.Provider, Enabled: body.Enabled,
		FromAddress: body.FromAddress, FromName: body.FromName,
		SmtpHost: body.SMTPHost, SmtpPort: int32(body.SMTPPort),
		SmtpUsername: body.SMTPUsername, SmtpTlsMode: body.SMTPTLSMode,
		ResendApiKey: body.ResendAPIKey, SmtpPassword: body.SMTPPassword,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emailTransportToJSON(resp))
}

func (h *Handler) handleTestEmailTransport(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	// Resolve the caller's own email via auth (no arbitrary recipient).
	email, err := h.resolveCallerEmail(r.Context(), tenantID, userID)
	if err != nil || email == "" {
		writeError(w, http.StatusBadRequest, "your account has no email address to test with")
		return
	}
	resp, err := h.audit.SendTestEmail(r.Context(), &auditv1.SendTestEmailRequest{TenantId: tenantID, ToAddress: email})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": resp.GetOk(), "error": resp.GetError()})
}

func (h *Handler) handleListEmailDeliveries(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	resp, err := h.audit.ListEmailDeliveries(r.Context(), &auditv1.ListEmailDeliveriesRequest{
		TenantId: tenantID, UserId: userID, PageSize: 25,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, emailDeliveriesToJSON(resp))
}
```

- `resolveCallerEmail` reuses `h.auth.ResolveUserEmails(tenant, [userID])` (the same RPC).
- `writeGRPCError` — if one doesn't exist, add a small helper: `status.Code(err)==codes.FailedPrecondition → 409`; `NotFound → 404`; `InvalidArgument → 400`; else `500`. (Check for an existing helper first: `grep -rn "func writeGRPCError\|status.Code" services/management/internal/handler`.)

Route registration in `handler.go` (after the notification-preferences block):

```go
	// FUT-019 Phase 3 — email transport (admin) + per-user delivery log.
	mux.Handle("GET /api/v1/notifications/email-transport",
		authMW(http.HandlerFunc(h.handleGetEmailTransport)))
	mux.Handle("PUT /api/v1/notifications/email-transport",
		authMW(http.HandlerFunc(h.handlePutEmailTransport)))
	mux.Handle("POST /api/v1/notifications/email-transport/test",
		authMW(http.HandlerFunc(h.handleTestEmailTransport)))
	mux.Handle("GET /api/v1/notifications/email-deliveries",
		authMW(http.HandlerFunc(h.handleListEmailDeliveries)))
```

- [ ] **Step 1: Write the failing test**

`email_transport_test.go` — mirror the existing management handler test harness (fake audit + auth servers). Cover: GET as non-admin → 403; GET as admin returns masked config; PUT round-trips; test-send resolves caller email + returns `{ok}`; audit `FailedPrecondition` → 409; delivery-log forces the JWT user id (a caller can't pass someone else's).

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -run EmailTransport -v`
Expected: FAIL.

- [ ] **Step 2: Implement** the handlers, JSON structs (`emailTransportPutBody`, `emailTransportToJSON`, `emailDeliveriesToJSON`), `requireEmailAdmin`, `resolveCallerEmail`, `writeGRPCError`, and route registration.

- [ ] **Step 3: Run to verify pass**

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -run Email -v && GOWORK=off go build ./... && GOWORK=off go vet ./...`
Expected: PASS + clean.

- [ ] **Step 4: Commit**

```bash
git add services/management/internal/handler/email_transport.go services/management/internal/handler/email_transport_test.go services/management/internal/handler/handler.go
git commit -m "feat(management): BFF email transport CRUD + test-send + delivery log"
```

---

## Task 12: FE API hooks

**Files:**
- Create: `frontend/src/lib/api/email-transport.ts`, `frontend/src/lib/api/email-deliveries.ts`

**Context:** mirror `notification-preferences.ts` (axios `apiClient` + React Query).

- [ ] **Step 1: Write `email-transport.ts`**

```typescript
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

export interface EmailTransportConfig {
  provider: "resend" | "smtp";
  enabled: boolean;
  from_address: string;
  from_name: string;
  smtp_host: string;
  smtp_port: number;
  smtp_username: string;
  smtp_tls_mode: "starttls" | "implicit" | "none";
  has_resend_key: boolean;
  has_smtp_password: boolean;
  last_test_at?: string;
  last_test_ok?: boolean;
  last_test_error?: string;
}

export interface EmailTransportPut {
  provider: "resend" | "smtp";
  enabled: boolean;
  from_address: string;
  from_name: string;
  smtp_host: string;
  smtp_port: number;
  smtp_username: string;
  smtp_tls_mode: "starttls" | "implicit" | "none";
  resend_api_key: string; // empty = keep existing
  smtp_password: string; // empty = keep existing
}

export const emailTransportKeys = { all: ["email-transport"] as const };

export function useEmailTransport() {
  return useQuery({
    queryKey: emailTransportKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<EmailTransportConfig>("/notifications/email-transport");
      return data;
    },
    staleTime: 30_000,
  });
}

export function useUpdateEmailTransport() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: EmailTransportPut) => {
      const { data } = await apiClient.put<EmailTransportConfig>("/notifications/email-transport", body);
      return data;
    },
    onSuccess: (data) => qc.setQueryData(emailTransportKeys.all, data),
  });
}

export function useSendTestEmail() {
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<{ ok: boolean; error: string }>(
        "/notifications/email-transport/test",
      );
      return data;
    },
  });
}
```

- [ ] **Step 2: Write `email-deliveries.ts`**

```typescript
import { useQuery } from "@tanstack/react-query";
import { apiClient } from "./client";

export interface EmailDelivery {
  id: string;
  category: string;
  subject: string;
  to_address: string;
  status: "pending" | "sent" | "failed";
  last_error: string;
  created_at?: string;
  sent_at?: string;
}

export interface EmailDeliveriesPage {
  deliveries: EmailDelivery[];
}

export const emailDeliveryKeys = { all: ["email-deliveries"] as const };

export function useEmailDeliveries(enabled = true) {
  return useQuery({
    queryKey: emailDeliveryKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<EmailDeliveriesPage>("/notifications/email-deliveries");
      return data;
    },
    staleTime: 30_000,
    enabled,
  });
}
```

- [ ] **Step 3: Typecheck**

Run: `cd frontend && npm run typecheck`
Expected: 0 errors.

- [ ] **Step 4: Commit**

```bash
git add frontend/src/lib/api/email-transport.ts frontend/src/lib/api/email-deliveries.ts
git commit -m "feat(fe): email transport + delivery-log API hooks"
```

---

## Task 13: FE transport panel + unlock email column

**Files:**
- Create: `frontend/src/components/settings/email-transport-panel.tsx`, `frontend/src/components/settings/__tests__/email-transport-panel.test.tsx`
- Modify: `frontend/src/routes/_authenticated.settings.notifications.tsx`

- [ ] **Step 1: Write `EmailTransportPanel`**

A card following the matrix card's class pattern (`rounded-lg border border-[var(--color-border)] bg-[var(--color-surface)] p-5 shadow-[var(--shadow-card)]`). Contents:
- Provider `<select>` Resend | SMTP; a **"Use Gmail"** button setting `provider=smtp, smtp_host="smtp.gmail.com", smtp_port=587, smtp_tls_mode="starttls"`.
- Conditional fields: Resend → API key `<input type="password" placeholder={cfg.has_resend_key ? "•••• configured" : ""}>`; SMTP → host/port/username/password (password placeholder `•••• configured` when `has_smtp_password`) + TLS mode select.
- From address + from name; **Enabled** checkbox.
- **Send test email** button → `useSendTestEmail().mutate()`; render `last_test_ok`/`last_test_error` inline (green/red) + the mutation result.
- **Save** → `useUpdateEmailTransport().mutate(body)`; empty secret inputs send `""` (keep existing). Toast on success/error (sonner).
- Gate to admins: `const isAdmin = useIsGlobalAdmin();` — render the panel only when `isAdmin`.

Test (`email-transport-panel.test.tsx`): renders fields for the resend provider; clicking "Use Gmail" switches to SMTP presets; Save calls the mutation with an empty `resend_api_key` when the input is left blank; the test button shows the returned result. Mock the hooks.

- [ ] **Step 2: Mount panel + unlock email column** in `_authenticated.settings.notifications.tsx`:
- Import + render `<EmailTransportPanel />` immediately before the matrix `<section>` (around line 103).
- Remove `hint="Wired in Phase 3+"` from the **email** `ChannelToggleCell` (line ~151). Leave the webhook cell's hint intact.
- Update the section description to note email delivers when a transport is configured; when `!transportEnabled`, render a subtle inline note under the matrix: *"Configure an email transport above to receive these."* (read enabled state from `useEmailTransport()`).

- [ ] **Step 3: Run FE gates**

Run: `cd frontend && npm run lint && npm run typecheck && npm run test`
Expected: all green (new panel test passes; existing notifications tests still pass — update any snapshot/assertion that referenced the email lock).

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/settings/ frontend/src/routes/_authenticated.settings.notifications.tsx
git commit -m "feat(fe): email transport panel + unlock the Email notification column"
```

---

## Task 14: FE topbar mail icon — email delivery-log dropdown

**Files:**
- Create: `frontend/src/components/shell/email-activity-menu.tsx`, `frontend/src/components/shell/__tests__/email-activity-menu.test.tsx`
- Modify: `frontend/src/components/shell/topbar.tsx`

- [ ] **Step 1: Write `EmailActivityMenu`**

Mirror `notifications-bell.tsx`'s Popover structure, using the lucide `Mail` icon and `useEmailDeliveries()`:
- `<Popover>` + `<PopoverTrigger asChild>` ghost icon `<Button aria-label="Email activity">` with `<Mail className="size-4" />`.
- `<PopoverContent align="end" className="w-[360px]">` — header "Email activity", a list of deliveries (category · subject · to-address · status badge Sent/Failed/Pending · relative time), a footer link to `/settings/notifications` ("Manage email settings").
- Empty states: no data → "No emails yet"; if `useEmailTransport().data?.enabled === false` → "Email isn't set up yet" + the settings link. (Import `useEmailTransport` for the enabled hint; it's admin-only server-side, so guard: on a 403 the hook errors — treat error as "unknown", just show "No emails yet".)

Test: renders the Mail button; open shows a delivery row from mocked `useEmailDeliveries`; empty state when no deliveries.

- [ ] **Step 2: Mount before the bell** in `topbar.tsx` (line ~127-128):

```tsx
<div className="flex items-center gap-1">
  <EmailActivityMenu />
  <NotificationsBell />
  <ThemeToggle />
```

Import `EmailActivityMenu`.

- [ ] **Step 3: Run FE gates**

Run: `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`
Expected: all green (build must pass — it regenerates the route tree).

- [ ] **Step 4: Commit**

```bash
git add frontend/src/components/shell/
git commit -m "feat(fe): topbar email delivery-log dropdown (mail icon before the bell)"
```

---

## Task 15: Docs + trackers

**Files:**
- Modify: `docs/SERVICES.md`, `docs/AUTH.md`, `services/audit/.env.example`, `CLAUDE.md`, `FE-STATUS.md`, `futures.md`, `status.md`

- [ ] **Step 1: Update docs**
- `docs/SERVICES.md` — audit: the 4 email RPCs + the `email_transport_config`/`email_deliveries` tables; management: the 4 BFF routes; auth: `ResolveUserEmails`.
- `docs/AUTH.md` — note the new audit→auth mTLS peer edge (`ResolveUserEmails`) + that auth's `MTLS_PEER_CN_ALLOWLIST` must include `registry-audit`.
- `services/audit/.env.example` — add `NOTIFY_EMAIL_KEY_HEX=` and `AUTH_GRPC_ADDR=` with comments (no secret value).
- `CLAUDE.md` §4 service catalogue — audit row gains "email notification transport (Resend/SMTP) + delivery log".

- [ ] **Step 2: Update trackers**
- `futures.md` FUT-019 — mark the **Email** channel shipped; leave **Webhook** as the remaining locked channel.
- `FE-STATUS.md` — new FE-API rows for the transport panel + the mail-icon delivery log.
- `status.md` — prepend a resolution row (branch `feat/fut-019-email-channel`, PR `#NNN` placeholder filled after merge).

- [ ] **Step 3: Commit**

```bash
git add docs/ services/audit/.env.example CLAUDE.md FE-STATUS.md futures.md status.md
git commit -m "docs(fut-019): email channel — SERVICES/AUTH/env/trackers"
```

---

## Final verification (before the review batch)

- [ ] **Backend:** `for s in audit auth management; do (cd services/$s && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./...); done` — all green (integration-gated repo tests skip without Docker; run them if Docker is up).
- [ ] **Proto:** `cd proto && buf generate && git diff --exit-code proto/gen` — no uncommitted stub drift.
- [ ] **Frontend:** `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build` — 4 gates green.
- [ ] **Deployment env reminder:** the live stack must set `NOTIFY_EMAIL_KEY_HEX` (32-byte hex) + `AUTH_GRPC_ADDR` on registry-audit, and add `registry-audit` to registry-auth's `MTLS_PEER_CN_ALLOWLIST`, before email works end-to-end. Document in the PR description (do NOT commit a real key).
- [ ] **Review batch:** per user preference, run the end-of-feature security-agent + qa-agent + code-review-agent batch (worktree-isolated, read-only). Focus areas: secret redaction (no key/password in logs, errors, or `last_error`); write-only/never-return secret handling; admin gating on config routes + per-user scoping on the delivery log; fail-closed KEK posture; the new audit→auth mTLS edge; SMTP-host SSRF decision (admin-only config, documented).

---

## Spec Coverage Check

| Spec § | Covered by |
|---|---|
| §4.1 `email_transport_config` | Task 1 |
| §4.2 `email_deliveries` (idempotency, indexes) | Task 1 (anchor = `source_scheduled_id`) |
| §5 `ResolveUserEmails` + mTLS edge | Task 5, Task 8, Task 15 (allowlist doc) |
| §6 transport iface + Resend + SMTP/Gmail | Task 4 |
| §6 dedicated `NOTIFY_EMAIL_KEY_HEX` + fail-closed | Task 2, Task 7, Task 8 |
| §6.1 email body (HTML+text, absolute link) | Task 10 (`renderMessage`) |
| §7.1 dispatcher enqueue (best-effort, idempotent) | Task 9 |
| §7.2 send loop + backoff | Task 4 (`Backoff`), Task 10 |
| §7.3 test-send (caller's own email) | Task 7, Task 11 |
| §8 failure posture (unset KEK/addr disables; bell unaffected) | Task 2, Task 7, Task 9, Task 10 |
| §9 audit RPCs (write-only/never-return secrets) | Task 6, Task 7 |
| §9.2 BFF routes (admin gate, per-user log) | Task 11 |
| §10.1 transport panel | Task 13 |
| §10.2 unlock Email column | Task 13 |
| §10.3 topbar mail icon (delivery log) | Task 14 |
| §11 error handling table | Task 7, Task 10, Task 11 |
| §12 testing | every task's test step |
| §13 files | all tasks |
