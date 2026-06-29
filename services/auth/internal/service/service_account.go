// Package service — ServiceAccountService (FE-API-048, Task 8).
//
// ServiceAccountService owns the business logic for workspace-owned machine
// identities. It composes the SA repository (CreateAtomic, Get, List, Update,
// Delete, CountKeysAffectedByScopeShrink), the user repository (for creator
// snapshots on audit events), and the API-key repository (for cascade key
// deletion on SA delete). All mutating operations emit audit events through the
// AuditEmitter interface so tests can assert the full lifecycle without a real
// audit backend.
//
// Redis is used for best-effort JWT revocation on SA disable (spec §5.5):
// setting revoke:user:<shadow_user_id> with a 25-minute TTL ensures any
// outstanding JWTs for the shadow user are rejected even before they expire
// naturally. The DB row (disabled_at IS NOT NULL) remains the authoritative
// source of truth — a Redis hiccup degrades gracefully.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// saRawSecretLen is the number of random bytes for SA-owned API key secrets.
// Matches the human-key rawSecretLen in auth.go (32 bytes → 64-char hex).
const saRawSecretLen = 32

// revokeKeyTTL is longer than the longest JWT TTL (tokenTTL = 5 min) so that
// any outstanding JWT for the shadow user is rejected before the revoke key
// expires. 25 minutes gives ample headroom without polluting Redis indefinitely.
const revokeKeyTTL = 25 * time.Minute

// AuditEvent is a lightweight audit record emitted by ServiceAccountService on
// every lifecycle mutation. The AuditEmitter interface accepts these; test fakes
// capture them in a slice for assertion. Production wiring (FE-API-048
// FUT-007) is a RabbitMQ publisher emitting on
// events.RoutingServiceAccountLifecycle; dev stacks without a broker
// fall through to a slog stand-in.
//
// Fields maps to the spec §5.7 "notable fields" column — callers populate only
// the fields relevant to the action.
type AuditEvent struct {
	// TenantID is the tenant the mutated SA belongs to. Carried so the
	// RabbitMQ publisher can populate the events.Event envelope without
	// reaching back into Fields for a magic-key tenant lookup.
	TenantID string
	Action   string
	ActorID  string
	Resource string
	// Fields contains action-specific metadata (e.g. creator snapshot on
	// service_account.created). May be nil for events with no extra context.
	Fields map[string]any
}

// AuditEmitter is a small interface wrapping audit event emission. The
// production implementation will publish to RabbitMQ (or call the audit gRPC
// service); tests supply a capturingAuditEmitter that accumulates events in
// memory.
type AuditEmitter interface {
	// Emit records one audit event. Implementations must not block indefinitely;
	// callers treat a non-nil error as a hard failure (the audit trail must be
	// complete).
	Emit(ctx context.Context, ev AuditEvent) error
}

// RedisCmdable is the minimal Redis interface ServiceAccountService needs for
// best-effort revoke-key operations. Using an interface instead of *redis.Client
// directly lets tests supply miniredis without importing the full client package
// in this file.
type RedisCmdable interface {
	// Set stores value at key with the given TTL. Returns an error command whose
	// .Err() reports the outcome.
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) interface{ Err() error }
	// Del deletes one or more keys. Returns an error command whose .Err() reports
	// the outcome (best-effort; callers ignore the error on the enable path).
	Del(ctx context.Context, keys ...string) interface{ Err() error }
}

// ServiceAccountService implements business logic for FE-API-048 service accounts.
type ServiceAccountService struct {
	// sa is the service_accounts repository.
	sa saRepo
	// users is used to load creator snapshots for audit events.
	users userRepo
	// keys is used by Delete to verify cascade deletion in tests; production
	// cascade is handled by the DB FK, but listing here allows integration tests
	// to assert the correct state without a real DB.
	keys apiKeyRepo
	// audit emits lifecycle events for every mutating operation.
	audit AuditEmitter
	// redis is used for best-effort JWT revocation on SA disable (spec §5.5).
	redis RedisCmdable
}

// NewServiceAccountService constructs a ServiceAccountService.
// All arguments are required; passing nil values will cause panics at runtime.
func NewServiceAccountService(
	sa saRepo,
	users userRepo,
	keys apiKeyRepo,
	audit AuditEmitter,
	rdb RedisCmdable,
) *ServiceAccountService {
	return &ServiceAccountService{
		sa:    sa,
		users: users,
		keys:  keys,
		audit: audit,
		redis: rdb,
	}
}

