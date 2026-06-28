// Package handler implements the gRPC ProxyService and the OCI pull-through HTTP handler.
package handler

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	aescrypto "github.com/steveokay/oci-janus/libs/crypto/aes"
	proxyv1 "github.com/steveokay/oci-janus/proto/gen/go/proxy/v1"
	"github.com/steveokay/oci-janus/services/proxy/internal/repository"
	"github.com/steveokay/oci-janus/services/proxy/internal/upstream"
)

// GRPCHandler implements proxyv1.ProxyServiceServer.
type GRPCHandler struct {
	proxyv1.UnimplementedProxyServiceServer
	repo *repository.Repository
	key  []byte // 32-byte AES-256 key for credential encryption
}

// NewGRPCHandler constructs a GRPCHandler.
func NewGRPCHandler(repo *repository.Repository, credentialKeyHex string) (*GRPCHandler, error) {
	key, err := hex.DecodeString(credentialKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode credential key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("credential key must be 32 bytes, got %d", len(key))
	}
	return &GRPCHandler{repo: repo, key: key}, nil
}

// RegisterUpstream creates a new upstream registry configuration for a tenant.
func (h *GRPCHandler) RegisterUpstream(ctx context.Context, req *proxyv1.RegisterUpstreamRequest) (*proxyv1.Upstream, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetUrl() == "" {
		return nil, status.Error(codes.InvalidArgument, "url is required")
	}
	authType := req.GetAuthType()
	if authType == "" {
		authType = "none"
	}
	if authType != "none" && authType != "basic" && authType != "token" {
		return nil, status.Errorf(codes.InvalidArgument, "auth_type must be none, basic, or token; got %q", authType)
	}

	// Validate URL is HTTPS and not a private address (SSRF guard).
	if err := upstream.ValidateUpstreamURL(req.GetUrl()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid upstream URL: %v", err)
	}

	// Encrypt password before storage.
	var passwordEnc []byte
	if req.GetPassword() != "" {
		passwordEnc, err = aescrypto.Encrypt([]byte(req.GetPassword()), h.key)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encrypt credential: %v", err)
		}
	}

	ttl := req.GetTtlSeconds()
	if ttl <= 0 {
		ttl = 3600
	}

	rec, err := h.repo.CreateUpstream(ctx, tenantID, req.GetName(), req.GetUrl(),
		authType, req.GetUsername(), passwordEnc, ttl)
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "upstream %q already exists for tenant", req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "create upstream: %v", err)
	}

	return upstreamToProto(rec), nil
}

// DeleteUpstream removes an upstream registry configuration.
func (h *GRPCHandler) DeleteUpstream(ctx context.Context, req *proxyv1.DeleteUpstreamRequest) (*emptypb.Empty, error) {
	upstreamID, err := uuid.Parse(req.GetUpstreamId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid upstream_id: %v", err)
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}

	if err := h.repo.DeleteUpstream(ctx, upstreamID, tenantID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "upstream not found")
		}
		return nil, status.Errorf(codes.Internal, "delete upstream: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ListUpstreams streams all upstream registry configurations for a tenant.
func (h *GRPCHandler) ListUpstreams(req *proxyv1.ListUpstreamsRequest, stream proxyv1.ProxyService_ListUpstreamsServer) error {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}

	recs, err := h.repo.ListUpstreams(stream.Context(), tenantID)
	if err != nil {
		return status.Errorf(codes.Internal, "list upstreams: %v", err)
	}

	for _, rec := range recs {
		if err := stream.Send(upstreamToProto(rec)); err != nil {
			return err
		}
	}
	return nil
}

func upstreamToProto(rec *repository.UpstreamRecord) *proxyv1.Upstream {
	return &proxyv1.Upstream{
		UpstreamId: rec.UpstreamID.String(),
		TenantId:   rec.TenantID.String(),
		Name:       rec.Name,
		Url:        rec.URL,
		AuthType:   rec.AuthType,
		Enabled:    rec.Enabled,
		TtlSeconds: rec.TTLSeconds,
	}
}

// ── FUT-013 — cache visibility surface ────────────────────────────────────────

// ListCachedManifests returns one page of the tenant's cache, keyed
// off a caller-opaque page_token. See proto/proxy/v1/proxy.proto for
// pagination contract.
func (h *GRPCHandler) ListCachedManifests(ctx context.Context, req *proxyv1.ListCachedManifestsRequest) (*proxyv1.ListCachedManifestsResponse, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}

	var upstreamID uuid.UUID
	if req.GetUpstreamId() != "" {
		upstreamID, err = uuid.Parse(req.GetUpstreamId())
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid upstream_id: %v", err)
		}
	}

	afterFetched, afterID, err := decodePageToken(req.GetPageToken())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid page_token: %v", err)
	}

	limit := int(req.GetPageSize())
	rows, err := h.repo.ListCachedManifests(ctx, tenantID, upstreamID,
		req.GetImageContains(), afterFetched, afterID, limit)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list cached manifests: %v", err)
	}

	resp := &proxyv1.ListCachedManifestsResponse{
		Manifests: make([]*proxyv1.CachedManifest, 0, len(rows)),
	}
	for _, row := range rows {
		resp.Manifests = append(resp.Manifests, cachedManifestToProto(row))
	}
	// We asked for `limit` rows; if we got exactly `limit` there might be
	// more, so emit a cursor pointing past the last row. If we got fewer,
	// we know we hit the end.
	effLimit := limit
	if effLimit <= 0 || effLimit > 100 {
		effLimit = 50
	}
	if len(rows) == effLimit {
		last := rows[len(rows)-1]
		resp.NextPageToken = encodePageToken(last.FetchedAt, last.ID)
	}
	return resp, nil
}

