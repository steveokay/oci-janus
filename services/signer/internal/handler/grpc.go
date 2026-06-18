// Package handler implements the SignerService gRPC server.
package handler

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/signer/internal/signing"
	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
)

// GRPCHandler implements signerv1.SignerServiceServer.
type GRPCHandler struct {
	signerv1.UnimplementedSignerServiceServer
	signer signing.Signer
	store  *sigstore.Store
}

// New creates a GRPCHandler.
func New(s signing.Signer, store *sigstore.Store) *GRPCHandler {
	return &GRPCHandler{signer: s, store: store}
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

	rec := h.store.FindRec(ctx, req.ManifestDigest, signerID)
	if rec == nil {
		return &signerv1.VerifyManifestResponse{Verified: false, FailureReason: "no signature found"}, nil
	}

	ok, err := h.signer.VerifyPayload(rec.RepositoryName, req.ManifestDigest, rec.SigB64)
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

// ListSignatures returns all known signatures for a manifest digest.
func (h *GRPCHandler) ListSignatures(ctx context.Context, req *signerv1.ListSignaturesRequest) (*signerv1.ListSignaturesResponse, error) {
	if req.ManifestDigest == "" {
		return nil, status.Error(codes.InvalidArgument, "manifest_digest is required")
	}

	recs := h.store.List(ctx, req.ManifestDigest)
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
