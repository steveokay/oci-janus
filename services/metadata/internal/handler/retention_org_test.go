// Tests for the FE-API-039 per-org default + inheritance handlers. All
// tests use the existing fakeRepo from grpc_test.go so we exercise the
// validation reuse + proto wiring without standing up a real Postgres.
// Repository-level integration coverage (SQL UPSERT, preview_until reset,
// fallback resolution) lives under services/metadata/internal/testutil/
// integration.
package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// validOrgUpsertReq returns a baseline UpsertOrgRetentionPolicyRequest that
// individual tests can tweak. Same shape as validUpsertReq for the per-repo
// path so the reused validateRetentionRules helper covers both surfaces.
func validOrgUpsertReq() *metadatav1.UpsertOrgRetentionPolicyRequest {
	return &metadatav1.UpsertOrgRetentionPolicyRequest{
		TenantId: "t1",
		OrgId:    "org-1",
		Enabled:  true,
		Rules: []*metadatav1.RetentionRule{
			{Kind: "max_age_days", Value: 90},
		},
		ProtectedTagPatterns: []string{"latest"},
		UpdatedBy:            "00000000-0000-0000-0000-000000000099",
	}
}

// ── GetOrgRetentionPolicy ────────────────────────────────────────────────

// TestGetOrgRetention_happyPath_returnsPolicy verifies the handler returns
// the repository's response untouched.
func TestGetOrgRetention_happyPath_returnsPolicy(t *testing.T) {
	want := &metadatav1.RetentionPolicy{
		OrgId:    "org-1",
		TenantId: "t1",
		Enabled:  true,
		Rules:    []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 90}},
	}
	h := newHandler(&fakeRepo{getOrgRetentionResult: want})

	got, err := h.GetOrgRetentionPolicy(context.Background(), &metadatav1.GetOrgRetentionPolicyRequest{
		TenantId: "t1", OrgId: "org-1",
	})
	requireNoErr(t, err)
	if got.GetOrgId() != "org-1" {
		t.Errorf("OrgId: got %q, want org-1", got.GetOrgId())
	}
}

