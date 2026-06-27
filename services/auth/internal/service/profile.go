// Package service — this file implements the user-self-service operations
// exposed via /users/me (FE-API-011/012/013): reading the current profile,
// patching display_name / email, and changing the password.
package service

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// Sentinel errors specific to the profile / password-change flows. Handlers
// translate these into HTTP status codes; the messages themselves are coarse
// because some of them are user-facing.
var (
	// ErrInvalidEmail is returned by UpdateUserProfile when the supplied email
	// fails the RFC 5322 sanity check (handler-level allowlist).
	ErrInvalidEmail = errors.New("invalid email address")
	// ErrInvalidDisplayName is returned for empty, over-length, or
	// control-character-containing display names.
	ErrInvalidDisplayName = errors.New("invalid display name")
	// ErrNoFieldsToUpdate is returned when PATCH /users/me supplies no fields.
	// Handlers translate this to 400 BADREQUEST.
	ErrNoFieldsToUpdate = errors.New("no fields supplied to update")
	// ErrPasswordRateLimited is returned when a single user has exceeded the
	// per-hour password-change attempt budget.
	ErrPasswordRateLimited = errors.New("too many password change attempts")
)

const (
	// maxDisplayNameLen is the maximum length, in bytes, of a display_name.
	// Chosen to match common UI constraints; the underlying column has no
	// length limit so changing this is purely an application policy.
	maxDisplayNameLen = 128
	// maxEmailLen caps the email length so a malicious caller can't push a
	// 10 MB string through to the DB before validation.
	maxEmailLen = 320 // RFC 5321 §4.5.3.1.3 max (local 64 + @ + domain 255)
	// passwordChangeWindow is the rolling window for per-user password-change
	// rate limiting.
	passwordChangeWindow = time.Hour
	// passwordChangeMaxAttempts is the maximum number of password-change
	// attempts (successful or not) per user within passwordChangeWindow.
	passwordChangeMaxAttempts = 5
	// userJTISetTTL bounds how long a user's active-JTI set lives in Redis
	// regardless of churn. It is set to 2× the token TTL so that even if a
	// user logs in repeatedly the set self-cleans some time after their last
	// token expires.
	userJTISetTTL = 2 * tokenTTL
)

// validateDisplayName enforces 1..128 char length and rejects ASCII control
// characters. The list of allowed printable Unicode characters is permissive
// on purpose — internationalised names (中野, naïve, etc.) must work.
func validateDisplayName(s string) error {
	if len(s) == 0 || len(s) > maxDisplayNameLen {
		return ErrInvalidDisplayName
	}
	for _, ch := range s {
		// Reject ASCII control chars (newlines, tabs, etc.) so display names
		// can't break log lines or terminal output. Allow other Unicode marks
		// so non-Latin scripts (e.g. combining marks) are not rejected.
		if unicode.IsControl(ch) {
			return ErrInvalidDisplayName
		}
	}
	return nil
}

