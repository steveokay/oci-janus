// Package handler implements the TenantService gRPC server.
package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/config/loader"
	errcodes "github.com/steveokay/oci-janus/libs/errors/codes"
	tenantv1 "github.com/steveokay/oci-janus/proto/gen/go/tenant/v1"
	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
)

// tenantNameRE enforces the CLAUDE.md §7 org name rule: lowercase alphanumeric + hyphens,
// 2-64 characters. Tenant names are used as subdomains so must be DNS-safe.
var tenantNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,63}$`)

// GRPCHandler implements tenantv1.TenantServiceServer.
type GRPCHandler struct {
	tenantv1.UnimplementedTenantServiceServer
	repo *repository.Repository
	// platformBaseDomain backs the wildcard `<slug>.<base>` host for every
	// tenant (FE-API-007). Empty is allowed but disables the fallback —
	// Tenant.host will be the bare slug in that case.
	platformBaseDomain string
	// deploymentMode controls the Phase 3.2 single-tenant guard in
	// CreateTenant. In "single" mode a second CreateTenant call returns
	// FailedPrecondition. In "multi" mode it's a no-op. Defaults to
	// the zero value DeploymentMode("") which is treated as "fail-closed
	// single" — a server constructed without a mode behaves like single,
	// matching how libs/config/loader.LoadDeploymentMode normalises an
	// empty env to single.
	deploymentMode loader.DeploymentMode
}

// New creates a GRPCHandler. platformBaseDomain is the wildcard zone every
// tenant gets a hostname under; pass "" to disable the fallback (tests).
// deploymentMode is the binary's posture — pass loader.DeploymentModeMulti
// to keep the legacy multi-tenant CreateTenant behaviour; pass
// loader.DeploymentModeSingle (or the zero value, which is treated the
// same) to gate against a second tenant being inserted.
func New(repo *repository.Repository, platformBaseDomain string, deploymentMode loader.DeploymentMode) *GRPCHandler {
	return &GRPCHandler{
		repo:               repo,
		platformBaseDomain: platformBaseDomain,
		deploymentMode:     deploymentMode,
	}
}

// CreateTenant creates a new tenant with a default policy.
//
// REDESIGN-001 Phase 3.2 (Q-001 hard-error) — in single mode the deployment
// owns exactly one tenant (the bootstrap one). Refusing a second CreateTenant
// here is the structural enforcement of CLAUDE.md's redesign banner: a
// misconfigured FE/CLI that tries to mint a second tenant gets a FAILED_PRECONDITION
// rather than silently corrupting the deployment posture. The check defaults
// to fail-closed: a zero-value deploymentMode (handler constructed without a
// mode argument, e.g. in legacy tests) is treated as single mode, matching the
// loader's empty-env normalisation.
func (h *GRPCHandler) CreateTenant(ctx context.Context, req *tenantv1.CreateTenantRequest) (*tenantv1.Tenant, error) {
	if req.Name == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}

	// Validate tenant name: must be a DNS-safe lowercase identifier per CLAUDE.md §7.
	// This prevents SQL injection via name, subdomain hijacking, and Redis key confusion.
	if !tenantNameRE.MatchString(req.Name) {
		return nil, status.Errorf(codes.InvalidArgument, "tenant name must match ^[a-z0-9][a-z0-9-]{1,63}$")
	}

	// Phase 3.2 single-tenant guard. Runs before the actual INSERT so the
	// caller learns the deployment is single-tenant before any DB write.
	// COUNT is checked TOCTOU-unsafe against the INSERT below — that's
	// acceptable because the slug index on `tenants` is the real uniqueness
	// gate; the guard only exists to give a precise error code (FailedPrecondition
	// vs the AlreadyExists the slug index would otherwise produce).
	if h.deploymentMode != loader.DeploymentModeMulti {
		count, err := h.repo.CountTenants(ctx)
		if err != nil {
			return nil, errcodes.MapDBError(err, "count tenants")
		}
		if count >= 1 {
			return nil, status.Error(codes.FailedPrecondition,
				"DEPLOYMENT_MODE=single only allows one tenant; use the bootstrap CLI to (re)mint the first one")
		}
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
	return h.buildTenantProto(rec), nil
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
	return h.buildTenantProto(rec), nil
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
		tenants = append(tenants, h.buildTenantProto(&recs[i]))
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

// UpdateTenant mutates a tenant's name and/or plan (FE-API-029).
//
// Authorization is the caller's responsibility — this RPC is reachable only
// over the internal mTLS gRPC port and registry-management gates the REST
// endpoint behind the platform-admin marker grant.
//
// Validation:
//   - tenant_id must parse as UUID.
//   - Name (when set) must match tenantNameRE so we never store an invalid
//     subdomain-ready slug seed.
//   - Plan (when set) must be one of "free"|"pro"|"enterprise" (kept in step
//     with the management-layer body validator so internal callers don't
//     drift from the REST contract).
//   - At least one of name/plan must be supplied — empty patches return
//     InvalidArgument so callers don't silently no-op.
//
// Slug recompute happens inside repo.UpdateTenant so the row is never
// observable with a name-and-slug mismatch. Duplicate-name attempts surface
// as AlreadyExists via the same pgconn 23505 mapping used by CreateTenant.
func (h *GRPCHandler) UpdateTenant(ctx context.Context, req *tenantv1.UpdateTenantRequest) (*tenantv1.Tenant, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid tenant_id")
	}

	// Pull pointer values via the generated getters. proto3 `optional` emits
	// `*T` plus `HasX()` so we can tell "field not present" from "empty value".
	var namePtr, planPtr *string
	if req.Name != nil {
		n := req.GetName()
		// Reject the empty string up front so the repo never has to defend
		// against "name = ''" reaching the DB.
		if n == "" {
			return nil, status.Error(codes.InvalidArgument, "name must not be empty")
		}
		if !tenantNameRE.MatchString(n) {
			return nil, status.Errorf(codes.InvalidArgument, "tenant name must match ^[a-z0-9][a-z0-9-]{1,63}$")
		}
		namePtr = &n
	}
	if req.Plan != nil {
		p := req.GetPlan()
		if !validTenantPlan(p) {
			return nil, status.Errorf(codes.InvalidArgument, "plan must be one of free, pro, enterprise")
		}
		planPtr = &p
	}
	if namePtr == nil && planPtr == nil {
		return nil, status.Error(codes.InvalidArgument, "at least one of name or plan is required")
	}

	rec, err := h.repo.UpdateTenant(ctx, tenantID, namePtr, planPtr)
	if err != nil {
		if isDuplicateKeyError(err) {
			return nil, status.Errorf(codes.AlreadyExists, "tenant name already in use")
		}
		return nil, errcodes.MapDBError(err, "update tenant")
	}
	if rec == nil {
		return nil, status.Errorf(codes.NotFound, "tenant %s not found", req.TenantId)
	}

	return h.buildTenantProto(rec), nil
}

// validTenantPlan is the allowlist applied to incoming plan values. Keep this
// in lockstep with the REST body validator in services/management; drifting
// the two leaves a hole where internal gRPC callers could write a plan the
// dashboard rejects.
func validTenantPlan(p string) bool {
	switch p {
	case "free", "pro", "enterprise":
		return true
	}
	return false
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
// up slug / host (FE-API-007).
func tenantToProto(rec *repository.TenantRecord) *tenantv1.Tenant {
	return &tenantv1.Tenant{
		TenantId:  rec.ID.String(),
		Name:      rec.Name,
		Plan:      rec.Plan,
		Slug:      rec.Slug,
		CreatedAt: timestamppb.New(rec.CreatedAt),
	}
}

// buildTenantProto builds a fully populated Tenant message. After REDESIGN-001
// RM-001 removed the per-tenant custom-domain feature, host is always the
// wildcard subdomain `<slug>.<platformBaseDomain>`. When platformBaseDomain is
// empty (tests / misconfig), host falls back to the bare slug so the field is
// never the meaningless ".". Empty Slug → tenant id so the host always parses
// as a valid hostname even if the migration backfill produced nothing.
func (h *GRPCHandler) buildTenantProto(rec *repository.TenantRecord) *tenantv1.Tenant {
	out := &tenantv1.Tenant{
		TenantId:  rec.ID.String(),
		Name:      rec.Name,
		Plan:      rec.Plan,
		Slug:      rec.Slug,
		CreatedAt: timestamppb.New(rec.CreatedAt),
	}

	// Wildcard host. Empty slug → tenant id; empty base domain → bare slug.
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
