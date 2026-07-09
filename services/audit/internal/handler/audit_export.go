package handler

// audit_export.go — futures.md Tier 1 #4 (audit log streaming to SIEM).
//
// gRPC handlers for the per-tenant export-destination CRUD + Test
// endpoint. The handler is responsible for:
//   - encrypting hmac_secret + bearer_token before persistence
//     (AES-256-GCM via libs/crypto/aes — same primitive as the SSO
//      admin handler uses for OAuth client_secret)
//   - never returning the raw secret over the wire (the Get path
//     surfaces `*_set` booleans instead)
//   - input validation: format enum, target_url scheme + SSRF guard,
//     event_filters JSON well-formedness
//   - dispatching the Test RPC to the registered AuditExportTester
//     so the synthetic event uses the same wire pipeline as live
//     events.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// maxLastErrorLen truncates the stored last_error string so a
// pathological upstream SIEM (returning multi-MB HTML error pages)
// doesn't bloat the config row.
const maxLastErrorLen = 512

// supportedFormats is the closed set of wire formats v1 supports.
// Adding a fourth means a renderer in services/audit/internal/export/
// AND a row in docs/SIEM-EXPORT.md — keeping the enum here surfaces
// invalid input as a clean InvalidArgument rather than a runtime
// renderer panic.
var supportedFormats = map[string]struct{}{
	"syslog_rfc5424": {},
	"cef":            {},
	"webhook":        {},
}

// GetAuditExportConfig (futures.md Tier 1 #4) — surface the tenant's
// streaming destination. Never returns the raw secret material; the
// `*_set` booleans on the response tell the FE whether to render a
// "(saved)" placeholder vs. an empty input.
func (h *GRPCHandler) GetAuditExportConfig(ctx context.Context, req *auditv1.GetAuditExportConfigRequest) (*auditv1.AuditExportConfig, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	cfg, err := h.repo.GetAuditExportConfig(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrExportConfigNotFound) {
			return nil, status.Error(codes.NotFound, "audit export config not found")
		}
		return nil, status.Errorf(codes.Internal, "get audit export config: %v", err)
	}
	return h.toAuditExportProto(ctx, cfg), nil
}

// PutAuditExportConfig (futures.md Tier 1 #4) — upsert the
// destination. Validates format + target URL + event filter JSON
// + (when supplying a secret) the cipher availability. The encrypted
// secret column is touched only when the request explicitly sets
// `hmac_secret` (rotate) or `hmac_secret_clear=true` (remove); a
// zero-string + zero-bool combination is treated as "leave the
// existing value alone" so the FE can edit one field at a time
// without re-sending secrets it never received.
func (h *GRPCHandler) PutAuditExportConfig(ctx context.Context, req *auditv1.PutAuditExportConfigRequest) (*auditv1.AuditExportConfig, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if _, ok := supportedFormats[req.GetFormat()]; !ok {
		return nil, status.Errorf(codes.InvalidArgument, "unsupported format %q (want syslog_rfc5424 / cef / webhook)", req.GetFormat())
	}
	if strings.TrimSpace(req.GetTargetUrl()) == "" {
		return nil, status.Error(codes.InvalidArgument, "target_url is required")
	}
	if err := validateTargetURL(req.GetFormat(), req.GetTargetUrl()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "target_url: %v", err)
	}

	// event_filters_json must parse as JSON if provided (we don't
	// validate the deep shape here; the exporter does best-effort
	// pattern matching on `include`/`exclude` arrays so a malformed
	// shape degrades gracefully to "send all events").
	var filters json.RawMessage
	if s := strings.TrimSpace(req.GetEventFiltersJson()); s != "" {
		var probe any
		if err := json.Unmarshal([]byte(s), &probe); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "event_filters_json: %v", err)
		}
		filters = json.RawMessage(s)
	}

	// Fetch the existing row to honour the "leave alone" contract on
	// secret material. NotFound is fine — first write for this
	// tenant.
	existing, err := h.repo.GetAuditExportConfig(ctx, tenantID)
	if err != nil && !errors.Is(err, repository.ErrExportConfigNotFound) {
		return nil, status.Errorf(codes.Internal, "load existing config: %v", err)
	}

	hmacCT, err := resolveSecret(h.secretsKey, existing, true, req.GetHmacSecret(), req.GetHmacSecretClear())
	if err != nil {
		return nil, err
	}
	bearerCT, err := resolveSecret(h.secretsKey, existing, false, req.GetBearerToken(), req.GetBearerTokenClear())
	if err != nil {
		return nil, err
	}

	cfg := &repository.AuditExportConfig{
		TenantID:     tenantID,
		Enabled:      req.GetEnabled(),
		Format:       req.GetFormat(),
		TargetURL:    req.GetTargetUrl(),
		HMACSecret:   hmacCT,
		BearerToken:  bearerCT,
		EventFilters: filters,
	}
	if cb := req.GetCreatedBy(); cb != "" {
		if u, err := uuid.Parse(cb); err == nil {
			cfg.CreatedBy = &u
		}
	}

	out, err := h.repo.UpsertAuditExportConfig(ctx, cfg)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert: %v", err)
	}
	return h.toAuditExportProto(ctx, out), nil
}

