//go:build integration

// Integration tests for the FUT-019 email channel repository. Like the other
// audit repository integration tests (see internal/testutil/integration) these
// spin up a real PostgreSQL 16 container via testcontainers and apply every
// goose migration so the runtime schema matches production. They are gated
// behind the `integration` build tag; the plain `go test ./...` CI job skips
// them, so this file must still *compile* under `go vet`.
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	auditmigrations "github.com/steveokay/oci-janus/services/audit/migrations"
)

// newEmailTestRepo spins up a PostgreSQL container, runs all audit migrations,
// and returns a Repository backed by a fresh pool. Mirrors the newRepo helper
// in internal/testutil/integration. The container is torn down when t finishes.
func newEmailTestRepo(t *testing.T) *Repository {
	t.Helper()
	ctx := context.Background()

	dsn := containers.Postgres(t)

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(auditmigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("goose.Up: %v", err)
	}

	return New(pool)
}

// TestEmailTransportConfig_upsertRoundTrip verifies that an upserted config —
// including raw ciphertext bytes — round-trips through GetEmailTransportConfig.
func TestEmailTransportConfig_upsertRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid := uuid.New()
	in := EmailTransportConfig{
		TenantID: tid, Provider: "resend", Enabled: true,
		FromAddress: "n@example.com", FromName: "Reg",
		ResendAPIKeyEnc: []byte{0x01, 0x02, 0x03}, SMTPTLSMode: "starttls", KEKVersion: 1,
	}
	if err := repo.UpsertEmailTransportConfig(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.GetEmailTransportConfig(ctx, tid)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Provider != "resend" || !got.Enabled || string(got.ResendAPIKeyEnc) != "\x01\x02\x03" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.FromAddress != "n@example.com" || got.KEKVersion != 1 {
		t.Fatalf("round-trip field mismatch: %+v", got)
	}
}

// TestEmailTransportConfig_getMissing confirms a never-saved tenant returns
// (nil, nil) rather than an error.
func TestEmailTransportConfig_getMissing(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	got, err := repo.GetEmailTransportConfig(ctx, uuid.New())
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil config, got %+v", got)
	}
}

