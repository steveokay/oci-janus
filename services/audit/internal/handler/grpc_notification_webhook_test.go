package handler

// grpc_notification_webhook_test.go — FUT-019 webhook gRPC handler tests.
//
// Covers the secret mask on read, the fail-closed seal without a KEK, URL
// validation on write, and the inline (non-error) disabled result on the test
// path. Reuses the package fakeRepo (grpc_test.go) and adds the webhook method
// set here so the fake stays functional for these paths.

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// ── fakeRepo webhook methods ─────────────────────────────────────────

func (f *fakeRepo) GetNotificationWebhookConfig(_ context.Context, _ uuid.UUID) (*repository.NotificationWebhookConfig, error) {
	return f.webhookCfg, nil
}

func (f *fakeRepo) UpsertNotificationWebhookConfig(_ context.Context, cfg repository.NotificationWebhookConfig) error {
	c := cfg
	// Refresh webhookCfg so the handler's reload-after-upsert sees the write.
	f.webhookCfg = &c
	return nil
}

func (f *fakeRepo) UpdateWebhookTestResult(_ context.Context, _ uuid.UUID, _ bool, _ string) error {
	return nil
}

// ── GetNotificationWebhookConfig ─────────────────────────────────────

func TestGetNotificationWebhookConfig_masksSecret(t *testing.T) {
	tenantID := uuid.New()
	fake := &fakeRepo{
		webhookCfg: &repository.NotificationWebhookConfig{
			TenantID:          tenantID,
			URL:               "https://x",
			SecretEnc:         []byte("ct"),
			Enabled:           true,
			EnabledCategories: []string{"scanner_freshness"},
		},
	}
	h := newHandler(fake)

	resp, err := h.GetNotificationWebhookConfig(context.Background(), &auditv1.GetNotificationWebhookConfigRequest{
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetHasSecret() {
		t.Errorf("expected HasSecret=true")
	}
	if resp.GetUrl() != "https://x" {
		t.Errorf("expected url https://x, got %q", resp.GetUrl())
	}
	if len(resp.GetEnabledCategories()) != 1 {
		t.Errorf("expected 1 enabled category, got %d", len(resp.GetEnabledCategories()))
	}
	// The proto carries no secret field at all — HasSecret is the only signal,
	// so the raw ciphertext can never be smuggled out over the wire.
}

// ── PutNotificationWebhookConfig ─────────────────────────────────────

func TestPutNotificationWebhookConfig_rejectsSecretWithoutKEK(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake) // no WithWebhookKEK → empty KEK

	// URL left empty so the handler skips the (real) DNS-backed ValidateURL and
	// reaches the seal path — the point under test is the fail-closed seal.
	_, err := h.PutNotificationWebhookConfig(context.Background(), &auditv1.PutNotificationWebhookConfigRequest{
		TenantId: uuid.New().String(),
		Secret:   "shh",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (err=%v)", status.Code(err), err)
	}
}

func TestPutNotificationWebhookConfig_rejectsBadURL(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake).WithWebhookKEK(make([]byte, 32))

	_, err := h.PutNotificationWebhookConfig(context.Background(), &auditv1.PutNotificationWebhookConfigRequest{
		TenantId: uuid.New().String(),
		Url:      "http://insecure",
		Enabled:  true,
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for non-HTTPS url, got %v (err=%v)", status.Code(err), err)
	}
}

// ── SendTestNotificationWebhook ──────────────────────────────────────

func TestSendTestNotificationWebhook_disabledInline(t *testing.T) {
	fake := &fakeRepo{webhookCfg: nil} // never configured
	h := newHandler(fake).WithWebhookKEK(make([]byte, 32))

	resp, err := h.SendTestNotificationWebhook(context.Background(), &auditv1.SendTestNotificationWebhookRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetOk() {
		t.Errorf("expected Ok=false for an unconfigured webhook transport")
	}
}
