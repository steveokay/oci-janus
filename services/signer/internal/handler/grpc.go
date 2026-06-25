// Package handler implements the SignerService gRPC server.
package handler

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/signer/internal/repository"
	"github.com/steveokay/oci-janus/services/signer/internal/signing"
	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
)

// ProxyCachePolicyRepo is the slice of *repository.Repository the
// FUT-017 proxy-cache-sign-policy handlers actually consume. Kept as an
// interface so unit tests can substitute an in-memory fake without
// spinning up a real PG pool.
type ProxyCachePolicyRepo interface {
	GetProxyCacheSignPolicy(ctx context.Context, tenantID, upstreamName string) (*repository.ProxyCacheSignPolicy, error)
	UpsertProxyCacheSignPolicy(ctx context.Context, p *repository.ProxyCacheSignPolicy) (*repository.ProxyCacheSignPolicy, error)
	ListProxyCacheSignPolicies(ctx context.Context, tenantID string) ([]*repository.ProxyCacheSignPolicy, error)
}

// GRPCHandler implements signerv1.SignerServiceServer.
//
// policyRepo is optional — when nil (in-memory dev mode without a DB),
// the FUT-017 proxy-cache policy RPCs return a clean FailedPrecondition
// rather than panicking. Production wiring in server.Run always supplies
// a real *repository.Repository when SIGNER_DB_DSN is set.
type GRPCHandler struct {
	signerv1.UnimplementedSignerServiceServer
	signer     signing.Signer
	store      *sigstore.Store
	policyRepo ProxyCachePolicyRepo
}

// New creates a GRPCHandler with no proxy-cache policy backing — callers
// that want the FUT-017 RPCs use WithProxyCachePolicyRepo to wire the
// repository.
func New(s signing.Signer, store *sigstore.Store) *GRPCHandler {
	return &GRPCHandler{signer: s, store: store}
}

// WithProxyCachePolicyRepo attaches the repository that backs the
// FUT-017 GetProxyCacheSignPolicy / SetProxyCacheSignPolicy /
// ListProxyCacheSignPolicies RPCs. Safe to chain after New().
func (h *GRPCHandler) WithProxyCachePolicyRepo(repo ProxyCachePolicyRepo) *GRPCHandler {
	h.policyRepo = repo
	return h
}

// SignManifest signs the manifest and stores the signature record.
func (h *GRPCHandler) SignManifest(ctx context.Context, req *signerv1.SignManifestRequest) (*signerv1.SignManifestResponse, error) {
	if req.TenantId == "" || req.ManifestDigest == "" || req.RepositoryName == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, repository_name and manifest_digest are required")
	}

	sigB64, err := h.signer.SignPayload(req.TenantId, req.RepositoryName, req.ManifestDigest)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "sign: %v", err)
	}

	sigDigest, err := signing.SignatureDigest(sigB64)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "signature digest: %v", err)
	}

	now := time.Now()
	signerID := req.SignerId
	if signerID == "" {
		signerID = h.signer.KeyID()
	}

	rec := &sigstore.Record{
		TenantID:        req.TenantId,
		SignerID:        signerID,
		ManifestDigest:  req.ManifestDigest,
		RepositoryName:  req.RepositoryName,
		SignatureDigest: sigDigest,
		KeyID:           h.signer.KeyID(),
		SigB64:          sigB64,
		SignedAt:        now,
	}
	h.store.Add(rec)

	return &signerv1.SignManifestResponse{
		Signature: &signerv1.Signature{
			SignerId:        signerID,
			ManifestDigest:  req.ManifestDigest,
			SignatureDigest: sigDigest,
			KeyId:           h.signer.KeyID(),
			SignedAt:        timestamppb.New(now),
		},
	}, nil
}

// VerifyManifest checks whether a stored signature for this manifest+signer is valid.
func (h *GRPCHandler) VerifyManifest(ctx context.Context, req *signerv1.VerifyManifestRequest) (*signerv1.VerifyManifestResponse, error) {
	if req.ManifestDigest == "" || req.TenantId == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and manifest_digest are required")
	}

	signerID := req.SignerId
	if signerID == "" {
		signerID = h.signer.KeyID()
	}

	rec := h.store.FindRec(ctx, req.TenantId, req.ManifestDigest, signerID)
	if rec == nil {
		return &signerv1.VerifyManifestResponse{Verified: false, FailureReason: "no signature found"}, nil
	}

	ok, err := h.signer.VerifyPayload(req.TenantId, rec.RepositoryName, req.ManifestDigest, rec.SigB64)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "verify: %v", err)
	}
	if !ok {
		return &signerv1.VerifyManifestResponse{Verified: false, FailureReason: "signature invalid"}, nil
	}

	return &signerv1.VerifyManifestResponse{
		Verified: true,
		Signature: &signerv1.Signature{
			SignerId:        rec.SignerID,
			ManifestDigest:  rec.ManifestDigest,
			SignatureDigest: rec.SignatureDigest,
			KeyId:           rec.KeyID,
			SignedAt:        timestamppb.New(rec.SignedAt),
		},
	}, nil
}