// ServiceAccountInput is the service-layer DTO for creating a new SA. It adds
// ActorUserID (the human admin who initiated the creation) to the repo-level
// CreateServiceAccountInput so audit events can capture a creator snapshot.
type ServiceAccountInput struct {
	TenantID      uuid.UUID
	Name          string
	Description   string
	AllowedScopes []string
	// ActorUserID is the human user performing the create. It is used to
	// snapshot creator.Email + creator.DisplayName in the audit event so that
	// audit attribution survives after the admin's account is deleted.
	ActorUserID uuid.UUID
}

// Create creates a new service account atomically (shadow user + SA row in one
// transaction) and emits a service_account.created audit event with a snapshot
// of the creator's identity.
//
// REDESIGN-001 Phase 5.3: the requested AllowedScopes must be a subset of the
// creator's effective scope grant (or empty, which is always allowed). This
// prevents a low-privilege user from minting an SA with higher authority than
// they themselves hold. Global admins bypass the check — they are the
// platform's effective root.
//
// Returns ErrAlreadyExists when a service account with the same name already
// exists in the tenant. Returns codes.PermissionDenied when the requested
// AllowedScopes exceed the creator's effective grant.
func (s *ServiceAccountService) Create(ctx context.Context, in ServiceAccountInput) (*repository.ServiceAccount, error) {
	// Snapshot the creator's identity before the atomic create so we have
	// something to put in the audit event even if the create fails partway.
	// ErrNotFound is a benign race (user deleted between auth and here) — we
	// proceed with empty snapshot fields so the audit event still records the
	// actor_id. Any other error (DB pool exhaustion, ctx cancellation) is
	// unexpected; log at WARN so operators can investigate without aborting
	// the create.
	creator, err := s.users.GetHumanByID(ctx, in.ActorUserID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		slog.WarnContext(ctx, "service_account: creator lookup failed, proceeding with empty snapshot",
			"actor_id", in.ActorUserID,
			"err", err,
		)
	}

	// Phase 5.3 delegation guard: a creator can only mint an SA whose
	// AllowedScopes are a subset of their effective grant. Global admins
	// bypass — they are the platform's effective root. An unknown creator
	// (nil creator from a benign ErrNotFound race above) gets the strictest
	// treatment: no roles loaded → an empty effective set → any non-empty
	// AllowedScopes is denied. That is the safe default for a request whose
	// actor we can't identify.
	if creator == nil || !creator.IsGlobalAdmin {
		var callerAssignments []repository.RoleAssignment
		if creator != nil {
			ra, rErr := s.users.GetUserRoles(ctx, in.ActorUserID, in.TenantID)
			if rErr != nil {
				return nil, rErr
			}
			callerAssignments = ra
		}
		if vErr := VerifyAllowedScopesSubset(callerAssignments, in.AllowedScopes); vErr != nil {
			return nil, vErr
		}
	}

	sa, _, err := s.sa.CreateAtomic(ctx, repository.CreateServiceAccountInput{
		TenantID:      in.TenantID,
		Name:          in.Name,
		Description:   in.Description,
		AllowedScopes: in.AllowedScopes,
		CreatedBy:     in.ActorUserID,
	})
	if err != nil {
		return nil, err
	}

	// Build the creator snapshot for the audit event. These fields are recorded
	// so that "created by" attribution survives even after the admin is deleted
	// (spec §4.2 — created_by FK is ON DELETE SET NULL; audit row is the durable
	// source of attribution).
	fields := map[string]any{
		"service_account_id": sa.ID.String(),
		"name":               sa.Name,
		"description":        sa.Description,
		"allowed_scopes":     sa.AllowedScopes,
	}
	if creator != nil {
		fields["creator_email"] = creator.Email
		dn := ""
		if creator.DisplayName != nil {
			dn = *creator.DisplayName
		}
		fields["creator_display_name"] = dn
	}

	if err := s.audit.Emit(ctx, AuditEvent{
		TenantID: in.TenantID.String(),
		Action:   "service_account.created",
		ActorID:  in.ActorUserID.String(),
		Resource: sa.ID.String(),
		Fields:   fields,
	}); err != nil {
		// Audit failure is a hard error — the audit trail must be complete for
		// service-account lifecycle events (spec §5.7). Log with context so an
		// operator can correlate the SA ID even when the event failed.
		slog.ErrorContext(ctx, "service_account: audit emit failed on create",
			"sa_id", sa.ID,
			"err", err,
		)
		return nil, err
	}

	return sa, nil
}

// Get returns the service account with the given primary key.
// Returns ErrNotFound when no such SA exists.
func (s *ServiceAccountService) Get(ctx context.Context, id uuid.UUID) (*repository.ServiceAccount, error) {
	return s.sa.Get(ctx, id)
}

