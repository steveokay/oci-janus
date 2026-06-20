// Package handler implements the TenantService gRPC server.
package handler

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

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
	// platformBaseDomain backs the wildcard `<slug>.<base>` host fallback for
	// tenants without a verified primary custom domain (FE-API-007). Empty is
	// allowed but disables the fallback — Tenant.host will be "" in that case
	// so callers can detect the misconfiguration.
	platformBaseDomain string
}

// New creates a GRPCHandler. platformBaseDomain is the wildcard zone every
// tenant gets a hostname under; pass "" to disable the fallback (tests).
func New(repo *repository.Repository, platformBaseDomain string) *GRPCHandler {
	return &GRPCHandler{repo: repo, platformBaseDomain: platformBaseDomain}
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
	// Brand-new tenant — no domains yet, so the host always comes from the
	// wildcard fallback. Pass nil to keep the proto-building helper uniform.
	return h.buildTenantProto(rec, nil), nil
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
	// Pull every domain so the host-selection algorithm sees the full picture
	// (primary first, then verified, then unverified). A missing domains
	// table or non-fatal error degrades to an empty list — the wildcard
	// fallback host still works.
	domains, err := h.repo.ListDomainsByTenant(ctx, tenantID)
	if err != nil {
		return nil, errcodes.MapDBError(err, "list tenant domains")
	}
	return h.buildTenantProto(rec, domains), nil
}

// ListTenants returns a page of tenants ordered by created_at DESC.
// The page_token is an opaque base64-url-encoded cursor — clients must not
// inspect or construct it; the server decodes it back to (created_at, id).
func (h *GRPCHandler) ListTenants(ctx context.Context, req *tenantv1.ListTenantsRequest) (*tenantv1.ListTenantsResponse, error) {
	afterCreated, afterID, err := decodePageToken(req.GetPageToken())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid page_token: %v", err)
	}

	recs, err := h.repo.ListTenants(ctx, req.GetPageSize(), afterCreated, afterID)
	if err != nil {
		return nil, errcodes.MapDBError(err, "list tenants")
	}

	tenants := make([]*tenantv1.Tenant, 0, len(recs))
	for i := range recs {
		// Listing skips the per-tenant domains lookup to keep page latency
		// bounded — the host falls back to the wildcard, and clients that
		// need the full domain list call GetTenant for the specific tenant.
		tenants = append(tenants, h.buildTenantProto(&recs[i], nil))
	}

	// Only emit next_page_token when this page is full — a short page implies
	// the caller has read every row, so no further pagination is needed.
	var nextToken string
	pageSize := req.GetPageSize()
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	if int32(len(recs)) == pageSize {
		last := recs[len(recs)-1]
		nextToken = encodePageToken(last.CreatedAt, last.ID)
	}

	return &tenantv1.ListTenantsResponse{
		Tenants:       tenants,
		NextPageToken: nextToken,
	}, nil
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

// tenantToProto remains for tests and any caller that needs the bare 1-4
// field shape. Real handler paths go through buildTenantProto so they pick
// up slug / host / domains (FE-API-007).
func tenantToProto(rec *repository.TenantRecord) *tenantv1.Tenant {
	return &tenantv1.Tenant{
		TenantId:  rec.ID.String(),
		Name:      rec.Name,
		Plan:      rec.Plan,
		Slug:      rec.Slug,
		CreatedAt: timestamppb.New(rec.CreatedAt),
	}
}

// buildTenantProto applies the FE-API-007 host-selection algorithm and emits
// a fully populated Tenant message:
//   1. If any domain in `domains` has is_primary=true AND verified=true, that
//      domain is the host and host_is_custom=true.
//   2. Otherwise host = `<slug>.<platformBaseDomain>` and host_is_custom=false.
//      When platformBaseDomain is empty (tests / misconfig), host falls back
//      to the bare slug so the field is never the meaningless ".".
// Slug fallback: empty Slug → use the tenant id, so the host always parses
// as a valid hostname even if the migration backfill produced nothing.
func (h *GRPCHandler) buildTenantProto(rec *repository.TenantRecord, domains []repository.DomainRecord) *tenantv1.Tenant {
	out := &tenantv1.Tenant{
		TenantId:  rec.ID.String(),
		Name:      rec.Name,
		Plan:      rec.Plan,
		Slug:      rec.Slug,
		CreatedAt: timestamppb.New(rec.CreatedAt),
	}

	// Surface every domain so the BFF can render verification status — the
	// dashboard's domain settings page needs the full list, not just the host.
	out.Domains = make([]*tenantv1.DomainEntry, 0, len(domains))
	for _, d := range domains {
		out.Domains = append(out.Domains, &tenantv1.DomainEntry{
			Domain:    d.Domain,
			Verified:  d.Verified,
			IsPrimary: d.IsPrimary,
		})
	}

	// Host selection — primary verified domain wins over the wildcard fallback.
	for _, d := range domains {
		if d.IsPrimary && d.Verified {
			out.Host = d.Domain
			out.HostIsCustom = true
			return out
		}
	}

	// Wildcard fallback. Empty slug → tenant id; empty base domain → bare slug.
	slug := rec.Slug
	if slug == "" {
		slug = rec.ID.String()
	}
	if h.platformBaseDomain != "" {
		out.Host = slug + "." + h.platformBaseDomain
	} else {
		out.Host = slug
	}
	return out
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

// encodePageToken returns an opaque cursor encoding (created_at, id) so clients
// cannot construct one by hand. Format: base64url("<RFC3339Nano>|<uuid>").
func encodePageToken(createdAt time.Time, id uuid.UUID) string {
	raw := createdAt.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodePageToken reverses encodePageToken. An empty input returns a zero
// time + zero UUID so the repository emits the first page.
func decodePageToken(token string) (time.Time, uuid.UUID, error) {
	if token == "" {
		return time.Time{}, uuid.Nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("base64: %w", err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, fmt.Errorf("malformed cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("time: %w", err)
	}
	id, err := uuid.Parse(parts[1])
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("uuid: %w", err)
	}
	return createdAt, id, nil
}