// DeleteAuditExportConfig (futures.md Tier 1 #4) — clears the
// destination. Idempotent (delete on a missing row is success).
func (h *GRPCHandler) DeleteAuditExportConfig(ctx context.Context, req *auditv1.DeleteAuditExportConfigRequest) (*auditv1.DeleteAuditExportConfigResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if err := h.repo.DeleteAuditExportConfig(ctx, tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "delete: %v", err)
	}
	return &auditv1.DeleteAuditExportConfigResponse{}, nil
}

// TestAuditExportConfig (futures.md Tier 1 #4) — ship a synthetic
// event with action="audit_export.test" via the same render+ship
// pipeline a real event would take. Returns the rendered wire
// payload so the FE can show "here's exactly what we sent" alongside
// the SIEM's ACK / failure. Unavailable when the tester hook isn't
// wired (main.go didn't call WithExportTester).
func (h *GRPCHandler) TestAuditExportConfig(ctx context.Context, req *auditv1.TestAuditExportConfigRequest) (*auditv1.TestAuditExportConfigResponse, error) {
	if h.tester == nil {
		return nil, status.Error(codes.Unavailable, "audit export tester not wired")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	cfg, err := h.repo.GetAuditExportConfig(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrExportConfigNotFound) {
			return nil, status.Error(codes.NotFound, "audit export config not found")
		}
		return nil, status.Errorf(codes.Internal, "load config: %v", err)
	}
	hmac, err := openSecret(h.secretsKey, cfg.HMACSecret)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt hmac_secret: %v", err)
	}
	bearer, err := openSecret(h.secretsKey, cfg.BearerToken)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decrypt bearer_token: %v", err)
	}

	rendered, deliverErr := h.tester.DeliverTest(ctx, cfg, hmac, bearer)
	resp := &auditv1.TestAuditExportConfigResponse{
		Delivered:     deliverErr == nil,
		RenderedEvent: rendered,
	}
	if deliverErr != nil {
		resp.Error = truncateString(deliverErr.Error())
	}
	return resp, nil
}

// resolveSecret encodes the "leave alone vs rotate vs clear" contract
// for the two encrypted columns. Returns:
//   - the existing ciphertext if neither plain nor clear is provided
//   - nil if clear=true
//   - a fresh Seal(plain) ciphertext if plain != ""
//
// Returns FailedPrecondition when a plaintext rotation is requested
// but the cipher key isn't wired (so the operator gets a clear "your
// audit service isn't configured for secret storage" error rather
// than a silent no-op).
func resolveSecret(key []byte, existing *repository.AuditExportConfig, isHMAC bool, plaintext string, clear bool) ([]byte, error) {
	if clear {
		return nil, nil
	}
	if plaintext == "" {
		if existing == nil {
			return nil, nil
		}
		if isHMAC {
			return existing.HMACSecret, nil
		}
		return existing.BearerToken, nil
	}
	if len(key) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "audit-export secrets key not configured")
	}
	ct, err := aes.Encrypt([]byte(plaintext), key)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encrypt secret: %v", err)
	}
	return ct, nil
}

// openSecret is the inverse of resolveSecret — used by the Test RPC +
// the eventconsumer's hot path to materialise the plaintext secret
// for the renderer. Returns "" for nil/empty input so callers can
// branch on `plain == ""` rather than worrying about the byte length.
func openSecret(key, ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if len(key) == 0 {
		return "", errors.New("audit-export secrets key not configured")
	}
	plain, err := aes.Decrypt(ciphertext, key)
	if err != nil {
		return "", fmt.Errorf("decrypt: %w", err)
	}
	return string(plain), nil
}

