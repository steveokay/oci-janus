package handler

// grpc_notification_webhook.go — FUT-019 webhook notification channel.
//
// gRPC handlers for the per-tenant org webhook config + the "send test" probe.
// Mirrors grpc_email.go: the handler owns the secret lifecycle —
//   - the HMAC secret is sealed (AES-256-GCM via sealSecret / the webhook KEK)
//     before persistence; an empty request secret keeps the stored ciphertext.
//   - the raw secret is NEVER returned over the wire; Get/Put surface the
//     has_secret boolean instead.
//   - SendTestNotificationWebhook decrypts the secret just long enough to sign
//     one canned payload, POSTs it, and records the (redacted) outcome. A
//     disabled/unconfigured transport is reported inline (Ok=false) rather than
//     as an RPC error so the settings panel can render it.

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
	// Validate the URL only when present (a config may be saved disabled with
	// categories pre-selected before the URL is known).
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
	secretCT, err := sealSecret(h.webhookKEK, existingSecret, req.GetSecret(), "NOTIFY_WEBHOOK_KEY_HEX")
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
		errStr = truncateString(sendErr.Error())
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
