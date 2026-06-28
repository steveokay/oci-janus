// Tests for the FE-API-037 retention policy handler. All tests use the
// existing fakeRepo from grpc_test.go — these methods exercise validation
// and proto wiring; the repository's preview_until reset semantics are
// covered by the integration test under services/metadata/internal/testutil/integration.
package handler

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// validUpsertReq returns a baseline request shape the validation tests can
// tweak without bothering with required fields each time.
func validUpsertReq() *metadatav1.UpsertRepoRetentionPolicyRequest {
	return &metadatav1.UpsertRepoRetentionPolicyRequest{
		TenantId: "t1",
		RepoId:   "r1",
		Enabled:  true,
		Rules: []*metadatav1.RetentionRule{
			{Kind: "max_age_days", Value: 30},
		},
		ProtectedTagPatterns: []string{"latest", "stable"},
		UpdatedBy:            "00000000-0000-0000-0000-000000000099",
	}
}

// ── GetRepoRetentionPolicy ────────────────────────────────────────────────

// TestGetRetention_happyPath_returnsPolicy verifies the handler returns the
// repo's response untouched.
func TestGetRetention_happyPath_returnsPolicy(t *testing.T) {
	want := &metadatav1.RetentionPolicy{
		RepoId:       "r1",
		TenantId:     "t1",
		Enabled:      true,
		Rules:        []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
		PreviewUntil: timestamppb.New(time.Now().Add(24 * time.Hour)),
	}
	h := newHandler(&fakeRepo{getRetentionResult: want})

	got, err := h.GetRepoRetentionPolicy(context.Background(), &metadatav1.GetRepoRetentionPolicyRequest{
		TenantId: "t1", RepoId: "r1",
	})
	requireNoErr(t, err)
	if got.GetRepoId() != "r1" {
		t.Errorf("RepoId: got %q, want r1", got.GetRepoId())
	}
}

// TestGetRetention_notFound_returnsNotFound verifies the ErrNotFound from the
// repo bubbles up as gRPC NotFound (mapped by mapErr).
func TestGetRetention_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getRetentionErr: repository.ErrNotFound})
	_, err := h.GetRepoRetentionPolicy(context.Background(), &metadatav1.GetRepoRetentionPolicyRequest{
		TenantId: "t1", RepoId: "r1",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetRetention_missingTenantID_returnsInvalidArgument.
func TestGetRetention_missingTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetRepoRetentionPolicy(context.Background(), &metadatav1.GetRepoRetentionPolicyRequest{
		RepoId: "r1",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ── UpsertRepoRetentionPolicy ─────────────────────────────────────────────

// TestUpsertRetention_happyPath_forwardsAllFields verifies a valid Upsert
// reaches the repo with every field intact.
func TestUpsertRetention_happyPath_forwardsAllFields(t *testing.T) {
	stub := &metadatav1.RetentionPolicy{
		RepoId:       "r1",
		TenantId:     "t1",
		Enabled:      true,
		Rules:        []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 30}},
		PreviewUntil: timestamppb.New(time.Now().Add(24 * time.Hour)),
	}
	f := &fakeRepo{upsertRetentionResult: stub}
	h := newHandler(f)

	got, err := h.UpsertRepoRetentionPolicy(context.Background(), validUpsertReq())
	requireNoErr(t, err)
	if got.GetPreviewUntil() == nil {
		t.Error("expected preview_until on response")
	}
	if len(f.upsertRetentionCalls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(f.upsertRetentionCalls))
	}
	call := f.upsertRetentionCalls[0]
	if call.tenantID != "t1" || call.repoID != "r1" || !call.enabled {
		t.Errorf("forwarded fields wrong: %+v", call)
	}
	if call.updatedBy != "00000000-0000-0000-0000-000000000099" {
		t.Errorf("updated_by lost: %q", call.updatedBy)
	}
}

// TestUpsertRetention_disabledNoRules_succeeds verifies a disable-with-no-rules
// upsert is allowed (operator turning a policy off without re-stating rules).
func TestUpsertRetention_disabledNoRules_succeeds(t *testing.T) {
	stub := &metadatav1.RetentionPolicy{RepoId: "r1", TenantId: "t1", Enabled: false}
	h := newHandler(&fakeRepo{upsertRetentionResult: stub})
	req := validUpsertReq()
	req.Enabled = false
	req.Rules = nil
	_, err := h.UpsertRepoRetentionPolicy(context.Background(), req)
	requireNoErr(t, err)
}

// TestUpsertRetention_enabledNoRules_returnsInvalidArgument verifies the
// enabled-but-empty-rules combination is rejected.
func TestUpsertRetention_enabledNoRules_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validUpsertReq()
	req.Rules = nil
	_, err := h.UpsertRepoRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertRetention_unknownKind_returnsInvalidArgument.
func TestUpsertRetention_unknownKind_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validUpsertReq()
	req.Rules = []*metadatav1.RetentionRule{{Kind: "shred_with_lasers", Value: 1}}
	_, err := h.UpsertRepoRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertRetention_duplicateKind_returnsInvalidArgument.
func TestUpsertRetention_duplicateKind_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validUpsertReq()
	req.Rules = []*metadatav1.RetentionRule{
		{Kind: "max_age_days", Value: 30},
		{Kind: "max_age_days", Value: 60},
	}
	_, err := h.UpsertRepoRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertRetention_zeroOrNegativeValue_returnsInvalidArgument verifies
// the value > 0 check fires.
func TestUpsertRetention_zeroOrNegativeValue_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validUpsertReq()
	req.Rules = []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 0}}
	if _, err := h.UpsertRepoRetentionPolicy(context.Background(), req); err == nil {
		t.Fatal("expected error for value=0")
	} else {
		requireCode(t, err, codes.InvalidArgument)
	}
	req.Rules = []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: -1}}
	if _, err := h.UpsertRepoRetentionPolicy(context.Background(), req); err == nil {
		t.Fatal("expected error for value=-1")
	} else {
		requireCode(t, err, codes.InvalidArgument)
	}
}

