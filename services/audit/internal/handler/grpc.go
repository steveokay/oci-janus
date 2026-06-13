// Package handler provides HTTP and gRPC endpoints for the audit service.
// This file implements the gRPC AuditService — specifically GetBuildHistory,
// which translates audit_events rows into BuildRecord proto messages for consumers
// such as registry-management.
package handler

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// GRPCHandler implements auditv1.AuditServiceServer.
type GRPCHandler struct {
	auditv1.UnimplementedAuditServiceServer
	repo *repository.Repository
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
		return nil, status.Error(codes.Internal, "failed to query build history")
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
