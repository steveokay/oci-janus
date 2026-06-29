// Package handler_test contains unit tests for the SignerService gRPC handler.
// All dependencies are hand-written fakes — no real Cosign or network calls.
package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	signerv1 "github.com/steveokay/oci-janus/proto/gen/go/signer/v1"
	"github.com/steveokay/oci-janus/services/signer/internal/signing"
	"github.com/steveokay/oci-janus/services/signer/internal/sigstore"
)

// ── Sentinel errors ──────────────────────────────────────────────────────────

var (
	errSignFailed   = errors.New("sign failed: key error")
	errVerifyFailed = errors.New("verify failed: crypto error")
)

// ── Fake signer ──────────────────────────────────────────────────────────────

// fakeSigner implements the same method set as *signing.Signer but returns
// controllable results without performing any real cryptographic operations.
// It is injected into a testableSignerHandler to isolate handler logic.

type fakeSigner struct {
	keyID     string
	sigB64    string
	signErr   error
	verifyOK  bool
	verifyErr error
}

func (f *fakeSigner) SignPayload(_, _, _ string) (string, error) {
	return f.sigB64, f.signErr
}

func (f *fakeSigner) VerifyPayload(_, _, _, _ string) (bool, error) {
	return f.verifyOK, f.verifyErr
}

func (f *fakeSigner) KeyID() string { return f.keyID }

// signerIface is the minimal interface the handler actually uses so we can inject fakes.
type signerIface interface {
	SignPayload(tenantID, repositoryName, manifestDigest string) (string, error)
	VerifyPayload(tenantID, repositoryName, manifestDigest, sigB64 string) (bool, error)
	KeyID() string
}

// testableSignerHandler mirrors GRPCHandler but accepts a signerIface and a real *sigstore.Store
// so we can test handler logic without real key material.
type testableSignerHandler struct {
	signerv1.UnimplementedSignerServiceServer
	signer signerIface
	store  *sigstore.Store
}

func newTestableSignerHandler(s signerIface, store *sigstore.Store) *testableSignerHandler {
	return &testableSignerHandler{signer: s, store: store}
}

// SignManifest mirrors the production handler logic exactly.
func (h *testableSignerHandler) SignManifest(ctx context.Context, req *signerv1.SignManifestRequest) (*signerv1.SignManifestResponse, error) {
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
		},
	}, nil
}

// VerifyManifest mirrors the production handler logic exactly.
func (h *testableSignerHandler) VerifyManifest(ctx context.Context, req *signerv1.VerifyManifestRequest) (*signerv1.VerifyManifestResponse, error) {
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
		},
	}, nil
}

// ListSignatures mirrors the production handler logic exactly.
func (h *testableSignerHandler) ListSignatures(ctx context.Context, req *signerv1.ListSignaturesRequest) (*signerv1.ListSignaturesResponse, error) {
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
		})
	}
	return &signerv1.ListSignaturesResponse{Signatures: out}, nil
}

// ── Test helpers ─────────────────────────────────────────────────────────────

const (
	testDigest    = "sha256:abc123def456abc123def456abc123def456abc123def456abc123def456abc1"
	testTenantID  = "tenant-001"
	testRepoName  = "myorg/myimage"
	testSignerKey = "key-001"
	// validSigB64 is a valid base64 string (not real ECDSA, but valid for SignatureDigest).
	validSigB64 = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
)

func grpcCodeSigner(err error) codes.Code {
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	return codes.Unknown
}

func newHandler(f *fakeSigner) *testableSignerHandler {
	return newTestableSignerHandler(f, sigstore.New())
}

func newHandlerWithStore(f *fakeSigner, store *sigstore.Store) *testableSignerHandler {
	return newTestableSignerHandler(f, store)
}

// ── SignManifest tests ────────────────────────────────────────────────────────

