// Package service — oidc_trust.go is the business-logic layer for the
// FUT-001 federated workload identity feature. It composes:
//
//   - OIDCTrustRepo                  — the CRUD repository for trust rows
//   - ServiceAccountRepo (saRepo)    — for cross-tenant FK validation
//   - jwksCache                      — per-issuer public-key cache
//   - allowedIssuers []string        — the deploy-time OIDC_ALLOWED_ISSUERS
//   - *Service                       — for IssueWorkloadToken (key ring access)
//   - AuditEmitter (optional)        — for FUT-001 audit events
//
// The four admin RPCs (List / Create / Update / Delete) thread through
// here from the gRPC handler. The ExchangeWorkloadToken RPC is the public
// entry point used by CI runners and is implemented in oidc_exchange.go.
package service

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// trustRepo is the subset of *repository.OIDCTrustRepo used by the
// service. Defined as an interface so tests can fake it without touching
// the database.
type trustRepo interface {
	Create(ctx context.Context, in repository.OIDCTrust) (*repository.OIDCTrust, error)
	GetByID(ctx context.Context, tenantID, id uuid.UUID) (*repository.OIDCTrust, error)
	List(ctx context.Context, tenantID uuid.UUID) ([]*repository.OIDCTrust, error)
	ListByIssuer(ctx context.Context, issuerURL string) ([]*repository.OIDCTrust, error)
	Update(ctx context.Context, in repository.OIDCTrust) (*repository.OIDCTrust, error)
	Delete(ctx context.Context, tenantID, id uuid.UUID) error
	MarkUsed(ctx context.Context, id uuid.UUID) error
}

// Compile-time guarantee that the concrete repo satisfies the interface.
var _ trustRepo = (*repository.OIDCTrustRepo)(nil)

// OIDCTrustService owns the admin CRUD + workload-token-exchange logic.
// Constructed at server startup; safe for concurrent use.
type OIDCTrustService struct {
	repo            trustRepo
	serviceAccounts saRepo
	jwks            *jwksCache
	allowedIssuers  []string
	// auth is the *Service used to mint workload JWTs. Holds the key ring.
	// We intentionally take a pointer to the same Service the rest of the
	// process uses so workload tokens share signing keys with login tokens.
	auth *Service
	// audit emits FUT-001 audit events. May be nil in tests / dev stacks
	// without a broker — emit calls become no-ops.
	audit AuditEmitter
}

// CreateOIDCTrustInput is the validated request payload for Create.
type CreateOIDCTrustInput struct {
	TenantID            uuid.UUID
	ServiceAccountID    uuid.UUID
	DisplayName         string
	IssuerURL           string
	Audience            string
	SubjectPattern      string
	JWKSCacheTTLSeconds int32
	// ActorID identifies the admin who created the trust — captured for
	// the auth.oidc_trust.created audit event.
	ActorID string
}

// UpdateOIDCTrustInput is the validated request payload for Update.
type UpdateOIDCTrustInput struct {
	ID                  uuid.UUID
	TenantID            uuid.UUID
	DisplayName         string
	SubjectPattern      string
	JWKSCacheTTLSeconds int32
	ActorID             string
}

// NewOIDCTrustService builds the service. allowedIssuers is the parsed
// CSV from OIDC_ALLOWED_ISSUERS — typically built via
// parseIssuerAllowlist(cfg.OIDCAllowedIssuers) at server startup.
//
// The HTTP client used for JWKS fetches has a 5-second timeout so a hung
// IdP cannot stall the exchange flow indefinitely.
func NewOIDCTrustService(repo trustRepo, sa saRepo, auth *Service, audit AuditEmitter, allowedIssuers []string) *OIDCTrustService {
	jwksClient := &http.Client{Timeout: 5 * time.Second}
	return &OIDCTrustService{
		repo:            repo,
		serviceAccounts: sa,
		jwks:            newJWKSCache(jwksClient),
		allowedIssuers:  allowedIssuers,
		auth:            auth,
		audit:           audit,
	}
}