// TestGetOrgRetention_notFound_returnsNotFound verifies ErrNotFound bubbles
// up as gRPC NotFound — the management BFF maps this to a typed 404 body.
func TestGetOrgRetention_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getOrgRetentionErr: repository.ErrNotFound})
	_, err := h.GetOrgRetentionPolicy(context.Background(), &metadatav1.GetOrgRetentionPolicyRequest{
		TenantId: "t1", OrgId: "org-1",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetOrgRetention_missingTenantID_returnsInvalidArgument.
func TestGetOrgRetention_missingTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetOrgRetentionPolicy(context.Background(), &metadatav1.GetOrgRetentionPolicyRequest{
		OrgId: "org-1",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetOrgRetention_missingOrgID_returnsInvalidArgument.
func TestGetOrgRetention_missingOrgID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetOrgRetentionPolicy(context.Background(), &metadatav1.GetOrgRetentionPolicyRequest{
		TenantId: "t1",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ── UpsertOrgRetentionPolicy ──────────────────────────────────────────────

// TestUpsertOrgRetention_happyPath_forwardsAllFields verifies a valid
// upsert reaches the repository with each field intact.
func TestUpsertOrgRetention_happyPath_forwardsAllFields(t *testing.T) {
	stub := &metadatav1.RetentionPolicy{
		OrgId:        "org-1",
		TenantId:     "t1",
		Enabled:      true,
		Rules:        []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 90}},
		PreviewUntil: timestamppb.New(time.Now().Add(24 * time.Hour)),
	}
	f := &fakeRepo{upsertOrgRetentionResult: stub}
	h := newHandler(f)

	got, err := h.UpsertOrgRetentionPolicy(context.Background(), validOrgUpsertReq())
	requireNoErr(t, err)
	if got.GetPreviewUntil() == nil {
		t.Error("expected preview_until on response")
	}
	if len(f.upsertOrgRetentionCalls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(f.upsertOrgRetentionCalls))
	}
	call := f.upsertOrgRetentionCalls[0]
	if call.tenantID != "t1" || call.orgID != "org-1" || !call.enabled {
		t.Errorf("BFF forwarding mismatch: %+v", call)
	}
}

// TestUpsertOrgRetention_unknownKind_returnsInvalidArgument verifies the
// shared validation rejects an out-of-allowlist rule kind on the org path
// too — the same retention_validation.go covers both surfaces.
func TestUpsertOrgRetention_unknownKind_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validOrgUpsertReq()
	req.Rules = []*metadatav1.RetentionRule{{Kind: "shred_with_lasers", Value: 1}}
	_, err := h.UpsertOrgRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertOrgRetention_overCapValue_returnsInvalidArgument verifies the
// per-kind cap also applies on the org path.
func TestUpsertOrgRetention_overCapValue_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validOrgUpsertReq()
	req.Rules = []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 9_999_999}}
	_, err := h.UpsertOrgRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertOrgRetention_invalidRegex_returnsInvalidArgument verifies a
// malformed pattern is rejected on the org path too.
func TestUpsertOrgRetention_invalidRegex_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validOrgUpsertReq()
	req.ProtectedTagPatterns = []string{"(unclosed"}
	_, err := h.UpsertOrgRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertOrgRetention_enabledNoRules_returnsInvalidArgument verifies the
// "enabled with empty rules is a silent no-op" guard applies here too.
func TestUpsertOrgRetention_enabledNoRules_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validOrgUpsertReq()
	req.Rules = nil
	_, err := h.UpsertOrgRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertOrgRetention_missingOrgID_returnsInvalidArgument.
func TestUpsertOrgRetention_missingOrgID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validOrgUpsertReq()
	req.OrgId = ""
	_, err := h.UpsertOrgRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// ── DeleteOrgRetentionPolicy ──────────────────────────────────────────────

// TestDeleteOrgRetention_happyPath verifies a successful delete returns
// an empty response and propagates the (tenant, org) tuple to the repo.
func TestDeleteOrgRetention_happyPath(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.DeleteOrgRetentionPolicy(context.Background(), &metadatav1.DeleteOrgRetentionPolicyRequest{
		TenantId: "t1", OrgId: "org-1",
	})
	requireNoErr(t, err)
	if len(f.deleteOrgRetentionCalls) != 1 {
		t.Fatalf("expected 1 delete call, got %d", len(f.deleteOrgRetentionCalls))
	}
}

// TestDeleteOrgRetention_notFound_returnsNotFound — the repository's
// ErrNotFound maps to gRPC NotFound via mapErr.
func TestDeleteOrgRetention_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{deleteOrgRetentionErr: repository.ErrNotFound})
	_, err := h.DeleteOrgRetentionPolicy(context.Background(), &metadatav1.DeleteOrgRetentionPolicyRequest{
		TenantId: "t1", OrgId: "org-1",
	})
	requireCode(t, err, codes.NotFound)
}

// ── GetEffectiveRetentionPolicy ───────────────────────────────────────────

// TestGetEffective_repoHit_returnsRepoSource verifies a per-repo row wins
// and the inherited_from label is "repo".
func TestGetEffective_repoHit_returnsRepoSource(t *testing.T) {
	policy := &metadatav1.RetentionPolicy{
		RepoId:   "r1",
		TenantId: "t1",
		Enabled:  true,
		Rules:    []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
	}
	res := &repository.EffectivePolicyResult{Policy: policy, InheritedFrom: "repo"}
	h := newHandler(&fakeRepo{effectiveRetentionResult: res})

	got, err := h.GetEffectiveRetentionPolicy(context.Background(), &metadatav1.GetEffectiveRetentionPolicyRequest{
		TenantId: "t1", RepoId: "r1",
	})
	requireNoErr(t, err)
	if got.GetInheritedFrom() != "repo" {
		t.Errorf("inherited_from: got %q, want repo", got.GetInheritedFrom())
	}
	if got.GetPolicy().GetRepoId() != "r1" {
		t.Errorf("repo_id: got %q, want r1", got.GetPolicy().GetRepoId())
	}
}

