package handler

// grpc_email.go — FUT-019 Phase 3 (email notification channel).
//
// gRPC handlers for the per-tenant email transport config + the per-user
// delivery-log query. The handler owns the secret lifecycle:
//   - resend_api_key + smtp_password are sealed (AES-256-GCM via
//     libs/crypto/aes) before persistence, using the injected email KEK —
//     mirrors audit_export.go's resolveSecret/openSecret split.
//   - the raw secret is NEVER returned over the wire; the Get/Put paths
//     surface has_resend_key / has_smtp_password booleans instead.
//   - SendTestEmail decrypts the secrets just long enough to build a
//     transport, sends one canned message, and records the outcome —
//     transport errors are already redacted of credentials by the email
//     package, and are surfaced inline (Ok=false) rather than as RPC errors.

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/email"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// defaultEmailPageSize is the page size applied when the caller leaves
// page_size at zero on ListEmailDeliveries.
const defaultEmailPageSize = 50

// maxEmailPageSize hard-caps the page size so a single call can't pull an
// unbounded slice of the delivery log across the wire.
const maxEmailPageSize = 50

// WithEmailKEK wires the AES-256-GCM key used to seal the email transport
// secrets (resend_api_key + smtp_password). Task 8 decodes NOTIFY_EMAIL_KEY_HEX
// and passes the 32-byte key here. When unset, a Put carrying a secret fails
// closed with FailedPrecondition.
func (h *GRPCHandler) WithEmailKEK(key []byte) *GRPCHandler {
	h.emailKEK = key
	return h
}

// WithEmailTransport injects the transport factory used by SendTestEmail. Task 8
// wires email.NewTransport; tests inject a fake. When left nil the handler
// defaults to email.NewTransport at call time.
func (h *GRPCHandler) WithEmailTransport(fn func(email.DecryptedConfig) (email.Transport, error)) *GRPCHandler {
	h.newEmailTransport = fn
	return h
}

// GetEmailTransportConfig returns the tenant's email transport config with the
// provider secrets masked to has_* booleans. A tenant that never saved a config
// gets sensible form defaults (resend provider, STARTTLS) rather than a NotFound.
func (h *GRPCHandler) GetEmailTransportConfig(ctx context.Context, req *auditv1.GetEmailTransportConfigRequest) (*auditv1.EmailTransportConfig, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	row, err := h.repo.GetEmailTransportConfig(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get email transport config: %v", err)
	}
	return emailConfigToProto(row), nil
}

// PutEmailTransportConfig upserts the tenant's email transport config. Each
// secret is sealed with sealSecret: an empty value keeps the stored ciphertext,
// a non-empty value re-encrypts under the email KEK. KEK version is stamped to
// 1. The response re-runs the Get mapping so secrets stay masked.
func (h *GRPCHandler) PutEmailTransportConfig(ctx context.Context, req *auditv1.PutEmailTransportConfigRequest) (*auditv1.EmailTransportConfig, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	// Load the existing row so an empty secret in the request preserves the
	// stored ciphertext (the FE never receives secrets, so it can't re-send
	// them when editing an unrelated field).
	existing, err := h.repo.GetEmailTransportConfig(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load existing email config: %v", err)
	}
	var existingResend, existingSMTP []byte
	if existing != nil {
		existingResend = existing.ResendAPIKeyEnc
		existingSMTP = existing.SMTPPasswordEnc
	}

	resendCT, err := sealSecret(h.emailKEK, existingResend, req.GetResendApiKey(), "NOTIFY_EMAIL_KEY_HEX")
	if err != nil {
		return nil, err
	}
	smtpCT, err := sealSecret(h.emailKEK, existingSMTP, req.GetSmtpPassword(), "NOTIFY_EMAIL_KEY_HEX")
	if err != nil {
		return nil, err
	}

	cfg := repository.EmailTransportConfig{
		TenantID:        tenantID,
		Provider:        req.GetProvider(),
		Enabled:         req.GetEnabled(),
		FromAddress:     req.GetFromAddress(),
		FromName:        req.GetFromName(),
		ResendAPIKeyEnc: resendCT,
		SMTPHost:        req.GetSmtpHost(),
		SMTPPort:        int(req.GetSmtpPort()),
		SMTPUsername:    req.GetSmtpUsername(),
		SMTPPasswordEnc: smtpCT,
		SMTPTLSMode:     req.GetSmtpTlsMode(),
		KEKVersion:      1,
	}
	if ub := req.GetUpdatedBy(); ub != "" {
		if u, perr := uuid.Parse(ub); perr == nil {
			cfg.UpdatedBy = &u
		}
	}

	if err := h.repo.UpsertEmailTransportConfig(ctx, cfg); err != nil {
		return nil, status.Errorf(codes.Internal, "upsert email transport config: %v", err)
	}

	// Reload + re-mask so the response reflects the freshly stored row without
	// ever echoing the secret material back.
	row, err := h.repo.GetEmailTransportConfig(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "reload email config: %v", err)
	}
	return emailConfigToProto(row), nil
}

