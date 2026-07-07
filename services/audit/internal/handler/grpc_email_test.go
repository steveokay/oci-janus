package handler

// grpc_email_test.go — FUT-019 Phase 3 email gRPC handler tests.
//
// Covers the secret lifecycle (mask on read, keep-on-empty, rotate, fail-closed
// without a KEK), the SendTestEmail record path (fake transport), and the
// ListEmailDeliveries page-size cap. Reuses the package fakeRepo (grpc_test.go)
// and adds the email method set here so the fake stays functional for these
// paths.

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/email"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// ── fakeRepo email methods ───────────────────────────────────────────

func (f *fakeRepo) GetEmailTransportConfig(_ context.Context, _ uuid.UUID) (*repository.EmailTransportConfig, error) {
	return f.emailCfg, f.emailCfgErr
}

func (f *fakeRepo) UpsertEmailTransportConfig(_ context.Context, cfg repository.EmailTransportConfig) error {
	c := cfg
	f.upsertedEmail = &c
	// Refresh emailCfg so the handler's reload-after-upsert sees the write.
	f.emailCfg = &c
	return nil
}

func (f *fakeRepo) UpdateEmailTestResult(_ context.Context, _ uuid.UUID, ok bool, errMsg string) error {
	f.testResultSet = true
	f.testResultOK = ok
	f.testResultErr = errMsg
	return nil
}

