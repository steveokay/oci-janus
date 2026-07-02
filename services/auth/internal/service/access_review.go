// Package service — access_review.go owns the FUT-004 access-review
// business logic.
//
// AccessReviewService is the operator-facing service the FE calls to
// render the /api-keys/review page (nudge-only stale-key surface). Two
// methods:
//
//   - ListStaleKeys(ctx, tenantID) — loads the tenant's
//     token_policies.idle_revoke_days (default 90 when unset), computes
//     `staleCutoff = now - idle_revoke_days`, hands off to
//     repository.APIKeyRepository.ListStaleKeys, and decorates each row
//     with a SuggestedAction + Reason per the spec §Feature 4 heuristic.
//
//   - SnoozeAPIKeyReview(ctx, keyID, days, actorID) — validates
//     `days ∈ [1, 90]`, computes `until = now + days`, persists via
//     SetReviewSnoozedUntil, emits an audit event, and re-loads the row
//     so the caller can hand a fresh StaleKeyView back to the FE.
//
// Nudge-only posture (spec Decision #4): this service NEVER auto-revokes.
// The weekly worker emits the audit event + notification; the operator
// picks Revoke / Keep / Snooze per row. FUT-003's idle_revoke worker is
// the auto-action.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

const (
	// defaultIdleThresholdDays is the fallback staleness cutoff when the
	// tenant has no token_policies row (or has one but idle_revoke_days
	// is NULL). Chosen to be conservative — a 90-day cutoff is a strong
	// signal "definitely stale" for CI bots + human keys alike without
	// nagging on active workspaces.
	defaultIdleThresholdDays = 90
	// snoozeMinDays / snoozeMaxDays bound the Snooze API. The FE's
	// primary "Snooze 30d" button hits the middle of the range; the
	// bounds exist so a hostile caller can't defer forever OR pass zero
	// to secretly "clear" the snooze via this endpoint (clearing goes
	// through a separate future path if we ever need it).
	snoozeMinDays = 1
	snoozeMaxDays = 90
	// suggestedRevokeGraceDays is the padding beyond the staleness
	// threshold at which the heuristic upgrades from KEEP → REVOKE. A
	// key that's been idle for `threshold + gracer` days is
	// well past due; a key that's within the grace window is still
	// "recently stale" and gets a softer KEEP suggestion.
	suggestedRevokeGraceDays = 14
)

// AccessReviewRepo is the narrow repository interface used by
// AccessReviewService. Defined here so tests can supply a fake without a
// real DB. Satisfied by *repository.APIKeyRepository.
type AccessReviewRepo interface {
	ListStaleKeys(ctx context.Context, tenantID uuid.UUID, staleCutoff time.Time) ([]repository.StaleKey, error)
	SetReviewSnoozedUntil(ctx context.Context, keyID uuid.UUID, until *time.Time) error
	GetTenantIDForKey(ctx context.Context, keyID uuid.UUID) (uuid.UUID, uuid.UUID, error)
}

// AccessReviewPolicyRepo is the narrow policy-read interface. Satisfied
// by *repository.TokenPolicyRepo.
type AccessReviewPolicyRepo interface {
	GetOrDefault(ctx context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error)
}

// Compile-time guarantees that the concrete repos satisfy the interfaces.
var (
	_ AccessReviewRepo       = (*repository.APIKeyRepository)(nil)
	_ AccessReviewPolicyRepo = (*repository.TokenPolicyRepo)(nil)
)

// StaleKeyView is the service-layer projection returned by ListStaleKeys.
// Wraps the repository projection with the suggested-action heuristic
// output so the caller (gRPC handler → BFF → FE) has the row ready to
// render without a second decision step.
type StaleKeyView struct {
	Key             repository.StaleKey
	SuggestedAction SuggestedAction
	Reason          string
}

// SuggestedAction is the operator-facing recommendation attached to
// each stale key. Mirrors the proto enum but stays as a Go int for
// service-layer code — the gRPC handler translates to the proto value.
type SuggestedAction int32

const (
	SuggestedActionUnspecified SuggestedAction = 0
	SuggestedActionRevoke      SuggestedAction = 1
	SuggestedActionKeep        SuggestedAction = 2
	SuggestedActionSnooze      SuggestedAction = 3
)

// AccessReviewService is the operator-facing service. Safe for concurrent use.
type AccessReviewService struct {
	repo     AccessReviewRepo
	policies AccessReviewPolicyRepo
	audit    AuditEmitter
	// now lets tests pin the wall clock. Prod uses time.Now.
	now func() time.Time
}

// NewAccessReviewService constructs an AccessReviewService. audit may be
// nil in test / dev stacks without a broker — emit calls become no-ops
// in that case.
func NewAccessReviewService(repo AccessReviewRepo, policies AccessReviewPolicyRepo, audit AuditEmitter) *AccessReviewService {
	return &AccessReviewService{
		repo:     repo,
		policies: policies,
		audit:    audit,
		now:      time.Now,
	}
}