// ListSignatures returns all known signatures for a tenant + manifest digest.
//
// QA-001: tenant_id is required so signature visibility is correctly scoped.
// Without this, two tenants pushing the same public-image digest would see
// each other's signature records — and worse, signed-image admission could
// admit images using a signature produced by another tenant.
func (h *GRPCHandler) ListSignatures(ctx context.Context, req *signerv1.ListSignaturesRequest) (*signerv1.ListSignaturesResponse, error) {
	if req.TenantId == "" || req.ManifestDigest == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and manifest_digest are required")
	}

	recs := h.store.List(ctx, req.TenantId, req.ManifestDigest)
	out := make([]*signerv1.Signature, 0, len(recs))
	for _, r := range recs {
		out = append(out, &signerv1.Signature{
			SignerId:        r.SignerID,
			ManifestDigest:  r.ManifestDigest,
			SignatureDigest: r.SignatureDigest,
			KeyId:           r.KeyID,
			SignedAt:        timestamppb.New(r.SignedAt),
		})
	}
	return &signerv1.ListSignaturesResponse{Signatures: out}, nil
}

// ── FUT-017: proxy-cache auto-sign policy RPCs ───────────────────────────────

// GetProxyCacheSignPolicy returns the per-upstream auto-sign policy. When no
// row exists for (tenant_id, upstream_name) the response is a zero-valued
// policy with auto_sign=false and an empty key_id — the absent-policy and
// disabled-policy states are deliberately indistinguishable so callers don't
// have to special-case NOT_FOUND for the most common "feature off" case.
func (h *GRPCHandler) GetProxyCacheSignPolicy(ctx context.Context, req *signerv1.GetProxyCacheSignPolicyRequest) (*signerv1.ProxyCacheSignPolicy, error) {
	if req.TenantId == "" || req.UpstreamName == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and upstream_name are required")
	}
	if h.policyRepo == nil {
		return nil, status.Error(codes.FailedPrecondition, "proxy-cache sign policy repository not configured (SIGNER_DB_DSN unset)")
	}
	p, err := h.policyRepo.GetProxyCacheSignPolicy(ctx, req.TenantId, req.UpstreamName)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get policy: %v", err)
	}
	if p == nil {
		// Synthesise a zero-valued response so callers can render the same
		// "off, no key" UI state without checking for an explicit "missing"
		// flag — see message-level doc on ProxyCacheSignPolicy.
		return &signerv1.ProxyCacheSignPolicy{
			TenantId:     req.TenantId,
			UpstreamName: req.UpstreamName,
		}, nil
	}
	return toProtoProxyCacheSignPolicy(p), nil
}

// SetProxyCacheSignPolicy upserts the policy row. Operators turn auto-sign
// on by setting auto_sign=true AND choosing a non-empty key_id — the
// consumer treats an empty key_id as "still disabled" regardless of the
// flag, so flipping auto_sign without picking a key is a safe no-op rather
// than an error here.
func (h *GRPCHandler) SetProxyCacheSignPolicy(ctx context.Context, req *signerv1.SetProxyCacheSignPolicyRequest) (*signerv1.ProxyCacheSignPolicy, error) {
	if req.TenantId == "" || req.UpstreamName == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and upstream_name are required")
	}
	if h.policyRepo == nil {
		return nil, status.Error(codes.FailedPrecondition, "proxy-cache sign policy repository not configured (SIGNER_DB_DSN unset)")
	}
	// We deliberately do NOT validate key_id against the signer's KeyID()
	// here — operators may legitimately point an upstream policy at a key
	// that is not the current process's default (multi-key setups). The
	// consumer-side guard refuses to sign with a mismatched/unknown key
	// and logs, which is the right place for the "did we recognise this
	// key" check.
	stored, err := h.policyRepo.UpsertProxyCacheSignPolicy(ctx, &repository.ProxyCacheSignPolicy{
		TenantID:     req.TenantId,
		UpstreamName: req.UpstreamName,
		AutoSign:     req.AutoSign,
		KeyID:        req.KeyId,
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "upsert policy: %v", err)
	}
	return toProtoProxyCacheSignPolicy(stored), nil
}

// ListProxyCacheSignPolicies streams every policy row for the tenant.
// Server-side streaming because the dashboard's "configure proxy upstreams"
// page paginates incrementally and the call set is small enough that a
// pagination token would be more ceremony than it's worth.
func (h *GRPCHandler) ListProxyCacheSignPolicies(req *signerv1.ListProxyCacheSignPoliciesRequest, stream signerv1.SignerService_ListProxyCacheSignPoliciesServer) error {
	if req.TenantId == "" {
		return status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if h.policyRepo == nil {
		return status.Error(codes.FailedPrecondition, "proxy-cache sign policy repository not configured (SIGNER_DB_DSN unset)")
	}
	rows, err := h.policyRepo.ListProxyCacheSignPolicies(stream.Context(), req.TenantId)
	if err != nil {
		return status.Errorf(codes.Internal, "list policies: %v", err)
	}
	for _, p := range rows {
		if err := stream.Send(toProtoProxyCacheSignPolicy(p)); err != nil {
			return err
		}
	}
	return nil
}

// toProtoProxyCacheSignPolicy converts the repository row into its proto
// representation. Kept local because the handler is the only consumer of
// the proto type and the repository is the only consumer of the row
// struct — exporting either would muddy the layer boundary.
func toProtoProxyCacheSignPolicy(p *repository.ProxyCacheSignPolicy) *signerv1.ProxyCacheSignPolicy {
	return &signerv1.ProxyCacheSignPolicy{
		TenantId:     p.TenantID,
		UpstreamName: p.UpstreamName,
		AutoSign:     p.AutoSign,
		KeyId:        p.KeyID,
		CreatedAt:    timestamppb.New(p.CreatedAt),
		UpdatedAt:    timestamppb.New(p.UpdatedAt),
	}
}
