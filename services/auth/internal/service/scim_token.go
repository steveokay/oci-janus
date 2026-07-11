package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// scimRepo is the slice of the repository the SCIM token service needs. Kept
// tiny so the token logic can be unit-tested with a hand-written fake and so the
// real adapter (scimRepoAdapter below) is the only place that touches the DB.
type scimRepo interface {
	getHash() (string, bool) // (argon2 hash, enabled)
	setHash(hash string, enabled bool)
	touch()
}

// scimTokenSvc mints and verifies the single global SCIM bearer token. The raw
// token is never persisted — only its Argon2id hash — and verification is
// fail-closed (disabled/unset config never verifies).
type scimTokenSvc struct {
	repo scimRepo
}

// newSCIMTokenSvc wraps a scimRepo with the token generate/verify logic.
func newSCIMTokenSvc(r scimRepo) *scimTokenSvc { return &scimTokenSvc{repo: r} }

// generate mints a new raw token (`scim.<64-hex>`), stores its Argon2 hash, and
// enables the feature. The raw value is returned once and never persisted.
func (s *scimTokenSvc) generate() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("scim token entropy: %w", err)
	}
	raw := "scim." + hex.EncodeToString(b)
	hash, err := argon2.Hash(raw)
	if err != nil {
		return "", fmt.Errorf("scim token hash: %w", err)
	}
	s.repo.setHash(hash, true)
	return raw, nil
}

// verify returns true iff the config is enabled and the presented token matches
// the stored Argon2 hash. Fail-closed: disabled or unset config never verifies.
func (s *scimTokenSvc) verify(raw string) (bool, error) {
	hash, enabled := s.repo.getHash()
	if !enabled || hash == "" {
		return false, nil
	}
	ok, err := argon2.Verify(raw, hash)
	if err != nil {
		return false, err
	}
	if ok {
		s.repo.touch()
	}
	return ok, nil
}

// scimConfigRepo is the concrete repository surface the production adapter needs.
// *repository.UserRepository satisfies it via the Task 3 methods.
type scimConfigRepo interface {
	GetSCIMConfig(ctx context.Context) (*repository.SCIMConfig, error)
	UpsertSCIMToken(ctx context.Context, tenantID uuid.UUID, tokenHash string, enabled bool) error
	TouchSCIMLastUsed(ctx context.Context) error
}

// scimRepoAdapter bridges the concrete repository (DB-backed scim_config methods)
// to the tiny scimRepo interface the token service consumes. It captures the
// request-scoped context per call and the bootstrap tenant id used on setHash
// (the singleton row's tenant_id in single-tenant deployments).
//
// getHash/touch tolerate a nil/unconfigured row by returning ("", false) —
// keeping verify fail-closed when SCIM has never been provisioned. setHash is
// exercised only by the Phase 3 admin token-generate path.
type scimRepoAdapter struct {
	ctx      context.Context
	repo     scimConfigRepo
	tenantID uuid.UUID
}

func (a *scimRepoAdapter) getHash() (string, bool) {
	cfg, err := a.repo.GetSCIMConfig(a.ctx)
	if err != nil || cfg == nil {
		return "", false
	}
	return cfg.TokenHash, cfg.Enabled
}

func (a *scimRepoAdapter) setHash(hash string, enabled bool) {
	// Errors are surfaced by generate's caller re-reading; the adapter interface
	// is intentionally error-free to match the unit-test fake. Best-effort here.
	_ = a.repo.UpsertSCIMToken(a.ctx, a.tenantID, hash, enabled)
}

func (a *scimRepoAdapter) touch() {
	// Best-effort audit convenience — never gates auth.
	_ = a.repo.TouchSCIMLastUsed(a.ctx)
}

// VerifySCIMToken verifies a raw SCIM bearer token against the stored config.
// Returns (true, nil) only when SCIM is enabled and the token matches. When the
// SCIM config repo has not been wired (SetSCIMRepo never called — e.g. legacy
// test fixtures), it fail-closes to (false, nil) so the SCIM surface denies by
// default. This is the Service-level entry point the handler layer calls.
func (s *Service) VerifySCIMToken(ctx context.Context, raw string) (bool, error) {
	if s.scimConfig == nil {
		return false, nil
	}
	svc := newSCIMTokenSvc(&scimRepoAdapter{ctx: ctx, repo: s.scimConfig, tenantID: s.scimTenantID})
	return svc.verify(raw)
}

// SetSCIMRepo wires the DB-backed scim_config repository plus the bootstrap
// tenant id used when a token is generated (Phase 3). Kept as a setter so the
// existing Service constructors stay signature-stable. Nil clears the wiring,
// leaving VerifySCIMToken fail-closed.
func (s *Service) SetSCIMRepo(r scimConfigRepo, bootstrapTenantID uuid.UUID) {
	s.scimConfig = r
	s.scimTenantID = bootstrapTenantID
}