// SendTestEmail decrypts the stored secrets, builds a transport, and sends one
// canned test message to req.ToAddress. A disabled/unconfigured transport is
// reported inline (Ok=false) rather than as an RPC error so the settings panel
// can render it. The outcome (and any redacted error) is recorded on the config
// row via UpdateEmailTestResult.
func (h *GRPCHandler) SendTestEmail(ctx context.Context, req *auditv1.SendTestEmailRequest) (*auditv1.SendTestEmailResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if strings.TrimSpace(req.GetToAddress()) == "" {
		return nil, status.Error(codes.InvalidArgument, "to_address is required")
	}

	row, err := h.repo.GetEmailTransportConfig(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load email config: %v", err)
	}
	if row == nil || !row.Enabled || row.Provider == "" {
		// Inline result, not an RPC error — the panel shows this verbatim.
		return &auditv1.SendTestEmailResponse{Ok: false, Error: "email transport not enabled"}, nil
	}

	// Decrypt the provider secrets. openSecret (audit_export.go) returns "" for
	// an unset column so a resend-only config doesn't fail on the smtp password.
	resendKey, err := openSecret(h.emailKEK, row.ResendAPIKeyEnc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt resend api key: %v", err)
	}
	smtpPass, err := openSecret(h.emailKEK, row.SMTPPasswordEnc)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt smtp password: %v", err)
	}

	cfg := email.DecryptedConfig{
		Provider:     row.Provider,
		FromAddress:  row.FromAddress,
		FromName:     row.FromName,
		ResendAPIKey: resendKey,
		SMTPHost:     row.SMTPHost,
		SMTPPort:     row.SMTPPort,
		SMTPUsername: row.SMTPUsername,
		SMTPPassword: smtpPass,
		SMTPTLSMode:  row.SMTPTLSMode,
	}

	// Default the factory to the production transport when unset (Task 8 wires
	// it explicitly; tests inject a fake).
	newTransport := h.newEmailTransport
	if newTransport == nil {
		newTransport = email.NewTransport
	}

	transport, err := newTransport(cfg)
	if err != nil {
		// Building the transport failed (e.g. missing key for the provider).
		// Record + surface the redacted error inline.
		errStr := truncateString(err.Error())
		_ = h.repo.UpdateEmailTestResult(ctx, tenantID, false, errStr)
		return &auditv1.SendTestEmailResponse{Ok: false, Error: errStr}, nil
	}

	msg := email.Message{
		To:       req.GetToAddress(),
		Subject:  "OCI Janus — test notification email",
		HTMLBody: "<p>This is a test email from your OCI Janus registry. If you received it, your email transport is configured correctly.</p>",
		TextBody: "This is a test email from your OCI Janus registry. If you received it, your email transport is configured correctly.",
	}
	sendErr := transport.Send(ctx, msg)
	ok := sendErr == nil
	var errStr string
	if sendErr != nil {
		// The transport already redacts secrets from its error string.
		errStr = truncateString(sendErr.Error())
	}
	// Best-effort: recording the result must not mask the send outcome.
	_ = h.repo.UpdateEmailTestResult(ctx, tenantID, ok, errStr)

	return &auditv1.SendTestEmailResponse{Ok: ok, Error: errStr}, nil
}

