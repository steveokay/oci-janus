// Package handler — analytics.go
//
// FE-API-030 — gRPC GetAnalytics RPC.
//
// Returns a time-bucketed series of audit-event counts for a single action,
// either tenant-wide or scoped to one repository. The BFF (registry-management)
// picks the bucket size / range — keeping the gRPC API generic means a future
// per-org or per-tag analytics route can reuse the same RPC without a proto
// round trip.
//
// This handler intentionally does NOT pre-allocate empty buckets — the BFF
// builds the zero-filled grid and merges the populated rows we return so the
// gRPC wire payload stays small for sparse 30-day series.
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// allowedAnalyticsActions is the action allowlist for GetAnalytics. We
// deliberately keep this narrow — currently only push/pull, the two metrics
// FE-API-030 surfaces — so a caller can't trigger a count of an arbitrary
// audit action just by changing the parameter.
//
// Note: pull.image is not currently produced by services/audit's
// eventconsumer (the core/pull path doesn't publish a routing key the audit
// service consumes — see services/audit/internal/eventconsumer/consumer.go).
// The query still works correctly when pull.image is absent — it just
// returns zero rows — which is the correct behaviour for FE-API-030: the
// BFF pre-allocates an empty series and the dashboard shows a flat-zero
// sparkline until pull events start being written.
var allowedAnalyticsActions = map[string]struct{}{
	"push.image": {},
	"pull.image": {},
}

// maxAnalyticsRangeSecs caps the look-back at 90 days to align with the
// partition retention. Asking for a deeper window is allowed by the proto
// but returns no extra data and just wastes a query plan, so we clamp here.
const maxAnalyticsRangeSecs int64 = 90 * 24 * 3600

// minAnalyticsBucketSecs is the smallest bucket the handler accepts. One
// minute is finer-grained than any UI currently renders but lets a future
// real-time view share the same RPC without a proto change.
const minAnalyticsBucketSecs int64 = 60

// maxAnalyticsBuckets caps the number of buckets a single call may produce.
// A request for 90 days at 1-minute buckets would return ~130k rows which
// is far past what a sparkline can render — and is almost certainly a bug.
const maxAnalyticsBuckets int64 = 365

// GetAnalytics returns a time-bucketed series of audit-event counts for one
// action. See proto/audit/v1/audit.proto for the wire contract.
//
//	The handler:
//	  - validates tenant_id, scope_type, action against allowlists;
//	  - clamps range_secs to a 90-day max look-back and bucket_secs to a
//	    minimum of 60s plus a max-bucket-count guard;
//	  - aligns range_start to a bucket boundary using date_bin in SQL so two
//	    calls with the same parameters at slightly different wall-clock times
//	    still produce a comparable grid;
//	  - returns only populated buckets (the BFF pre-allocates zeros).
func (h *GRPCHandler) GetAnalytics(ctx context.Context, req *auditv1.GetAnalyticsRequest) (*auditv1.GetAnalyticsResponse, error) {
	tenantUUID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	scope, err := resolveAnalyticsScope(req.GetScopeType(), req.GetRepoId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	action := req.GetAction()
	if _, ok := allowedAnalyticsActions[action]; !ok {
		return nil, status.Error(codes.InvalidArgument, "action not allowed")
	}

	rangeSecs := req.GetRangeSecs()
	if rangeSecs <= 0 {
		return nil, status.Error(codes.InvalidArgument, "range_secs must be positive")
	}
	if rangeSecs > maxAnalyticsRangeSecs {
		rangeSecs = maxAnalyticsRangeSecs
	}

	bucketSecs := req.GetBucketSecs()
	if bucketSecs < minAnalyticsBucketSecs {
		return nil, status.Error(codes.InvalidArgument, "bucket_secs below minimum (60)")
	}
	if bucketSecs > rangeSecs {
		return nil, status.Error(codes.InvalidArgument, "bucket_secs greater than range_secs")
	}
	// Require an evenly divisible grid so the BFF's pre-allocation lines up
	// 1:1 with the date_bin output. Anything else risks an off-by-one bucket
	// at the cutoff edge.
	if rangeSecs%bucketSecs != 0 {
		return nil, status.Error(codes.InvalidArgument, "range_secs must be a multiple of bucket_secs")
	}
	if rangeSecs/bucketSecs > maxAnalyticsBuckets {
		return nil, status.Error(codes.InvalidArgument, "too many buckets requested")
	}

	// REM-020 Fix B: align rangeEnd to the next bucket boundary at-or-after
	// `now` so the trailing bucket is guaranteed to cover the current
	// moment. The previous implementation truncated rangeStart DOWN to the
	// nearest bucket and set rangeEnd = rangeStart + rangeSecs — which
	// silently dropped activity in the interval (rangeEnd, now], up to one
	// bucket wide (6h for 7d/6h, 1d for 30d/1d). On a fresh push the
	// dashboard read "0 pulls / pushes last 7d" with audit_events
	// containing the new rows because they fell after rangeEnd.
	//
	// rangeStart slides back by the same amount so the (rangeStart,
	// rangeEnd] window is still exactly rangeSecs wide and the bucket grid
	// still aligns 1:1 with the BFF's pre-allocation. date_bin uses
	// rangeStart as the origin so the first bucket boundary is exactly
	// rangeStart.
	now := time.Now().UTC()
	rangeEnd := time.Unix(((now.Unix()+bucketSecs-1)/bucketSecs)*bucketSecs, 0).UTC()
	rangeStart := rangeEnd.Add(-time.Duration(rangeSecs) * time.Second)

	rows, err := h.repo.GetAnalytics(ctx, tenantUUID, scope, action, rangeStart, rangeEnd, bucketSecs)
	if err != nil {
		slog.ErrorContext(ctx, "GetAnalytics query failed",
			"tenant_id", req.GetTenantId(),
			"scope_type", req.GetScopeType(),
			"action", action,
			"error", err,
		)
		return nil, errcodes.MapDBError(err, "failed to query analytics")
	}

	// Project the rows into the proto wire shape. The total is summed here
	// rather than in SQL so the handler stays the single source of truth
	// for what counts as "in the window" — keeps the migration path open
	// for future filters without divergent SQL/Go totals.
	var total int64
	buckets := make([]*auditv1.AnalyticsBucket, 0, len(rows))
	for _, r := range rows {
		buckets = append(buckets, &auditv1.AnalyticsBucket{
			BucketStart: timestamppb.New(r.BucketStart),
			Count:       r.Count,
		})
		total += r.Count
	}

	return &auditv1.GetAnalyticsResponse{
		Buckets:    buckets,
		Total:      total,
		RangeStart: timestamppb.New(rangeStart),
	}, nil
}

// resolveAnalyticsScope parses the (scope_type, repo_id) request fields into
// the repository-layer scope shape. Unknown scope_type values are rejected
// here so the SQL layer never sees an unchecked string.
func resolveAnalyticsScope(scopeType, repoID string) (repository.AnalyticsScope, error) {
	switch scopeType {
	case "tenant":
		return repository.AnalyticsScope{TenantWide: true}, nil
	case "repo":
		if repoID == "" {
			return repository.AnalyticsScope{}, fmt.Errorf("repo_id required for repo scope")
		}
		if _, err := uuid.Parse(repoID); err != nil {
			return repository.AnalyticsScope{}, fmt.Errorf("invalid repo_id")
		}
		return repository.AnalyticsScope{TenantWide: false, RepoID: repoID}, nil
	default:
		return repository.AnalyticsScope{}, fmt.Errorf("unknown scope_type")
	}
}