// WithClock replaces the wall-clock reader. Chainable builder so tests
// can pin `now` without disturbing the constructor signature.
func (s *AccessReviewService) WithClock(fn func() time.Time) *AccessReviewService {
	if fn != nil {
		s.now = fn
	}
	return s
}

// ListStaleKeys returns the tenant's stale keys with a suggested action
// per row. Resolves the idle-revoke threshold from token_policies (falls
// back to defaultIdleThresholdDays when unset) so the FE always shows a
// sensible list even for a fresh tenant that hasn't configured a policy.
//
// The result is deterministic given a fixed clock — the heuristic's
// only stochastic input is `now()`, which tests pin via WithClock.
func (s *AccessReviewService) ListStaleKeys(ctx context.Context, tenantID uuid.UUID) ([]StaleKeyView, error) {
	if tenantID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	threshold := s.resolveThreshold(ctx, tenantID)
	now := s.now().UTC()
	staleCutoff := now.Add(-time.Duration(threshold) * 24 * time.Hour)

	rows, err := s.repo.ListStaleKeys(ctx, tenantID, staleCutoff)
	if err != nil {
		return nil, fmt.Errorf("list stale keys: %w", err)
	}

	out := make([]StaleKeyView, 0, len(rows))
	for _, k := range rows {
		action, reason := suggestedActionFor(k, staleCutoff, now)
		out = append(out, StaleKeyView{
			Key:             k,
			SuggestedAction: action,
			Reason:          reason,
		})
	}
	return out, nil
}

// resolveThreshold returns the tenant's idle_revoke_days when the policy
// is set OR defaultIdleThresholdDays when it isn't. Errors reading the
// policy fall back to the default with a warn log — the review surface
// is nudge-only, so degrading to the default is preferable to a hard
// failure that hides all stale keys from the operator.
func (s *AccessReviewService) resolveThreshold(ctx context.Context, tenantID uuid.UUID) int32 {
	policy, err := s.policies.GetOrDefault(ctx, tenantID)
	if err != nil {
		slog.WarnContext(ctx, "access review: policy read failed, using default threshold",
			"tenant_id", tenantID, "err", err)
		return defaultIdleThresholdDays
	}
	if policy == nil || policy.IdleRevokeDays == nil {
		return defaultIdleThresholdDays
	}
	return *policy.IdleRevokeDays
}

// suggestedActionFor implements the spec §Feature 4 heuristic as a pure
// function so the four branches are trivially unit-testable.
//
// Branches:
//  1. rotation_due_at set AND in the past → REVOKE, reason=rotation_lapsed
//     (both idle + lapsed is reported as "both" so the FE can render a
//     stronger message; the action stays REVOKE).
//  2. last_used_at is well past the cutoff (cutoff - grace) → REVOKE, reason=idle
//  3. last_used_at within the grace window of the cutoff → KEEP, reason=idle
//     (recently stale, could just be an infrequent CI bot)
//  4. Otherwise → SNOOZE, reason=idle
//     (uncertain case — the FE's default snooze button is the safe pick)
//
// A NULL last_used_at counts as "well past" so a never-used key gets a
// firm REVOKE suggestion — no reason to keep a key that was created but
// never touched.
func suggestedActionFor(k repository.StaleKey, staleCutoff, now time.Time) (SuggestedAction, string) {
	rotationLapsed := k.RotationDueAt != nil && k.RotationDueAt.Before(now)
	idleCutoffFar := staleCutoff.Add(-time.Duration(suggestedRevokeGraceDays) * 24 * time.Hour)

	// Distinguish "idle" vs "rotation_lapsed" vs "both".
	isIdle := k.LastUsedAt == nil || k.LastUsedAt.Before(staleCutoff)
	switch {
	case rotationLapsed && isIdle:
		return SuggestedActionRevoke, "both"
	case rotationLapsed:
		return SuggestedActionRevoke, "rotation_lapsed"
	}

	// From here, only the idle branch applies.
	// Never-used → well past.
	if k.LastUsedAt == nil {
		return SuggestedActionRevoke, "idle"
	}
	// Well past cutoff (older than cutoff - grace).
	if k.LastUsedAt.Before(idleCutoffFar) {
		return SuggestedActionRevoke, "idle"
	}
	// Recently past cutoff but within grace window → soft KEEP.
	if k.LastUsedAt.Before(staleCutoff) {
		return SuggestedActionKeep, "idle"
	}
	// Uncertain fallback — SNOOZE lets the operator defer without deciding.
	return SuggestedActionSnooze, "idle"
}

// SnoozeAPIKeyReviewInput is the validated request payload for Snooze.
//
// TenantID (SEC-069) is the caller's tenant as asserted by the BFF from
// the JWT claims. uuid.Nil skips the cross-check (rolling-deploy
// tolerance for callers built before the field existed); when set, it
// must match the key's own tenant or the call fails with NotFound.
type SnoozeAPIKeyReviewInput struct {
	KeyID    uuid.UUID
	Days     int32
	ActorID  uuid.UUID
	TenantID uuid.UUID
}

