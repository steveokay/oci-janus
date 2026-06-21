// Tests for the FE-API-038 EvaluateRetention handler. These cover validation
// (kind allowlist, regex compile, missing tenant_id), cap clamping, and the
// happy-path proto round-trip. The repository-layer rule semantics
// (max_age_days vs max_count etc.) are covered by retention_eval_test.go in
// services/metadata/internal/repository — the handler test is concerned only
// with wiring and input validation.
package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// validEvalReq returns a baseline EvaluateRetentionRequest the tests can
// tweak. enabled=true + at least one rule mirrors the most common UI path
// (operator clicks "preview" before saving).
func validEvalReq() *metadatav1.EvaluateRetentionRequest {
	return &metadatav1.EvaluateRetentionRequest{
		TenantId: "t1",
		RepoId:   "r1",
		Candidate: &metadatav1.RetentionPolicyCandidate{
			Enabled: true,
			Rules: []*metadatav1.RetentionRule{
				{Kind: "max_age_days", Value: 30},
			},
			ProtectedTagPatterns: []string{"latest"},
		},
	}
}

// TestEvaluateRetention_happyPath_forwardsCandidate verifies the handler
// forwards the candidate to the repository verbatim and converts the result
// into the proto wire shape (including non-nil slices).
func TestEvaluateRetention_happyPath_forwardsCandidate(t *testing.T) {
	now := time.Now().UTC()
	stub := &repository.EvaluationResult{
		WouldDelete: []repository.EvaluationCandidate{
			{
				ManifestID:     "m1",
				ManifestDigest: "sha256:aaa",
				Tags:           []string{"v1.0"},
				PushedAt:       now.Add(-100 * 24 * time.Hour),
				SizeBytes:      1024,
				Reasons:        []string{"max_age_days"},
			},
		},
		ProtectedSkipped: []repository.EvaluationProtected{
			{
				ManifestID:     "m2",
				ManifestDigest: "sha256:bbb",
				Tags:           []string{"latest"},
				MatchedPattern: "latest",
			},
		},
		TotalCount:  1,
		TotalBytes:  1024,
		EvaluatedAt: now,
		Truncated:   false,
	}
	f := &fakeRepo{evalRetentionResult: stub}
	h := newHandler(f)

	resp, err := h.EvaluateRetention(context.Background(), validEvalReq())
	requireNoErr(t, err)
	if len(resp.GetWouldDelete()) != 1 {
		t.Fatalf("would_delete: got %d, want 1", len(resp.GetWouldDelete()))
	}
	if got := resp.GetWouldDelete()[0]; got.GetManifestDigest() != "sha256:aaa" || len(got.GetReasons()) != 1 {
		t.Errorf("would_delete[0] wire shape wrong: %+v", got)
	}
	if len(resp.GetProtectedSkipped()) != 1 {
		t.Fatalf("protected_skipped: got %d, want 1", len(resp.GetProtectedSkipped()))
	}
	if resp.GetTotalCount() != 1 || resp.GetTotalBytes() != 1024 {
		t.Errorf("totals mismatched: count=%d bytes=%d", resp.GetTotalCount(), resp.GetTotalBytes())
	}
	if len(f.evalRetentionCalls) != 1 {
		t.Fatalf("expected 1 eval call, got %d", len(f.evalRetentionCalls))
	}
	if f.evalRetentionCalls[0].tenantID != "t1" || f.evalRetentionCalls[0].repoID != "r1" {
		t.Errorf("forwarded ids wrong: %+v", f.evalRetentionCalls[0])
	}
}