// TestGetEffective_orgFallback_returnsOrgSource verifies the fallback path
// returns inherited_from="org" and populates org_id.
func TestGetEffective_orgFallback_returnsOrgSource(t *testing.T) {
	policy := &metadatav1.RetentionPolicy{
		OrgId:    "org-1",
		TenantId: "t1",
		Enabled:  true,
		Rules:    []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 90}},
	}
	res := &repository.EffectivePolicyResult{Policy: policy, InheritedFrom: "org", OrgID: "org-1"}
	h := newHandler(&fakeRepo{effectiveRetentionResult: res})

	got, err := h.GetEffectiveRetentionPolicy(context.Background(), &metadatav1.GetEffectiveRetentionPolicyRequest{
		TenantId: "t1", RepoId: "r1",
	})
	requireNoErr(t, err)
	if got.GetInheritedFrom() != "org" {
		t.Errorf("inherited_from: got %q, want org", got.GetInheritedFrom())
	}
	if got.GetOrgId() != "org-1" {
		t.Errorf("org_id: got %q, want org-1", got.GetOrgId())
	}
}

// TestGetEffective_notFound_returnsNotFound verifies the "neither layer
// has a row" case maps to gRPC NotFound (BFF surfaces no-policy).
func TestGetEffective_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{effectiveRetentionErr: repository.ErrNotFound})
	_, err := h.GetEffectiveRetentionPolicy(context.Background(), &metadatav1.GetEffectiveRetentionPolicyRequest{
		TenantId: "t1", RepoId: "r1",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetEffective_missingRepoID_returnsInvalidArgument.
func TestGetEffective_missingRepoID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetEffectiveRetentionPolicy(context.Background(), &metadatav1.GetEffectiveRetentionPolicyRequest{
		TenantId: "t1",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ── LookupOrgIDByName ─────────────────────────────────────────────────────

// TestLookupOrgIDByName_happyPath verifies the BFF lookup forwards the
// (tenant, name) tuple and returns the resolved org_id.
func TestLookupOrgIDByName_happyPath(t *testing.T) {
	f := &fakeRepo{lookupOrgIDResult: "org-xyz"}
	h := newHandler(f)

	resp, err := h.LookupOrgIDByName(context.Background(), &metadatav1.LookupOrgIDByNameRequest{
		TenantId: "t1", Name: "myorg",
	})
	requireNoErr(t, err)
	if resp.GetOrgId() != "org-xyz" {
		t.Errorf("org_id: got %q, want org-xyz", resp.GetOrgId())
	}
	if len(f.lookupOrgIDCalls) != 1 || f.lookupOrgIDCalls[0].name != "myorg" {
		t.Errorf("BFF forwarded wrong call args: %+v", f.lookupOrgIDCalls)
	}
}

// TestLookupOrgIDByName_notFound_returnsNotFound — the BFF surfaces this
// as a 404 (so a typo'd org name doesn't return a useful disambig).
func TestLookupOrgIDByName_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{lookupOrgIDErr: repository.ErrNotFound})
	_, err := h.LookupOrgIDByName(context.Background(), &metadatav1.LookupOrgIDByNameRequest{
		TenantId: "t1", Name: "ghost",
	})
	requireCode(t, err, codes.NotFound)
}

// TestLookupOrgIDByName_missingName_returnsInvalidArgument.
func TestLookupOrgIDByName_missingName_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.LookupOrgIDByName(context.Background(), &metadatav1.LookupOrgIDByNameRequest{
		TenantId: "t1",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// Static reference so the import stays used even if a refactor pulls the
// explicit repository.ErrNotFound out of test bodies.
var _ = errors.Is