// validateEmail performs a permissive but real RFC 5322 parse via net/mail.
// The handler still rejects obviously-wrong inputs (empty, no '@', whitespace)
// but the heavy lifting lives here so service-level callers (and tests) get
// the same answer.
func validateEmail(s string) error {
	if len(s) == 0 || len(s) > maxEmailLen {
		return ErrInvalidEmail
	}
	// Reject leading/trailing whitespace; net/mail tolerates them in some
	// formats which would let callers smuggle whitespace into the DB.
	if strings.TrimSpace(s) != s {
		return ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(s)
	if err != nil {
		return ErrInvalidEmail
	}
	// ParseAddress accepts "Name <email@host>" — we want the bare address only.
	if addr.Address != s {
		return ErrInvalidEmail
	}
	return nil
}

// UpdateUserProfile validates and applies the optional display_name / email
// changes. At least one of the two must be non-nil — otherwise the handler
// would be making a no-op call which is almost always a client bug.
//
// On a UNIQUE-constraint clash with another user's email in the same tenant
// the repository returns ErrAlreadyExists, which we forward verbatim so the
// handler can map it to a 409.
func (s *Service) UpdateUserProfile(
	ctx context.Context,
	userID uuid.UUID,
	displayName, email *string,
) (*repository.User, error) {
	if displayName == nil && email == nil {
		return nil, ErrNoFieldsToUpdate
	}
	if displayName != nil {
		if err := validateDisplayName(*displayName); err != nil {
			return nil, err
		}
	}
	if email != nil && *email != "" {
		if err := validateEmail(*email); err != nil {
			return nil, err
		}
	}
	return s.users.UpdateProfile(ctx, userID, repository.UpdateProfileRequest{
		DisplayName: displayName,
		Email:       email,
	})
}

// MarkOnboardingComplete flips users.onboarding_complete to true for the given
// user and returns the refreshed row. REDESIGN-001 Phase 4.3 — invoked by
// POST /api/v1/users/me/onboarding/complete when the wizard concludes.
//
// This is a thin pass-through to the repository: there's no policy to enforce
// here (the wizard is purely a UI affordance, not a security boundary) and no
// side effects beyond the column flip and updated_at touch. The handler is the
// only caller, but we still route through the service layer so future hooks
// (audit emit, metrics, "first user completed onboarding" platform-level
// notification) can land here without churning the handler.
//
// Idempotent: calling it on a user whose flag is already true succeeds.
// Returns repository.ErrNotFound when the user id no longer exists so the
// handler can map it to 401.
func (s *Service) MarkOnboardingComplete(ctx context.Context, userID uuid.UUID) (*repository.User, error) {
	return s.users.MarkOnboardingComplete(ctx, userID)
}

// ChangePassword verifies the supplied current password, enforces the password
// policy on the new password, persists the new argon2id hash, and revokes all
// of the user's currently active JTIs from Redis so any other sessions are
// forced to re-authenticate.
//
// Returns ErrInvalidCredentials on a wrong current password (handlers MUST
// translate this to 401 with a generic message — never reveal user existence).
// Returns a *PasswordPolicyError when the new password is too weak; this is
// safe to surface to the caller because no state has been mutated yet.
// Returns ErrPasswordRateLimited when the user has exceeded the per-hour
// attempt budget — the handler maps this to 429.
func (s *Service) ChangePassword(
	ctx context.Context,
	userID uuid.UUID,
	currentPassword, newPassword string,
) error {
	// Rate-limit BEFORE the expensive Argon2id verify so an attacker cannot
	// burn CPU brute-forcing the current password under the same throttle.
	if err := s.checkPasswordChangeRateLimit(ctx, userID); err != nil {
		return err
	}
	// Record the attempt up-front (whether it succeeds or fails) so the limit
	// counts ALL tries — otherwise a brute-force run only "costs" the attacker
	// successful guesses, which is exactly what we don't want.
	s.recordPasswordChangeAttempt(ctx, userID)

	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		// A logged-in JWT whose `sub` no longer exists is a malformed-state
		// situation — return ErrInvalidCredentials so the response shape is
		// indistinguishable from "wrong current password".
		if errors.Is(err, repository.ErrNotFound) {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("get user: %w", err)
	}

	ok, err := argon2pkg.Verify(currentPassword, user.PasswordHash)
	if err != nil {
		return fmt.Errorf("verify current password: %w", err)
	}
	if !ok {
		return ErrInvalidCredentials
	}

	// Enforce policy on new password BEFORE hashing so we never pay the
	// Argon2id cost for an obviously-weak input.
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	// Reject "new == current" so users can't accidentally no-op a change. This
	// is a usability gate, not a security one — Argon2 already deduped via the
	// salt, but reusing the same plaintext defeats the point of changing.
	if newPassword == currentPassword {
		return &PasswordPolicyError{"new password must differ from current password"}
	}

	newHash, err := argon2pkg.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := s.users.UpdatePasswordHash(ctx, userID, newHash); err != nil {
		return fmt.Errorf("persist new password: %w", err)
	}

	// Revoke every active JTI for this user so any other live sessions
	// (browser tabs, mobile app, leaked tokens) are immediately invalidated.
	// A failure here is non-fatal — the password is already changed; we log
	// and return success because re-running the request would not help.
	if revErr := s.revokeAllUserTokens(ctx, userID); revErr != nil {
		// Caller (the handler) does not surface this to the client; instead
		// it logs server-side. Wrap so callers can errors.Is(err, redis.Nil).
		return fmt.Errorf("revoke active sessions: %w", revErr)
	}

	return nil
}

// recordIssuedJTI adds the JTI to the user's active-token set in Redis.
// Called from IssueToken. The set has a sliding TTL so it self-cleans some
// time after the user's last token expires; Redis SET membership scales fine
// even when a heavy user has thousands of historical tokens.
//
// Errors are swallowed by the caller because token issuance must not fail
// just because Redis is briefly unavailable — the worst case is that a
// password change won't revoke this one JTI, and the token's natural 5-minute
// expiry will catch it anyway.
func (s *Service) recordIssuedJTI(ctx context.Context, userID, jti string) error {
	if userID == "" || jti == "" {
		return nil
	}
	key := userJTIsKey(userID)
	pipe := s.redis.Pipeline()
	pipe.SAdd(ctx, key, jti)
	pipe.Expire(ctx, key, userJTISetTTL)
	_, err := pipe.Exec(ctx)
	return err
}

// revokeAllUserTokens marks every known JTI for the user as revoked in Redis
// (using the same `jwt:revoked:<jti>` key that ValidateToken consults) and
// clears the user's active-JTI set so subsequent lookups don't repeat work.
//
// The TTL on each revocation entry is tokenTTL because we don't know each
// JTI's actual remaining lifetime here — using the full TTL is safe (entries
// for already-expired tokens will be no-ops in practice; ValidateToken
// rejects expired tokens before consulting the revocation list).
func (s *Service) revokeAllUserTokens(ctx context.Context, userID uuid.UUID) error {
	key := userJTIsKey(userID.String())
	jtis, err := s.redis.SMembers(ctx, key).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	if len(jtis) == 0 {
		return nil
	}
	pipe := s.redis.Pipeline()
	for _, j := range jtis {
		// Use tokenTTL as a generous upper bound — token would have expired
		// naturally by then even without this revocation entry.
		pipe.Set(ctx, revokedKey(j), "1", tokenTTL)
	}
	pipe.Del(ctx, key)
	_, err = pipe.Exec(ctx)
	return err
}

// checkPasswordChangeRateLimit returns ErrPasswordRateLimited when the user
// has exceeded passwordChangeMaxAttempts in the past passwordChangeWindow.
// Redis unavailability is treated as fail-open — refusing legitimate password
// changes because Redis is down would be worse than the brief rate-limit
// bypass it allows.
func (s *Service) checkPasswordChangeRateLimit(ctx context.Context, userID uuid.UUID) error {
	count, err := s.redis.Get(ctx, passwordChangeKey(userID.String())).Int()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil // fail-open
	}
	if count >= passwordChangeMaxAttempts {
		return ErrPasswordRateLimited
	}
	return nil
}

// recordPasswordChangeAttempt increments the per-user counter and refreshes
// the window expiry. Best-effort; errors are swallowed because the counter is
// not the primary security boundary (the policy + Argon2 verify is).
func (s *Service) recordPasswordChangeAttempt(ctx context.Context, userID uuid.UUID) {
	key := passwordChangeKey(userID.String())
	pipe := s.redis.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, passwordChangeWindow)
	_, _ = pipe.Exec(ctx)
}

// userJTIsKey is the Redis key under which we track the set of currently
// active JTIs for a user. SREM/SADD scale, and a SET keeps the lookup O(N)
// only at password-change time (rare).
func userJTIsKey(userID string) string { return "user:jtis:" + userID }

// passwordChangeKey is the Redis key for the per-user password-change attempt
// counter (rate limit).
func passwordChangeKey(userID string) string { return "rl:pwchange:" + userID }