// List returns a page of service accounts for the given tenant. Parameters are
// forwarded directly to the repository layer; see ServiceAccountRepo.List for
// pagination semantics.
func (s *ServiceAccountService) List(
	ctx context.Context,
	tenantID uuid.UUID,
	includeDisabled bool,
	pageSize int,
	pageToken string,
) ([]repository.ServiceAccountWithStats, string, error) {
	return s.sa.List(ctx, tenantID, includeDisabled, pageSize, pageToken)
}

// UpdateServiceAccountInput is the service-layer DTO for PATCH operations.
// Each pointer field is "unchanged" when nil.
type UpdateServiceAccountInput struct {
	ID       uuid.UUID
	TenantID uuid.UUID
	// Name: nil = leave unchanged.
	Name *string
	// Description: nil = leave unchanged.
	Description *string
	// AllowedScopes: nil = leave unchanged; non-nil replaces the stored value.
	AllowedScopes *[]string
	// ActorUserID is the human admin performing the update, used for audit.
	ActorUserID uuid.UUID
}

// Update applies a partial update to the service account and emits a
// service_account.updated audit event. Returns ErrNotFound when the SA does
// not exist, ErrAlreadyExists when the new name collides.
func (s *ServiceAccountService) Update(ctx context.Context, in UpdateServiceAccountInput) (*repository.ServiceAccount, error) {
	sa, err := s.sa.Update(ctx, repository.UpdateServiceAccountInput{
		ID:            in.ID,
		TenantID:      in.TenantID,
		Name:          in.Name,
		Description:   in.Description,
		AllowedScopes: in.AllowedScopes,
	})
	if err != nil {
		return nil, err
	}

	// Build a fields map summarising what changed so the audit event is useful
	// for incident investigation. We include only fields that were explicitly
	// provided (non-nil pointers) — unchanged fields are omitted.
	fields := map[string]any{
		"service_account_id": sa.ID.String(),
	}
	if in.Name != nil {
		fields["name"] = *in.Name
	}
	if in.Description != nil {
		fields["description"] = *in.Description
	}
	if in.AllowedScopes != nil {
		fields["allowed_scopes"] = *in.AllowedScopes
	}

	if err := s.audit.Emit(ctx, AuditEvent{
		TenantID: in.TenantID.String(),
		Action:   "service_account.updated",
		ActorID:  in.ActorUserID.String(),
		Resource: sa.ID.String(),
		Fields:   fields,
	}); err != nil {
		slog.ErrorContext(ctx, "service_account: audit emit failed on update",
			"sa_id", sa.ID,
			"err", err,
		)
		return nil, err
	}

	return sa, nil
}

// SetDisabled enables or disables the service account identified by id in the
// given tenant. When disabling, a Redis key revoke:user:<shadow_user_id> is
// written with revokeKeyTTL so any outstanding JWTs for the shadow user are
// rejected before they expire (spec §5.5). When enabling, the key is deleted.
//
// Redis failure on the set/del step is best-effort — it is logged at WARN and
// the call proceeds. The DB row (disabled_at IS NOT NULL) is the authoritative
// source of truth for ValidateAPIKey; Redis is a performance optimisation for
// the JWT path.
//
// Order: Update DB → set/del revoke key (warn on error) → emit audit (hard
// error on failure). This ordering ensures the DB is always the source of
// truth and the audit trail is always complete even when Redis is degraded.
func (s *ServiceAccountService) SetDisabled(ctx context.Context, id, tenantID uuid.UUID, disabled bool, actor uuid.UUID) error {
	// 1. Persist the disabled_at change. This is the authoritative write.
	sa, err := s.sa.Update(ctx, repository.UpdateServiceAccountInput{
		ID:       id,
		TenantID: tenantID,
		Disabled: &disabled,
	})
	if err != nil {
		return err
	}

	// 2. Best-effort Redis revoke key. A failure here is not fatal — the DB is
	// the authority for ValidateAPIKey; this key only accelerates JWT rejection.
	revokeKey := "revoke:user:" + sa.ShadowUserID.String()
	if disabled {
		if err := s.redis.Set(ctx, revokeKey, "1", revokeKeyTTL).Err(); err != nil {
			slog.WarnContext(ctx, "service_account: set revoke key failed",
				"err", err,
				"user_id", sa.ShadowUserID,
				"sa_id", sa.ID,
			)
			// Intentionally not returning — audit must still fire and the DB is the
			// source of truth.
		}
	} else {
		// Enabling: clear any stale revoke key so JWT validation resumes working.
		// Error is ignored — if the key doesn't exist Del returns 0 rows affected,
		// not an error; and if Redis is down we don't want to fail the enable.
		_ = s.redis.Del(ctx, revokeKey).Err()
	}

	// 3. Emit audit event. This is a hard error — the lifecycle audit trail must
	// be complete per spec §5.7.
	action := "service_account.enabled"
	if disabled {
		action = "service_account.disabled"
	}
	return s.audit.Emit(ctx, AuditEvent{
		TenantID: tenantID.String(),
		Action:   action,
		ActorID:  actor.String(),
		Resource: sa.ID.String(),
	})
}

