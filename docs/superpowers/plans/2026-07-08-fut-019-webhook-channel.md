# FUT-019 Webhook Notification Channel Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver scheduled notifications to a single admin-configured org webhook (one signed HTTP POST per notification for enabled categories), unlocking the third (Webhook) column of the Settings › Notifications matrix.

**Architecture:** `services/audit` owns the pipeline, mirroring the email channel. The per-minute dispatcher, after writing the bell `audit_events` row + the email fan-out, best-effort enqueues one `notification_webhook_deliveries` row when the tenant's webhook config is enabled and the notification's category is in the config's `enabled_categories`. A new send loop drains that queue with `FOR UPDATE SKIP LOCKED` + webhook-style backoff, HMAC-SHA256-signs a generic JSON envelope, and POSTs it (HTTPS-only + SSRF-blocked). Config + the sealed HMAC secret (AES-256-GCM under a dedicated `NOTIFY_WEBHOOK_KEY_HEX`) live in a new `notification_webhook_config` table. The BFF exposes admin config CRUD + test-send, and overlays the tenant's enabled categories onto the matrix read. The FE adds an admin webhook panel and unlocks the Webhook column (admin-editable, read-only for non-admins).

**Tech Stack:** Go 1.25 (pgx/v5, `crypto/hmac`, `net/http`, `libs/crypto/aes`, gRPC/buf), React + TanStack Query/Router, PostgreSQL 16 (goose migrations).

**Spec:** `docs/superpowers/specs/2026-07-08-fut-019-webhook-channel-design.md`

**Build note:** all Go build/test/vet run with `GOWORK=off` from the service dir (matches Docker/CI). Proto regen is `cd proto && buf generate` (module-scoped). Frontend gates: `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`.

**Patterns this mirrors (read before starting):**
- Email channel (the template): `services/audit/internal/{email,repository/email.go,handler/grpc_email.go}`, `services/management/internal/handler/email_transport.go`, `frontend/src/components/settings/email-transport-panel.tsx`, `frontend/src/lib/api/email-transport.ts`.
- HMAC + SSRF to copy: `services/webhook/internal/delivery/{dispatcher.go,ssrf.go}`.

