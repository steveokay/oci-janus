// Package handler — FE-API-039 per-org default retention policy + inheritance.
//
// Three new RPCs mirror the per-repo CRUD shape, plus one resolution RPC:
//
//	GetOrgRetentionPolicy        — per-org default lookup.
//	UpsertOrgRetentionPolicy     — full-replace upsert with preview semantics.
//	DeleteOrgRetentionPolicy     — drop the org default.
//	GetEffectiveRetentionPolicy  — per-repo first, then org default (if
//	                                enabled), else NotFound.
//
// Validation is shared with the per-repo handlers via retention_validation.go
// so the allowlist + value caps + regex compile checks live in exactly one
// place. The shape sharing is also intentional — RetentionPolicy carries
// both `repo_id` and `org_id`, with one always empty depending on which
// table the row came from; the GetEffective response wraps the policy with
// an `inherited_from` label so callers know which.
package handler

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// GetOrgRetentionPolicy returns the default retention policy attached to an
// org. NotFound when no row exists; the BFF maps NotFound to a typed 404
// ("no-org-default") so the UI renders the empty state cleanly.
func (h *MetadataHandler) GetOrgRetentionPolicy(ctx context.Context, req *metadatav1.GetOrgRetentionPolicyRequest) (*metadatav1.RetentionPolicy, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetOrgId() == "" {
		return nil, status.Error(codes.InvalidArgument, "org_id is required")
	}
	policy, err := h.repo.GetOrgRetentionPolicy(ctx, req.GetTenantId(), req.GetOrgId())
	if err != nil {
		return nil, mapErr(err)
	}
	return policy, nil
}

// UpsertOrgRetentionPolicy validates the request and writes through to the
// repository. preview_until is owned server-side and shared with the per-repo
// path via decidePreviewUntil. The same validation rules apply: rule kinds
// from the allowlist, values within per-kind caps, no duplicate kinds, all
// protected_tag_patterns compile as Go regexps.
//
// FK violations on org_id surface as NotFound so a "org deleted between BFF
// lookup and gRPC call" race returns 404 rather than 500.
func (h *MetadataHandler) UpsertOrgRetentionPolicy(ctx context.Context, req *metadatav1.UpsertOrgRetentionPolicyRequest) (*metadatav1.RetentionPolicy, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetOrgId() == "" {
		return nil, status.Error(codes.InvalidArgument, "org_id is required")
	}
	if err := validateRetentionRules(req.GetEnabled(), req.GetRules()); err != nil {
		return nil, err
	}
	if err := validateProtectedTagPatterns(req.GetProtectedTagPatterns()); err != nil {
		return nil, err
	}

	policy, err := h.repo.UpsertOrgRetentionPolicy(
		ctx,
		req.GetTenantId(),
		req.GetOrgId(),
		req.GetEnabled(),
		req.GetRules(),
		req.GetProtectedTagPatterns(),
		req.GetUpdatedBy(),
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return policy, nil
}

// DeleteOrgRetentionPolicy removes the org default. NotFound when no row
// exists so the BFF surfaces a 404 (callers expect to know whether they
// actually cleared something — not idempotent on purpose).
func (h *MetadataHandler) DeleteOrgRetentionPolicy(ctx context.Context, req *metadatav1.DeleteOrgRetentionPolicyRequest) (*emptypb.Empty, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetOrgId() == "" {
		return nil, status.Error(codes.InvalidArgument, "org_id is required")
	}
	if err := h.repo.DeleteOrgRetentionPolicy(ctx, req.GetTenantId(), req.GetOrgId()); err != nil {
		return nil, mapErr(err)
	}
	return &emptypb.Empty{}, nil
}

// GetEffectiveRetentionPolicy resolves what policy actually applies to a
// repo: the per-repo row if it exists (enabled or not), else the org default
// (only when enabled = TRUE), else NotFound. The response wraps the policy
// with an `inherited_from` label so the BFF + UI can render
// "(inherited from org default)" without a second round-trip.
//
// Tenant isolation is enforced in the repository SQL (the JOIN against
// repositories filters on tenant_id).
func (h *MetadataHandler) GetEffectiveRetentionPolicy(ctx context.Context, req *metadatav1.GetEffectiveRetentionPolicyRequest) (*metadatav1.EffectiveRetentionPolicy, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRepoId() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}
	res, err := h.repo.GetEffectiveRetentionPolicy(ctx, req.GetTenantId(), req.GetRepoId())
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "no effective retention policy")
		}
		return nil, mapErr(err)
	}
	return &metadatav1.EffectiveRetentionPolicy{
		Policy:        res.Policy,
		InheritedFrom: res.InheritedFrom,
		OrgId:         res.OrgID,
	}, nil
}

// LookupOrgIDByName maps an org name to its UUID inside a tenant. Read-only
// (unlike GetOrCreateOrganization), so a missing org returns NotFound. The
// management BFF uses this to translate /api/v1/orgs/{org} URLs to the
// org_id required by the per-org retention RPCs without an unintended row
// insert. Kept in this file because the FE-API-039 routes are the only
// caller today; promote to grpc.go if/when a second route picks it up.
func (h *MetadataHandler) LookupOrgIDByName(ctx context.Context, req *metadatav1.LookupOrgIDByNameRequest) (*metadatav1.LookupOrgIDByNameResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	orgID, err := h.repo.LookupOrgIDByName(ctx, req.GetTenantId(), req.GetName())
	if err != nil {
		return nil, mapErr(err)
	}
	return &metadatav1.LookupOrgIDByNameResponse{OrgId: orgID}, nil
}