// SnoozeAPIKeyReview defers the next access-review nudge for the given
// key by `days` (must be in [1, 90]). Emits an
// auth.access_review.snoozed audit event on success. Returns the
// updated StaleKey shape so the caller can rehydrate the FE row (the
// FE optimistically renders the snooze-until badge without a refetch).
//
// Validation:
//   - Days ∈ [snoozeMinDays, snoozeMaxDays] (SEC lesson from FUT-003:
//     don't guard on "if days > 0"; explicitly reject the out-of-range
//     values so a caller can't pass 0 to secretly clear the snooze).
//   - ActorID non-zero — the BFF is expected to supply it from the JWT.
func (s *AccessReviewService) SnoozeAPIKeyReview(ctx context.Context, in SnoozeAPIKeyReviewInput) (*repository.StaleKey, error) {
	if in.KeyID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "key_id is required")
	}
	if in.ActorID == uuid.Nil {
		return nil, status.Error(codes.InvalidArgument, "actor_id is required")
	}
	if in.Days < snoozeMinDays || in.Days > snoozeMaxDays {
		return nil, status.Errorf(codes.InvalidArgument,
			"days must be in [%d, %d] (got %d)", snoozeMinDays, snoozeMaxDays, in.Days)
	}

	// Load tenant + owner up front so we can populate the audit event
	// AND return a fresh StaleKey shape to the caller without a second
	// round-trip on the update path.
	tenantID, ownerUserID, err := s.repo.GetTenantIDForKey(ctx, in.KeyID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "api key not found")
		}
		return nil, fmt.Errorf("lookup key tenant: %w", err)
	}

	// SEC-069: when the caller asserted a tenant, the key must belong to
	// it. NotFound (not PermissionDenied) on mismatch — an opaque failure
	// that doesn't leak whether the key id exists in another tenant.
	// This is the service-layer backstop behind the BFF's tenant-scoped
	// pre-flight scan (SEC-068): even if a future BFF regression drops
	// that scan, a cross-tenant snooze can no longer reach the DB write.
	if in.TenantID != uuid.Nil && in.TenantID != tenantID {
		slog.WarnContext(ctx, "access review: snooze tenant mismatch rejected",
			"asserted_tenant_id", in.TenantID,
			"key_id", in.KeyID,
		)
		return nil, status.Error(codes.NotFound, "api key not found")
	}

	now := s.now().UTC()
	until := now.Add(time.Duration(in.Days) * 24 * time.Hour)
	if err := s.repo.SetReviewSnoozedUntil(ctx, in.KeyID, &until); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "api key not found")
		}
		return nil, fmt.Errorf("set review snoozed until: %w", err)
	}

	s.emitSnoozed(ctx, tenantID, in.KeyID, in.ActorID, in.Days, until)

	// Return a fresh StaleKey shape so the FE can render the snoozed
	// badge without waiting for a list refetch. We only populate the
	// fields the caller actually needs — the full row is not required
	// for the snooze confirmation UX.
	return &repository.StaleKey{
		ID:                 in.KeyID,
		TenantID:           tenantID,
		OwnerUserID:        ownerUserID,
		ReviewSnoozedUntil: &until,
	}, nil
}

// emitSnoozed publishes an auth.access_review.snoozed audit event. Best-
// effort: a nil emitter (test / dev without broker) is a no-op. Errors
// are logged but never bubbled up — the DB write already succeeded and
// is the source of truth.
//
// The payload is pre-serialised into Fields["payload_json"] so the
// downstream publisher can hand it straight to RabbitMQ without
// re-marshalling. Mirrors the FUT-003 token-policy emit pattern.
func (s *AccessReviewService) emitSnoozed(ctx context.Context, tenantID, keyID, actorID uuid.UUID, days int32, until time.Time) {
	if s.audit == nil {
		return
	}
	payload := events.AccessReviewSnoozedPayload{
		TenantID:     tenantID.String(),
		KeyID:        keyID.String(),
		ActorID:      actorID.String(),
		SnoozedUntil: until.Format(time.RFC3339),
		DaysSnoozed:  days,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		slog.WarnContext(ctx, "access review: marshal snoozed payload failed", "err", err)
		return
	}
	ev := AuditEvent{
		TenantID: tenantID.String(),
		Action:   events.RoutingAccessReviewSnoozed,
		ActorID:  actorID.String(),
		Resource: keyID.String(),
		Fields: map[string]any{
			"payload_json": string(raw),
			"key_id":       keyID.String(),
			"actor_id":     actorID.String(),
		},
	}
	if err := s.audit.Emit(ctx, ev); err != nil {
		slog.WarnContext(ctx, "access review: audit emit failed",
			"tenant_id", tenantID, "key_id", keyID, "err", err)
	}
}