// Delete hard-deletes the service account and its shadow user (which cascades
// to api_keys and role_assignments via FK). A snapshot of the SA name is taken
// before deletion so the audit event carries a human-readable identifier even
// after the row is gone.
//
// Returns ErrNotFound when no SA with the given id exists.
func (s *ServiceAccountService) Delete(ctx context.Context, id uuid.UUID, actor uuid.UUID) error {
	// Snapshot the SA name before deletion so the audit event can include it.
	// If Get fails (e.g. SA already gone), forward ErrNotFound and skip audit.
	sa, err := s.sa.Get(ctx, id)
	if err != nil {
		return err
	}
	name := sa.Name
	shadowUserID := sa.ShadowUserID
	tenantID := sa.TenantID

	// Delete cascades to shadow user → api_keys → role_assignments via DB FKs.
	if err := s.sa.Delete(ctx, id); err != nil {
		return err
	}

	// Clear any stale revoke key for the shadow user now that it is gone.
	// Best-effort — ignore the error.
	_ = s.redis.Del(ctx, "revoke:user:"+shadowUserID.String()).Err()

	// Emit audit event with the name snapshot so the audit trail is useful
	// even after the row has been hard-deleted.
	return s.audit.Emit(ctx, AuditEvent{
		TenantID: tenantID.String(),
		Action:   "service_account.deleted",
		ActorID:  actor.String(),
		Resource: id.String(),
		Fields: map[string]any{
			"name":           name,
			"shadow_user_id": shadowUserID.String(),
		},
	})
}

// CountKeysAffectedByScopeShrink returns the number of active API keys for the
// SA whose scopes include at least one value that is absent from the proposed
// new allowed_scopes set. Callers display this count to warn operators before
// narrowing an SA's scope allowlist.
//
// A count of 0 means the scope change is safe. Defence-in-depth: the tenantID
// argument is validated against the SA's stored tenant before delegating to the
// repo, preventing a caller from querying across tenants by id alone.
func (s *ServiceAccountService) CountKeysAffectedByScopeShrink(
	ctx context.Context,
	saID uuid.UUID,
	tenantID uuid.UUID,
	proposed []string,
) (int64, error) {
	// Load the SA to assert tenant ownership before running the count query.
	// This prevents an attacker with a valid saID from probing another tenant's
	// key counts by supplying a mismatched tenantID.
	sa, err := s.sa.Get(ctx, saID)
	if err != nil {
		return 0, err
	}
	if sa.TenantID != tenantID {
		return 0, repository.ErrNotFound
	}

	return s.sa.CountKeysAffectedByScopeShrink(ctx, saID, proposed)
}

// IssueKeyResult carries the persisted API key record plus the plaintext secret
// that is shown to the caller exactly once. The raw secret is not stored; it
// cannot be recovered after this call.
type IssueKeyResult struct {
	Key       *repository.APIKey
	RawSecret string
}

