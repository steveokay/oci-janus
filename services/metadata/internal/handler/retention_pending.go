// Package handler — FE-API-040 retention executor primitives.
//
// Three RPCs the gRPC handler exposes for services/gc's retention executor:
//
//	MarkManifestRetentionPending     — soft-delete write, idempotent.
//	ClearManifestRetentionPending    — undo write.
//	ListPendingDeleteManifests       — past-grace lookup for the finaliser.
//
// All three forward straight to the repository — there's no per-rule
// validation here because the candidates were already validated by
// EvaluateRetention (FE-API-038) before the executor reached this seam.
// The handler still does tenant_id / manifest_id presence checks so a
// malformed RPC fails fast before hitting Postgres.
package handler

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// MarkManifestRetentionPending stamps retention_pending_delete_at on the
// manifest. Idempotent — see the repository note for why the existing
// timestamp is preserved on a re-run.
func (h *MetadataHandler) MarkManifestRetentionPending(
	ctx context.Context,
	req *metadatav1.MarkManifestRetentionPendingRequest,
) (*emptypb.Empty, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetManifestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "manifest_id is required")
	}
	if err := h.repo.MarkManifestRetentionPending(ctx, req.GetTenantId(), req.GetManifestId()); err != nil {
		return nil, mapErr(err)
	}
	return &emptypb.Empty{}, nil
}

// ClearManifestRetentionPending unsets the column. Used by the UI undo
// affordance during the grace window and by tests.
func (h *MetadataHandler) ClearManifestRetentionPending(
	ctx context.Context,
	req *metadatav1.ClearManifestRetentionPendingRequest,
) (*emptypb.Empty, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetManifestId() == "" {
		return nil, status.Error(codes.InvalidArgument, "manifest_id is required")
	}
	if err := h.repo.ClearManifestRetentionPending(ctx, req.GetTenantId(), req.GetManifestId()); err != nil {
		return nil, mapErr(err)
	}
	return &emptypb.Empty{}, nil
}

// ListPendingDeleteManifests returns manifests whose grace window has
// elapsed. Tenant scoping is optional — see the proto comment for why the
// cross-tenant scan exists.
//
// limit is clamped to [1, repository.MaxPendingDeleteLimit] with a default
// of repository.DefaultPendingDeleteLimit. The repository re-clamps
// defensively but the handler is the canonical clamp point.
func (h *MetadataHandler) ListPendingDeleteManifests(
	ctx context.Context,
	req *metadatav1.ListPendingDeleteManifestsRequest,
) (*metadatav1.ListPendingDeleteManifestsResponse, error) {
	// grace_window_secs >= 0 is enforced server-side; a negative value
	// would otherwise widen the eligibility window into the future.
	graceSecs := req.GetGraceWindowSecs()
	if graceSecs < 0 {
		graceSecs = 0
	}

	limit := int(req.GetLimit())
	switch {
	case limit <= 0:
		limit = repository.DefaultPendingDeleteLimit
	case limit > repository.MaxPendingDeleteLimit:
		limit = repository.MaxPendingDeleteLimit
	}

	out, err := h.repo.ListPendingDeleteManifests(ctx, req.GetTenantId(), graceSecs, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	// Always return a non-nil slice so the JSON encoder downstream emits []
	// instead of null. Matches the rest of this handler family.
	if out == nil {
		out = []*metadatav1.PendingDeleteManifest{}
	}
	return &metadatav1.ListPendingDeleteManifestsResponse{Manifests: out}, nil
}
