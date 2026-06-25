package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// validAnalyticsRequest is the common starting shape for the analytics tests.
// Bucket arithmetic mirrors the BFF's 24h preset (24 buckets of 1h each).
func validAnalyticsRequest() *auditv1.GetAnalyticsRequest {
	return &auditv1.GetAnalyticsRequest{
		TenantId:   uuid.New().String(),
		ScopeType:  "tenant",
		Action:     "push.image",
		RangeSecs:  24 * 3600,
		BucketSecs: 3600,
	}
}

func TestGetAnalytics_validTenantScope_returnsBuckets(t *testing.T) {
	// Two populated buckets — handler must echo them verbatim and sum the total.
	now := time.Now().UTC().Truncate(time.Hour)
	fake := &fakeRepo{
		analytics: []*repository.AnalyticsBucketRow{
			{BucketStart: now.Add(-2 * time.Hour), Count: 3},
			{BucketStart: now.Add(-time.Hour), Count: 5},
		},
	}
	h := newHandler(fake)

	resp, err := h.GetAnalytics(context.Background(), validAnalyticsRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(resp.GetBuckets()) != 2 {
		t.Fatalf("expected 2 buckets, got %d", len(resp.GetBuckets()))
	}
	if resp.GetTotal() != 8 {
		t.Errorf("expected total=8 (3+5), got %d", resp.GetTotal())
	}
	if resp.GetRangeStart() == nil {
		t.Errorf("expected range_start to be set")
	}

	// One call must have been routed to the repo with TenantWide=true.
	if len(fake.analyticsCalls) != 1 {
		t.Fatalf("expected 1 repo call, got %d", len(fake.analyticsCalls))
	}
	if !fake.analyticsCalls[0].scope.TenantWide {
		t.Errorf("expected TenantWide=true for scope=tenant")
	}
	if fake.analyticsCalls[0].bucketSecs != 3600 {
		t.Errorf("expected bucketSecs=3600, got %d", fake.analyticsCalls[0].bucketSecs)
	}
}

func TestGetAnalytics_validRepoScope_forwardsRepoID(t *testing.T) {
	repoID := uuid.New().String()
	fake := &fakeRepo{}
	h := newHandler(fake)

	req := validAnalyticsRequest()
	req.ScopeType = "repo"
	req.RepoId = repoID

	_, err := h.GetAnalytics(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.analyticsCalls) != 1 {
		t.Fatalf("expected 1 repo call, got %d", len(fake.analyticsCalls))
	}
	if fake.analyticsCalls[0].scope.TenantWide {
		t.Errorf("expected TenantWide=false for scope=repo")
	}
	if fake.analyticsCalls[0].scope.RepoID != repoID {
		t.Errorf("expected RepoID=%q, got %q", repoID, fake.analyticsCalls[0].scope.RepoID)
	}
}

func TestGetAnalytics_invalidTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	req := validAnalyticsRequest()
	req.TenantId = "not-a-uuid"

	_, err := h.GetAnalytics(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetAnalytics_unknownScopeType_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	req := validAnalyticsRequest()
	req.ScopeType = "org" // not allowed

	_, err := h.GetAnalytics(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetAnalytics_repoScopeMissingRepoID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	req := validAnalyticsRequest()
	req.ScopeType = "repo"
	req.RepoId = "" // missing

	_, err := h.GetAnalytics(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetAnalytics_unknownAction_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	req := validAnalyticsRequest()
	req.Action = "tenant.created"

	_, err := h.GetAnalytics(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetAnalytics_negativeRangeSecs_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	req := validAnalyticsRequest()
	req.RangeSecs = 0

	_, err := h.GetAnalytics(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetAnalytics_bucketSecsBelowMinimum_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	req := validAnalyticsRequest()
	req.BucketSecs = 30 // less than the 60s minimum

	_, err := h.GetAnalytics(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetAnalytics_rangeNotMultipleOfBucket_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	req := validAnalyticsRequest()
	req.RangeSecs = 24 * 3600
	req.BucketSecs = 700 // does not divide evenly

	_, err := h.GetAnalytics(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetAnalytics_tooManyBuckets_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	req := validAnalyticsRequest()
	// 365 buckets is the limit; ask for one more.
	req.RangeSecs = 366 * 60
	req.BucketSecs = 60

	_, err := h.GetAnalytics(context.Background(), req)
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("expected InvalidArgument, got %v", err)
	}
}

func TestGetAnalytics_emptyResult_returnsZeroTotal(t *testing.T) {
	h := newHandler(&fakeRepo{analytics: nil})

	resp, err := h.GetAnalytics(context.Background(), validAnalyticsRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetTotal() != 0 {
		t.Errorf("expected total=0, got %d", resp.GetTotal())
	}
	if len(resp.GetBuckets()) != 0 {
		t.Errorf("expected 0 buckets, got %d", len(resp.GetBuckets()))
	}
}

func TestGetAnalytics_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{analyticsErr: errors.New("db offline")})

	_, err := h.GetAnalytics(context.Background(), validAnalyticsRequest())
	if status.Code(err) != codes.Internal {
		t.Errorf("expected Internal, got %v", err)
	}
}

func TestGetAnalytics_rangeStartAlignedToBucketBoundary(t *testing.T) {
	// 1-hour buckets: range_start must be at the top of an hour, not mid-hour.
	h := newHandler(&fakeRepo{})

	resp, err := h.GetAnalytics(context.Background(), validAnalyticsRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rs := resp.GetRangeStart().AsTime()
	if rs.Minute() != 0 || rs.Second() != 0 {
		t.Errorf("expected range_start aligned to top of hour, got %v", rs)
	}
}

// REM-020 Fix B regression: the window must extend AT-OR-PAST `now`.
// Previously rangeEnd was aligned-down (rangeStart) + rangeSecs, which
// dropped up to one bucket of trailing activity (6h for 7d/6h, 1d for
// 30d/1d). A push at 15:00 with rangeEnd at 12:00 silently fell outside
// the window and the dashboard read "0 pushes" while audit_events held
// the row.
func TestGetAnalytics_rangeEndCoversNow(t *testing.T) {
	repo := &fakeRepo{}
	h := newHandler(repo)
	req := validAnalyticsRequest()

	before := time.Now().UTC()
	_, err := h.GetAnalytics(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repo.analyticsCalls) != 1 {
		t.Fatalf("analyticsCalls: got %d, want 1", len(repo.analyticsCalls))
	}
	call := repo.analyticsCalls[0]

	// rangeEnd MUST be at or past `now` (the moment we captured before the
	// call) so newly-landed events at `now` cannot fall outside the window.
	if call.rangeEnd.Before(before) {
		t.Errorf("rangeEnd %v < now %v — recent activity would be excluded",
			call.rangeEnd, before)
	}
	// Sanity: width must still equal range_secs so the bucket grid lines up
	// with the BFF's pre-allocation.
	if got := call.rangeEnd.Sub(call.rangeStart); got != time.Duration(req.GetRangeSecs())*time.Second {
		t.Errorf("window width: got %v, want %v", got, time.Duration(req.GetRangeSecs())*time.Second)
	}
}
