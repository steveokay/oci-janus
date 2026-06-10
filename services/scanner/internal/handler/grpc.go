// Package handler implements the ScannerService gRPC server.
package handler

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/scanner/internal/store"
	"github.com/steveokay/oci-janus/services/scanner/internal/worker"
)

// GRPCHandler implements scannerv1.ScannerServiceServer.
type GRPCHandler struct {
	scannerv1.UnimplementedScannerServiceServer
	pool      *worker.Pool
	scanStore *store.Store
}

// New creates a GRPCHandler.
func New(pool *worker.Pool, scanStore *store.Store) *GRPCHandler {
	return &GRPCHandler{pool: pool, scanStore: scanStore}
}

// TriggerScan manually queues a scan for a manifest that has already been pushed.
// This is used by CI/CD pipelines to force a re-scan or scan on demand.
func (h *GRPCHandler) TriggerScan(ctx context.Context, req *scannerv1.TriggerScanRequest) (*scannerv1.TriggerScanResponse, error) {
	if req.TenantId == "" || req.ManifestDigest == "" || req.RepositoryName == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id, repository_name, and manifest_digest are required")
	}

	// repo_id is unknown at this point — the worker will look up the manifest by digest.
	// We pass repository_name as the repo identifier; the metadata service resolves it.
	scanID := h.pool.TriggerScanJob(req.TenantId, "", req.RepositoryName, req.ManifestDigest)
	return &scannerv1.TriggerScanResponse{ScanId: scanID}, nil
}

// GetScanStatus returns the current status of a scan job by scan_id.
func (h *GRPCHandler) GetScanStatus(_ context.Context, req *scannerv1.GetScanStatusRequest) (*scannerv1.GetScanStatusResponse, error) {
	if req.ScanId == "" {
		return nil, status.Error(codes.InvalidArgument, "scan_id is required")
	}

	rec, ok := h.scanStore.Get(req.ScanId)
	if !ok {
		return nil, status.Errorf(codes.NotFound, "scan %s not found", req.ScanId)
	}

	counts := make(map[string]int32, len(rec.SeverityCounts))
	for k, v := range rec.SeverityCounts {
		counts[k] = int32(v)
	}

	resp := &scannerv1.GetScanStatusResponse{
		Status:         rec.Status,
		SeverityCounts: counts,
	}
	if rec.CompletedAt != nil {
		resp.CompletedAt = timestampProto(rec.CompletedAt)
	}
	return resp, nil
}

func timestampProto(t *time.Time) *timestamppb.Timestamp {
	if t == nil {
		return nil
	}
	return timestamppb.New(*t)
}
