// Package handler implements the TenantService gRPC server.
package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
)

// GRPCHandler implements tenantv1.TenantServiceServer.
type GRPCHandler struct {
	tenantv1.UnimplementedTenantServiceServer
	repo *repository.Repository
}

// New creates a GRPCHandler.
func New(repo *repository.Repository) *GRPCHandler {
	return &GRPCHandler{repo: repo}
}

// CreateTenant creates a new tenant with a default policy.
func (h *GRPCHandler) CreateTenant(ctx context.Context, req *tenantv1.CreateTenantRequest) (*tenantv1.Tenant, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	plan := req.Plan
	if plan == "" {
		plan = "standard"
	}

	rec, err := h.repo.CreateTenant(ctx, req.Name, plan)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create tenant: %v", err)
	}
	return tenantToProto(rec), nil
}

// GetTenant returns a tenant by ID.
func (h *GRPCHandler) GetTenant(ctx context.Context, req *tenantv1.GetTenantRequest) (*tenantv1.Tenant, error) {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	rec, err := h.repo.GetTenant(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get tenant: %v", err)
	}
	if rec == nil {
		return nil, status.Errorf(codes.NotFound, "tenant %s not found", req.TenantId)
	}
	return tenantToProto(rec), nil
}

// DeleteTenant removes a tenant.
func (h *GRPCHandler) DeleteTenant(ctx context.Context, req *tenantv1.DeleteTenantRequest) (*emptypb.Empty, error) {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	if err := h.repo.DeleteTenant(ctx, tenantID); err != nil {
		return nil, status.Errorf(codes.Internal, "delete tenant: %v", err)
	}
	return &emptypb.Empty{}, nil
}

// ResolveDomain returns the tenant_id for a verified custom domain.
func (h *GRPCHandler) ResolveDomain(ctx context.Context, req *tenantv1.ResolveDomainRequest) (*tenantv1.ResolveDomainResponse, error) {
	if req.Domain == "" {
		return nil, status.Error(codes.InvalidArgument, "domain is required")
	}
	tenantID, found, err := h.repo.ResolveDomain(ctx, req.Domain)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolve domain: %v", err)
	}
	resp := &tenantv1.ResolveDomainResponse{Found: found}
	if found {
		resp.TenantId = tenantID.String()
	}
	return resp, nil
}

// RegisterDomain starts the custom domain verification flow.
// Returns a DNS TXT verification token the tenant must publish at _registry-verify.<domain>.
func (h *GRPCHandler) RegisterDomain(ctx context.Context, req *tenantv1.RegisterDomainRequest) (*tenantv1.RegisterDomainResponse, error) {
	if req.TenantId == "" || req.Domain == "" {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and domain are required")
	}
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	token, err := generateToken()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate token: %v", err)
	}

	if _, err := h.repo.RegisterDomain(ctx, tenantID, req.Domain, token); err != nil {
		return nil, status.Errorf(codes.Internal, "register domain: %v", err)
	}

	return &tenantv1.RegisterDomainResponse{VerificationToken: token}, nil
}

// GetTenantPolicy returns a tenant's scan/signing/quota policy.
func (h *GRPCHandler) GetTenantPolicy(ctx context.Context, req *tenantv1.GetTenantPolicyRequest) (*tenantv1.TenantPolicy, error) {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}
	p, err := h.repo.GetPolicy(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get policy: %v", err)
	}
	return policyToProto(p), nil
}

// UpdateTenantPolicy upserts a tenant's policy.
func (h *GRPCHandler) UpdateTenantPolicy(ctx context.Context, req *tenantv1.UpdateTenantPolicyRequest) (*tenantv1.TenantPolicy, error) {
	tenantID, err := uuid.Parse(req.TenantId)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	p := &repository.PolicyRecord{
		TenantID:           tenantID,
		ScanOnPush:         req.ScanOnPush,
		BlockOnSeverity:    req.BlockOnSeverity,
		AllowUnscanned:     req.AllowUnscanned,
		ProxyCacheEnabled:  req.ProxyCacheEnabled,
		SigningRequired:    req.SigningRequired,
		ExemptRepositories: req.ExemptRepositories,
		StorageQuotaBytes:  req.StorageQuotaBytes,
	}
	if err := h.repo.UpdatePolicy(ctx, p); err != nil {
		return nil, status.Errorf(codes.Internal, "update policy: %v", err)
	}
	return policyToProto(p), nil
}

func tenantToProto(rec *repository.TenantRecord) *tenantv1.Tenant {
	return &tenantv1.Tenant{
		TenantId:  rec.ID.String(),
		Name:      rec.Name,
		Plan:      rec.Plan,
		CreatedAt: timestamppb.New(rec.CreatedAt),
	}
}

func policyToProto(p *repository.PolicyRecord) *tenantv1.TenantPolicy {
	return &tenantv1.TenantPolicy{
		TenantId:           p.TenantID.String(),
		ScanOnPush:         p.ScanOnPush,
		BlockOnSeverity:    p.BlockOnSeverity,
		AllowUnscanned:     p.AllowUnscanned,
		ProxyCacheEnabled:  p.ProxyCacheEnabled,
		SigningRequired:    p.SigningRequired,
		ExemptRepositories: p.ExemptRepositories,
		StorageQuotaBytes:  p.StorageQuotaBytes,
	}
}

// generateToken returns a 32-byte cryptographically random hex string.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