// IssueKey creates a new API key owned by the given service account. The caller
// must supply a set of scopes that is a subset of the SA's AllowedScopes; the
// method returns ErrScopesNotAllowed when the subset check fails.
//
// Ownership is expressed via the polymorphic ServiceAccountID column (not
// UserID) so ValidateAPIKey can apply the SA's AllowedScopes intersection
// logic (auth.go § intersectScopes).
//
// Audit: emits service_account.key_issued on success. Audit failure is a hard
// error — the key is NOT rolled back (callers must handle the dual-write risk
// if audit is unavailable, but in practice the audit emitter should be
// durable).
func (s *ServiceAccountService) IssueKey(
	ctx context.Context,
	saID uuid.UUID,
	tenantID uuid.UUID,
	name string,
	scopes []string,
	actor uuid.UUID,
) (*IssueKeyResult, error) {
	// 1. Load SA and assert tenant ownership to prevent cross-tenant key issuance.
	sa, err := s.sa.Get(ctx, saID)
	if err != nil {
		return nil, err
	}
	if sa.TenantID != tenantID {
		return nil, repository.ErrNotFound
	}

	// 2. Validate that requested scopes are a subset of the SA's AllowedScopes.
	//    Build a set for O(1) lookup.
	allowed := make(map[string]struct{}, len(sa.AllowedScopes))
	for _, s := range sa.AllowedScopes {
		allowed[s] = struct{}{}
	}
	for _, sc := range scopes {
		if _, ok := allowed[sc]; !ok {
			return nil, &ErrScopeNotAllowed{Scope: sc}
		}
	}

	// 3. Generate a cryptographically secure random secret.
	raw := make([]byte, saRawSecretLen)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	rawSecret := hex.EncodeToString(raw) // 64-char lowercase hex

	// 4. Hash the secret with argon2id before persisting. The raw secret is
	//    never stored.
	hash, err := argon2pkg.Hash(rawSecret)
	if err != nil {
		return nil, err
	}

	// 5. Persist the key.
	if scopes == nil {
		scopes = []string{}
	}
	key, err := s.keys.Create(ctx, repository.CreateAPIKeyRequest{
		TenantID:         tenantID,
		ServiceAccountID: &saID,
		Name:             name,
		KeyHash:          hash,
		KeyPrefix:        rawSecret[:12], // first 12 chars for display
		Scopes:           scopes,
	})
	if err != nil {
		return nil, err
	}

	// 6. Emit audit event. Hard error — the audit trail must be complete.
	if err := s.audit.Emit(ctx, AuditEvent{
		TenantID: tenantID.String(),
		Action:   "service_account.key_issued",
		ActorID:  actor.String(),
		Resource: saID.String(),
		Fields: map[string]any{
			"service_account_id": saID.String(),
			"key_id":             key.ID.String(),
			"key_name":           name,
			"scopes":             scopes,
		},
	}); err != nil {
		slog.ErrorContext(ctx, "service_account: audit emit failed on key_issued",
			"sa_id", saID,
			"key_id", key.ID,
			"err", err,
		)
		return nil, err
	}

	return &IssueKeyResult{Key: key, RawSecret: rawSecret}, nil
}

// ListKeys returns all active API keys owned by the given service account.
// Tenant isolation is enforced by first loading the SA and confirming its
// tenant before querying keys.
func (s *ServiceAccountService) ListKeys(
	ctx context.Context,
	saID uuid.UUID,
	tenantID uuid.UUID,
) ([]*repository.APIKey, error) {
	// Confirm the SA belongs to the caller's tenant before listing keys.
	// Returning ErrNotFound instead of ErrForbidden so callers cannot probe
	// cross-tenant existence via the key-list path.
	sa, err := s.sa.Get(ctx, saID)
	if err != nil {
		return nil, err
	}
	if sa.TenantID != tenantID {
		return nil, repository.ErrNotFound
	}

	return s.keys.ListByServiceAccount(ctx, saID)
}

// RevokeKey deletes the API key identified by keyID from the service account
// identified by saID. Tenant isolation is enforced via the SA tenant check;
// the polymorphic DeleteByServiceAccount repository method ensures only keys
// whose service_account_id matches saID are deleted (no cross-SA revocation).
//
// Audit: emits service_account.key_revoked on success. Audit failure is a
// hard error.
func (s *ServiceAccountService) RevokeKey(
	ctx context.Context,
	keyID uuid.UUID,
	saID uuid.UUID,
	tenantID uuid.UUID,
	actor uuid.UUID,
) error {
	// Confirm SA exists and belongs to the caller's tenant.
	sa, err := s.sa.Get(ctx, saID)
	if err != nil {
		return err
	}
	if sa.TenantID != tenantID {
		return repository.ErrNotFound
	}

	// Delete the key using the service-account ownership column so a human
	// key with the same UUID cannot be accidentally revoked.
	if err := s.keys.DeleteByServiceAccount(ctx, keyID, saID); err != nil {
		return err
	}

	// Emit audit event.
	return s.audit.Emit(ctx, AuditEvent{
		TenantID: tenantID.String(),
		Action:   "service_account.key_revoked",
		ActorID:  actor.String(),
		Resource: saID.String(),
		Fields: map[string]any{
			"service_account_id": saID.String(),
			"key_id":             keyID.String(),
		},
	})
}

// ErrScopeNotAllowed is returned by IssueKey when the requested scopes
// include a value that is absent from the SA's AllowedScopes.
type ErrScopeNotAllowed struct {
	Scope string
}

func (e *ErrScopeNotAllowed) Error() string {
	return "scope not allowed for this service account: " + e.Scope
}
