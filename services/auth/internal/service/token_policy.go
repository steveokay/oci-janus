// Package service — token_policy.go owns the FUT-003 workspace-wide
// token-policy business logic.
//
// TokenPolicyService wraps TokenPolicyRepo with:
//   - Input validation: each non-nil limit field must be in [1, 3650] days
//     (1 day to 10 years). idle_revoke_days additionally has a 7-day floor
//     so a fresh policy doesn't nuke every key on the next worker tick.
//   - Audit emission: on successful Put, emit auth.token_policy.changed
//     with the before/after diff so /activity surfaces the change.
//
// The service is admin-facing only. The public-consumer path (CreateAPIKey
// enforcement, idle-revoke worker) reads through the repo directly.
package service

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

const (
	// tokenPolicyMinDays is the smallest legal value for any policy field
	// (1 day). Zero is rejected because a max_ttl_days of 0 would forbid
	// every new key, and a rotation_interval_days of 0 would mark every
	// key overdue the moment it's issued.
	tokenPolicyMinDays int32 = 1
	// tokenPolicyMaxDays caps every policy field at 10 years. Longer than
	// this is either a data-entry mistake or an operator with a strange
	// use case — either way the service says no rather than silently
	// storing a value that never fires.
	tokenPolicyMaxDays int32 = 3650
	// tokenPolicyMinIdleRevokeDays is the additional floor on
	// idle_revoke_days. Chosen so a fresh policy that is applied while
	// the workspace has some low-activity CI bots doesn't nuke them on
	// the next tick (which would surprise operators — the policy should
	// be a slow-moving guardrail, not a mass-revoke button).
	tokenPolicyMinIdleRevokeDays int32 = 7
)

// TokenPolicyRepo is the narrow repository interface used by
// TokenPolicyService. Defined here so tests can supply a fake without a
// real DB. Satisfied by *repository.TokenPolicyRepo.
type TokenPolicyRepo interface {
	GetOrDefault(ctx context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error)
	Upsert(ctx context.Context, in repository.TokenPolicy) (*repository.TokenPolicy, error)
}

// Compile-time guarantee that the concrete repo satisfies the interface.
var _ TokenPolicyRepo = (*repository.TokenPolicyRepo)(nil)

// TokenPolicyService is the admin-facing service for reading + writing
// workspace token policies. Safe for concurrent use.
type TokenPolicyService struct {
	repo  TokenPolicyRepo
	audit AuditEmitter
}

// NewTokenPolicyService constructs a TokenPolicyService. audit may be nil
// in test / dev stacks without a broker — emit calls become no-ops in
// that case.
func NewTokenPolicyService(repo TokenPolicyRepo, audit AuditEmitter) *TokenPolicyService {
	return &TokenPolicyService{repo: repo, audit: audit}
}

// PutTokenPolicyInput is the validated request payload for Put.
type PutTokenPolicyInput struct {
	TenantID             uuid.UUID
	MaxTTLDays           *int32
	RotationIntervalDays *int32
	IdleRevokeDays       *int32
	// RequireMFA is the admin TOTP-MFA-enforcement toggle (Tier-1 #1). A
	// pointer for symmetry with the other optional fields (nil = "not
	// supplied"), but note the repository column is a plain non-nullable
	// bool: nil is coerced to false at write time (see Put), so nil and
	// &false are indistinguishable at the DB. The gRPC handler always
	// supplies a non-nil pointer because the proto field is a plain bool.
	RequireMFA *bool
	// ActorID is the caller's user_id — the BFF plumbs it from the JWT
	// sub. Recorded in updated_by_user_id and in the audit event.
	ActorID uuid.UUID
}

// Get returns the current policy for the given tenant. When no row
// exists, returns a zero-valued policy (all limit fields nil) — not
// ErrNotFound. This matches the grandfathering semantics: a fresh
// tenant with no policy configured has "no cap" everywhere.
func (s *TokenPolicyService) Get(ctx context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error) {
	if tenantID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	return s.repo.GetOrDefault(ctx, tenantID)
}