**Decisions locked (from the spec):**
1. Shared org webhook — **one** POST per scheduled notification (idempotency keys on `source_scheduled_id` alone), not per-user.
2. The matrix Webhook column is admin-editable and reflects the tenant config's `enabled_categories`; the per-user `webhook_enabled` column/field is retired (ignored on write, not read).
3. Grants live in the **same** migration as the table creation (learning from the #290 email-grant bug).
4. Payload is a generic signed JSON envelope, not provider-native.
5. No new mTLS peer edge (no per-user resolution) — audit does not dial auth for this channel.

---

## File Structure

**services/audit (create):**
- `migrations/20260708120000_webhook_channel.sql` — the two tables + grants.
- `internal/webhook/transport.go` — `Poster` (SSRF-protected client), `buildPayload`, HMAC + URL-redaction helpers, `Backoff`, `MaxAttempts`, `ValidateURL`.
- `internal/webhook/transport_test.go`
- `internal/webhook/sender.go` — the send loop (`Sender`).
- `internal/webhook/sender_test.go`
- `internal/repository/webhook_notify.go` — config + deliveries queries.
- `internal/repository/webhook_notify_test.go`
- `internal/handler/grpc_notification_webhook.go` — the 3 audit RPCs.
- `internal/handler/grpc_notification_webhook_test.go`

**services/audit (modify):**
- `internal/config/config.go` — `NOTIFY_WEBHOOK_KEY_HEX` + fail-closed validation.
- `internal/scheduler/loops.go` — best-effort webhook enqueue in `dispatchOne`.
- `internal/server/server.go` — decode the webhook KEK, start the `Sender`, attach the KEK + poster to the gRPC handler, enable the runner's webhook arm.
- `.env.example` — document the new var.

**proto (modify + regen):**
- `proto/audit/v1/audit.proto` — 3 webhook RPCs + messages.

**services/management (create + modify):**
- `internal/handler/notification_webhook.go` — 3 route handlers + admin gate.
- `internal/handler/notification_webhook_test.go`
- `internal/handler/handler.go` — register 3 routes.
- `internal/handler/notification_preferences.go` — overlay tenant webhook categories onto the matrix read.

**frontend (create + modify):**
- `src/lib/api/notification-webhook.ts`
- `src/components/settings/notification-webhook-panel.tsx` (+ test)
- `src/routes/_authenticated.settings.notifications.tsx` — mount panel + unlock webhook column.
- `src/routes/__tests__/settings.notifications.channel-toggle.test.tsx` — update for admin-editable/read-only.

**docs/trackers (modify):** `docs/SERVICES.md`, `CLAUDE.md`, `FE-STATUS.md`, `futures.md`, `status.md`.

---

## Task 1: Migration — `notification_webhook_config` + `notification_webhook_deliveries` (+ grants)

**Files:**
- Create: `services/audit/migrations/20260708120000_webhook_channel.sql`

- [ ] **Step 1: Write the migration**

```sql
-- +goose Up
-- FUT-019 Webhook channel — a single admin-configured org webhook that
-- receives one signed POST per scheduled notification for enabled categories.
--
-- notification_webhook_config: one row per tenant. The HMAC secret is
-- AES-256-GCM sealed under NOTIFY_WEBHOOK_KEY_HEX (see services/audit config);
-- kek_version tracks the KEK generation for rotate-kek (RED-FU-015).
-- enabled_categories is the tenant-level per-category selection driven by the
-- Settings › Notifications matrix Webhook column.
CREATE TABLE notification_webhook_config (
    tenant_id          UUID PRIMARY KEY,
    url                TEXT,
    secret_enc         BYTEA,
    enabled            BOOLEAN     NOT NULL DEFAULT false,
    enabled_categories TEXT[]      NOT NULL DEFAULT '{}',
    kek_version        SMALLINT    NOT NULL DEFAULT 1,
    last_test_at       TIMESTAMPTZ,
    last_test_ok       BOOLEAN,
    last_test_error    TEXT,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by         UUID
);

-- notification_webhook_deliveries: per-send log AND send queue. The dispatcher
-- inserts one pending row per scheduled notification (shared endpoint, so one
-- row — NOT one per user); the send loop drains them.
CREATE TABLE notification_webhook_deliveries (
    id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id           UUID        NOT NULL,
    category            TEXT        NOT NULL,
    subject             TEXT        NOT NULL,
    body_summary        TEXT        NOT NULL,
    link                TEXT,
    source_scheduled_id UUID        NOT NULL,
    status              TEXT        NOT NULL DEFAULT 'pending',
    attempts            INT         NOT NULL DEFAULT 0,
    next_attempt_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_error          TEXT,
    response_status     INT,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    delivered_at        TIMESTAMPTZ,
    CONSTRAINT webhook_delivery_status_chk CHECK (status IN ('pending','delivered','failed')),
    CONSTRAINT webhook_delivery_idem UNIQUE (source_scheduled_id)
);

CREATE INDEX idx_webhook_deliveries_claim
    ON notification_webhook_deliveries (next_attempt_at)
    WHERE status = 'pending';

CREATE INDEX idx_webhook_deliveries_tenant
    ON notification_webhook_deliveries (tenant_id, created_at DESC);

-- Grants — the audit runtime pool authenticates as the low-privilege
-- registry_audit_app role (SEC-001, migration 20240101000002). Without these
-- GRANTs every query fails with "permission denied" surfaced as codes.Internal
-- (the exact FUT-019 email bug fixed in migration 20260707130000 / PR #290).
-- SELECT+INSERT+UPDATE covers every verb the repository issues; no DELETE path.
GRANT INSERT, SELECT, UPDATE ON notification_webhook_config     TO registry_audit_app;
GRANT INSERT, SELECT, UPDATE ON notification_webhook_deliveries TO registry_audit_app;

-- +goose Down
REVOKE INSERT, SELECT, UPDATE ON notification_webhook_deliveries FROM registry_audit_app;
REVOKE INSERT, SELECT, UPDATE ON notification_webhook_config     FROM registry_audit_app;
DROP TABLE notification_webhook_deliveries;
DROP TABLE notification_webhook_config;
```

- [ ] **Step 2: Verify the migration applies + grants land**

Run (from the compose dir with the stack up, or any PG16):
```
cd services/audit && GOWORK=off go build ./... 2>&1 | head
```
Expected: builds clean (the migration is embedded via `embed.FS`; a syntax error would fail the goose parse at boot, not the build — the repository integration test in Task 2 is the real gate).

- [ ] **Step 3: Commit**

```bash
git add services/audit/migrations/20260708120000_webhook_channel.sql
git commit -m "feat(audit): webhook channel migration (config + deliveries + grants)"
```

---

## Task 2: Repository — `webhook_notify.go`

Mirrors `services/audit/internal/repository/email.go`. Reuses the `nullString`/`nullInt` helpers already in that package.

**Files:**
- Create: `services/audit/internal/repository/webhook_notify.go`
- Test: `services/audit/internal/repository/webhook_notify_test.go`

- [ ] **Step 1: Write the failing integration test**

Mirror `email_test.go`'s testcontainers setup. Key test (add to the existing suite pattern — it stands up a real PG16 and runs migrations, so it also guards the Task 1 grants as a regression against the #290 class of bug):

```go
package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestWebhookNotify_configRoundTrip(t *testing.T) {
	pool := newTestPool(t) // same helper email_test.go uses
	r := New(pool)
	ctx := context.Background()
	tenant := uuid.New()

	// Unset config → (nil, nil).
	got, err := r.GetNotificationWebhookConfig(ctx, tenant)
	if err != nil {
		t.Fatalf("get unset: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for unconfigured tenant, got %+v", got)
	}

	// Upsert enabled config with two categories + a sealed secret.
	if err := r.UpsertNotificationWebhookConfig(ctx, NotificationWebhookConfig{
		TenantID:          tenant,
		URL:               "https://hooks.example.com/x",
		SecretEnc:         []byte("ciphertext"),
		Enabled:           true,
		EnabledCategories: []string{"scanner_freshness", "cert_expiry_warning"},
		KEKVersion:        1,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err = r.GetNotificationWebhookConfig(ctx, tenant)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.Enabled || got.URL != "https://hooks.example.com/x" || len(got.EnabledCategories) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if string(got.SecretEnc) != "ciphertext" {
		t.Fatalf("secret not persisted")
	}
}

func TestWebhookNotify_deliveryQueue(t *testing.T) {
	pool := newTestPool(t)
	r := New(pool)
	ctx := context.Background()
	tenant := uuid.New()
	sched := uuid.New()

	d := WebhookDelivery{
		TenantID: tenant, Category: "scanner_freshness",
		Subject: "s", BodySummary: "b", Link: "/x", SourceScheduledID: sched,
	}
	if err := r.EnqueueWebhookDelivery(ctx, d); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Idempotent on source_scheduled_id — second insert is a no-op.
	if err := r.EnqueueWebhookDelivery(ctx, d); err != nil {
		t.Fatalf("re-enqueue: %v", err)
	}

	claimed, err := r.ClaimPendingWebhookDeliveries(ctx, nowUTC(), 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 claimed (idempotent), got %d", len(claimed))
	}
	if err := r.MarkWebhookDelivered(ctx, claimed[0].ID, 200); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
}
```

(Use the same `newTestPool` / `nowUTC` helpers the email repository test uses. If they aren't exported test helpers, copy email_test.go's setup verbatim.)

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/repository/ -run TestWebhookNotify -count=1`
Expected: FAIL — `undefined: NotificationWebhookConfig` etc.

- [ ] **Step 3: Write the repository**

```go
package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// FUT-019 Webhook channel persistence. Two tables back the feature
// (migration 20260708120000_webhook_channel):
//
//   notification_webhook_config      — one row per tenant. The admin org
//                                      webhook URL + sealed HMAC secret +
//                                      the tenant-level enabled_categories set.
//                                      secret_enc is BYTEA ciphertext — this
//                                      layer is SQL-only; the handler seals/
//                                      opens it with the webhook KEK.
//   notification_webhook_deliveries  — per-send log AND send queue. The
//                                      dispatcher enqueues one pending row per
//                                      scheduled notification; the send loop
//                                      claims (lease via next_attempt_at),
//                                      posts, then marks delivered or failed.

// NotificationWebhookConfig mirrors a notification_webhook_config row. SecretEnc
// holds ciphertext (BYTEA); the handler seals/opens it with the webhook KEK, so
// nil means "no secret stored yet".
type NotificationWebhookConfig struct {
	TenantID          uuid.UUID
	URL               string
	SecretEnc         []byte
	Enabled           bool
	EnabledCategories []string
	KEKVersion        int16
	LastTestAt        *time.Time
	LastTestOK        *bool
	LastTestError     string
	UpdatedAt         time.Time
	UpdatedBy         *uuid.UUID
}

// WebhookDelivery mirrors a notification_webhook_deliveries row. It is both the
// audit log of a send and the unit of work drained by the send loop.
type WebhookDelivery struct {
	ID                uuid.UUID
	TenantID          uuid.UUID
	Category          string
	Subject           string
	BodySummary       string
	Link              string
	SourceScheduledID uuid.UUID
	Status            string
	Attempts          int
	NextAttemptAt     time.Time
	LastError         string
	ResponseStatus    int
	CreatedAt         time.Time
	DeliveredAt       *time.Time
}

// ── notification_webhook_config ──────────────────────────────────────

// GetNotificationWebhookConfig returns the webhook config for a tenant, or
// (nil, nil) when the tenant has never saved one.
func (r *Repository) GetNotificationWebhookConfig(
	ctx context.Context,
	tenantID uuid.UUID,
) (*NotificationWebhookConfig, error) {
	const q = `
		SELECT tenant_id, COALESCE(url, ''), secret_enc, enabled,
		       enabled_categories, kek_version,
		       last_test_at, last_test_ok, COALESCE(last_test_error, ''),
		       updated_at, updated_by
		  FROM notification_webhook_config
		 WHERE tenant_id = $1`
	var c NotificationWebhookConfig
	err := r.pool.QueryRow(ctx, q, tenantID).Scan(
		&c.TenantID, &c.URL, &c.SecretEnc, &c.Enabled,
		&c.EnabledCategories, &c.KEKVersion,
		&c.LastTestAt, &c.LastTestOK, &c.LastTestError,
		&c.UpdatedAt, &c.UpdatedBy,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("get notification webhook config: %w", err)
	}
	return &c, nil
}

// UpsertNotificationWebhookConfig inserts-or-replaces the whole config row.
// Pass the previously-stored ciphertext through unchanged when not rotating the
// secret. updated_at is stamped to now() on both paths.
func (r *Repository) UpsertNotificationWebhookConfig(
	ctx context.Context,
	cfg NotificationWebhookConfig,
) error {
	// enabled_categories is NOT NULL DEFAULT '{}'; a nil slice must land as an
	// empty array, not NULL, so normalise here.
	cats := cfg.EnabledCategories
	if cats == nil {
		cats = []string{}
	}
	const q = `
		INSERT INTO notification_webhook_config
			(tenant_id, url, secret_enc, enabled, enabled_categories,
			 kek_version, last_test_at, last_test_ok, last_test_error,
			 updated_by, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (tenant_id) DO UPDATE SET
			url                = EXCLUDED.url,
			secret_enc         = EXCLUDED.secret_enc,
			enabled            = EXCLUDED.enabled,
			enabled_categories = EXCLUDED.enabled_categories,
			kek_version        = EXCLUDED.kek_version,
			last_test_at       = EXCLUDED.last_test_at,
			last_test_ok       = EXCLUDED.last_test_ok,
			last_test_error    = EXCLUDED.last_test_error,
			updated_by         = EXCLUDED.updated_by,
			updated_at         = now()`
	_, err := r.pool.Exec(ctx, q,
		cfg.TenantID, nullString(cfg.URL), cfg.SecretEnc, cfg.Enabled, cats,
		cfg.KEKVersion, cfg.LastTestAt, cfg.LastTestOK, nullString(cfg.LastTestError),
		cfg.UpdatedBy,
	)
	if err != nil {
		return fmt.Errorf("upsert notification webhook config: %w", err)
	}
	return nil
}

// UpdateWebhookTestResult records the outcome of a "send test" probe without
// touching the rest of the config. A missing config row is a no-op.
func (r *Repository) UpdateWebhookTestResult(
	ctx context.Context,
	tenantID uuid.UUID,
	ok bool,
	errMsg string,
) error {
	const q = `
		UPDATE notification_webhook_config
		   SET last_test_at    = now(),
		       last_test_ok    = $2,
		       last_test_error = $3
		 WHERE tenant_id = $1`
	_, err := r.pool.Exec(ctx, q, tenantID, ok, nullString(errMsg))
	if err != nil {
		return fmt.Errorf("update webhook test result: %w", err)
	}
	return nil
}

// ── notification_webhook_deliveries ──────────────────────────────────

// EnqueueWebhookDelivery inserts one pending delivery row. The unique
// (source_scheduled_id) constraint makes the fan-out idempotent: a dispatcher
// retry after a crash lands in ON CONFLICT DO NOTHING, so the org webhook is
// never posted twice for the same scheduled notification.
func (r *Repository) EnqueueWebhookDelivery(ctx context.Context, d WebhookDelivery) error {
	const q = `
		INSERT INTO notification_webhook_deliveries
			(tenant_id, category, subject, body_summary, link, source_scheduled_id)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (source_scheduled_id) DO NOTHING`
	_, err := r.pool.Exec(ctx, q,
		d.TenantID, d.Category, d.Subject, d.BodySummary, nullString(d.Link), d.SourceScheduledID,
	)
	if err != nil {
		return fmt.Errorf("enqueue webhook delivery: %w", err)
	}
	return nil
}

// ClaimPendingWebhookDeliveries atomically leases up to `limit` pending rows due
// now-or-earlier, pushing next_attempt_at one minute out so a crashed sender's
// rows become claimable again after the lease expires. FOR UPDATE SKIP LOCKED
// lets workers drain without contending. Mirrors ClaimPendingEmailDeliveries.
func (r *Repository) ClaimPendingWebhookDeliveries(
	ctx context.Context,
	now time.Time,
	limit int,
) ([]*WebhookDelivery, error) {
	if limit <= 0 {
		limit = 10
	}
	const q = `
		WITH claimed AS (
			SELECT id FROM notification_webhook_deliveries
			 WHERE status = 'pending' AND next_attempt_at <= $1
			 ORDER BY next_attempt_at
			   FOR UPDATE SKIP LOCKED
			 LIMIT $2
		)
		UPDATE notification_webhook_deliveries d
		   SET next_attempt_at = now() + interval '1 minute'
		  FROM claimed
		 WHERE d.id = claimed.id
		RETURNING d.id, d.tenant_id, d.category, d.subject, d.body_summary,
		          COALESCE(d.link, ''), d.source_scheduled_id, d.status,
		          d.attempts, d.next_attempt_at, COALESCE(d.last_error, ''),
		          COALESCE(d.response_status, 0), d.created_at, d.delivered_at`
	rows, err := r.pool.Query(ctx, q, now, limit)
	if err != nil {
		return nil, fmt.Errorf("claim pending webhook deliveries: %w", err)
	}
	defer rows.Close()
	out := make([]*WebhookDelivery, 0, limit)
	for rows.Next() {
		var d WebhookDelivery
		if err := rows.Scan(
			&d.ID, &d.TenantID, &d.Category, &d.Subject, &d.BodySummary,
			&d.Link, &d.SourceScheduledID, &d.Status,
			&d.Attempts, &d.NextAttemptAt, &d.LastError,
			&d.ResponseStatus, &d.CreatedAt, &d.DeliveredAt,
		); err != nil {
			return nil, fmt.Errorf("scan claimed webhook delivery: %w", err)
		}
		out = append(out, &d)
	}
	return out, rows.Err()
}

// MarkWebhookDelivered flips a delivery to delivered, stamps delivered_at,
// records the HTTP status, and clears any prior error.
func (r *Repository) MarkWebhookDelivered(ctx context.Context, id uuid.UUID, responseStatus int) error {
	const q = `
		UPDATE notification_webhook_deliveries
		   SET status          = 'delivered',
		       delivered_at    = now(),
		       response_status = $2,
		       last_error      = NULL
		 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, responseStatus)
	if err != nil {
		return fmt.Errorf("mark webhook delivered: %w", err)
	}
	return nil
}

// MarkWebhookFailed records a send failure. The caller owns the retry policy:
// it passes the bumped attempt count, the computed next_attempt_at (backoff),
// `failed` (true once the budget is exhausted → row parked at 'failed'), the
// last HTTP status (0 when no response), and the redacted error.
func (r *Repository) MarkWebhookFailed(
	ctx context.Context,
	id uuid.UUID,
	attempts int,
	nextAttempt time.Time,
	failed bool,
	responseStatus int,
	errMsg string,
) error {
	const q = `
		UPDATE notification_webhook_deliveries
		   SET attempts        = $2,
		       next_attempt_at = $3,
		       status          = CASE WHEN $4 THEN 'failed' ELSE 'pending' END,
		       response_status = $5,
		       last_error      = $6
		 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id, attempts, nextAttempt, failed, nullResponseStatus(responseStatus), nullString(errMsg))
	if err != nil {
		return fmt.Errorf("mark webhook failed: %w", err)
	}
	return nil
}