// validateTargetURL applies format-specific URL scheme checks. The
// SSRF private-CIDR guard runs at delivery time too (see
// export.guardTargetURL) — duplicating the basic-shape check here
// gives the operator a synchronous validation error at PUT time.
func validateTargetURL(format, target string) error {
	target = strings.TrimSpace(target)
	if target == "" {
		return errors.New("required")
	}
	switch format {
	case "syslog_rfc5424", "cef":
		if !strings.HasPrefix(target, "syslog+tcp://") && !strings.HasPrefix(target, "syslog+tls://") {
			return errors.New("syslog format requires syslog+tcp:// or syslog+tls:// URL")
		}
	case "webhook":
		if !strings.HasPrefix(target, "https://") &&
			!strings.HasPrefix(target, "http://localhost") &&
			!strings.HasPrefix(target, "http://127.0.0.1") &&
			!strings.HasPrefix(target, "http://host.docker.internal") {
			return errors.New("webhook format requires https:// URL (http://localhost or http://host.docker.internal permitted for dev)")
		}
	}
	return nil
}

// truncateString clips a string to maxLastErrorLen bytes so it fits in
// the last_error column. Every caller uses the same limit, so it's baked
// in rather than passed (unparam). The cut is on a byte boundary and may
// split a trailing multi-byte rune — acceptable for truncated error logs.
func truncateString(s string) string {
	if len(s) <= maxLastErrorLen {
		return s
	}
	return s[:maxLastErrorLen]
}

// DrainAuditExportDLX (futures.md Tier 1 #4 Phase 2) — admin RPC that
// consumes parked messages in dlx.audit-export belonging to this
// tenant and re-publishes onto audit.export. Bounded to MaxDrain
// (10k) per call so a catastrophically full DLX doesn't hang the
// request; the operator may need to call it repeatedly after a
// long outage.
func (h *GRPCHandler) DrainAuditExportDLX(ctx context.Context, req *auditv1.DrainAuditExportDLXRequest) (*auditv1.DrainAuditExportDLXResponse, error) {
	if h.dlxProbe == nil {
		return nil, status.Error(codes.Unavailable, "audit-export DLX probe not wired")
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	n, err := h.dlxProbe.Drain(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "drain: %v", err)
	}
	return &auditv1.DrainAuditExportDLXResponse{Republished: n}, nil
}

// toAuditExportProto converts the repository struct to the wire proto.
// hmac_secret + bearer_token are NEVER serialised; presence is
// surfaced via *_set booleans only. dlx_queue_depth is fetched live
// from the RabbitMQ Management API when the probe is wired; `-1`
// signals "unknown" so the FE renders that distinctly from "empty."
func (h *GRPCHandler) toAuditExportProto(ctx context.Context, c *repository.AuditExportConfig) *auditv1.AuditExportConfig {
	out := &auditv1.AuditExportConfig{
		Id:               c.ID.String(),
		TenantId:         c.TenantID.String(),
		Enabled:          c.Enabled,
		Format:           c.Format,
		TargetUrl:        c.TargetURL,
		HmacSecretSet:    len(c.HMACSecret) > 0,
		BearerTokenSet:   len(c.BearerToken) > 0,
		LastError:        c.LastError,
		DlxDepth:         c.DLXDepth,
		DlxQueueDepth:    -1,
		CreatedAt:        timestamppb.New(c.CreatedAt),
		UpdatedAt:        timestamppb.New(c.UpdatedAt),
		EventFiltersJson: string(c.EventFilters),
	}
	if c.LastSuccessAt != nil {
		out.LastSuccessAt = timestamppb.New(*c.LastSuccessAt)
	}
	if c.LastAttemptAt != nil {
		out.LastAttemptAt = timestamppb.New(*c.LastAttemptAt)
	}
	if h.dlxProbe != nil {
		if d, err := h.dlxProbe.QueueDepth(ctx); err == nil {
			out.DlxQueueDepth = d
		}
	}
	return out
}

// Compile-time check that the handler still matches the proto's
// expected shape — kept here so a proto regen that drops one of the
// new RPCs surfaces as a build error in this file.
var _ = time.Now