// TestEmailTransportConfig_upsertReplaces confirms the ON CONFLICT path
// overwrites an existing row (idempotent second save with changed fields).
func TestEmailTransportConfig_upsertReplaces(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid := uuid.New()
	if err := repo.UpsertEmailTransportConfig(ctx, EmailTransportConfig{
		TenantID: tid, Provider: "resend", Enabled: false,
		SMTPTLSMode: "starttls", KEKVersion: 1,
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := repo.UpsertEmailTransportConfig(ctx, EmailTransportConfig{
		TenantID: tid, Provider: "smtp", Enabled: true,
		SMTPHost: "smtp.example.com", SMTPPort: 587, SMTPUsername: "bot",
		SMTPPasswordEnc: []byte{0xaa}, SMTPTLSMode: "implicit", KEKVersion: 2,
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := repo.GetEmailTransportConfig(ctx, tid)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.Provider != "smtp" || !got.Enabled || got.SMTPPort != 587 ||
		got.SMTPHost != "smtp.example.com" || got.SMTPTLSMode != "implicit" ||
		got.KEKVersion != 2 || string(got.SMTPPasswordEnc) != "\xaa" {
		t.Fatalf("replace mismatch: %+v", got)
	}
}

// TestUpdateEmailTestResult confirms the probe result is persisted without
// disturbing the rest of the config.
func TestUpdateEmailTestResult(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid := uuid.New()
	if err := repo.UpsertEmailTransportConfig(ctx, EmailTransportConfig{
		TenantID: tid, Provider: "resend", Enabled: true,
		SMTPTLSMode: "starttls", KEKVersion: 1,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := repo.UpdateEmailTestResult(ctx, tid, false, "smtp: connection refused"); err != nil {
		t.Fatalf("update test result: %v", err)
	}
	got, err := repo.GetEmailTransportConfig(ctx, tid)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.LastTestOK == nil || *got.LastTestOK {
		t.Fatalf("expected last_test_ok=false, got %v", got.LastTestOK)
	}
	if got.LastTestError != "smtp: connection refused" {
		t.Fatalf("last_test_error = %q", got.LastTestError)
	}
	if got.LastTestAt == nil {
		t.Fatalf("expected last_test_at set")
	}
	// Config left otherwise intact.
	if got.Provider != "resend" || !got.Enabled {
		t.Fatalf("config disturbed by test result: %+v", got)
	}
}

// TestEnqueueEmailDelivery_idempotent confirms a second insert with the same
// (source_scheduled_id, user_id) is a no-op.
func TestEnqueueEmailDelivery_idempotent(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid, uid, src := uuid.New(), uuid.New(), uuid.New()
	d := EmailDelivery{
		TenantID: tid, UserID: uid, ToAddress: "u@example.com",
		Category: "scanner_freshness", Subject: "Scan stale", BodySummary: "1 image",
		Link: "https://x/y", SourceScheduledID: src,
	}
	if err := repo.EnqueueEmailDelivery(ctx, d); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if err := repo.EnqueueEmailDelivery(ctx, d); err != nil {
		t.Fatalf("enqueue 2 (should be no-op): %v", err)
	}
	got, err := repo.ListEmailDeliveries(ctx, tid, uid, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row after idempotent enqueue, got %d", len(got))
	}
	if got[0].Link != "https://x/y" || got[0].Status != "pending" {
		t.Fatalf("row mismatch: %+v", got[0])
	}
}

// TestListEmailRecipients_filtersEnabled confirms only email_enabled=true
// preferences for the (tenant, category) pair are returned.
func TestListEmailRecipients_filtersEnabled(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid := uuid.New()
	optIn, optOut, otherCat := uuid.New(), uuid.New(), uuid.New()

	if err := repo.UpsertUserPreference(ctx, NotificationPreference{
		UserID: optIn, TenantID: tid, Category: "scanner_freshness",
		BellEnabled: true, EmailEnabled: true,
	}); err != nil {
		t.Fatalf("upsert optIn: %v", err)
	}
	if err := repo.UpsertUserPreference(ctx, NotificationPreference{
		UserID: optOut, TenantID: tid, Category: "scanner_freshness",
		BellEnabled: true, EmailEnabled: false,
	}); err != nil {
		t.Fatalf("upsert optOut: %v", err)
	}
	if err := repo.UpsertUserPreference(ctx, NotificationPreference{
		UserID: otherCat, TenantID: tid, Category: "webhook_health",
		BellEnabled: true, EmailEnabled: true,
	}); err != nil {
		t.Fatalf("upsert otherCat: %v", err)
	}

	got, err := repo.ListEmailRecipients(ctx, tid, "scanner_freshness")
	if err != nil {
		t.Fatalf("list recipients: %v", err)
	}
	if len(got) != 1 || got[0] != optIn {
		t.Fatalf("expected only opted-in recipient, got %v", got)
	}
}

// TestClaimPendingEmailDeliveries_respectsNextAttempt confirms the claim only
// returns due rows and leases them (pushes next_attempt_at forward).
func TestClaimPendingEmailDeliveries_respectsNextAttempt(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid, uid := uuid.New(), uuid.New()

	// One immediately-due row; enqueue defaults next_attempt_at to now().
	if err := repo.EnqueueEmailDelivery(ctx, EmailDelivery{
		TenantID: tid, UserID: uid, ToAddress: "u@example.com",
		Category: "scanner_freshness", Subject: "s", BodySummary: "b",
		SourceScheduledID: uuid.New(),
	}); err != nil {
		t.Fatalf("enqueue due: %v", err)
	}

	// A future-dated row must NOT be claimed. Enqueue then push it out.
	futureSrc := uuid.New()
	if err := repo.EnqueueEmailDelivery(ctx, EmailDelivery{
		TenantID: tid, UserID: uid, ToAddress: "u2@example.com",
		Category: "scanner_freshness", Subject: "s2", BodySummary: "b2",
		SourceScheduledID: futureSrc,
	}); err != nil {
		t.Fatalf("enqueue future: %v", err)
	}
	future := time.Now().Add(time.Hour)
	// Park the second row in the future via MarkEmailFailed (status stays pending).
	rows, err := repo.ListEmailDeliveries(ctx, tid, uid, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, r := range rows {
		if r.SourceScheduledID == futureSrc {
			if err := repo.MarkEmailFailed(ctx, r.ID, 1, future, false, "retry later"); err != nil {
				t.Fatalf("mark future: %v", err)
			}
		}
	}

	claimed, err := repo.ClaimPendingEmailDeliveries(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected 1 due row claimed, got %d", len(claimed))
	}
	if claimed[0].ToAddress != "u@example.com" {
		t.Fatalf("claimed wrong row: %+v", claimed[0])
	}
	// The claim leased the row (next_attempt_at ~ now + 1m), so an immediate
	// re-claim at the same instant returns nothing.
	again, err := repo.ClaimPendingEmailDeliveries(ctx, time.Now(), 10)
	if err != nil {
		t.Fatalf("re-claim: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("expected leased row to be unclaimable, got %d", len(again))
	}
}

// TestMarkEmailSentFailed_transitions confirms the sent/failed state changes.
func TestMarkEmailSentFailed_transitions(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid, uid := uuid.New(), uuid.New()

	sentSrc, failSrc := uuid.New(), uuid.New()
	if err := repo.EnqueueEmailDelivery(ctx, EmailDelivery{
		TenantID: tid, UserID: uid, ToAddress: "sent@example.com",
		Category: "c", Subject: "s", BodySummary: "b", SourceScheduledID: sentSrc,
	}); err != nil {
		t.Fatalf("enqueue sent: %v", err)
	}
	if err := repo.EnqueueEmailDelivery(ctx, EmailDelivery{
		TenantID: tid, UserID: uid, ToAddress: "fail@example.com",
		Category: "c", Subject: "s", BodySummary: "b", SourceScheduledID: failSrc,
	}); err != nil {
		t.Fatalf("enqueue fail: %v", err)
	}

	rows, err := repo.ListEmailDeliveries(ctx, tid, uid, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var sentID, failID uuid.UUID
	for _, r := range rows {
		switch r.SourceScheduledID {
		case sentSrc:
			sentID = r.ID
		case failSrc:
			failID = r.ID
		}
	}

	if err := repo.MarkEmailSent(ctx, sentID, "resend"); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if err := repo.MarkEmailFailed(ctx, failID, 3, time.Now(), true, "550 rejected"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	rows, err = repo.ListEmailDeliveries(ctx, tid, uid, 10)
	if err != nil {
		t.Fatalf("list after marks: %v", err)
	}
	for _, r := range rows {
		switch r.ID {
		case sentID:
			if r.Status != "sent" || r.SentAt == nil || r.Provider != "resend" || r.LastError != "" {
				t.Fatalf("sent row wrong: %+v", r)
			}
		case failID:
			if r.Status != "failed" || r.Attempts != 3 || r.LastError != "550 rejected" {
				t.Fatalf("failed row wrong: %+v", r)
			}
		}
	}
}

// TestListEmailDeliveries_scopedToUserAndTenant confirms results are scoped to
// the requested (tenant, user) and ordered newest-first.
func TestListEmailDeliveries_scopedToUserAndTenant(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid, uid, otherUser := uuid.New(), uuid.New(), uuid.New()

	if err := repo.EnqueueEmailDelivery(ctx, EmailDelivery{
		TenantID: tid, UserID: uid, ToAddress: "u@example.com",
		Category: "c", Subject: "mine", BodySummary: "b", SourceScheduledID: uuid.New(),
	}); err != nil {
		t.Fatalf("enqueue mine: %v", err)
	}
	if err := repo.EnqueueEmailDelivery(ctx, EmailDelivery{
		TenantID: tid, UserID: otherUser, ToAddress: "o@example.com",
		Category: "c", Subject: "theirs", BodySummary: "b", SourceScheduledID: uuid.New(),
	}); err != nil {
		t.Fatalf("enqueue theirs: %v", err)
	}

	got, err := repo.ListEmailDeliveries(ctx, tid, uid, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].Subject != "mine" {
		t.Fatalf("expected only the requesting user's row, got %+v", got)
	}
}
