package handler

// grpc_pr_registry.go — FUT-023 Phase 1 (ephemeral PR-scoped registries).
//
// gRPC handlers for the five PR-registry RPCs:
//
//   - GetPRRegistryConfig / PutPRRegistryConfig — per-tenant config CRUD. The
//     handler owns the webhook-secret lifecycle: Put seals the incoming secret
//     with the AES-256-GCM KEK (mirroring audit's grpc_email.go sealSecret
//     pattern — an empty incoming secret keeps the stored ciphertext), and the
//     raw secret is NEVER returned over the wire (Get/Put surface has_secret).
//   - HandlePREvent — the webhook dispatch entry point. Delegates the entire
//     authenticate → parse → provision/promote/teardown flow to
//     prregistry.Service, then maps its package-local Outcome onto the proto
//     HandlePREventResponse_OUTCOME_* enum.
//   - ListPRNamespaces — paginated lifecycle-row list (defaults status='active').
//   - DeleteOrganization — the teardown primitive for a PR org namespace.
//
// When PR_REGISTRY_KEY_HEX is unset the prregistry.Service is not wired
// (prSvc==nil / prKEK empty): HandlePREvent returns OUTCOME_DISABLED and a Put
// carrying a secret fails closed with FailedPrecondition.

import (
	"context"
	"errors"
	"regexp"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	grpcmw "github.com/steveokay/oci-janus/libs/middleware/grpc"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/prregistry"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// prOrgNameRE mirrors the platform org-name allowlist (CLAUDE.md §7). The
// metadata handler package has no existing compiled org validator (the
// management BFF owns validateOrgName), so the regex is declared here to
// gate promote_target_org on the PutPRRegistryConfig write path (SEC-084) —
// a bad target org must be rejected at ingest, not discovered at merge time.
var prOrgNameRE = regexp.MustCompile(`^[a-z0-9-]{2,64}$`)

// GetPRRegistryConfig returns the tenant's PR-registry config with the webhook
// secret masked to a has_secret boolean. A tenant that never wrote a config
// gets sensible defaults (disabled, no secret, no promote target) rather than
// a NotFound — absence is the normal state for a tenant that hasn't touched the
// feature.
func (h *MetadataHandler) GetPRRegistryConfig(ctx context.Context, req *metadatav1.GetPRRegistryConfigRequest) (*metadatav1.PRRegistryConfig, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	cfg, err := h.repo.GetPRRegistryConfig(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Never configured — return form defaults so the FE renders a
			// fresh, editable panel rather than a 404.
			return &metadatav1.PRRegistryConfig{
				TenantId:  req.GetTenantId(),
				Enabled:   false,
				HasSecret: false,
			}, nil
		}
		return nil, mapErr(err)
	}
	return prRegistryConfigToProto(cfg), nil
}