// Put validates + persists a policy update. Emits auth.token_policy.changed
// with the before/after snapshot on success.
//
// Validation rules:
//   - Every non-nil field must be in [tokenPolicyMinDays, tokenPolicyMaxDays].
//   - IdleRevokeDays, if set, must additionally be >= tokenPolicyMinIdleRevokeDays.
//   - ActorID must be non-zero (BFF is expected to supply it).
//
// Nil fields are PRESERVED at the DB layer via COALESCE in Upsert, so a
// partial update that only touches max_ttl_days keeps the other two.
func (s *TokenPolicyService) Put(ctx context.Context, in PutTokenPolicyInput) (*repository.TokenPolicy, error) {
	if in.TenantID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if in.ActorID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "actor_id is required")
	}
	if err := validateTokenPolicyField("max_ttl_days", in.MaxTTLDays, tokenPolicyMinDays); err != nil {
		return nil, err
	}
	if err := validateTokenPolicyField("rotation_interval_days", in.RotationIntervalDays, tokenPolicyMinDays); err != nil {
		return nil, err
	}
	if err := validateTokenPolicyField("idle_revoke_days", in.IdleRevokeDays, tokenPolicyMinIdleRevokeDays); err != nil {
		return nil, err
	}

	// Load the previous state so we can emit a before/after diff. A miss
	// returns a zero policy (all-nil), which the audit consumer renders
	// as "unset → new-value".
	before, err := s.repo.GetOrDefault(ctx, in.TenantID)
	if err != nil {
		return nil, err
	}

	actorID := in.ActorID
	// require_mfa has no NULL/preserve state at the DB (plain bool column),
	// so a nil input pointer is coerced to false rather than passed through
	// like the *int32 limit fields.
	requireMFA := false
	if in.RequireMFA != nil {
		requireMFA = *in.RequireMFA
	}
	after, err := s.repo.Upsert(ctx, repository.TokenPolicy{
		TenantID:             in.TenantID,
		MaxTTLDays:           in.MaxTTLDays,
		RotationIntervalDays: in.RotationIntervalDays,
		IdleRevokeDays:       in.IdleRevokeDays,
		RequireMFA:           requireMFA,
		UpdatedByUserID:      &actorID,
	})
	if err != nil {
		return nil, err
	}

	s.emitPolicyChanged(ctx, in.TenantID, in.ActorID, before, after)
	return after, nil
}

// validateTokenPolicyField enforces the per-field limits. A nil pointer
// (unset field) is always accepted — the caller's "no change" intent.
// A non-nil value below `minVal` or above tokenPolicyMaxDays returns
// InvalidArgument with a descriptive message.
func validateTokenPolicyField(name string, v *int32, minVal int32) error {
	if v == nil {
		return nil
	}
	if *v < minVal {
		return status.Errorf(codes.InvalidArgument,
			"%s must be >= %d (got %d)", name, minVal, *v)
	}
	if *v > tokenPolicyMaxDays {
		return status.Errorf(codes.InvalidArgument,
			"%s must be <= %d (got %d)", name, tokenPolicyMaxDays, *v)
	}
	return nil
}

// emitPolicyChanged fires auth.token_policy.changed with the before/after
// snapshot. Best-effort: a nil emitter (test / dev without broker) is a
// no-op. Errors are logged but never bubbled up — the DB write already
// succeeded and is the source of truth.
//
// The Fields map carries a JSON-serialised TokenPolicyChangedPayload so a
// downstream publisher can hand it straight to RabbitMQ without
// re-marshalling. This mirrors the OIDC-trust emit pattern in the same
// package.
func (s *TokenPolicyService) emitPolicyChanged(ctx context.Context, tenantID, actorID uuid.UUID, before, after *repository.TokenPolicy) {
	if s.audit == nil {
		return
	}
	beforeSnap := snapshot(before)
	afterSnap := snapshot(after)
	// SEC-067 (2026-07-01): don't emit audit for a no-op change. A
	// caller PUTting an all-nil body against a tenant with no policy
	// produced an empty-diff audit row that read like real activity in
	// /activity but had no policy shift behind it — credit-laundering
	// into the audit trail. If before and after snapshots are
	// byte-identical, skip the emit entirely.
	if beforeSnap == afterSnap {
		return
	}
	payload := events.TokenPolicyChangedPayload{
		TenantID: tenantID.String(),
		ActorID:  actorID.String(),
		Before:   beforeSnap,
		After:    afterSnap,
	}
	// Encode once so the publisher-side dispatcher can pass raw JSON to
	// Publish without re-marshalling. Marshal cannot fail for this shape,
	// but a defensive log guards against future field additions with
	// non-marshalable types.
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.WarnContext(ctx, "token policy: marshal audit payload failed", "err", err)
		return
	}
	ev := AuditEvent{
		TenantID: tenantID.String(),
		Action:   events.RoutingTokenPolicyChanged,
		ActorID:  actorID.String(),
		Resource: tenantID.String(),
		Fields: map[string]any{
			"payload_json": string(raw),
			"actor_id":     actorID.String(),
		},
	}
	if err := s.audit.Emit(ctx, ev); err != nil {
		slog.WarnContext(ctx, "token policy: audit emit failed",
			"tenant_id", tenantID,
			"err", err,
		)
	}
}

// snapshot converts a repository.TokenPolicy pointer to the compact
// PolicySnapshot wire form. Nil-safe: a nil input yields a zero-value
// snapshot so the audit consumer's "unset → new-value" renderer still
// works if a caller ever hands over a nil before-state.
func snapshot(p *repository.TokenPolicy) events.PolicySnapshot {
	if p == nil {
		return events.PolicySnapshot{}
	}
	return events.PolicySnapshot{
		MaxTTLDays:           p.MaxTTLDays,
		RotationIntervalDays: p.RotationIntervalDays,
		IdleRevokeDays:       p.IdleRevokeDays,
	}
}

// tokenPolicyUpdatedAtFallback is unused today but reserved for a future
// gRPC translation that wants a stable Time even when the repo returns
// zero (no row). Kept here so the pattern is greppable if the need
// arises. The var elides "unused" lints by referencing time.Time.
var _ = time.Time{}
