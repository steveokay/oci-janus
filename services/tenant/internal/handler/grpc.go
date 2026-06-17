// Package handler implements the TenantService gRPC server.
package handler

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
)

// domainRE matches RFC 1123 fully-qualified domain names.
// Used to reject non-hostname values before they reach the DB, DNS lookup, or Redis key.
// Allows only lowercase labels separated by dots, with a multi-char TLD.
var domainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// tenantNameRE enforces the CLAUDE.md §7 org name rule: lowercase alphanumeric + hyphens,
// 2-64 characters. Tenant names are used as subdomains so must be DNS-safe.
var tenantNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

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

	// Validate tenant name: must be a DNS-safe lowercase identifier per CLAUDE.md §7.
	// This prevents SQL injection via name, subdomain hijacking, and Redis key confusion.
	if !tenantNameRE.MatchString(req.Name) {
		return nil, status.Errorf(codes.InvalidArgument, "tenant name must match ^[a-z0-9][a-z0-9-]{1,63}$")
	}

	plan := req.Plan
	if plan == "" {
		plan = "standard"
	}

	rec, err := h.repo.CreateTenant(ctx, req.Name, plan)
	if err != nil {
		// Map PostgreSQL unique violation to a gRPC AlreadyExists so callers get
		// a meaningful status code instead of an opaque Internal error.
		if isDuplicateKeyError(err) {
			return nil, status.Errorf(codes.AlreadyExists, "tenant name %q is already in use", req.Name)
		}
		return nil, errcodes.MapDBError(err, "create tenant")
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
		return nil, errcodes.MapDBError(err, "get tenant")
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
		return nil, errcodes.MapDBError(err, "delete tenant")
	}
	return &emptypb.Empty{}, nil
}

// ResolveDomain returns the tenant_id for a verified custom domain.
func (h *GRPCHandler) ResolveDomain(ctx context.Context, req *tenantv1.ResolveDomainRequest) (*tenantv1.ResolveDomainResponse, error) {
	if req.Domain == "" {
		return nil, status.Error(codes.InvalidArgument, "domain is required")
	}

	// Reject raw IP addresses — a domain lookup on an IP would silently succeed
	// and could be used to probe internal network addresses (SSRF).
	if net.ParseIP(req.Domain) != nil {
		return nil, status.Errorf(codes.InvalidArgument, "domain must be a hostname, not an IP address")
	}
	// Validate domain is a well-formed RFC 1123 hostname — rejects null bytes,
	// newlines, and Redis special characters before any downstream use.
	if !domainRE.MatchString(req.Domain) {
		return nil, status.Errorf(codes.InvalidArgument, "domain %q is not a valid RFC 1123 hostname", req.Domain)
	}

	tenantID, found, err := h.repo.ResolveDomain(ctx, req.Domain)
	if err != nil {
		return nil, errcodes.MapDBError(err, "resolve domain")
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

	// Validate domain is a well-formed RFC 1123 hostname — rejects IP addresses,
	// null bytes, newlines, and Redis special characters before any downstream use.
	// This prevents SSRF via DNS lookup on attacker-controlled IPs and Redis key injection.
	if net.ParseIP(req.Domain) != nil {
		return nil, status.Errorf(codes.InvalidArgument, "domain must be a hostname, not an IP address")
	}
	if !domainRE.MatchString(req.Domain) {
		return nil, status.Errorf(codes.InvalidArgument, "domain %q is not a valid RFC 1123 hostname", req.Domain)
	}

	token, err := generateToken()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "generate token: %v", err)
	}

	if _, err := h.repo.RegisterDomain(ctx, tenantID, req.Domain, token); err != nil {
		return nil, errcodes.MapDBError(err, "register domain")
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
		return nil, errcodes.MapDBError(err, "get policy")
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
		return nil, errcodes.MapDBError(err, "update policy")
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

// isDuplicateKeyError reports whether err is a PostgreSQL unique-constraint violation
// (SQLSTATE 23505). Used to surface AlreadyExists gRPC status codes instead of
// opaque Internal errors when callers attempt to create a duplicate resource.
func isDuplicateKeyError(err error) bool {
	var pgErr *pgconn.PgError
	// errors.As unwraps wrapped errors so this works even if the repository
	// layer wraps the pgconn error with fmt.Errorf("...: %w", err).
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// generateToken returns a 32-byte cryptographically random hex string.
func generateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("rand.Read: %w", err)
	}
	return hex.EncodeToString(b), nil
}