// PutPRRegistryConfig upserts the tenant's PR-registry config. The webhook
// secret follows the keep-vs-replace contract: an empty req.WebhookSecret
// keeps the stored ciphertext (the FE never receives the secret, so it can't
// re-send it when editing an unrelated field); a non-empty value is re-sealed
// under the KEK. A non-empty secret with no KEK wired fails closed with
// FailedPrecondition. The response re-runs the Get mapping so the secret stays
// masked.
func (h *MetadataHandler) PutPRRegistryConfig(ctx context.Context, req *metadatav1.PutPRRegistryConfigRequest) (*metadatav1.PRRegistryConfig, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}

	// Validate the promote target against the org-name allowlist (SEC-084).
	// Empty is allowed (no promote target); a non-empty value that fails the
	// regex is rejected here so an unusable target can never be persisted —
	// promote-on-merge would otherwise derive an invalid destination org.
	if org := req.GetPromoteTargetOrg(); org != "" && !prOrgNameRE.MatchString(org) {
		return nil, status.Error(codes.InvalidArgument, "promote_target_org must match ^[a-z0-9-]{2,64}$")
	}

	// Load the existing row so an empty incoming secret preserves the stored
	// ciphertext. ErrNotFound is the first-write case — no existing secret.
	var existingEnc []byte
	var existingKEKVersion int16
	existing, err := h.repo.GetPRRegistryConfig(ctx, tenantID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return nil, mapErr(err)
	}
	if existing != nil {
		existingEnc = existing.WebhookSecretEnc
		existingKEKVersion = existing.KEKVersion
	}

	// Seal the secret: empty ⇒ keep existing; non-empty ⇒ re-encrypt under the
	// KEK (FailedPrecondition when the KEK isn't wired).
	secretEnc := existingEnc
	kekVersion := existingKEKVersion
	if req.GetWebhookSecret() != "" {
		if len(h.prKEK) == 0 {
			return nil, status.Error(codes.FailedPrecondition, "PR_REGISTRY_KEY_HEX not configured")
		}
		ct, err := aes.Encrypt([]byte(req.GetWebhookSecret()), h.prKEK)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encrypt webhook secret: %v", err)
		}
		secretEnc = ct
		// Stamp KEK version 1 — Phase 1 has a single active KEK; the column
		// exists so a future rotation can distinguish generations.
		kekVersion = 1
	}

	cfg := repository.PRRegistryConfig{
		TenantID:         tenantID,
		Enabled:          req.GetEnabled(),
		WebhookSecretEnc: secretEnc,
		KEKVersion:       kekVersion,
		PromoteTargetOrg: req.GetPromoteTargetOrg(),
	}
	// updated_by is optional — a CLI/bot write persists NULL rather than the
	// zero UUID. A non-empty value MUST be a valid UUID.
	if ub := req.GetUpdatedBy(); ub != "" {
		u, err := uuid.Parse(ub)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "updated_by must be a UUID when provided")
		}
		cfg.UpdatedBy = &u
	}

	if err := h.repo.UpsertPRRegistryConfig(ctx, cfg); err != nil {
		return nil, mapErr(err)
	}

	// Reload + re-mask so the response reflects the freshly stored row without
	// ever echoing the secret material back.
	row, err := h.repo.GetPRRegistryConfig(ctx, tenantID)
	if err != nil {
		return nil, mapErr(err)
	}
	return prRegistryConfigToProto(row), nil
}

// HandlePREvent authenticates + dispatches one SCM webhook via
// prregistry.Service and maps the outcome onto the proto enum.
//
// When the feature is disabled (KEK unset ⇒ prSvc==nil) OR the tenant has no
// config row, the RPC returns OUTCOME_DISABLED — a normal fail-closed state,
// not an error. A signature mismatch surfaces as PermissionDenied. Any other
// non-nil error (a promote/store failure) is returned as Internal so it is NOT
// swallowed: the namespace stays intact for GitHub to retry.
func (h *MetadataHandler) HandlePREvent(ctx context.Context, req *metadatav1.HandlePREventRequest) (*metadatav1.HandlePREventResponse, error) {
	// The SCM webhook receiver (registry-management) is unauthenticated and
	// carries no tenant on the request body. In single mode SingleTenantInjector
	// has populated x-tenant-id on the context with the bootstrap tenant, so fall
	// back to it when the caller left tenant_id empty.
	rawTenant := req.GetTenantId()
	if rawTenant == "" {
		rawTenant = grpcmw.TenantIDFromIncomingContext(ctx)
	}
	tenantID, err := uuid.Parse(rawTenant)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	// Feature not wired — fail closed with DISABLED rather than dereferencing a
	// nil Service.
	if h.prSvc == nil {
		return &metadatav1.HandlePREventResponse{Outcome: metadatav1.HandlePREventResponse_OUTCOME_DISABLED}, nil
	}

	cfg, err := h.repo.GetPRRegistryConfig(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// No config for this tenant — the integration is off.
			return &metadatav1.HandlePREventResponse{Outcome: metadatav1.HandlePREventResponse_OUTCOME_DISABLED}, nil
		}
		return nil, mapErr(err)
	}

	outcome, orgName, err := h.prSvc.HandleEvent(ctx, *cfg, req.GetProvider(), req.GetRawBody(), req.GetSignature(), req.GetEvent())
	if err != nil {
		if errors.Is(err, prregistry.ErrSignatureMismatch) {
			return nil, status.Error(codes.PermissionDenied, "webhook signature mismatch")
		}
		// Any other error (promote/store failure) must NOT be swallowed —
		// Internal so GitHub retries and the namespace survives.
		return nil, status.Errorf(codes.Internal, "handle pr event: %v", err)
	}

	return &metadatav1.HandlePREventResponse{
		Outcome: outcomeToProto(outcome),
		OrgName: orgName,
	}, nil
}