// GetCacheStats returns the page-header aggregate for the cache page.
func (h *GRPCHandler) GetCacheStats(ctx context.Context, req *proxyv1.GetCacheStatsRequest) (*proxyv1.CacheStats, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}
	rec, err := h.repo.GetCacheStats(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get cache stats: %v", err)
	}
	return &proxyv1.CacheStats{
		TotalManifests:  rec.TotalManifests,
		TotalBytes:      rec.TotalBytes,
		UniqueUpstreams: rec.UniqueUpstreams,
		TotalPulls:      rec.TotalPulls,
	}, nil
}

// GetCachedManifest returns the FUT-016 detail-page projection for a
// single proxy_manifests row, body bytes included. The BFF parses the
// body server-side; we keep this handler dumb (repo lookup + proto
// mapping only).
func (h *GRPCHandler) GetCachedManifest(ctx context.Context, req *proxyv1.GetCachedManifestRequest) (*proxyv1.CachedManifestDetail, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id: %v", err)
	}
	row, err := h.repo.GetCachedManifestByID(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "cached manifest not found")
		}
		return nil, status.Errorf(codes.Internal, "get cached manifest: %v", err)
	}
	return &proxyv1.CachedManifestDetail{
		Manifest: cachedManifestToProto(&repository.CachedManifestRow{
			ID:           row.ID,
			UpstreamID:   row.UpstreamID,
			UpstreamName: row.UpstreamName,
			Image:        row.Image,
			Reference:    row.Reference,
			Digest:       row.Digest,
			MediaType:    row.MediaType,
			SizeBytes:    row.SizeBytes,
			FetchedAt:    row.FetchedAt,
			LastPulledAt: row.LastPulledAt,
			PullCount:    row.PullCount,
		}),
		Body: row.Body,
	}, nil
}

// DeleteCachedManifest evicts a single cached manifest row by id.
// The underlying layer blobs in services/storage are NOT removed here —
// that's the existing GC mark-sweep's job. See the package doc on
// repository.DeleteCachedManifestByID for the rationale.
func (h *GRPCHandler) DeleteCachedManifest(ctx context.Context, req *proxyv1.DeleteCachedManifestRequest) (*emptypb.Empty, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}
	id, err := uuid.Parse(req.GetId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid id: %v", err)
	}
	if err := h.repo.DeleteCachedManifestByID(ctx, tenantID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "cached manifest not found")
		}
		return nil, status.Errorf(codes.Internal, "delete cached manifest: %v", err)
	}
	return &emptypb.Empty{}, nil
}

func cachedManifestToProto(rec *repository.CachedManifestRow) *proxyv1.CachedManifest {
	out := &proxyv1.CachedManifest{
		Id:           rec.ID.String(),
		UpstreamId:   rec.UpstreamID.String(),
		UpstreamName: rec.UpstreamName,
		Image:        rec.Image,
		Reference:    rec.Reference,
		Digest:       rec.Digest,
		MediaType:    rec.MediaType,
		SizeBytes:    rec.SizeBytes,
		FetchedAt:    timestamppb.New(rec.FetchedAt),
		PullCount:    rec.PullCount,
	}
	if rec.LastPulledAt != nil {
		out.LastPulledAt = timestamppb.New(*rec.LastPulledAt)
	}
	return out
}

// Page tokens are base64(unixnano|uuid). Opaque to callers; only this
// service should encode/decode them. Versioned by a one-byte prefix so
// we can extend the schema later without breaking in-flight tokens.
const pageTokenV1 = byte(1)

func encodePageToken(fetchedAt time.Time, id uuid.UUID) string {
	raw := fmt.Sprintf("%c%d|%s", pageTokenV1, fetchedAt.UnixNano(), id.String())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodePageToken(token string) (time.Time, uuid.UUID, error) {
	if token == "" {
		return time.Time{}, uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("decode: %w", err)
	}
	if len(raw) < 2 || raw[0] != pageTokenV1 {
		return time.Time{}, uuid.Nil, fmt.Errorf("unsupported token version")
	}
	parts := strings.SplitN(string(raw[1:]), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, fmt.Errorf("malformed token body")
	}
	var ns int64
	if _, err := fmt.Sscanf(parts[0], "%d", &ns); err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("parse timestamp: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("parse id: %w", err)
	}
	return time.Unix(0, ns).UTC(), id, nil
}