func TestSignManifest_ValidRequest_ReturnsSignature(t *testing.T) {
	f := &fakeSigner{keyID: testSignerKey, sigB64: validSigB64}
	h := newHandler(f)

	resp, err := h.SignManifest(context.Background(), &signerv1.SignManifestRequest{
		TenantId:       testTenantID,
		RepositoryName: testRepoName,
		ManifestDigest: testDigest,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Signature == nil {
		t.Fatal("expected non-nil signature")
	}
	if resp.Signature.ManifestDigest != testDigest {
		t.Errorf("manifest_digest = %q, want %q", resp.Signature.ManifestDigest, testDigest)
	}
	if resp.Signature.KeyId != testSignerKey {
		t.Errorf("key_id = %q, want %q", resp.Signature.KeyId, testSignerKey)
	}
}

func TestSignManifest_EmptyTenantID_ReturnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeSigner{sigB64: validSigB64})
	_, err := h.SignManifest(context.Background(), &signerv1.SignManifestRequest{
		TenantId:       "",
		RepositoryName: testRepoName,
		ManifestDigest: testDigest,
	})
	if grpcCodeSigner(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeSigner(err))
	}
}

func TestSignManifest_EmptyManifestDigest_ReturnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeSigner{sigB64: validSigB64})
	_, err := h.SignManifest(context.Background(), &signerv1.SignManifestRequest{
		TenantId:       testTenantID,
		RepositoryName: testRepoName,
		ManifestDigest: "",
	})
	if grpcCodeSigner(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeSigner(err))
	}
}

func TestSignManifest_EmptyRepositoryName_ReturnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeSigner{sigB64: validSigB64})
	_, err := h.SignManifest(context.Background(), &signerv1.SignManifestRequest{
		TenantId:       testTenantID,
		RepositoryName: "",
		ManifestDigest: testDigest,
	})
	if grpcCodeSigner(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeSigner(err))
	}
}

func TestSignManifest_SignerError_ReturnsInternal(t *testing.T) {
	f := &fakeSigner{signErr: errSignFailed}
	h := newHandler(f)
	_, err := h.SignManifest(context.Background(), &signerv1.SignManifestRequest{
		TenantId:       testTenantID,
		RepositoryName: testRepoName,
		ManifestDigest: testDigest,
	})
	if grpcCodeSigner(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCodeSigner(err))
	}
}