func (f *fakeRepo) ListEmailDeliveries(_ context.Context, _ uuid.UUID, _ uuid.UUID, limit int) ([]*repository.EmailDelivery, error) {
	f.lastListLimit = limit
	out := f.emailDeliveries
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ── fake transport ───────────────────────────────────────────────────

// fakeTransport is an email.Transport double: it records the last sent message
// and returns sendErr (nil = success).
type fakeTransport struct {
	sendErr error
	sent    *email.Message
}

func (t *fakeTransport) Send(_ context.Context, msg email.Message) error {
	m := msg
	t.sent = &m
	return t.sendErr
}
func (t *fakeTransport) Name() string { return "fake" }

// fakeTransportFactory returns a factory that always yields ft, ignoring the
// config (the config path is exercised by the email package's own tests).
func fakeTransportFactory(ft *fakeTransport) func(email.DecryptedConfig) (email.Transport, error) {
	return func(email.DecryptedConfig) (email.Transport, error) { return ft, nil }
}

// testKEK is a deterministic 32-byte AES-256 key for the seal/open tests.
var testKEK = []byte("0123456789abcdef0123456789abcdef")

// ── GetEmailTransportConfig ──────────────────────────────────────────

func TestGetEmailTransportConfig_masksSecrets(t *testing.T) {
	tenantID := uuid.New()
	fake := &fakeRepo{
		emailCfg: &repository.EmailTransportConfig{
			TenantID:        tenantID,
			Provider:        "resend",
			Enabled:         true,
			FromAddress:     "noreply@example.com",
			ResendAPIKeyEnc: []byte("sealed-ciphertext"),
		},
	}
	h := newHandler(fake)

	resp, err := h.GetEmailTransportConfig(context.Background(), &auditv1.GetEmailTransportConfigRequest{
		TenantId: tenantID.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetHasResendKey() {
		t.Errorf("expected HasResendKey=true")
	}
	if resp.GetHasSmtpPassword() {
		t.Errorf("expected HasSmtpPassword=false (no smtp password stored)")
	}
	if !resp.GetEnabled() || resp.GetProvider() != "resend" {
		t.Errorf("expected enabled resend provider, got enabled=%v provider=%q", resp.GetEnabled(), resp.GetProvider())
	}
	// The proto carries no secret field at all — assert the masked booleans are
	// the only signal (there is no way to smuggle the ciphertext out).
}

func TestGetEmailTransportConfig_nilRowReturnsDefaults(t *testing.T) {
	fake := &fakeRepo{emailCfg: nil}
	h := newHandler(fake)

	resp, err := h.GetEmailTransportConfig(context.Background(), &auditv1.GetEmailTransportConfigRequest{
		TenantId: uuid.New().String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetProvider() != "resend" || resp.GetSmtpTlsMode() != "starttls" {
		t.Errorf("expected defaults resend/starttls, got %q/%q", resp.GetProvider(), resp.GetSmtpTlsMode())
	}
}

// ── PutEmailTransportConfig ──────────────────────────────────────────

func TestPutEmailTransportConfig_emptySecretPreservesCiphertext(t *testing.T) {
	tenantID := uuid.New()
	existingCT, err := aes.Encrypt([]byte("existing-key"), testKEK)
	if err != nil {
		t.Fatalf("seed encrypt: %v", err)
	}
	fake := &fakeRepo{
		emailCfg: &repository.EmailTransportConfig{
			TenantID:        tenantID,
			Provider:        "resend",
			Enabled:         true,
			ResendAPIKeyEnc: existingCT,
		},
	}
	h := newHandler(fake).WithEmailKEK(testKEK)

	_, err = h.PutEmailTransportConfig(context.Background(), &auditv1.PutEmailTransportConfigRequest{
		TenantId:     tenantID.String(),
		Provider:     "resend",
		Enabled:      true,
		ResendApiKey: "", // empty → keep existing
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.upsertedEmail == nil {
		t.Fatal("expected an upsert")
	}
	if string(fake.upsertedEmail.ResendAPIKeyEnc) != string(existingCT) {
		t.Errorf("expected existing ciphertext preserved, got a different value")
	}
	if fake.upsertedEmail.KEKVersion != 1 {
		t.Errorf("expected KEKVersion=1, got %d", fake.upsertedEmail.KEKVersion)
	}
}

func TestPutEmailTransportConfig_secretReEncrypts(t *testing.T) {
	tenantID := uuid.New()
	fake := &fakeRepo{}
	h := newHandler(fake).WithEmailKEK(testKEK)

	_, err := h.PutEmailTransportConfig(context.Background(), &auditv1.PutEmailTransportConfigRequest{
		TenantId:     tenantID.String(),
		Provider:     "resend",
		Enabled:      true,
		ResendApiKey: "brand-new-key",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.upsertedEmail == nil || len(fake.upsertedEmail.ResendAPIKeyEnc) == 0 {
		t.Fatal("expected sealed ciphertext to be stored")
	}
	// Ciphertext must not equal the plaintext, and must decrypt back under the KEK.
	if string(fake.upsertedEmail.ResendAPIKeyEnc) == "brand-new-key" {
		t.Fatal("secret was stored as plaintext")
	}
	pt, err := aes.Decrypt(fake.upsertedEmail.ResendAPIKeyEnc, testKEK)
	if err != nil {
		t.Fatalf("decrypt stored secret: %v", err)
	}
	if string(pt) != "brand-new-key" {
		t.Errorf("expected decrypted secret 'brand-new-key', got %q", string(pt))
	}
}

func TestPutEmailTransportConfig_secretWithoutKEKFailsClosed(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake) // no WithEmailKEK → empty KEK

	_, err := h.PutEmailTransportConfig(context.Background(), &auditv1.PutEmailTransportConfigRequest{
		TenantId:     uuid.New().String(),
		Provider:     "resend",
		ResendApiKey: "secret-but-no-key",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v (err=%v)", status.Code(err), err)
	}
	if fake.upsertedEmail != nil {
		t.Error("expected no upsert when secret sealing fails")
	}
}

// ── SendTestEmail ────────────────────────────────────────────────────

func TestSendTestEmail_successRecordsOK(t *testing.T) {
	tenantID := uuid.New()
	fake := &fakeRepo{
		emailCfg: &repository.EmailTransportConfig{
			TenantID: tenantID,
			Provider: "resend",
			Enabled:  true,
		},
	}
	ft := &fakeTransport{}
	h := newHandler(fake).WithEmailKEK(testKEK).WithEmailTransport(fakeTransportFactory(ft))

	resp, err := h.SendTestEmail(context.Background(), &auditv1.SendTestEmailRequest{
		TenantId:  tenantID.String(),
		ToAddress: "ops@example.com",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.GetOk() || resp.GetError() != "" {
		t.Errorf("expected Ok=true no error, got Ok=%v err=%q", resp.GetOk(), resp.GetError())
	}
	if !fake.testResultSet || !fake.testResultOK {
		t.Errorf("expected last_test_ok=true recorded")
	}
	if ft.sent == nil || ft.sent.To != "ops@example.com" {
		t.Errorf("expected the test message sent to ops@example.com")
	}
}

func TestSendTestEmail_transportFailureRecordsError(t *testing.T) {
	tenantID := uuid.New()
	fake := &fakeRepo{
		emailCfg: &repository.EmailTransportConfig{
			TenantID: tenantID,
			Provider: "resend",
			Enabled:  true,
		},
	}
	ft := &fakeTransport{sendErr: errors.New("smtp 550 rejected")}
	h := newHandler(fake).WithEmailKEK(testKEK).WithEmailTransport(fakeTransportFactory(ft))

	resp, err := h.SendTestEmail(context.Background(), &auditv1.SendTestEmailRequest{
		TenantId:  tenantID.String(),
		ToAddress: "ops@example.com",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetOk() {
		t.Errorf("expected Ok=false on send failure")
	}
	if resp.GetError() == "" {
		t.Errorf("expected a redacted error string")
	}
	if !fake.testResultSet || fake.testResultOK {
		t.Errorf("expected last_test_ok=false recorded, got set=%v ok=%v", fake.testResultSet, fake.testResultOK)
	}
}

func TestSendTestEmail_disabledReturnsInlineNotEnabled(t *testing.T) {
	tenantID := uuid.New()
	fake := &fakeRepo{
		emailCfg: &repository.EmailTransportConfig{TenantID: tenantID, Provider: "resend", Enabled: false},
	}
	h := newHandler(fake).WithEmailKEK(testKEK).WithEmailTransport(fakeTransportFactory(&fakeTransport{}))

	resp, err := h.SendTestEmail(context.Background(), &auditv1.SendTestEmailRequest{
		TenantId:  tenantID.String(),
		ToAddress: "ops@example.com",
	})
	if err != nil {
		t.Fatalf("unexpected RPC error: %v", err)
	}
	if resp.GetOk() || resp.GetError() != "email transport not enabled" {
		t.Errorf("expected inline not-enabled result, got Ok=%v err=%q", resp.GetOk(), resp.GetError())
	}
}

// ── ListEmailDeliveries ──────────────────────────────────────────────

func TestListEmailDeliveries_capsPageSize(t *testing.T) {
	tenantID := uuid.New()
	userID := uuid.New()
	fake := &fakeRepo{
		emailDeliveries: []*repository.EmailDelivery{
			{ID: uuid.New(), Category: "scan", Subject: "s1", ToAddress: "a@x.com", Status: "sent"},
		},
	}
	h := newHandler(fake)

	resp, err := h.ListEmailDeliveries(context.Background(), &auditv1.ListEmailDeliveriesRequest{
		TenantId: tenantID.String(),
		UserId:   userID.String(),
		PageSize: 1000, // over the cap
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.lastListLimit != maxEmailPageSize {
		t.Errorf("expected page size capped to %d, handler passed %d", maxEmailPageSize, fake.lastListLimit)
	}
	if len(resp.GetDeliveries()) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(resp.GetDeliveries()))
	}
	if resp.GetDeliveries()[0].GetStatus() != "sent" {
		t.Errorf("expected status 'sent', got %q", resp.GetDeliveries()[0].GetStatus())
	}
}

// (page-size default asserted below)

func TestListEmailDeliveries_defaultPageSize(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake)

	_, err := h.ListEmailDeliveries(context.Background(), &auditv1.ListEmailDeliveriesRequest{
		TenantId: uuid.New().String(),
		UserId:   uuid.New().String(),
		PageSize: 0, // default
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.lastListLimit != defaultEmailPageSize {
		t.Errorf("expected default page size %d, got %d", defaultEmailPageSize, fake.lastListLimit)
	}
}
