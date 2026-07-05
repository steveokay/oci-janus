package handler

import (
	"context"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	corev1 "github.com/steveokay/oci-janus/proto/gen/go/core/v1"
	"github.com/steveokay/oci-janus/services/core/internal/service"
)

// referrerLister is the subset of *service.Registry that the CoreHandler
// depends on. Declaring it as an interface (rather than taking the concrete
// *service.Registry) keeps the handler unit-testable with a hand-written fake
// that returns canned descriptors, so ListReferrers can be exercised without a
// live Redis/referrer store (CLAUDE.md §5 — no business logic in libs, keep the
// seam thin and local to its single caller).
type referrerLister interface {
	// GetReferrers returns the referrer descriptors for the given subject
	// digest, a flag indicating whether the list was filtered by artifactType,
	// and any error. It mirrors (*service.Registry).GetReferrers exactly.
	GetReferrers(ctx context.Context, tenantID, repoName, subjectDigest, artifactType string) ([]service.ReferrerDescriptor, bool, error)
}

// CoreHandler implements corev1.CoreServiceServer, exposing the OCI referrers
// listing over gRPC so the management BFF can surface a "Referrers tab" without
// re-implementing the Redis-backed referrer store. It is a thin adapter: it
// validates the request, delegates to the shared *service.Registry, and maps
// the service-layer descriptors onto the generated proto messages.
type CoreHandler struct {
	// UnimplementedCoreServiceServer is embedded for forward compatibility so
	// adding new RPCs to the proto does not break the build until this handler
	// implements them.
	corev1.UnimplementedCoreServiceServer

	// registry is the shared service layer instance (the same one wired into
	// the HTTP OCI handler) — never a second Registry.
	registry referrerLister
}

// NewCoreHandler constructs a CoreHandler backed by the given referrer lister
// (in production the shared *service.Registry). It reuses the already-built
// Registry — callers must not construct a second one.
func NewCoreHandler(registry referrerLister) *CoreHandler {
	return &CoreHandler{registry: registry}
}

// ListReferrers returns the OCI referrers for a subject manifest digest.
//
// Validation: tenant_id, repository, and subject_digest are all required, and
// subject_digest must be a well-formed "sha256:<hex64>" digest (reusing the
// package-level digestRE). Missing or malformed input returns InvalidArgument.
//
// When artifact_type is supplied the underlying store filters to that type and
// the response's filtered flag is set to true (OCI §4.5 semantics). On a
// store/lookup failure the error is mapped to codes.Internal and logged with
// slog so an operator can correlate the failure with the request.
func (h *CoreHandler) ListReferrers(ctx context.Context, req *corev1.ListReferrersRequest) (*corev1.ListReferrersResponse, error) {
	if req.GetTenantId() == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if req.GetRepository() == "" {
		return nil, status.Error(codes.InvalidArgument, "repository is required")
	}
	if req.GetSubjectDigest() == "" {
		return nil, status.Error(codes.InvalidArgument, "subject_digest is required")
	}
	// digestRE is defined in http.go (^sha256:[a-f0-9]{64}$) and shared across
	// the handler package — reuse it rather than re-declaring the pattern.
	if !digestRE.MatchString(req.GetSubjectDigest()) {
		return nil, status.Error(codes.InvalidArgument, "subject_digest must match sha256:<hex64>")
	}

	descs, filtered, err := h.registry.GetReferrers(
		ctx,
		req.GetTenantId(),
		req.GetRepository(),
		req.GetSubjectDigest(),
		req.GetArtifactType(),
	)
	if err != nil {
		// The referrer store is Redis-backed; a lookup failure is an internal
		// fault (not a client error). Log with context so the trace/tenant is
		// captured, then return an opaque Internal to the caller.
		slog.ErrorContext(ctx, "ListReferrers: referrer store lookup failed",
			"tenant_id", req.GetTenantId(),
			"repository", req.GetRepository(),
			"subject_digest", req.GetSubjectDigest(),
			"error", err,
		)
		return nil, status.Error(codes.Internal, "failed to list referrers")
	}

	// Map service-layer descriptors onto the generated proto messages.
	out := make([]*corev1.ReferrerDescriptor, 0, len(descs))
	for _, d := range descs {
		out = append(out, &corev1.ReferrerDescriptor{
			MediaType:    d.MediaType,
			Digest:       d.Digest,
			Size:         d.Size,
			ArtifactType: d.ArtifactType,
			Annotations:  d.Annotations,
		})
	}

	return &corev1.ListReferrersResponse{
		Referrers: out,
		Filtered:  filtered,
	}, nil
}