// nullResponseStatus maps 0 → nil (SQL NULL) for the nullable response_status
// column so "no HTTP response received" stays distinct from a real status 0.
func nullResponseStatus(code int) any {
	if code == 0 {
		return nil
	}
	return code
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/audit && GOWORK=off go test ./internal/repository/ -run TestWebhookNotify -count=1`
Expected: PASS (requires Docker for testcontainers, same as the email repo test).

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/repository/webhook_notify.go services/audit/internal/repository/webhook_notify_test.go
git commit -m "feat(audit): webhook channel repository (config + delivery queue)"
```

---

## Task 3: Transport — `internal/webhook/transport.go`

Copies the HMAC + SSRF machinery from `services/webhook/internal/delivery/{dispatcher.go,ssrf.go}` into a new audit-local package (avoids a cross-service import; a shared `libs/webhook` extraction is a deferred follow-up per the spec).

**Files:**
- Create: `services/audit/internal/webhook/transport.go`
- Test: `services/audit/internal/webhook/transport_test.go`

- [ ] **Step 1: Write the failing test**

```go
package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPoster_signsAndPosts(t *testing.T) {
	var gotSig, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Registry-Signature")
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := newPosterForTest() // SSRF check bypassed for httptest loopback — see note
	secret := []byte("shh")
	body := []byte(`{"event":"notification"}`)
	code, err := p.Post(context.Background(), srv.URL, body, secret)
	if err != nil {
		t.Fatal(err)
	}
	if code != 200 {
		t.Fatalf("code = %d", code)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Fatalf("sig = %q want %q", gotSig, want)
	}
	if gotCT != "application/json" || gotBody != string(body) {
		t.Fatalf("ct=%q body=%q", gotCT, gotBody)
	}
}

func TestValidateURL_rejectsHTTPAndPrivate(t *testing.T) {
	if err := ValidateURL("http://example.com/x"); err == nil {
		t.Fatal("expected HTTPS rejection")
	}
	if err := ValidateURL("https://127.0.0.1/x"); err == nil {
		t.Fatal("expected private-IP rejection")
	}
}

func TestBuildPayload_shape(t *testing.T) {
	raw := buildPayload("scanner_freshness", "Subject", "Summary", "/repos/x", "tid", time.Unix(0, 0).UTC())
	for _, want := range []string{`"event":"notification"`, `"category":"scanner_freshness"`, `"subject":"Subject"`, `"tenant_id":"tid"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("payload missing %q: %s", want, raw)
		}
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

Note on `newPosterForTest`: the production `Poster` uses an SSRF-blocking dialer that rejects loopback, so a plain `NewPoster()` can't hit `httptest` (127.0.0.1). Add a test-only constructor in `transport.go` that builds a `Poster` with a stock `http.Client` (no SSRF dialer) so the signing/POST path is unit-testable; the SSRF behaviour is covered separately by `TestValidateURL_rejectsHTTPAndPrivate` against the pure `ValidateURL`/`isPrivateIP` functions.

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/webhook/ -run 'TestPoster|TestValidateURL|TestBuildPayload|TestBackoff' -count=1`
Expected: FAIL — package `webhook` / `Poster` undefined.

- [ ] **Step 3: Write the transport**

```go
// Package webhook implements the FUT-019 webhook notification channel: a
// signed HTTP POST transport (with SSRF protection) plus a send loop that
// drains the notification_webhook_deliveries queue.
//
// The SSRF dialer + HMAC signing + URL-redaction helpers are copied from
// services/webhook/internal/delivery (dispatcher.go + ssrf.go). Extracting a
// shared libs/webhook consumed by both services is a deferred follow-up (it
// would touch registry-webhook, out of scope for this build).
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MaxAttempts is the retry ceiling; on the MaxAttempts-th failure the delivery
// flips to 'failed'.
const MaxAttempts = 5

// Backoff returns the retry delay for a given (1-based) attempt, clamped to the
// last bucket. Mirrors the email + registry-webhook schedule.
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

// maxResponseBytes caps the response body we drain from the endpoint (PENTEST-007).
const maxResponseBytes = 8 * 1024

// Poster sends one signed POST to the org webhook. Constructed with an
// SSRF-blocking dialer so a hostile/misconfigured URL can't reach internal
// services even after ValidateURL (defence-in-depth against DNS rebinding).
type Poster struct {
	client *http.Client
}

