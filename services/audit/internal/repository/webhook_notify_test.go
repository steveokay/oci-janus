//go:build integration

// Integration tests for the FUT-019 webhook channel repository. Like the email
// channel tests they spin up a real PostgreSQL 16 container via testcontainers
// and apply every goose migration so the runtime schema matches production.
// They reuse newEmailTestRepo (same package) for the container + migration
// bootstrap. Gated behind the `integration` build tag.
package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestWebhookNotifyConfig_roundTrip verifies that GetNotificationWebhookConfig
// returns (nil, nil) for an unconfigured tenant and, after an upsert, returns
// the saved URL, enabled flag, enabled_categories, and the raw secret bytes.
func TestWebhookNotifyConfig_roundTrip(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid := uuid.New()

	// Unconfigured tenant → (nil, nil).
	got, err := repo.GetNotificationWebhookConfig(ctx, tid)
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil config for unconfigured tenant, got %+v", got)
	}

	// Save a config, then read it back.
	in := NotificationWebhookConfig{
		TenantID:          tid,
		URL:               "https://hooks.example.com/x",
		SecretEnc:         []byte("ciphertext"),
		Enabled:           true,
		EnabledCategories: []string{"scanner_freshness", "webhook_health"},
		KEKVersion:        1,
	}
	if err := repo.UpsertNotificationWebhookConfig(ctx, in); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err = repo.GetNotificationWebhookConfig(ctx, tid)
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.URL != "https://hooks.example.com/x" || !got.Enabled {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if string(got.SecretEnc) != "ciphertext" {
		t.Fatalf("secret bytes mismatch: %q", got.SecretEnc)
	}
	if len(got.EnabledCategories) != 2 ||
		got.EnabledCategories[0] != "scanner_freshness" ||
		got.EnabledCategories[1] != "webhook_health" {
		t.Fatalf("enabled_categories mismatch: %v", got.EnabledCategories)
	}
	if got.KEKVersion != 1 {
		t.Fatalf("kek_version mismatch: %d", got.KEKVersion)
	}
}

// TestWebhookNotifyDelivery_queue confirms EnqueueWebhookDelivery is idempotent
// on source_scheduled_id, that ClaimPendingWebhookDeliveries leases exactly one
// due row, and that MarkWebhookDelivered closes it.
func TestWebhookNotifyDelivery_queue(t *testing.T) {
	ctx := context.Background()
	repo := newEmailTestRepo(t)
	tid, src := uuid.New(), uuid.New()

	d := WebhookDelivery{
		TenantID:          tid,
		Category:          "scanner_freshness",
		Subject:           "Scan stale",
		BodySummary:       "1 image",
		Link:              "https://x/y",
		SourceScheduledID: src,
	}
	// Two enqueues with the SAME source_scheduled_id → second is a no-op.
	if err := repo.EnqueueWebhookDelivery(ctx, d); err != nil {
		t.Fatalf("enqueue 1: %v", err)
	}
	if err := repo.EnqueueWebhookDelivery(ctx, d); err != nil {
		t.Fatalf("enqueue 2 (should be no-op): %v", err)
	}

	claimed, err := repo.ClaimPendingWebhookDeliveries(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("expected exactly 1 claimed row (idempotent enqueue), got %d", len(claimed))
	}
	row := claimed[0]
	if row.Category != "scanner_freshness" || row.Subject != "Scan stale" ||
		row.Link != "https://x/y" || row.Status != "pending" {
		t.Fatalf("claimed row mismatch: %+v", row)
	}

	if err := repo.MarkWebhookDelivered(ctx, row.ID, 200); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
}