// ListEmailDeliveries returns a user's recent email deliveries, newest first,
// scoped to a single tenant. page_size defaults to and is capped at 50.
func (h *GRPCHandler) ListEmailDeliveries(ctx context.Context, req *auditv1.ListEmailDeliveriesRequest) (*auditv1.ListEmailDeliveriesResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	userID, err := uuid.Parse(req.GetUserId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid user_id")
	}

	pageSize := int(req.GetPageSize())
	if pageSize <= 0 {
		pageSize = defaultEmailPageSize
	}
	if pageSize > maxEmailPageSize {
		pageSize = maxEmailPageSize
	}

	rows, err := h.repo.ListEmailDeliveries(ctx, tenantID, userID, pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list email deliveries: %v", err)
	}

	out := make([]*auditv1.EmailDelivery, 0, len(rows))
	for _, r := range rows {
		d := &auditv1.EmailDelivery{
			Id:        r.ID.String(),
			Category:  r.Category,
			Subject:   r.Subject,
			ToAddress: r.ToAddress,
			Status:    r.Status,
			LastError: r.LastError,
			CreatedAt: timestamppb.New(r.CreatedAt),
		}
		if r.SentAt != nil {
			d.SentAt = timestamppb.New(*r.SentAt)
		}
		out = append(out, d)
	}
	return &auditv1.ListEmailDeliveriesResponse{Deliveries: out}, nil
}

// emailConfigToProto maps a repository config row onto the wire proto, masking
// the provider secrets to has_* booleans. A nil row (tenant never configured
// email) maps to form defaults so the FE renders a fresh, editable form.
func emailConfigToProto(c *repository.EmailTransportConfig) *auditv1.EmailTransportConfig {
	if c == nil {
		return &auditv1.EmailTransportConfig{Provider: "resend", SmtpTlsMode: "starttls"}
	}
	out := &auditv1.EmailTransportConfig{
		Provider:        c.Provider,
		Enabled:         c.Enabled,
		FromAddress:     c.FromAddress,
		FromName:        c.FromName,
		SmtpHost:        c.SMTPHost,
		SmtpPort:        int32(c.SMTPPort),
		SmtpUsername:    c.SMTPUsername,
		SmtpTlsMode:     c.SMTPTLSMode,
		HasResendKey:    len(c.ResendAPIKeyEnc) > 0,
		HasSmtpPassword: len(c.SMTPPasswordEnc) > 0,
		LastTestError:   c.LastTestError,
	}
	if c.LastTestAt != nil {
		out.LastTestAt = timestamppb.New(*c.LastTestAt)
	}
	if c.LastTestOK != nil {
		out.LastTestOk = *c.LastTestOK
	}
	return out
}

// sealSecret encodes the "keep existing vs rotate" contract for a sealed
// secret column. An empty plaintext keeps the stored ciphertext; a non-empty
// plaintext is freshly encrypted under the KEK. Returns FailedPrecondition when
// a rotation is requested but the KEK isn't wired — keyEnvName names the env var
// the operator must set (e.g. NOTIFY_EMAIL_KEY_HEX / NOTIFY_WEBHOOK_KEY_HEX) so
// the error points at the right channel rather than a silent no-op.
func sealSecret(kek, existing []byte, plaintext, keyEnvName string) ([]byte, error) {
	if plaintext == "" {
		return existing, nil // keep existing
	}
	if len(kek) == 0 {
		return nil, status.Errorf(codes.FailedPrecondition, "secrets key (%s) not configured", keyEnvName)
	}
	ct, err := aes.Encrypt([]byte(plaintext), kek)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt secret: %v", err)
	}
	return ct, nil
}