// ListPRNamespaces returns a tenant's PR namespaces, newest first. An empty
// status defaults to "active" per the design (§6): callers typically want the
// live namespaces. Pass "torn_down" for the history, or the handler cannot
// express "all states" from an empty string — that's intentional, matching the
// proto field comment ('active'(default)|'torn_down'|”(all) is documented, but
// the default-on-empty keeps the common list scoped to live namespaces).
func (h *MetadataHandler) ListPRNamespaces(ctx context.Context, req *metadatav1.ListPRNamespacesRequest) (*metadatav1.ListPRNamespacesResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	status_ := req.GetStatus()
	if status_ == "" {
		status_ = "active"
	}
	rows, next, err := h.repo.ListPRNamespaces(ctx, tenantID, status_, int(req.GetPageSize()), req.GetPageToken())
	if err != nil {
		// A malformed page_token is caller error — surface InvalidArgument
		// (400) rather than letting it flow through mapErr → MapDBError →
		// Internal (500). Checked BEFORE the generic mapErr (PR #293 review).
		if errors.Is(err, repository.ErrInvalidPageToken) {
			return nil, status.Error(codes.InvalidArgument, "invalid page_token")
		}
		return nil, mapErr(err)
	}
	out := &metadatav1.ListPRNamespacesResponse{
		Namespaces:    make([]*metadatav1.PRNamespace, 0, len(rows)),
		NextPageToken: next,
	}
	for i := range rows {
		out.Namespaces = append(out.Namespaces, prNamespaceToProto(&rows[i]))
	}
	return out, nil
}

// DeleteOrganization deletes an org scoped by (tenant_id, org_id). Backs the
// teardown primitive; ErrNotFound maps to NotFound.
func (h *MetadataHandler) DeleteOrganization(ctx context.Context, req *metadatav1.DeleteOrganizationRequest) (*emptypb.Empty, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	orgID, err := uuid.Parse(req.GetOrgId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "org_id must be a UUID")
	}
	return &emptypb.Empty{}, mapErr(h.repo.DeleteOrganization(ctx, tenantID, orgID))
}

// ── mapping helpers ──────────────────────────────────────────────────────────

// prRegistryConfigToProto maps a repository config row onto the wire proto,
// masking the webhook secret to has_secret. The secret ciphertext is NEVER put
// on the wire.
func prRegistryConfigToProto(c *repository.PRRegistryConfig) *metadatav1.PRRegistryConfig {
	out := &metadatav1.PRRegistryConfig{
		TenantId:         c.TenantID.String(),
		Enabled:          c.Enabled,
		HasSecret:        len(c.WebhookSecretEnc) > 0,
		PromoteTargetOrg: c.PromoteTargetOrg,
	}
	if !c.UpdatedAt.IsZero() {
		out.UpdatedAt = timestamppb.New(c.UpdatedAt)
	}
	return out
}

// prNamespaceToProto maps a repository lifecycle row onto the wire proto.
// torn_down_at is nil-safe (a still-active namespace has no teardown time).
func prNamespaceToProto(ns *repository.PRNamespace) *metadatav1.PRNamespace {
	out := &metadatav1.PRNamespace{
		Provider:   ns.Provider,
		SourceRepo: ns.SourceRepo,
		PrNumber:   int32(ns.PRNumber),
		OrgName:    ns.OrgName,
		Status:     ns.Status,
	}
	if !ns.CreatedAt.IsZero() {
		out.CreatedAt = timestamppb.New(ns.CreatedAt)
	}
	if ns.TornDownAt != nil {
		out.TornDownAt = timestamppb.New(*ns.TornDownAt)
	}
	return out
}

// outcomeToProto maps the package-local prregistry.Outcome onto the proto
// enum. An unrecognised outcome falls through to OUTCOME_UNSPECIFIED (defensive
// — every current Outcome is covered).
func outcomeToProto(o prregistry.Outcome) metadatav1.HandlePREventResponse_Outcome {
	switch o {
	case prregistry.OutcomeIgnored:
		return metadatav1.HandlePREventResponse_OUTCOME_IGNORED
	case prregistry.OutcomeProvisioned:
		return metadatav1.HandlePREventResponse_OUTCOME_PROVISIONED
	case prregistry.OutcomePromotedAndTornDown:
		return metadatav1.HandlePREventResponse_OUTCOME_PROMOTED_AND_TORN_DOWN
	case prregistry.OutcomeTornDown:
		return metadatav1.HandlePREventResponse_OUTCOME_TORN_DOWN
	case prregistry.OutcomeDisabled:
		return metadatav1.HandlePREventResponse_OUTCOME_DISABLED
	default:
		return metadatav1.HandlePREventResponse_OUTCOME_UNSPECIFIED
	}
}