// List returns every trust row for the tenant. The handler-level admin
// check is the only authorisation gate — within an admin's tenant they
// see everything.
func (s *OIDCTrustService) List(ctx context.Context, tenantID uuid.UUID) ([]*repository.OIDCTrust, error) {
	return s.repo.List(ctx, tenantID)
}

// Get returns one trust row by (tenant_id, id). Returns ErrNotFound if
// missing.
func (s *OIDCTrustService) Get(ctx context.Context, tenantID, id uuid.UUID) (*repository.OIDCTrust, error) {
	return s.repo.GetByID(ctx, tenantID, id)
}

// Create validates the input and persists a new trust row. The
// validations match the spec's "trust create" contract:
//
//   - display_name + audience must be non-empty.
//   - issuer_url must be in the OIDC_ALLOWED_ISSUERS allowlist.
//   - subject_pattern must parse as a valid glob.
//   - service_account_id must belong to tenant_id (cross-tenant FK
//     validation — the DB FK alone only checks that the SA exists, not
//     that it belongs to the trust's tenant).
//
// All failures return codes.InvalidArgument so the handler can surface a
// clean 400 without parsing pg error strings.
func (s *OIDCTrustService) Create(ctx context.Context, in CreateOIDCTrustInput) (*repository.OIDCTrust, error) {
	if err := s.validateOnCreate(ctx, in); err != nil {
		return nil, err
	}
	row, err := s.repo.Create(ctx, repository.OIDCTrust{
		TenantID:            in.TenantID,
		ServiceAccountID:    in.ServiceAccountID,
		DisplayName:         in.DisplayName,
		IssuerURL:           in.IssuerURL,
		Audience:            in.Audience,
		SubjectPattern:      in.SubjectPattern,
		JWKSCacheTTLSeconds: in.JWKSCacheTTLSeconds,
	})
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return nil, status.Error(codes.AlreadyExists, "a trust with the same (issuer_url, subject_pattern) already exists in this tenant")
		}
		return nil, err
	}
	s.emitTrustEvent(ctx, "auth.oidc_trust.created", in.ActorID, row)
	return row, nil
}

// Update mutates the trust's display_name, subject_pattern, and
// jwks_cache_ttl_seconds. Other fields are append-only by design — see
// the OIDCTrustRepo.Update comment for why.
func (s *OIDCTrustService) Update(ctx context.Context, in UpdateOIDCTrustInput) (*repository.OIDCTrust, error) {
	if strings.TrimSpace(in.DisplayName) == "" {
		return nil, status.Error(codes.InvalidArgument, "display_name is required")
	}
	if err := validateGlobSyntax(in.SubjectPattern); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "subject_pattern: %v", err)
	}
	row, err := s.repo.Update(ctx, repository.OIDCTrust{
		ID:                  in.ID,
		TenantID:            in.TenantID,
		DisplayName:         in.DisplayName,
		SubjectPattern:      in.SubjectPattern,
		JWKSCacheTTLSeconds: in.JWKSCacheTTLSeconds,
	})
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "trust not found")
		}
		if errors.Is(err, repository.ErrAlreadyExists) {
			return nil, status.Error(codes.AlreadyExists, "a trust with the same (issuer_url, subject_pattern) already exists in this tenant")
		}
		return nil, err
	}
	s.emitTrustEvent(ctx, "auth.oidc_trust.updated", in.ActorID, row)
	return row, nil
}

// Delete removes the trust row. The cascade FK from service_accounts
// also fires this delete when the SA is deleted, so admin-deletion and
// SA-deletion converge on the same end state.
func (s *OIDCTrustService) Delete(ctx context.Context, tenantID, id uuid.UUID, actorID string) error {
	// Load the row first so the audit event can carry the full identity.
	// A failed lookup short-circuits — the audit event is only emitted on
	// successful delete.
	row, err := s.repo.GetByID(ctx, tenantID, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return status.Error(codes.NotFound, "trust not found")
		}
		return err
	}
	if err := s.repo.Delete(ctx, tenantID, id); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return status.Error(codes.NotFound, "trust not found")
		}
		return err
	}
	s.emitTrustEvent(ctx, "auth.oidc_trust.deleted", actorID, row)
	return nil
}