// TestEvaluateRetention_missingTenantID_returnsInvalidArgument verifies the
// tenant_id guard fires before the repository is touched.
func TestEvaluateRetention_missingTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validEvalReq()
	req.TenantId = ""
	_, err := h.EvaluateRetention(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestEvaluateRetention_missingRepoID_returnsInvalidArgument.
func TestEvaluateRetention_missingRepoID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validEvalReq()
	req.RepoId = ""
	_, err := h.EvaluateRetention(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestEvaluateRetention_missingCandidate_returnsInvalidArgument verifies the
// candidate is required — the handler must not call into the evaluator
// with a nil policy because the semantics there are ambiguous.
func TestEvaluateRetention_missingCandidate_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validEvalReq()
	req.Candidate = nil
	_, err := h.EvaluateRetention(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestEvaluateRetention_unknownKind_returnsInvalidArgument verifies the
// handler reuses the same kind allowlist as the Upsert path.
func TestEvaluateRetention_unknownKind_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validEvalReq()
	req.Candidate.Rules = []*metadatav1.RetentionRule{{Kind: "shred_with_lasers", Value: 1}}
	_, err := h.EvaluateRetention(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestEvaluateRetention_invalidRegex_returnsInvalidArgument.
func TestEvaluateRetention_invalidRegex_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validEvalReq()
	req.Candidate.ProtectedTagPatterns = []string{"(unclosed"}
	_, err := h.EvaluateRetention(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestEvaluateRetention_enabledWithEmptyRules_returnsInvalidArgument verifies
// the same enabled-but-empty guard from Upsert fires here too — a silent
// no-op policy isn't useful to preview.
func TestEvaluateRetention_enabledWithEmptyRules_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validEvalReq()
	req.Candidate.Rules = nil
	_, err := h.EvaluateRetention(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestEvaluateRetention_overLargeCaps_clamped verifies a hostile request that
// asks for max_delete_results=999_999 gets clamped to MaxMaxDeleteResults
// before the repository call.
func TestEvaluateRetention_overLargeCaps_clamped(t *testing.T) {
	f := &fakeRepo{evalRetentionResult: &repository.EvaluationResult{}}
	h := newHandler(f)
	req := validEvalReq()
	req.MaxDeleteResults = 999_999
	req.MaxProtectedResults = 9_999
	_, err := h.EvaluateRetention(context.Background(), req)
	requireNoErr(t, err)
	if len(f.evalRetentionCalls) != 1 {
		t.Fatalf("expected 1 eval call, got %d", len(f.evalRetentionCalls))
	}
	call := f.evalRetentionCalls[0]
	if call.maxDeleteResults != repository.MaxMaxDeleteResults {
		t.Errorf("maxDeleteResults: got %d, want %d", call.maxDeleteResults, repository.MaxMaxDeleteResults)
	}
	if call.maxProtectedResults != repository.MaxMaxProtectedResults {
		t.Errorf("maxProtectedResults: got %d, want %d", call.maxProtectedResults, repository.MaxMaxProtectedResults)
	}
}

// TestEvaluateRetention_zeroCaps_useDefault verifies the proto-zero path:
// caller didn't set the cap, handler substitutes the defaults.
func TestEvaluateRetention_zeroCaps_useDefault(t *testing.T) {
	f := &fakeRepo{evalRetentionResult: &repository.EvaluationResult{}}
	h := newHandler(f)
	req := validEvalReq()
	req.MaxDeleteResults = 0
	req.MaxProtectedResults = 0
	_, err := h.EvaluateRetention(context.Background(), req)
	requireNoErr(t, err)
	call := f.evalRetentionCalls[0]
	if call.maxDeleteResults != repository.DefaultMaxDeleteResults {
		t.Errorf("maxDeleteResults: got %d, want default %d", call.maxDeleteResults, repository.DefaultMaxDeleteResults)
	}
	if call.maxProtectedResults != repository.DefaultMaxProtectedResults {
		t.Errorf("maxProtectedResults: got %d, want default %d", call.maxProtectedResults, repository.DefaultMaxProtectedResults)
	}
}

// TestEvaluateRetention_repoNotFound_returnsNotFound verifies the
// repository's ErrNotFound surfaces as gRPC NotFound — the management BFF
// uses this to render "repository not found" on the dry-run endpoint.
func TestEvaluateRetention_repoNotFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{evalRetentionErr: repository.ErrNotFound})
	_, err := h.EvaluateRetention(context.Background(), validEvalReq())
	requireCode(t, err, codes.NotFound)
}

// TestEvaluateRetention_genericRepoErr_returnsNonOK verifies other repo
// errors (DB connectivity, decode failures) surface as a non-OK code. We
// don't pin the exact code — libs/errors/codes.MapDBError may emit Internal
// or ResourceExhausted depending on the underlying error class.
func TestEvaluateRetention_genericRepoErr_returnsNonOK(t *testing.T) {
	h := newHandler(&fakeRepo{evalRetentionErr: errors.New("db down")})
	_, err := h.EvaluateRetention(context.Background(), validEvalReq())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	// Not OK, not the validation/sentinel codes that have their own tests.
	if st.Code() == codes.OK || st.Code() == codes.InvalidArgument || st.Code() == codes.NotFound {
		t.Errorf("unexpected status code: %v", st.Code())
	}
}