// NewPoster builds a Poster with an SSRF-protected HTTP client (blocks private
// IP ranges, validates every resolved IP then dials by literal to close the
// DNS-rebinding gap — copied from registry-webhook's dispatcher).
func NewPoster() *Poster {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupHost(ctx, host)
			if err != nil {
				return nil, err
			}
			var dialIP string
			for _, ipStr := range ips {
				ip := net.ParseIP(ipStr)
				if ip == nil {
					return nil, fmt.Errorf("SSRF protection: unparseable IP %q for %s", ipStr, host)
				}
				if isPrivateIP(ip) {
					return nil, fmt.Errorf("SSRF protection: blocked connection to private IP %s", ipStr)
				}
				if dialIP == "" {
					dialIP = ip.String()
				}
			}
			if dialIP == "" {
				return nil, fmt.Errorf("SSRF protection: no IPs resolved for %s", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(dialIP, port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
	return &Poster{client: &http.Client{Transport: transport, Timeout: 20 * time.Second}}
}

// newPosterForTest builds a Poster with a stock client (no SSRF dialer) so the
// signing/POST path is unit-testable against httptest loopback. NOT used in
// production wiring.
func newPosterForTest() *Poster {
	return &Poster{client: &http.Client{Timeout: 5 * time.Second}}
}

// Post signs body with the HMAC secret and POSTs it to targetURL. Returns the
// HTTP status (0 when the request failed before a response) and a redacted
// error. The raw URL never reaches the error (it may carry a token in the query
// / userinfo) — sanitised scheme://host/path only.
func (p *Poster) Post(ctx context.Context, targetURL string, body, secret []byte) (int, error) {
	sig := computeHMAC(body, secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Registry-Signature", "sha256="+sig)
	req.Header.Set("User-Agent", "registry-audit-notify/1.0")

	resp, err := p.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("POST to %s: %w", sanitizeURLForError(targetURL), stripURLFromError(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("endpoint returned status %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// buildPayload renders the generic signed notification envelope.
func buildPayload(category, subject, summary, link, tenantID string, ts time.Time) []byte {
	b, _ := json.Marshal(map[string]any{
		"event":     "notification",
		"category":  category,
		"subject":   subject,
		"summary":   summary,
		"link":      link,
		"tenant_id": tenantID,
		"timestamp": ts.UTC().Format(time.RFC3339),
	})
	return b
}

func computeHMAC(payload, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}

// ── SSRF + URL redaction (copied from services/webhook/internal/delivery) ──

var privateRanges []*net.IPNet

func init() {
	cidrs := []string{
		"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "127.0.0.0/8",
		"0.0.0.0/8", "169.254.0.0/16", "100.64.0.0/10",
		"::1/128", "fc00::/7", "fe80::/10",
	}
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic("invalid private CIDR: " + cidr)
		}
		privateRanges = append(privateRanges, network)
	}
}

// ValidateURL checks the destination is HTTPS and doesn't resolve to a private/
// loopback/link-local IP. Called on the config PUT path so a bad URL is
// rejected before it's ever stored.
func ValidateURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return fmt.Errorf("webhook URL must use HTTPS (got %q)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("webhook URL has no host")
	}
	addrs, err := net.LookupHost(host)
	if err != nil {
		return fmt.Errorf("DNS lookup for %q: %w", host, err)
	}
	for _, addr := range addrs {
		ip := net.ParseIP(addr)
		if ip == nil {
			continue
		}
		if isPrivateIP(ip) {
			return fmt.Errorf("webhook destination %q resolves to private IP %s — blocked (SSRF protection)", host, addr)
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	for _, network := range privateRanges {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func sanitizeURLForError(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return "[redacted url]"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func stripURLFromError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/audit && GOWORK=off go test ./internal/webhook/ -run 'TestPoster|TestValidateURL|TestBuildPayload|TestBackoff' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/webhook/transport.go services/audit/internal/webhook/transport_test.go
git commit -m "feat(audit): webhook transport (HMAC sign + SSRF-protected POST)"
```

---

## Task 4: Sender — `internal/webhook/sender.go`

Mirrors `services/audit/internal/email/sender.go`. The send loop drains the queue and posts via the `Poster`, decrypting the per-tenant HMAC secret with the webhook KEK.

**Files:**
- Create: `services/audit/internal/webhook/sender.go`
- Test: `services/audit/internal/webhook/sender_test.go`

- [ ] **Step 1: Write the failing test**

```go
package webhook

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// fakeSenderRepo implements senderRepo in memory.
type fakeSenderRepo struct {
	cfg       *repository.NotificationWebhookConfig
	claimed   []*repository.WebhookDelivery
	delivered []uuid.UUID
	failed    []uuid.UUID
}

func (f *fakeSenderRepo) GetNotificationWebhookConfig(_ context.Context, _ uuid.UUID) (*repository.NotificationWebhookConfig, error) {
	return f.cfg, nil
}
func (f *fakeSenderRepo) ClaimPendingWebhookDeliveries(_ context.Context, _ time.Time, _ int) ([]*repository.WebhookDelivery, error) {
	c := f.claimed
	f.claimed = nil // drain once
	return c, nil
}
func (f *fakeSenderRepo) MarkWebhookDelivered(_ context.Context, id uuid.UUID, _ int) error {
	f.delivered = append(f.delivered, id)
	return nil
}
func (f *fakeSenderRepo) MarkWebhookFailed(_ context.Context, id uuid.UUID, _ int, _ time.Time, _ bool, _ int, _ string) error {
	f.failed = append(f.failed, id)
	return nil
}

func key32() []byte { b := make([]byte, 32); return b }

func TestSender_runTick_delivers(t *testing.T) {
	kek := key32()
	enc, _ := aes.Encrypt([]byte("shh"), kek)
	repo := &fakeSenderRepo{
		cfg: &repository.NotificationWebhookConfig{Enabled: true, URL: "https://x", SecretEnc: enc},
		claimed: []*repository.WebhookDelivery{{
			ID: uuid.New(), TenantID: uuid.New(), Category: "scanner_freshness", Subject: "s", BodySummary: "b",
		}},
	}
	s := NewSender(repo, kek, "")
	// Inject a fake poster that always succeeds so no network is touched.
	s.post = func(_ context.Context, _ string, _, _ []byte) (int, error) { return 200, nil }
	s.runTick(context.Background())
	if len(repo.delivered) != 1 {
		t.Fatalf("expected 1 delivered, got %d", len(repo.delivered))
	}
}

func TestSender_runTick_idlesWithoutKEK(t *testing.T) {
	repo := &fakeSenderRepo{claimed: []*repository.WebhookDelivery{{ID: uuid.New()}}}
	s := NewSender(repo, nil, "") // no KEK → disabled
	s.runTick(context.Background())
	if len(repo.delivered) != 0 || repo.claimed == nil {
		t.Fatal("expected no work when KEK unset")
	}
}

func TestSender_runTick_disabledConfigAgesToTerminal(t *testing.T) {
	repo := &fakeSenderRepo{
		cfg:     &repository.NotificationWebhookConfig{Enabled: false},
		claimed: []*repository.WebhookDelivery{{ID: uuid.New(), Attempts: MaxAttempts - 1}},
	}
	s := NewSender(repo, key32(), "")
	s.runTick(context.Background())
	if len(repo.failed) != 1 {
		t.Fatalf("expected disabled-config row to be failed, got %d", len(repo.failed))
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/webhook/ -run TestSender -count=1`
Expected: FAIL — `NewSender` undefined.

- [ ] **Step 3: Write the sender**

```go
package webhook

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// senderRepo is the subset of repository.Repository the send loop depends on.
// Declared as an interface here so the loop is unit-testable without Postgres.
type senderRepo interface {
	GetNotificationWebhookConfig(ctx context.Context, tenantID uuid.UUID) (*repository.NotificationWebhookConfig, error)
	ClaimPendingWebhookDeliveries(ctx context.Context, now time.Time, limit int) ([]*repository.WebhookDelivery, error)
	MarkWebhookDelivered(ctx context.Context, id uuid.UUID, responseStatus int) error
	MarkWebhookFailed(ctx context.Context, id uuid.UUID, attempts int, next time.Time, failed bool, responseStatus int, errMsg string) error
}

// Sender drains the notification_webhook_deliveries queue and posts each row to
// the tenant's org webhook. Idle (no DB work) when the KEK is unset.
type Sender struct {
	repo         senderRepo
	kek          []byte
	interval     time.Duration
	batch        int
	platformHost string // absolute-link base for the payload's link field
	poster       *Poster
	// post is the injection point for the POST call (defaults to poster.Post;
	// overridden in tests to avoid the network).
	post func(ctx context.Context, url string, body, secret []byte) (int, error)
}

// NewSender returns a Sender ready to Start. interval ~20s + batch 20 mirror the
// email sender cadence.
func NewSender(repo senderRepo, kek []byte, platformHost string) *Sender {
	p := NewPoster()
	s := &Sender{
		repo: repo, kek: kek, interval: 20 * time.Second, batch: 20,
		platformHost: platformHost, poster: p,
	}
	s.post = p.Post
	return s
}

// Start runs the send loop until ctx is cancelled. Best-effort: per-tick errors
// log and continue.
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

// runTick claims a batch of due deliveries and posts each. Idles when the KEK is
// unset so the whole channel disables cleanly.
func (s *Sender) runTick(ctx context.Context) {
	if len(s.kek) == 0 {
		return // webhook channel disabled
	}
	now := time.Now().UTC()
	rows, err := s.repo.ClaimPendingWebhookDeliveries(ctx, now, s.batch)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 webhook sender: claim failed", "err", err)
		return
	}
	// One tenant in single mode; config loaded lazily + cached per tick.
	type resolved struct {
		url    string
		secret []byte
		ok     bool // config present + enabled + secret decrypted
	}
	cache := map[uuid.UUID]resolved{}
	for _, d := range rows {
		r, seen := cache[d.TenantID]
		if !seen {
			r = s.resolve(ctx, d.TenantID)
			cache[d.TenantID] = r
		}
		if !r.ok {
			// Config disabled/absent/secret-less. The row was leased
			// (next_attempt_at = now()+1min); a bare continue would re-claim it
			// forever. Age it toward terminal state via fail().
			s.fail(ctx, d, 0, errors.New("webhook transport not enabled or not configured"))
			continue
		}
		body := buildPayload(d.Category, d.Subject, d.BodySummary, s.link(d.Link), d.TenantID.String(), now)
		code, err := s.post(ctx, r.url, body, r.secret)
		if err != nil {
			s.fail(ctx, d, code, err)
			continue
		}
		if err := s.repo.MarkWebhookDelivered(ctx, d.ID, code); err != nil {
			slog.ErrorContext(ctx, "FUT-019 webhook sender: mark delivered failed", "err", err, "id", d.ID)
		}
	}
}

// resolve loads + decrypts a tenant's webhook config. ok=false (no error
// surfaced) when the tenant has no config, it's disabled, or the secret is
// missing/undecryptable — the caller ages the row toward terminal state.
func (s *Sender) resolve(ctx context.Context, tenantID uuid.UUID) (r struct {
	url    string
	secret []byte
	ok     bool
}) {
	cfg, err := s.repo.GetNotificationWebhookConfig(ctx, tenantID)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 webhook sender: load config failed", "err", err)
		return
	}
	if cfg == nil || !cfg.Enabled || cfg.URL == "" || len(cfg.SecretEnc) == 0 {
		return
	}
	secret, err := aes.Decrypt(cfg.SecretEnc, s.kek)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 webhook sender: decrypt secret failed", "err", err)
		return
	}
	r.url, r.secret, r.ok = cfg.URL, secret, true
	return
}

// fail records a send failure, computing the next attempt via Backoff and
// flipping the row to 'failed' once the retry budget is exhausted.
func (s *Sender) fail(ctx context.Context, d *repository.WebhookDelivery, code int, sendErr error) {
	attempts := d.Attempts + 1
	failed := attempts >= MaxAttempts
	next := time.Now().UTC().Add(Backoff(attempts))
	if err := s.repo.MarkWebhookFailed(ctx, d.ID, attempts, next, failed, code, sendErr.Error()); err != nil {
		slog.ErrorContext(ctx, "FUT-019 webhook sender: mark failed errored", "err", err, "id", d.ID)
	}
}

// link builds the absolute payload link when a platform host is configured,
// else the raw (possibly relative) link.
func (s *Sender) link(raw string) string {
	if s.platformHost != "" && raw != "" {
		return s.platformHost + raw
	}
	return raw
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/audit && GOWORK=off go test ./internal/webhook/ -count=1`
Expected: PASS (all transport + sender tests).

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/webhook/sender.go services/audit/internal/webhook/sender_test.go
git commit -m "feat(audit): webhook send loop (drain queue + retry backoff)"
```

---

## Task 5: Config — `NOTIFY_WEBHOOK_KEY_HEX`

**Files:**
- Modify: `services/audit/internal/config/config.go`
- Test: `services/audit/internal/config/config_test.go`

- [ ] **Step 1: Add the failing test** (append to the existing `config_test.go`)

```go
func TestValidate_webhookKEK(t *testing.T) {
	base := func() *Config {
		return &Config{
			BaseConfig: loader.BaseConfig{MTLSCACertPath: "ca", MTLSCertPath: "c", MTLSKeyPath: "k"},
			DBDSN:      "postgres://x", RabbitMQURL: "amqp://x",
		}
	}
	// Unset → OK (channel disabled).
	if err := validate(base()); err != nil {
		t.Fatalf("unset should pass: %v", err)
	}
	// Wrong length → fail closed.
	c := base()
	c.NotifyWebhookKeyHex = "abcd"
	if err := validate(c); err == nil {
		t.Fatal("short webhook KEK should fail")
	}
	// Valid 64-hex → OK.
	c = base()
	c.NotifyWebhookKeyHex = strings.Repeat("ab", 32)
	if err := validate(c); err != nil {
		t.Fatalf("valid webhook KEK should pass: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/config/ -run TestValidate_webhookKEK -count=1`
Expected: FAIL — `NotifyWebhookKeyHex` undefined.

- [ ] **Step 3: Add the field + validation**

In the `Config` struct, after `NotifyEmailKeyHex`:
```go
	// NotifyWebhookKeyHex (FUT-019 webhook channel) is the 64-char hex
	// AES-256-GCM key sealing notification_webhook_config.secret_enc (the org
	// webhook HMAC secret). Empty disables the webhook channel: the config PUT
	// rejects a secret with FailedPrecondition and the send loop idles.
	// Set-but-not-32-bytes fails closed at startup.
	NotifyWebhookKeyHex string `mapstructure:"NOTIFY_WEBHOOK_KEY_HEX"`
```

In `validate`, after the `NotifyEmailKeyHex` block:
```go
	// FUT-019 webhook channel — same fail-closed posture as the email KEK.
	if cfg.NotifyWebhookKeyHex != "" {
		if _, err := hex.DecodeString(cfg.NotifyWebhookKeyHex); err != nil {
			return fmt.Errorf("NOTIFY_WEBHOOK_KEY_HEX: not valid hex: %w", err)
		}
		if len(cfg.NotifyWebhookKeyHex) != 64 {
			return fmt.Errorf("NOTIFY_WEBHOOK_KEY_HEX: expected 64 hex chars (32 bytes), got %d", len(cfg.NotifyWebhookKeyHex))
		}
	}
```

(If `config_test.go` doesn't already import `strings`/`loader`, add them.)

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/audit && GOWORK=off go test ./internal/config/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/config/config.go services/audit/internal/config/config_test.go
git commit -m "feat(audit): NOTIFY_WEBHOOK_KEY_HEX config (fail-closed)"
```

---

## Task 6: Proto — webhook RPCs + messages

**Files:**
- Modify: `proto/audit/v1/audit.proto`
- Regen: `proto/gen/go/audit/v1/*` (committed)

- [ ] **Step 1: Add the RPCs to the `AuditService` service block**

```proto
  // FUT-019 webhook notification channel (admin-configured org webhook).
  rpc GetNotificationWebhookConfig(GetNotificationWebhookConfigRequest) returns (NotificationWebhookConfig);
  rpc PutNotificationWebhookConfig(PutNotificationWebhookConfigRequest) returns (NotificationWebhookConfig);
  rpc SendTestNotificationWebhook(SendTestNotificationWebhookRequest) returns (SendTestNotificationWebhookResponse);
```

- [ ] **Step 2: Add the messages** (use the next free field-number sequencing convention already in the file; do not reuse any number)

```proto
// FUT-019 webhook notification channel.
message NotificationWebhookConfig {
  string url = 1;
  bool enabled = 2;
  // has_secret masks the HMAC secret — the raw value is never returned.
  bool has_secret = 3;
  repeated string enabled_categories = 4;
  google.protobuf.Timestamp last_test_at = 5;
  bool last_test_ok = 6;
  string last_test_error = 7;
}

message GetNotificationWebhookConfigRequest {
  string tenant_id = 1;
}

message PutNotificationWebhookConfigRequest {
  string tenant_id = 1;
  string updated_by = 2;
  string url = 3;
  bool enabled = 4;
  // secret is write-only: empty = keep the stored ciphertext, non-empty = re-seal.
  string secret = 5;
  repeated string enabled_categories = 6;
}

message SendTestNotificationWebhookRequest {
  string tenant_id = 1;
}

message SendTestNotificationWebhookResponse {
  bool ok = 1;
  string error = 2;
}
```

- [ ] **Step 3: Regenerate the stubs**

Run: `cd proto && buf generate`
Expected: `proto/gen/go/audit/v1/audit.pb.go` + `audit_grpc.pb.go` updated with the new types + `AuditServiceServer`/`AuditServiceClient` methods.

- [ ] **Step 4: Verify build**

Run: `cd services/audit && GOWORK=off go build ./... 2>&1 | head`
Expected: FAILS to build `internal/handler` (the `GRPCHandler` doesn't yet implement the 3 new `AuditServiceServer` methods) — that's expected; Task 7 implements them. Confirm the proto package itself builds:
`cd proto && GOWORK=off go build ./... 2>&1 | head` → clean.

- [ ] **Step 5: Commit**

```bash
git add proto/audit/v1/audit.proto proto/gen/go/audit/v1/
git commit -m "feat(proto): audit webhook notification RPCs + messages"
```

---

## Task 7: gRPC handler — `grpc_notification_webhook.go`

Mirrors `grpc_email.go`. Reuses the handler's existing `sealSecret`/`openSecret` helpers (already present for the email + export secrets) and adds `webhookKEK` + `webhookPoster` fields.

**Files:**
- Create: `services/audit/internal/handler/grpc_notification_webhook.go`
- Test: `services/audit/internal/handler/grpc_notification_webhook_test.go`
- Modify: `services/audit/internal/handler/grpc.go` (add the two fields to `GRPCHandler` if not shared) — see Step 3.

- [ ] **Step 1: Write the failing test**

```go
package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
)

func TestGetNotificationWebhookConfig_masksSecret(t *testing.T) {
	repo := newFakeAuditRepo() // same fake grpc_email_test.go builds
	tenant := uuid.New()
	repo.webhookCfg = &repository.NotificationWebhookConfig{
		TenantID: tenant, URL: "https://x", Enabled: true,
		SecretEnc: []byte("ct"), EnabledCategories: []string{"scanner_freshness"},
	}
	h := NewGRPC(repo)
	got, err := h.GetNotificationWebhookConfig(context.Background(), &auditv1.GetNotificationWebhookConfigRequest{TenantId: tenant.String()})
	if err != nil {
		t.Fatal(err)
	}
	if !got.GetHasSecret() {
		t.Fatal("has_secret should be true")
	}
	if got.GetUrl() != "https://x" || len(got.GetEnabledCategories()) != 1 {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestPutNotificationWebhookConfig_rejectsSecretWithoutKEK(t *testing.T) {
	h := NewGRPC(newFakeAuditRepo()) // no WithWebhookKEK → KEK unset
	_, err := h.PutNotificationWebhookConfig(context.Background(), &auditv1.PutNotificationWebhookConfigRequest{
		TenantId: uuid.NewString(), Url: "https://x", Secret: "shh",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("want FailedPrecondition, got %v", err)
	}
}

func TestPutNotificationWebhookConfig_rejectsBadURL(t *testing.T) {
	h := NewGRPC(newFakeAuditRepo()).WithWebhookKEK(make([]byte, 32))
	_, err := h.PutNotificationWebhookConfig(context.Background(), &auditv1.PutNotificationWebhookConfigRequest{
		TenantId: uuid.NewString(), Url: "http://insecure", Enabled: true,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("want InvalidArgument for non-HTTPS URL, got %v", err)
	}
}

func TestSendTestNotificationWebhook_disabledInline(t *testing.T) {
	h := NewGRPC(newFakeAuditRepo()).WithWebhookKEK(make([]byte, 32))
	resp, err := h.SendTestNotificationWebhook(context.Background(), &auditv1.SendTestNotificationWebhookRequest{TenantId: uuid.NewString()})
	if err != nil {
		t.Fatalf("disabled transport should be inline, not RPC error: %v", err)
	}
	if resp.GetOk() {
		t.Fatal("expected ok=false for unconfigured webhook")
	}
}
```

Extend the shared `fakeAuditRepo` (in `grpc_email_test.go` / `testutil`) with `webhookCfg` + the webhook repo methods (`GetNotificationWebhookConfig`, `UpsertNotificationWebhookConfig`, `UpdateWebhookTestResult`) so it satisfies the interface the handler depends on.

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/handler/ -run NotificationWebhook -count=1`
Expected: FAIL — methods undefined.

- [ ] **Step 3: Write the handler**

First, extend `GRPCHandler` (in `grpc.go`, alongside the `emailKEK`/`newEmailTransport` fields) and its `auditRepo` interface:
```go
// add to the GRPCHandler struct:
	webhookKEK    []byte
	webhookPoster *webhook.Poster

// add to the auditRepo interface used by GRPCHandler:
	GetNotificationWebhookConfig(ctx context.Context, tenantID uuid.UUID) (*repository.NotificationWebhookConfig, error)
	UpsertNotificationWebhookConfig(ctx context.Context, cfg repository.NotificationWebhookConfig) error
	UpdateWebhookTestResult(ctx context.Context, tenantID uuid.UUID, ok bool, errMsg string) error
```

Then `grpc_notification_webhook.go`:
```go
package handler

import (
	"context"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
	"github.com/steveokay/oci-janus/services/audit/internal/webhook"
)

// WithWebhookKEK wires the AES-256-GCM key sealing the org webhook HMAC secret.
// When unset, a Put carrying a secret fails closed with FailedPrecondition.
func (h *GRPCHandler) WithWebhookKEK(key []byte) *GRPCHandler {
	h.webhookKEK = key
	return h
}

// WithWebhookPoster injects the poster used by SendTestNotificationWebhook.
// Defaults to webhook.NewPoster() at call time when unset.
func (h *GRPCHandler) WithWebhookPoster(p *webhook.Poster) *GRPCHandler {
	h.webhookPoster = p
	return h
}

// GetNotificationWebhookConfig returns the tenant's org webhook config with the
// HMAC secret masked to has_secret. A tenant that never saved a config gets
// form defaults (empty, disabled) rather than a NotFound.
func (h *GRPCHandler) GetNotificationWebhookConfig(ctx context.Context, req *auditv1.GetNotificationWebhookConfigRequest) (*auditv1.NotificationWebhookConfig, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	row, err := h.repo.GetNotificationWebhookConfig(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get webhook config: %v", err)
	}
	return webhookConfigToProto(row), nil
}

// PutNotificationWebhookConfig upserts the tenant's org webhook config. The
// secret is sealed with sealSecret (empty keeps the stored ciphertext); the URL
// is validated (HTTPS + non-private) before persistence. The response re-runs
// the Get mapping so the secret stays masked.
func (h *GRPCHandler) PutNotificationWebhookConfig(ctx context.Context, req *auditv1.PutNotificationWebhookConfigRequest) (*auditv1.NotificationWebhookConfig, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	// Validate the URL only when a URL is present (a config may be saved
	// disabled with categories pre-selected before the URL is known).
	if req.GetUrl() != "" {
		if err := webhook.ValidateURL(req.GetUrl()); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid webhook url: %v", err)
		}
	}
	existing, err := h.repo.GetNotificationWebhookConfig(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load existing webhook config: %v", err)
	}
	var existingSecret []byte
	if existing != nil {
		existingSecret = existing.SecretEnc
	}
	secretCT, err := sealSecret(h.webhookKEK, existingSecret, req.GetSecret())
	if err != nil {
		return nil, err // FailedPrecondition when KEK unset + secret supplied
	}
	cfg := repository.NotificationWebhookConfig{
		TenantID:          tenantID,
		URL:               req.GetUrl(),
		SecretEnc:         secretCT,
		Enabled:           req.GetEnabled(),
		EnabledCategories: req.GetEnabledCategories(),
		KEKVersion:        1,
	}
	if ub := req.GetUpdatedBy(); ub != "" {
		if u, perr := uuid.Parse(ub); perr == nil {
			cfg.UpdatedBy = &u
		}
	}
	if err := h.repo.UpsertNotificationWebhookConfig(ctx, cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "upsert webhook config: %v", err)
	}
	row, err := h.repo.GetNotificationWebhookConfig(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reload webhook config: %v", err)
	}
	return webhookConfigToProto(row), nil
}

// SendTestNotificationWebhook decrypts the secret, builds a transport, and posts
// one canned test payload to the configured URL. A disabled/unconfigured
// transport is reported inline (Ok=false) rather than as an RPC error. The
// outcome (redacted error) is recorded via UpdateWebhookTestResult.
func (h *GRPCHandler) SendTestNotificationWebhook(ctx context.Context, req *auditv1.SendTestNotificationWebhookRequest) (*auditv1.SendTestNotificationWebhookResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	row, err := h.repo.GetNotificationWebhookConfig(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load webhook config: %v", err)
	}
	if row == nil || !row.Enabled || row.URL == "" || len(row.SecretEnc) == 0 {
		return &auditv1.SendTestNotificationWebhookResponse{Ok: false, Error: "webhook transport not enabled or missing url/secret"}, nil
	}
	secret, err := openSecret(h.webhookKEK, row.SecretEnc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt webhook secret: %v", err)
	}
	poster := h.webhookPoster
	if poster == nil {
		poster = webhook.NewPoster()
	}
	body := webhook.TestPayload(tenantID.String())
	code, sendErr := poster.Post(ctx, row.URL, body, []byte(secret))
	ok := sendErr == nil
	var errStr string
	if sendErr != nil {
		errStr = truncateString(sendErr.Error(), maxLastErrorLen)
	}
	_ = h.repo.UpdateWebhookTestResult(ctx, tenantID, ok, errStr)
	_ = code // response_status not surfaced on the test path
	return &auditv1.SendTestNotificationWebhookResponse{Ok: ok, Error: errStr}, nil
}

// webhookConfigToProto maps a repository config row onto the wire proto, masking
// the HMAC secret to has_secret. A nil row maps to disabled form defaults.
func webhookConfigToProto(c *repository.NotificationWebhookConfig) *auditv1.NotificationWebhookConfig {
	if c == nil {
		return &auditv1.NotificationWebhookConfig{EnabledCategories: []string{}}
	}
	out := &auditv1.NotificationWebhookConfig{
		Url:               c.URL,
		Enabled:           c.Enabled,
		HasSecret:         len(c.SecretEnc) > 0,
		EnabledCategories: c.EnabledCategories,
		LastTestError:     c.LastTestError,
	}
	if c.LastTestAt != nil {
		out.LastTestAt = timestamppb.New(*c.LastTestAt)
	}
	if c.LastTestOK != nil {
		out.LastTestOk = *c.LastTestOK
	}
	if out.EnabledCategories == nil {
		out.EnabledCategories = []string{}
	}
	return out
}
```

Add a `TestPayload` helper to `transport.go` (keeps the canned test body next to `buildPayload`):
```go
// TestPayload builds the canned body used by SendTestNotificationWebhook.
func TestPayload(tenantID string) []byte {
	return buildPayload("test", "OCI Janus — test notification webhook",
		"This is a test webhook from your OCI Janus registry. If you received it, your webhook transport is configured correctly.",
		"", tenantID, time.Now().UTC())
}
```

- [ ] **Step 4: Wire the handler into `server.go`** — deferred to Task 9 (keeps this task's build green via the fake). Verify:

Run: `cd services/audit && GOWORK=off go test ./internal/handler/ -run NotificationWebhook -count=1 && GOWORK=off go build ./... 2>&1 | head`
Expected: handler tests PASS; `go build ./...` still fails only in `server.go` if the new server-side setters aren't called yet — acceptable until Task 9. If the fake in the handler package makes the whole package build, `go vet ./internal/handler/` should be clean.

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/handler/grpc_notification_webhook.go services/audit/internal/handler/grpc_notification_webhook_test.go services/audit/internal/handler/grpc.go
git commit -m "feat(audit): webhook config gRPC handlers (get/put/test, secret masked)"
```

---

## Task 8: Dispatch — `enqueueWebhook` in the scheduler

**Files:**
- Modify: `services/audit/internal/scheduler/loops.go`
- Test: `services/audit/internal/scheduler/loops_test.go`

- [ ] **Step 1: Write the failing test** (mirror the existing `enqueueEmail` dispatch test)

```go
func TestDispatchOne_enqueuesWebhookForEnabledCategory(t *testing.T) {
	repo := newFakeSchedulerRepo()
	repo.webhookCfg = &repository.NotificationWebhookConfig{
		Enabled: true, URL: "https://x", SecretEnc: []byte("ct"),
		EnabledCategories: []string{"scanner_freshness"},
	}
	r := New(repo, Registry(), RunnerConfig{}).WithWebhookEnabled()
	sn := &repository.ScheduledNotification{
		ID: uuid.New(), TenantID: uuid.New(), Category: "scanner_freshness",
		Payload: mustBuildScannerPayload(t),
	}
	categoryByName := map[string]Category{"scanner_freshness": scannerFreshnessCategory{}}
	if err := r.dispatchOne(context.Background(), sn, categoryByName); err != nil {
		t.Fatal(err)
	}
	if len(repo.webhookEnqueued) != 1 {
		t.Fatalf("expected 1 webhook enqueue, got %d", len(repo.webhookEnqueued))
	}
}

func TestDispatchOne_skipsWebhookForDisabledCategory(t *testing.T) {
	repo := newFakeSchedulerRepo()
	repo.webhookCfg = &repository.NotificationWebhookConfig{
		Enabled: true, EnabledCategories: []string{"cert_expiry_warning"}, // NOT scanner_freshness
	}
	r := New(repo, Registry(), RunnerConfig{}).WithWebhookEnabled()
	sn := &repository.ScheduledNotification{ID: uuid.New(), TenantID: uuid.New(), Category: "scanner_freshness", Payload: mustBuildScannerPayload(t)}
	_ = r.dispatchOne(context.Background(), sn, map[string]Category{"scanner_freshness": scannerFreshnessCategory{}})
	if len(repo.webhookEnqueued) != 0 {
		t.Fatalf("category not in set — expected 0 enqueues, got %d", len(repo.webhookEnqueued))
	}
}
```

Extend the scheduler fake with `webhookCfg`, `webhookEnqueued`, and the two new repo methods (`GetNotificationWebhookConfig`, `EnqueueWebhookDelivery`).

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/audit && GOWORK=off go test ./internal/scheduler/ -run Webhook -count=1`
Expected: FAIL — `WithWebhookEnabled` / repo methods undefined.

- [ ] **Step 3: Implement**

Extend the `schedulerRepo` interface (after the email fan-out surface):
```go
	// FUT-019 webhook channel fan-out surface.
	GetNotificationWebhookConfig(ctx context.Context, tenantID uuid.UUID) (*repository.NotificationWebhookConfig, error)
	EnqueueWebhookDelivery(ctx context.Context, d repository.WebhookDelivery) error
```

Add a `webhookEnabled bool` field to `Runner` + a setter:
```go
// WithWebhookEnabled turns on the FUT-019 webhook fan-out. server.go calls this
// only when NOTIFY_WEBHOOK_KEY_HEX is set (so the send loop can actually
// deliver); otherwise the runner skips webhook enqueue entirely.
func (r *Runner) WithWebhookEnabled() *Runner {
	r.webhookEnabled = true
	return r
}
```

In `dispatchOne`, after the `r.enqueueEmail(ctx, sn, rendered)` line:
```go
	// FUT-019 webhook channel — best-effort org-webhook enqueue. Never fails
	// the bell write.
	r.enqueueWebhook(ctx, sn, rendered)
```

Add the method:
```go
// enqueueWebhook enqueues one org-webhook delivery for a rendered notification
// when the tenant's webhook config is enabled and this category is in its
// enabled set. Best-effort: every failure logs + returns so a webhook problem
// never fails bell delivery. Disabled (webhookEnabled=false) short-circuits.
func (r *Runner) enqueueWebhook(
	ctx context.Context,
	sn *repository.ScheduledNotification,
	rendered RenderedNotification,
) {
	if !r.webhookEnabled {
		return
	}
	cfg, err := r.repo.GetNotificationWebhookConfig(ctx, sn.TenantID)
	if err != nil {
		slog.WarnContext(ctx, "FUT-019 webhook: load config failed", "err", err, "category", sn.Category)
		return
	}
	if cfg == nil || !cfg.Enabled {
		return
	}
	if !containsCategory(cfg.EnabledCategories, sn.Category) {
		return
	}
	// Idempotent on source_scheduled_id — a dispatcher retry never double-posts.
	if err := r.repo.EnqueueWebhookDelivery(ctx, repository.WebhookDelivery{
		TenantID:          sn.TenantID,
		Category:          sn.Category,
		Subject:           rendered.Title,
		BodySummary:       rendered.Summary,
		Link:              rendered.Link,
		SourceScheduledID: sn.ID,
	}); err != nil {
		slog.WarnContext(ctx, "FUT-019 webhook: enqueue failed", "err", err, "category", sn.Category)
	}
}

// containsCategory reports whether cat is in the tenant's enabled set.
func containsCategory(set []string, cat string) bool {
	for _, c := range set {
		if c == cat {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd services/audit && GOWORK=off go test ./internal/scheduler/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add services/audit/internal/scheduler/loops.go services/audit/internal/scheduler/loops_test.go
git commit -m "feat(audit): dispatcher enqueues org-webhook deliveries"
```

---

## Task 9: Server wiring — decode KEK, start sender, attach handler

**Files:**
- Modify: `services/audit/internal/server/server.go`

- [ ] **Step 1: Decode the webhook KEK** (after the `emailKEK` decode block, ~line 159)

```go
	// FUT-019 webhook channel — decode the org-webhook HMAC KEK once at boot
	// (same 32-byte-checking helper as the email KEK). Empty idles the webhook
	// send loop and disables the runner's webhook fan-out.
	var webhookKEK []byte
	if keyHex := cfg.NotifyWebhookKeyHex; keyHex != "" {
		k, err := decodeHexKey(keyHex)
		if err != nil {
			return fmt.Errorf("NOTIFY_WEBHOOK_KEY_HEX: %w", err)
		}
		webhookKEK = k
	}
```

- [ ] **Step 2: Enable the runner's webhook arm** (in the runner goroutine, alongside `WithEmailResolver`)

```go
		runner := scheduler.New(repo, scheduler.Registry(), scheduler.RunnerConfig{})
		if authClient != nil {
			runner.WithEmailResolver(authEmailResolver{c: authClient})
		}
		// FUT-019 webhook channel — enable org-webhook fan-out only when the
		// webhook KEK is present (so the send loop can actually deliver).
		if len(webhookKEK) > 0 {
			runner.WithWebhookEnabled()
		}
		runner.Start(ctx)
```

- [ ] **Step 3: Start the webhook send loop** (after the email sender goroutine, ~line 209)

```go
	// FUT-019 webhook channel — send loop. Drains notification_webhook_deliveries
	// and POSTs to the per-tenant org webhook. Idles when webhookKEK is empty.
	go func() {
		slog.Info("FUT-019: starting webhook sender loop")
		webhook.NewSender(repo, webhookKEK, cfg.PlatformHost).Start(ctx)
		slog.Info("FUT-019: webhook sender stopped")
	}()
```

Add the import: `"github.com/steveokay/oci-janus/services/audit/internal/webhook"`.

- [ ] **Step 4: Attach the KEK to the gRPC handler** (in the `handler.NewGRPC(repo)...` builder chain, ~line 295)

```go
		WithEmailKEK(emailKEK).
		WithEmailTransport(email.NewTransport).
		// FUT-019 webhook channel — attach the org-webhook HMAC KEK so the
		// Get/Put/SendTest handlers can seal/unseal the secret + post a test.
		WithWebhookKEK(webhookKEK)
```

- [ ] **Step 5: Verify build + full audit test**

Run: `cd services/audit && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./... -count=1`
Expected: builds clean; all tests pass (integration tests need Docker).

- [ ] **Step 6: Commit**

```bash
git add services/audit/internal/server/server.go
git commit -m "feat(audit): wire webhook KEK + send loop + runner arm"
```

---

## Task 10: BFF — routes + matrix overlay

**Files:**
- Create: `services/management/internal/handler/notification_webhook.go`
- Test: `services/management/internal/handler/notification_webhook_test.go`
- Modify: `services/management/internal/handler/handler.go` (register 3 routes)
- Modify: `services/management/internal/handler/notification_preferences.go` (overlay tenant webhook categories onto the matrix read)

- [ ] **Step 1: Write the failing handler test** (mirror `email_transport_test.go` — admin gate, SA deny, 409 mapping)

```go
func TestGetNotificationWebhook_requiresAdmin(t *testing.T) {
	h, audit := newTestHandler(t)      // same helpers email_transport_test.go uses
	req := newAuthedRequest(t, "GET", "/api/v1/notifications/webhook-config", nil, nonAdminClaims())
	rec := httptest.NewRecorder()
	h.handleGetNotificationWebhook(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin should get 403, got %d", rec.Code)
	}
	_ = audit
}

func TestPutNotificationWebhook_deniesServiceAccount(t *testing.T) {
	h, _ := newTestHandler(t)
	req := newAuthedRequest(t, "PUT", "/api/v1/notifications/webhook-config", strings.NewReader(`{"url":"https://x"}`), adminServiceAccountClaims())
	rec := httptest.NewRecorder()
	h.handlePutNotificationWebhook(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("SA bearer should get 403, got %d", rec.Code)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -run NotificationWebhook -count=1`
Expected: FAIL — handlers undefined.

- [ ] **Step 3: Write the BFF handler**

```go
// Package handler — notification_webhook.go
//
// FUT-019 webhook notification channel (BFF surface). Three admin routes front
// the auditv1 webhook config RPCs:
//
//	GET  /api/v1/notifications/webhook-config       (admin) → GetNotificationWebhookConfig
//	PUT  /api/v1/notifications/webhook-config       (admin) → PutNotificationWebhookConfig
//	POST /api/v1/notifications/webhook-config/test  (admin) → SendTestNotificationWebhook
//
// Auth posture mirrors the email transport routes: platform-admin primitive
// required AND service-account bearers denied (a deployment-wide config change
// must never clear the gate via an SA token, Decision #24). The HMAC secret is
// write-only — the GET returns has_secret only; an empty secret on PUT keeps the
// stored value. FailedPrecondition → 409 (reuses writeGRPCError from
// email_transport.go).
package handler

import (
	"encoding/json"
	"net/http"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/management/internal/middleware"
)

type notificationWebhookJSON struct {
	URL               string   `json:"url"`
	Enabled           bool     `json:"enabled"`
	HasSecret         bool     `json:"has_secret"`
	EnabledCategories []string `json:"enabled_categories"`
	LastTestAt        string   `json:"last_test_at,omitempty"`
	LastTestOK        bool     `json:"last_test_ok"`
	LastTestError     string   `json:"last_test_error,omitempty"`
}

type notificationWebhookPutBody struct {
	URL               string   `json:"url"`
	Enabled           bool     `json:"enabled"`
	Secret            string   `json:"secret"` // empty = keep existing
	EnabledCategories []string `json:"enabled_categories"`
}

func notificationWebhookToJSON(c *auditv1.NotificationWebhookConfig) notificationWebhookJSON {
	out := notificationWebhookJSON{
		URL:               c.GetUrl(),
		Enabled:           c.GetEnabled(),
		HasSecret:         c.GetHasSecret(),
		EnabledCategories: c.GetEnabledCategories(),
		LastTestOK:        c.GetLastTestOk(),
		LastTestError:     c.GetLastTestError(),
	}
	if out.EnabledCategories == nil {
		out.EnabledCategories = []string{}
	}
	if t := c.GetLastTestAt(); t != nil {
		out.LastTestAt = t.AsTime().UTC().Format(time.RFC3339)
	}
	return out
}

func (h *Handler) handleGetNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) { // same admin+SA gate as the email transport routes
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	resp, err := h.audit.GetNotificationWebhookConfig(r.Context(), &auditv1.GetNotificationWebhookConfigRequest{TenantId: tenantID})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationWebhookToJSON(resp))
}

func (h *Handler) handlePutNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var body notificationWebhookPutBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	userID := middleware.UserIDFromContext(r.Context())
	cats := body.EnabledCategories
	if cats == nil {
		cats = []string{}
	}
	resp, err := h.audit.PutNotificationWebhookConfig(r.Context(), &auditv1.PutNotificationWebhookConfigRequest{
		TenantId:          tenantID,
		UpdatedBy:         userID,
		Url:               body.URL,
		Enabled:           body.Enabled,
		Secret:            body.Secret,
		EnabledCategories: cats,
	})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, notificationWebhookToJSON(resp))
}

func (h *Handler) handleTestNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	if !h.requireEmailAdmin(w, r) {
		return
	}
	tenantID := middleware.TenantIDFromContext(r.Context())
	resp, err := h.audit.SendTestNotificationWebhook(r.Context(), &auditv1.SendTestNotificationWebhookRequest{TenantId: tenantID})
	if err != nil {
		writeGRPCError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": resp.GetOk(), "error": resp.GetError()})
}
```

(Add the `time` import. `requireEmailAdmin` + `writeGRPCError` are reused from `email_transport.go` — the gate is generic despite the "email" name; a rename to `requireNotifyAdmin` is optional polish, not required.)

- [ ] **Step 4: Register the routes** in `handler.go` (next to the email-transport routes, ~line 478)

```go
	mux.Handle("GET /api/v1/notifications/webhook-config",
		authMW(http.HandlerFunc(h.handleGetNotificationWebhook)))
	mux.Handle("PUT /api/v1/notifications/webhook-config",
		authMW(http.HandlerFunc(h.handlePutNotificationWebhook)))
	mux.Handle("POST /api/v1/notifications/webhook-config/test",
		authMW(http.HandlerFunc(h.handleTestNotificationWebhook)))
```

- [ ] **Step 5: Overlay tenant webhook categories onto the matrix read** in `notification_preferences.go` `handleGetNotificationPreferences` (replace the webhook-flag source)

After fetching `resp` (the per-user prefs) and before the merge loop, fetch the tenant webhook config and build the enabled set:
```go
	// FUT-019 webhook channel — the Webhook matrix column is tenant-level, not
	// per-user: it reflects the org webhook config's enabled_categories. Fetch
	// it once and overlay below (the per-user webhook_enabled flag is ignored).
	webhookCats := map[string]struct{}{}
	if wc, werr := h.audit.GetNotificationWebhookConfig(r.Context(), &auditv1.GetNotificationWebhookConfigRequest{TenantId: tenantID}); werr == nil {
		for _, c := range wc.GetEnabledCategories() {
			webhookCats[c] = struct{}{}
		}
	}
	// A webhook-config error is non-fatal — the matrix still renders bell+email;
	// the Webhook column just shows all-off until the config loads.
```

In the merge loop, set `WebhookEnabled` from the overlay instead of the per-user pref:
```go
	for _, cat := range knownNotificationCategories {
		row := NotificationPreferenceRow{
			NotificationCategoryMeta: cat,
			BellEnabled:              true,
		}
		if pref, ok := dbByCategory[cat.Key]; ok {
			row.BellEnabled = pref.GetBellEnabled()
			row.EmailEnabled = pref.GetEmailEnabled()
		}
		// Webhook is tenant-level (org config), not per-user.
		_, row.WebhookEnabled = webhookCats[cat.Key]
		out.Preferences = append(out.Preferences, row)
	}
```

And in `handlePatchNotificationPreferences`, stop forwarding the per-user webhook flag (the audit handler ignores it, but keep the wire honest) — set `WebhookEnabled: false` explicitly in the `patchProtos` construction, with a comment:
```go
		patchProtos = append(patchProtos, &auditv1.NotificationPreference{
			Category:     row.Category,
			BellEnabled:  row.BellEnabled,
			EmailEnabled: row.EmailEnabled,
			// FUT-019 webhook channel — webhook is tenant-level + admin-gated;
			// the per-user PATCH never writes it (admins use the webhook-config
			// PUT). Force false so a stale client body can't persist it.
			WebhookEnabled: false,
		})
```

- [ ] **Step 6: Run to verify it passes + build**

Run: `cd services/management && GOWORK=off go test ./internal/handler/ -count=1 && GOWORK=off go build ./... && GOWORK=off go vet ./...`
Expected: PASS + clean build.

- [ ] **Step 7: Commit**

```bash
git add services/management/internal/handler/notification_webhook.go services/management/internal/handler/notification_webhook_test.go services/management/internal/handler/handler.go services/management/internal/handler/notification_preferences.go
git commit -m "feat(management): webhook config BFF routes + matrix overlay"
```

---

## Task 11: FE API client — `notification-webhook.ts`

**Files:**
- Create: `frontend/src/lib/api/notification-webhook.ts`

- [ ] **Step 1: Write the client** (mirrors `email-transport.ts`)

```ts
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { apiClient } from "./client";

export interface NotificationWebhookConfig {
  url: string;
  enabled: boolean;
  has_secret: boolean;
  enabled_categories: string[];
  last_test_at?: string;
  last_test_ok?: boolean;
  last_test_error?: string;
}

export interface NotificationWebhookPut {
  url: string;
  enabled: boolean;
  secret: string; // empty = keep existing
  enabled_categories: string[];
}

export const notificationWebhookKeys = { all: ["notification-webhook"] as const };

export function useNotificationWebhook() {
  return useQuery({
    queryKey: notificationWebhookKeys.all,
    queryFn: async () => {
      const { data } = await apiClient.get<NotificationWebhookConfig>("/notifications/webhook-config");
      return data;
    },
    staleTime: 30_000,
  });
}

export function useUpdateNotificationWebhook() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (body: NotificationWebhookPut) => {
      const { data } = await apiClient.put<NotificationWebhookConfig>("/notifications/webhook-config", body);
      return data;
    },
    onSuccess: (data) => {
      qc.setQueryData(notificationWebhookKeys.all, data);
      // The matrix Webhook column reads from this config — refresh it too.
      void qc.invalidateQueries({ queryKey: ["notification-preferences"] });
    },
  });
}

export function useSendTestNotificationWebhook() {
  return useMutation({
    mutationFn: async () => {
      const { data } = await apiClient.post<{ ok: boolean; error: string }>(
        "/notifications/webhook-config/test",
      );
      return data;
    },
  });
}
```

- [ ] **Step 2: Add the vite dev-proxy route** — `/notifications/webhook-config` is under `/api/v1` which already proxies to management (`localhost:8091`, the `/api/v1` catch-all in `vite.config.ts`). No proxy edit needed (unlike `/access/*` which needed explicit rules). Confirm by reading `vite.config.ts` line ~80 (`"/api/v1": { target: "http://localhost:8091" }`).

- [ ] **Step 3: Commit**

```bash
git add frontend/src/lib/api/notification-webhook.ts
git commit -m "feat(fe): notification webhook API client"
```

---

## Task 12: FE panel — `notification-webhook-panel.tsx`

A near-clone of `email-transport-panel.tsx`, minus the provider switch (webhook has one shape). Admin-only (`useIsGlobalAdmin` → render null for non-admins). Fields: URL, secret (write-only, `has_secret` placeholder), enabled toggle, Save + Send-test buttons, `TestResult` banner. **No category checkboxes here** — categories live in the matrix (Task 13).

**Files:**
- Create: `frontend/src/components/settings/notification-webhook-panel.tsx`
- Test: `frontend/src/components/settings/__tests__/notification-webhook-panel.test.tsx`
- Modify: `frontend/src/routes/_authenticated.settings.notifications.tsx` (mount below `EmailTransportPanel`)

- [ ] **Step 1: Write the failing test** (mirror `email-transport-panel.test.tsx`)

```tsx
import { render, screen } from "@testing-library/react";
import { describe, it, expect, vi } from "vitest";
import { NotificationWebhookPanel } from "@/components/settings/notification-webhook-panel";
import { renderWithProviders } from "@/test/util"; // whatever the email panel test uses

vi.mock("@/lib/api/abilities", () => ({ useIsGlobalAdmin: () => true }));
vi.mock("@/lib/api/notification-webhook", () => ({
  useNotificationWebhook: () => ({ data: { url: "https://x", enabled: true, has_secret: true, enabled_categories: [] }, isLoading: false, isError: false, refetch: vi.fn() }),
  useUpdateNotificationWebhook: () => ({ mutate: vi.fn(), isPending: false }),
  useSendTestNotificationWebhook: () => ({ mutate: vi.fn(), isPending: false, data: undefined }),
}));

describe("NotificationWebhookPanel", () => {
  it("renders the URL field for admins", () => {
    renderWithProviders(<NotificationWebhookPanel />);
    expect(screen.getByLabelText(/webhook url/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /send test/i })).toBeInTheDocument();
  });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd frontend && npm run test -- notification-webhook-panel`
Expected: FAIL — module not found.

- [ ] **Step 3: Write the panel** — clone `email-transport-panel.tsx` with these concrete changes:
  - Header: `Webhook` icon (`Webhook` from lucide-react) + title "Notification webhook".
  - `FormState`: `{ url: string; enabled: boolean; secret: string }` (no provider/SMTP/from fields).
  - `seedFrom(cfg)`: `{ url: cfg.url, enabled: cfg.enabled, secret: "" }`.
  - Fields rendered: one `Input id="webhook-url"` with `<Label htmlFor="webhook-url">Webhook URL</Label>` (type text, placeholder `https://hooks.example.com/...`); one `Input id="webhook-secret" type="password"` with `<Label>Signing secret</Label>` and `placeholder={data?.has_secret ? "•••• configured" : ""}`; the enabled checkbox; the `TestResult` banner (identical); Save + "Send test" buttons.
  - `save()` builds `NotificationWebhookPut`: `{ url, enabled, secret, enabled_categories: data?.enabled_categories ?? [] }` — **preserve the existing category set** (the matrix owns it; the panel must not clobber it), then `update.mutate(body, { onSuccess: (next) => { setForm(seedFrom(next)); toast.success("Webhook saved."); }, onError: ... })`.
  - Send-test button calls `sendTest.mutate()`.
  - Admin gate: `const isAdmin = useIsGlobalAdmin(); if (!isAdmin) return null;` then an inner component, exactly like the email panel.
  - Reuse the same `CARD_CLASS`.

- [ ] **Step 4: Mount it** in `_authenticated.settings.notifications.tsx`:
```tsx
import { NotificationWebhookPanel } from "@/components/settings/notification-webhook-panel";
// ...
    <div className="space-y-6">
      <EmailTransportPanel />
      <NotificationWebhookPanel />
      <NotificationsSection />
    </div>
```

- [ ] **Step 5: Run to verify it passes**

Run: `cd frontend && npm run test -- notification-webhook-panel`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add frontend/src/components/settings/notification-webhook-panel.tsx frontend/src/components/settings/__tests__/notification-webhook-panel.test.tsx frontend/src/routes/_authenticated.settings.notifications.tsx
git commit -m "feat(fe): notification webhook admin panel"
```

---

## Task 13: FE matrix — unlock the Webhook column (admin-editable / read-only)

The Webhook column currently passes `hint="Wired in Phase 3+"` (locked for everyone). Unlock it: admins can toggle (each toggle does a read-modify-write `PUT` of the webhook config's `enabled_categories`); non-admins see it read-only.

**Files:**
- Modify: `frontend/src/routes/_authenticated.settings.notifications.tsx`
- Modify: `frontend/src/routes/__tests__/settings.notifications.channel-toggle.test.tsx`

- [ ] **Step 1: Update the test** for the new semantics

```tsx
// Webhook cell: for a NON-admin, the checkbox is disabled (read-only).
it("webhook column is read-only for non-admins", () => {
  // mock useIsGlobalAdmin -> false, useNotificationWebhook -> { enabled_categories: ["scanner_freshness"] }
  // render the matrix; assert the scanner_freshness Webhook checkbox is checked AND disabled.
});

// Webhook cell: for an ADMIN, toggling fires a webhook-config PUT (not the prefs PATCH).
it("admin webhook toggle PUTs the webhook config", async () => {
  // mock useIsGlobalAdmin -> true; spy on useUpdateNotificationWebhook().mutate
  // click a Webhook checkbox; assert the webhook mutate was called with the
  // updated enabled_categories set (and the prefs update was NOT called).
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd frontend && npm run test -- settings.notifications.channel-toggle`
Expected: FAIL (current code locks the column unconditionally).

- [ ] **Step 3: Implement**

In `NotificationsSection`:
- Read admin + webhook config:
```tsx
import { useIsGlobalAdmin } from "@/lib/api/abilities";
import { useNotificationWebhook, useUpdateNotificationWebhook } from "@/lib/api/notification-webhook";
// ...
  const isAdmin = useIsGlobalAdmin();
  const webhookCfg = useNotificationWebhook().data;
  const updateWebhook = useUpdateNotificationWebhook();
```
- Add a webhook-category toggle handler (RMW PUT):
```tsx
  async function toggleWebhookCategory(category: string, next: boolean): Promise<void> {
    if (!webhookCfg) return;
    const set = new Set(webhookCfg.enabled_categories);
    if (next) set.add(category);
    else set.delete(category);
    setPendingCell(`${category}:webhook`);
    try {
      await updateWebhook.mutateAsync({
        url: webhookCfg.url,
        enabled: webhookCfg.enabled,
        secret: "", // keep existing
        enabled_categories: Array.from(set),
      });
      toast.success(`Webhook ${next ? "enabled" : "disabled"} for this category.`);
    } catch {
      toast.error("Couldn't update the webhook. Check the BFF logs.");
    } finally {
      setPendingCell(null);
    }
  }
```
- Change the Webhook `ChannelToggleCell` render (the third one) to be config-driven + admin-gated:
```tsx
                  <ChannelToggleCell
                    enabled={(webhookCfg?.enabled_categories ?? []).includes(row.key)}
                    pending={pendingCell === `${row.key}:webhook`}
                    onChange={(v) => void toggleWebhookCategory(row.key, v)}
                    // Non-admins see the column read-only; the hint doubles as
                    // the disabled explanation. Admins get a live checkbox.
                    hint={isAdmin ? undefined : "Admin-managed"}
                  />
```
- Update the section copy (remove "Phase 3+"): change the webhook clause in the description `<p>` to "webhook posts to the org endpoint configured above (admin-managed)."

- [ ] **Step 4: Run the full FE gate**

Run: `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build`
Expected: all four green.

- [ ] **Step 5: Commit**

```bash
git add frontend/src/routes/_authenticated.settings.notifications.tsx frontend/src/routes/__tests__/settings.notifications.channel-toggle.test.tsx
git commit -m "feat(fe): unlock Webhook column (admin-editable, read-only for others)"
```

---

## Task 14: Docs + trackers

**Files:**
- Modify: `docs/SERVICES.md` (audit RPCs + BFF routes), `CLAUDE.md` (audit service row), `services/audit/.env.example` (`NOTIFY_WEBHOOK_KEY_HEX`), `FE-STATUS.md` (FE-API-058), `futures.md` (retire the deferred webhook note), `status.md` (DONE row).

- [ ] **Step 1: `services/audit/.env.example`** — add:
```
# FUT-019 webhook notification channel — 32-byte hex (64 chars) AES-256-GCM KEK
# sealing the org webhook HMAC secret. Unset disables the webhook channel.
NOTIFY_WEBHOOK_KEY_HEX=
```

- [ ] **Step 2: `docs/SERVICES.md`** — under registry-audit, add the 3 webhook RPCs; under registry-management, add the 3 BFF routes (mirror the email-transport entries).

- [ ] **Step 3: `CLAUDE.md`** — extend the registry-audit row's "Notable" to mention the webhook channel + `NOTIFY_WEBHOOK_KEY_HEX` (alongside the existing `NOTIFY_EMAIL_KEY_HEX`).

- [ ] **Step 4: `FE-STATUS.md`** — add FE-API-058 (webhook panel + matrix column unlock).

- [ ] **Step 5: `futures.md`** — remove/mark-done the deferred "Webhook channel" note carried from the email-channel spec.

- [ ] **Step 6: `status.md`** — prepend a DONE row:
```
| FUT-019 — webhook notification channel (org webhook) | Unlocks the **Webhook** column of Settings › Notifications with an admin-configured shared org webhook. `services/audit` owns it: new `notification_webhook_config` (per-tenant url + AES-256-GCM-sealed HMAC secret under a **dedicated `NOTIFY_WEBHOOK_KEY_HEX`** KEK + tenant-level `enabled_categories`) and `notification_webhook_deliveries` (queue + log, idempotent on `source_scheduled_id`). The dispatcher best-effort enqueues one delivery per scheduled notification when the config is enabled + the category is selected; a send loop drains it with `FOR UPDATE SKIP LOCKED` + webhook backoff (`5s→30s→5m→30m→2h`, 5 attempts), HMAC-SHA256-signs a generic JSON envelope (`X-Registry-Signature`), and POSTs HTTPS-only + SSRF-blocked (patterns copied from `services/webhook`). 3 audit RPCs (`Get`/`PutNotificationWebhookConfig` write-only secret exposed as `has_secret`; `SendTestNotificationWebhook`) fronted by 3 admin BFF routes; the matrix read overlays the tenant `enabled_categories` onto the Webhook column (per-user webhook flag retired). New audit env `NOTIFY_WEBHOOK_KEY_HEX`; **no new mTLS peer edge** (no per-user resolution). FE: admin `NotificationWebhookPanel`, Webhook column unlocked (admin-editable / read-only for others; FE-API-058). Design + plan under `docs/superpowers/`. | branch `feat/fut-019-webhook-channel` (PR #NNN) | 2026-07-08 | DONE |
```

- [ ] **Step 7: Commit**

```bash
git add docs/SERVICES.md CLAUDE.md services/audit/.env.example FE-STATUS.md futures.md status.md
git commit -m "docs(fut-019): webhook channel — SERVICES/CLAUDE/env/trackers"
```

---

## Final verification (before opening the PR)

- [ ] **Backend:** `cd services/audit && GOWORK=off go build ./... && GOWORK=off go vet ./... && GOWORK=off go test ./... -count=1` (Docker up for integration) — all green.
- [ ] **Backend (management):** same triple for `services/management`.
- [ ] **Proto:** `cd proto && buf generate && git diff --exit-code proto/gen` — no drift.
- [ ] **Frontend:** `cd frontend && npm run lint && npm run typecheck && npm run test && npm run build` — all four green.
- [ ] **golangci-lint** (matches CI): `golangci-lint run ./...` in `services/audit` + `services/management` — 0 findings (watch for `dupl` against the email handlers → add `//nolint:dupl` with a reason if it fires, as the email/auth handlers did).
- [ ] **End-of-feature review batch:** security + qa + code-review agents in parallel, worktree-isolated read-only (per the established review-pace).

---

## Self-review (plan vs spec)

- **§4 data model** → Tasks 1–2 (both tables + grants in one migration; repo structs/methods). ✓
- **§5 dispatch** → Task 8 (`enqueueWebhook`, category-gated, best-effort). ✓
- **§6 send loop / transport** → Tasks 3–4 (Poster + SSRF + HMAC + backoff; Sender drain + resolve + fail). ✓
- **§7 API** → Task 6 (proto), Task 7 (handlers + secret masking + URL validation), Task 10 (BFF routes + matrix overlay + per-user webhook write neutralised). ✓
- **§8 config** → Task 5 (`NOTIFY_WEBHOOK_KEY_HEX` fail-closed) + Task 9 (server wiring). ✓
- **§9 frontend** → Tasks 11–13 (api client, admin panel, column unlock). ✓
- **§10 testing** → unit + integration + FE gates across tasks; final verification block. ✓
- **§11 security** → HTTPS-only + SSRF (Task 3), secret sealed/masked/redacted (Tasks 3/7), admin-gated + SA-denied (Task 10), fail-closed KEK (Task 5), HMAC signature (Task 3). ✓
- **§12 docs** → Task 14. ✓
- **Type consistency:** `NotificationWebhookConfig`/`WebhookDelivery` structs, repo methods (`GetNotificationWebhookConfig`, `UpsertNotificationWebhookConfig`, `UpdateWebhookTestResult`, `EnqueueWebhookDelivery`, `ClaimPendingWebhookDeliveries`, `MarkWebhookDelivered`, `MarkWebhookFailed`), proto messages, and handler methods are named identically across Tasks 2/6/7/8/9/10. ✓