// validateOnCreate centralises the input gates that run before the DB
// INSERT. Every gate returns a clean codes.InvalidArgument so the gRPC
// handler doesn't need to translate.
func (s *OIDCTrustService) validateOnCreate(ctx context.Context, in CreateOIDCTrustInput) error {
	if strings.TrimSpace(in.DisplayName) == "" {
		return status.Error(codes.InvalidArgument, "display_name is required")
	}
	if strings.TrimSpace(in.Audience) == "" {
		return status.Error(codes.InvalidArgument, "audience is required")
	}
	if strings.TrimSpace(in.IssuerURL) == "" {
		return status.Error(codes.InvalidArgument, "issuer_url is required")
	}
	// SEC-063: reject plaintext HTTP issuers before the allowlist check.
	// An HTTP issuer would leak the JWKS discovery to on-path attackers,
	// and OIDC's discovery + signature-verification contracts assume TLS.
	// The allowlist gate runs first if any prefix accidentally allows
	// `http://`; the explicit check here is defence in depth.
	if !strings.HasPrefix(in.IssuerURL, "https://") {
		return status.Error(codes.InvalidArgument, "issuer_url must use https://")
	}
	if !issuerAllowed(s.allowedIssuers, in.IssuerURL) {
		return status.Error(codes.InvalidArgument, "issuer_url not in OIDC_ALLOWED_ISSUERS")
	}
	if err := validateGlobSyntax(in.SubjectPattern); err != nil {
		return status.Errorf(codes.InvalidArgument, "subject_pattern: %v", err)
	}
	if in.ServiceAccountID == uuid.Nil {
		return status.Error(codes.InvalidArgument, "service_account_id is required")
	}
	// Cross-tenant FK validation: the raw DB FK only checks the SA
	// exists; here we enforce it belongs to the trust's tenant so an
	// admin cannot register a trust whose mint-target lives in someone
	// else's workspace.
	sa, err := s.serviceAccounts.Get(ctx, in.ServiceAccountID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return status.Error(codes.InvalidArgument, "service_account_id not found")
		}
		return err
	}
	if sa.TenantID != in.TenantID {
		return status.Error(codes.InvalidArgument, "service_account_id belongs to a different tenant")
	}
	if sa.DisabledAt != nil {
		return status.Error(codes.InvalidArgument, "service_account_id is disabled")
	}
	return nil
}

// emitTrustEvent fires a best-effort audit event for a successful trust
// mutation. Wrapped so a nil emitter (test / dev stacks) is a no-op.
// Errors are logged but never bubbled up — the DB write is the source
// of truth.
//
// The Fields map carries the OIDCTrustPayload-equivalent shape so a
// publisher routing on Action (see server.go's audit-router) can build
// the right RabbitMQ envelope without re-fetching the trust row.
func (s *OIDCTrustService) emitTrustEvent(ctx context.Context, action, actorID string, row *repository.OIDCTrust) {
	if s.audit == nil {
		return
	}
	ev := AuditEvent{
		TenantID: row.TenantID.String(),
		Action:   action,
		ActorID:  actorID,
		Resource: row.ID.String(),
		Fields: map[string]any{
			"trust_id":           row.ID.String(),
			"tenant_id":          row.TenantID.String(),
			"service_account_id": row.ServiceAccountID.String(),
			"display_name":       row.DisplayName,
			"issuer_url":         row.IssuerURL,
			"audience":           row.Audience,
			"subject_pattern":    row.SubjectPattern,
			"actor_id":           actorID,
		},
	}
	if err := s.audit.Emit(ctx, ev); err != nil {
		slog.WarnContext(ctx, "oidc trust: audit emit failed",
			"action", action,
			"trust_id", row.ID,
			"err", err,
		)
	}
}

