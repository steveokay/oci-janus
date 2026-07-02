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
// The HTTP client used for JWKS fetches is hardened per CLAUDE.md §13:
//
//   - Timeout (5s) bounds the whole request so a hung IdP cannot stall
//     the exchange flow indefinitely.
//   - TLSHandshakeTimeout + ResponseHeaderTimeout (SEC-062) bound the
//     sub-phases so a hostile server cannot burn ~4.9s stalling the TLS
//     handshake or dribbling response headers under the overall cap.
//   - CheckRedirect returns ErrUseLastResponse (SEC-058) so a discovery
//     or JWKS response cannot 30x-redirect us onto an internal endpoint —
//     we treat the first response as authoritative and never chase a
//     Location header off the vetted host.
func NewOIDCTrustService(repo trustRepo, sa saRepo, auth *Service, audit AuditEmitter, allowedIssuers []string) *OIDCTrustService {
	return &OIDCTrustService{
		repo:            repo,
		serviceAccounts: sa,
		jwks:            newJWKSCache(newJWKSHTTPClient()),
		allowedIssuers:  allowedIssuers,
		auth:            auth,
		audit:           audit,
	}
}

// newJWKSHTTPClient builds the hardened HTTP client used for OIDC
// discovery + JWKS fetches. Extracted from NewOIDCTrustService so the
// SEC-058 no-redirect and SEC-062 timeout guarantees can be asserted in a
// unit test against the exact production configuration.
func newJWKSHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSHandshakeTimeout:   3 * time.Second,
			ResponseHeaderTimeout: 3 * time.Second,
		},
		// SEC-058: do not follow redirects. Return the 30x response as-is
		// so getJSON sees a non-200 and fails, rather than chasing the
		// Location header to a potentially internal address.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
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
	if err := validateJWKSCacheTTL(in.JWKSCacheTTLSeconds); err != nil {
		return nil, err
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
// jwksCacheTTL bounds (SEC-060). IdPs typically publish key-rotation TTLs
// of 300s–3600s; 60s tolerates an operator dropping low for a temporary
// rotation, and 86400s (24h) is the ceiling beyond which a legitimate IdP
// key rotation would be missed. 0 is a sentinel meaning "use the repo
// default (3600)" — preserved so existing callers that omit the field
// keep working.
const (
	jwksCacheTTLMinSeconds = 60
	jwksCacheTTLMaxSeconds = 86400
)

// validateJWKSCacheTTL rejects an out-of-band cache TTL. Unbounded values
// are a resource-exhaustion vector in both directions (SEC-060): a
// negative/tiny TTL turns us into a JWKS-refetch amplifier against the
// upstream IdP, while an enormous TTL pins a (possibly transiently
// attacker-controlled) key set for the life of the deployment. 0 passes
// through untouched — the repository layer maps it to the 3600s default.
func validateJWKSCacheTTL(ttl int32) error {
	if ttl == 0 {
		return nil
	}
	if ttl < jwksCacheTTLMinSeconds || ttl > jwksCacheTTLMaxSeconds {
		return status.Errorf(codes.InvalidArgument,
			"jwks_cache_ttl_seconds must be 0 (default) or between %d and %d",
			jwksCacheTTLMinSeconds, jwksCacheTTLMaxSeconds)
	}
	return nil
}

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
	if err := validateJWKSCacheTTL(in.JWKSCacheTTLSeconds); err != nil {
		return err
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