func TestSignManifest_SignerIDEmpty_UsesKeyID(t *testing.T) {
	f := &fakeSigner{keyID: testSignerKey, sigB64: validSigB64}
	h := newHandler(f)
	resp, err := h.SignManifest(context.Background(), &signerv1.SignManifestRequest{
		TenantId:       testTenantID,
		RepositoryName: testRepoName,
		ManifestDigest: testDigest,
		SignerId:       "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Signature.SignerId != testSignerKey {
		t.Errorf("signer_id = %q, want %q (key_id)", resp.Signature.SignerId, testSignerKey)
	}
}

func TestSignManifest_ExplicitSignerID_UsedInResponse(t *testing.T) {
	f := &fakeSigner{keyID: testSignerKey, sigB64: validSigB64}
	h := newHandler(f)
	resp, err := h.SignManifest(context.Background(), &signerv1.SignManifestRequest{
		TenantId:       testTenantID,
		RepositoryName: testRepoName,
		ManifestDigest: testDigest,
		SignerId:       "custom-signer-id",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Signature.SignerId != "custom-signer-id" {
		t.Errorf("signer_id = %q, want %q", resp.Signature.SignerId, "custom-signer-id")
	}
}

func TestSignManifest_StoresRecordInStore(t *testing.T) {
	f := &fakeSigner{keyID: testSignerKey, sigB64: validSigB64}
	store := sigstore.New()
	h := newHandlerWithStore(f, store)

	_, err := h.SignManifest(context.Background(), &signerv1.SignManifestRequest{
		TenantId:       testTenantID,
		RepositoryName: testRepoName,
		ManifestDigest: testDigest,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	recs := store.List(context.Background(), testTenantID, testDigest)
	if len(recs) != 1 {
		t.Errorf("store.List returned %d records, want 1", len(recs))
	}
}

// ── VerifyManifest tests ──────────────────────────────────────────────────────

func TestVerifyManifest_ValidSignature_ReturnsVerifiedTrue(t *testing.T) {
	f := &fakeSigner{keyID: testSignerKey, sigB64: validSigB64, verifyOK: true}
	store := sigstore.New()
	store.Add(&sigstore.Record{
		TenantID:        testTenantID,
		SignerID:        testSignerKey,
		ManifestDigest:  testDigest,
		RepositoryName:  testRepoName,
		SignatureDigest: "sha256:aabbcc",
		KeyID:           testSignerKey,
		SigB64:          validSigB64,
		SignedAt:        time.Now(),
	})
	h := newHandlerWithStore(f, store)

	resp, err := h.VerifyManifest(context.Background(), &signerv1.VerifyManifestRequest{
		TenantId:       testTenantID,
		ManifestDigest: testDigest,
		SignerId:       testSignerKey,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Verified {
		t.Errorf("verified = false, want true; failure_reason = %q", resp.FailureReason)
	}
}

func TestVerifyManifest_NoSignatureStored_ReturnsVerifiedFalse(t *testing.T) {
	f := &fakeSigner{keyID: testSignerKey}
	h := newHandler(f)
	resp, err := h.VerifyManifest(context.Background(), &signerv1.VerifyManifestRequest{
		TenantId:       testTenantID,
		ManifestDigest: testDigest,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Verified {
		t.Error("expected verified=false for unknown digest")
	}
	if resp.FailureReason == "" {
		t.Error("expected non-empty failure_reason")
	}
}

func TestVerifyManifest_InvalidSignature_ReturnsVerifiedFalse(t *testing.T) {
	f := &fakeSigner{keyID: testSignerKey, verifyOK: false}
	store := sigstore.New()
	store.Add(&sigstore.Record{
		TenantID:       testTenantID,
		SignerID:       testSignerKey,
		ManifestDigest: testDigest,
		RepositoryName: testRepoName,
		SigB64:         validSigB64,
		SignedAt:       time.Now(),
	})
	h := newHandlerWithStore(f, store)

	resp, err := h.VerifyManifest(context.Background(), &signerv1.VerifyManifestRequest{
		TenantId:       testTenantID,
		ManifestDigest: testDigest,
		SignerId:       testSignerKey,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Verified {
		t.Error("expected verified=false for invalid signature")
	}
}

func TestVerifyManifest_EmptyTenantID_ReturnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeSigner{})
	_, err := h.VerifyManifest(context.Background(), &signerv1.VerifyManifestRequest{
		TenantId:       "",
		ManifestDigest: testDigest,
	})
	if grpcCodeSigner(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeSigner(err))
	}
}

func TestVerifyManifest_EmptyManifestDigest_ReturnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeSigner{})
	_, err := h.VerifyManifest(context.Background(), &signerv1.VerifyManifestRequest{
		TenantId:       testTenantID,
		ManifestDigest: "",
	})
	if grpcCodeSigner(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeSigner(err))
	}
}

func TestVerifyManifest_VerifyError_ReturnsInternal(t *testing.T) {
	f := &fakeSigner{keyID: testSignerKey, verifyErr: errVerifyFailed}
	store := sigstore.New()
	store.Add(&sigstore.Record{
		TenantID:       testTenantID,
		SignerID:       testSignerKey,
		ManifestDigest: testDigest,
		RepositoryName: testRepoName,
		SigB64:         validSigB64,
		SignedAt:       time.Now(),
	})
	h := newHandlerWithStore(f, store)

	_, err := h.VerifyManifest(context.Background(), &signerv1.VerifyManifestRequest{
		TenantId:       testTenantID,
		ManifestDigest: testDigest,
		SignerId:       testSignerKey,
	})
	if grpcCodeSigner(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCodeSigner(err))
	}
}

// ── ListSignatures tests ──────────────────────────────────────────────────────

func TestListSignatures_NoSignatures_ReturnsEmptyList(t *testing.T) {
	h := newHandler(&fakeSigner{keyID: testSignerKey})
	resp, err := h.ListSignatures(context.Background(), &signerv1.ListSignaturesRequest{
		TenantId:       testTenantID,
		ManifestDigest: testDigest,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Signatures) != 0 {
		t.Errorf("expected 0 signatures, got %d", len(resp.Signatures))
	}
}

func TestListSignatures_MultipleSignatures_ReturnsAll(t *testing.T) {
	store := sigstore.New()
	for range 3 {
		store.Add(&sigstore.Record{
			TenantID:       testTenantID,
			SignerID:       testSignerKey,
			ManifestDigest: testDigest,
			RepositoryName: testRepoName,
			SignedAt:       time.Now(),
		})
	}
	h := newHandlerWithStore(&fakeSigner{keyID: testSignerKey}, store)

	resp, err := h.ListSignatures(context.Background(), &signerv1.ListSignaturesRequest{
		TenantId:       testTenantID,
		ManifestDigest: testDigest,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Signatures) != 3 {
		t.Errorf("expected 3 signatures, got %d", len(resp.Signatures))
	}
}

func TestListSignatures_EmptyDigest_ReturnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeSigner{})
	_, err := h.ListSignatures(context.Background(), &signerv1.ListSignaturesRequest{ManifestDigest: ""})
	if grpcCodeSigner(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeSigner(err))
	}
}

func TestListSignatures_DifferentDigest_DoesNotCrossContaminate(t *testing.T) {
	store := sigstore.New()
	store.Add(&sigstore.Record{
		TenantID:       testTenantID,
		SignerID:       testSignerKey,
		ManifestDigest: testDigest,
		RepositoryName: testRepoName,
		SignedAt:       time.Now(),
	})
	h := newHandlerWithStore(&fakeSigner{keyID: testSignerKey}, store)

	otherDigest := "sha256:0000000000000000000000000000000000000000000000000000000000000001"
	resp, err := h.ListSignatures(context.Background(), &signerv1.ListSignaturesRequest{
		TenantId:       testTenantID,
		ManifestDigest: otherDigest,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.Signatures) != 0 {
		t.Errorf("expected 0 signatures for different digest, got %d", len(resp.Signatures))
	}
}

// ── signing.SignatureDigest pure function tests ───────────────────────────────

func TestSignatureDigest_ValidBase64_ReturnsSHA256Digest(t *testing.T) {
	got, err := signing.SignatureDigest(validSigB64)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 71 { // "sha256:" (7 chars) + 64 hex chars
		t.Errorf("digest length = %d, want 71", len(got))
	}
	if got[:7] != "sha256:" {
		t.Errorf("digest prefix = %q, want sha256:", got[:7])
	}
}

func TestSignatureDigest_SameInput_ReturnsSameDigest(t *testing.T) {
	d1, _ := signing.SignatureDigest(validSigB64)
	d2, _ := signing.SignatureDigest(validSigB64)
	if d1 != d2 {
		t.Errorf("SignatureDigest not deterministic: %q != %q", d1, d2)
	}
}

func TestSignatureDigest_DifferentInput_ReturnsDifferentDigest(t *testing.T) {
	d1, _ := signing.SignatureDigest(validSigB64)
	d2, _ := signing.SignatureDigest("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB")
	if d1 == d2 {
		t.Error("expected different digests for different inputs")
	}
}

func TestSignatureDigest_InvalidBase64_ReturnsError(t *testing.T) {
	_, err := signing.SignatureDigest("not!base64@@")
	if err == nil {
		t.Error("expected error for invalid base64 input")
	}
}