// TestUpsertRetention_outOfRangeValue_returnsInvalidArgument verifies the
// per-kind cap fires (e.g. max_age_days > 36500).
func TestUpsertRetention_outOfRangeValue_returnsInvalidArgument(t *testing.T) {
	cases := []struct {
		name  string
		kind  string
		value int64
	}{
		{"max_age_days_over_100y", "max_age_days", 36501},
		{"max_count_over_10M", "max_count", 10_000_001},
		{"max_size_bytes_over_100TiB", "max_size_bytes", 100*1024*1024*1024*1024 + 1},
		{"dangling_grace_over_1y", "dangling_grace_days", 366},
		{"max_idle_over_100y", "max_idle_days", 36501},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandler(&fakeRepo{})
			req := validUpsertReq()
			req.Rules = []*metadatav1.RetentionRule{{Kind: tc.kind, Value: tc.value}}
			_, err := h.UpsertRepoRetentionPolicy(context.Background(), req)
			requireCode(t, err, codes.InvalidArgument)
		})
	}
}

// TestUpsertRetention_maxIdleDaysAccepted verifies max_idle_days is accepted
// at the API level even though the executor (FE-API-040) does not honor it
// yet. This is the forward-compat guarantee documented in the migration.
func TestUpsertRetention_maxIdleDaysAccepted(t *testing.T) {
	stub := &metadatav1.RetentionPolicy{RepoId: "r1", TenantId: "t1", Enabled: true}
	h := newHandler(&fakeRepo{upsertRetentionResult: stub})
	req := validUpsertReq()
	req.Rules = []*metadatav1.RetentionRule{{Kind: "max_idle_days", Value: 90}}
	_, err := h.UpsertRepoRetentionPolicy(context.Background(), req)
	requireNoErr(t, err)
}

// TestUpsertRetention_invalidRegexPattern_returnsInvalidArgument.
func TestUpsertRetention_invalidRegexPattern_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validUpsertReq()
	// Unclosed group is a hard Go regexp error.
	req.ProtectedTagPatterns = []string{"latest", "(unclosed"}
	_, err := h.UpsertRepoRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertRetention_patternTooLong_returnsInvalidArgument.
func TestUpsertRetention_patternTooLong_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	req := validUpsertReq()
	long := make([]byte, 257)
	for i := range long {
		long[i] = 'a'
	}
	req.ProtectedTagPatterns = []string{string(long)}
	_, err := h.UpsertRepoRetentionPolicy(context.Background(), req)
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertRetention_repoNotFound_returnsNotFound verifies the FK violation
// path (repo deleted between BFF lookup and upsert).
func TestUpsertRetention_repoNotFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{upsertRetentionErr: repository.ErrNotFound})
	_, err := h.UpsertRepoRetentionPolicy(context.Background(), validUpsertReq())
	requireCode(t, err, codes.NotFound)
}

// ── DeleteRepoRetentionPolicy ─────────────────────────────────────────────

// TestDeleteRetention_happyPath_succeeds.
func TestDeleteRetention_happyPath_succeeds(t *testing.T) {
	f := &fakeRepo{}
	h := newHandler(f)
	_, err := h.DeleteRepoRetentionPolicy(context.Background(), &metadatav1.DeleteRepoRetentionPolicyRequest{
		TenantId: "t1", RepoId: "r1",
	})
	requireNoErr(t, err)
	if len(f.deleteRetentionCalls) != 1 || f.deleteRetentionCalls[0].repoID != "r1" {
		t.Errorf("expected one delete call for r1, got %+v", f.deleteRetentionCalls)
	}
}

// TestDeleteRetention_notFound_returnsNotFound.
func TestDeleteRetention_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{deleteRetentionErr: repository.ErrNotFound})
	_, err := h.DeleteRepoRetentionPolicy(context.Background(), &metadatav1.DeleteRepoRetentionPolicyRequest{
		TenantId: "t1", RepoId: "r1",
	})
	requireCode(t, err, codes.NotFound)
}

// TestDeleteRetention_missingRepoID_returnsInvalidArgument.
func TestDeleteRetention_missingRepoID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.DeleteRepoRetentionPolicy(context.Background(), &metadatav1.DeleteRepoRetentionPolicyRequest{
		TenantId: "t1",
	})
	requireCode(t, err, codes.InvalidArgument)
}
