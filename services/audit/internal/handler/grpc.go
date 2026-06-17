// Package handler provides HTTP and gRPC endpoints for the audit service.
// This file implements the gRPC AuditService — specifically GetBuildHistory,
// which translates audit_events rows into BuildRecord proto messages for consumers
// such as registry-management.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// auditRepo is the subset of repository.Repository used by GRPCHandler,
// defined as an interface so unit tests can inject a fake.
type auditRepo interface {
	GetBuildHistory(ctx context.Context, tenantID uuid.UUID, repoID, tag string, limit int) ([]*repository.BuildHistoryRow, error)
	CountPulls(ctx context.Context, tenantID uuid.UUID, since time.Time) (int64, error)
}

// GRPCHandler implements auditv1.AuditServiceServer.
type GRPCHandler struct {
	auditv1.UnimplementedAuditServiceServer
	repo auditRepo
}

// NewGRPC returns a GRPCHandler backed by repo.
func NewGRPC(repo *repository.Repository) *GRPCHandler {
	return &GRPCHandler{repo: repo}
}

// GetBuildHistory returns push/build audit records for a specific repo and tag.
// It queries audit_events filtered by tenant_id, repo_id (from metadata JSON),
// and tag, returning results ordered newest-first.
func (h *GRPCHandler) GetBuildHistory(ctx context.Context, req *auditv1.GetBuildHistoryRequest) (*auditv1.GetBuildHistoryResponse, error) {
	// Validate tenant_id is a valid UUID to prevent SQL injection via parameterised queries.
	tenantUUID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if req.GetRepoId() == "" {
		return nil, status.Error(codes.InvalidArgument, "repo_id is required")
	}

	rows, err := h.repo.GetBuildHistory(ctx, tenantUUID, req.GetRepoId(), req.GetTag(), int(req.GetLimit()))
	if err != nil {
		slog.ErrorContext(ctx, "GetBuildHistory query failed",
			"tenant_id", req.GetTenantId(),
			"repo_id", req.GetRepoId(),
			"error", err,
		)
		return nil, errcodes.MapDBError(err, "failed to query build history")
	}

	builds := make([]*auditv1.BuildRecord, 0, len(rows))
	for _, row := range rows {
		builds = append(builds, buildRecordFromRow(row))
	}

	return &auditv1.GetBuildHistoryResponse{
		Builds: builds,
		Total:  int32(len(builds)),
	}, nil
}

// GetDailyPullCount returns the count of pull.image events for the tenant in the
// last 24 hours. Non-zero counts surface on the management dashboard stat tile.
func (h *GRPCHandler) GetDailyPullCount(ctx context.Context, req *auditv1.GetDailyPullCountRequest) (*auditv1.GetDailyPullCountResponse, error) {
	tenantUUID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	count, err := h.repo.CountPulls(ctx, tenantUUID, time.Now().Add(-24*time.Hour))
	if err != nil {
		slog.ErrorContext(ctx, "GetDailyPullCount query failed",
			"tenant_id", req.GetTenantId(),
			"error", err,
		)
		return nil, errcodes.MapDBError(err, "failed to count pull events")
	}
	return &auditv1.GetDailyPullCountResponse{Count: count}, nil
}

// buildRecordFromRow converts a repository.BuildHistoryRow into the proto wire type.
// Optional metadata fields (commit_hash, duration) are extracted from the JSONB
// metadata column if present; missing fields are left as empty strings.
func buildRecordFromRow(row *repository.BuildHistoryRow) *auditv1.BuildRecord {
	// Map audit outcome ("success"/"failure") to the build status vocabulary.
	buildStatus := "success"
	if row.Outcome == "failure" {
		buildStatus = "failed"
	}

	// Extract optional CI metadata stored in the audit event's metadata JSON.
	var meta struct {
		CommitHash string `json:"commit_hash"`
		Duration   string `json:"duration"`
	}
	if len(row.Metadata) > 0 {
		// Best-effort parse — missing keys leave fields as empty strings.
		_ = json.Unmarshal(row.Metadata, &meta)
	}

	return &auditv1.BuildRecord{
		BuildId:     row.ID.String(),
		Status:      buildStatus,
		CommitHash:  meta.CommitHash,
		TriggeredBy: row.ActorID,
		Duration:    meta.Duration,
		OccurredAt:  timestamppb.New(row.OccurredAt),
	}
}
