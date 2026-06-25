// Package service — FUT-012 Phase A — tenant-user lifecycle.
//
// Wires the three new RPCs into the persistence layer. The handler
// imports nothing about the password / argon2 / Redis details — every
// security-sensitive operation runs here so a future SCIM or SAML
// auto-provisioning path can reuse the same helpers.
package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── Errors ────────────────────────────────────────────────────────────

// ErrInvalidEmail / ErrInvalidDisplayName are exported in profile.go.
// FUT-012 reuses those same sentinels so the handler error-mapping
// table doesn't grow per-RPC variants.

// ErrInvalidStatusTransition fires when SetUserDisabled is called on a
// user whose current status isn't 'active' or 'disabled' (e.g. an
// 'invited' row). The handler maps this to FailedPrecondition so the
// FE can render a useful "cancel the invite first" message.
var ErrInvalidStatusTransition = errors.New("invalid status transition")

// ── Invite token constants ────────────────────────────────────────────

const (
	// inviteTokenRawLen is the byte count of the random token before
	// hex-encoding. 32 bytes → 64 hex chars on the wire — same shape
	// as the api-key secret so the FE copy-button UX matches.
	inviteTokenRawLen = 32
	// defaultInviteExpiry mirrors the GitHub / GitLab "invitations
	// expire after 7 days" convention. Callers can override via the
	// RPC request field; zero falls back here.
	defaultInviteExpiry = 7 * 24 * time.Hour
	// maxInviteExpiry caps how far into the future an operator can
	// push the token deadline. 30 days lets a holiday inviter survive
	// a typical absence; longer than that is operationally suspect.
	maxInviteExpiry = 30 * 24 * time.Hour
)

// ── ListTenantUsers ───────────────────────────────────────────────────

// ListTenantUsers returns one page of tenant users (FUT-012 Phase A).
// The handler does the tenant-admin / platform-admin gate; this method
// trusts the caller has already authorised the request.
func (s *Service) ListTenantUsers(
	ctx context.Context,
	tenantID uuid.UUID,
	opts repository.ListTenantUsersOpts,
) ([]repository.TenantUserSummary, string, int32, error) {
	return s.users.ListTenantUsers(ctx, tenantID, opts)
}

// ── InviteUser ────────────────────────────────────────────────────────

// InviteUserInput is the validated shape the handler passes in. Email
// + DisplayName have been parsed from the request but not yet
// validated against the auth-service rules; this method runs the same
// checks the user-create + profile paths use.
type InviteUserInput struct {
	TenantID        uuid.UUID
	Email           string
	DisplayName     string
	InvitedBy       uuid.UUID
	InitialOrgRole  string // optional; "" = no initial grant
	InitialOrgName  string // optional; required iff InitialOrgRole != ""
	ExpiresIn       time.Duration
}

// InviteUserResult carries the new user_id, the raw single-use token
// (shown to the operator ONCE), and the absolute expiry. The handler
// echoes all three onto the wire.
type InviteUserResult struct {
	UserID          uuid.UUID
	InviteToken     string
	InviteExpiresAt time.Time
}

// InviteUser provisions a users row in 'invited' status. The raw token
// is returned to the caller in plaintext — only the argon2id hash
// lands in the DB. The FE's expected UX: copy-link affordance
// immediately after the call returns, no DB read path needed.
//
// Username is derived from the local-part of the email (everything
// before the @). A future Phase 2 SMTP-driven flow will instead use a
// dedicated invite-redemption page where the recipient picks their
// own username; until then the local-part fallback is sufficient for
// the platform-admin / tenant-admin smoke flows.
func (s *Service) InviteUser(ctx context.Context, in InviteUserInput) (*InviteUserResult, error) {
	if err := validateEmail(in.Email); err != nil {
		return nil, err
	}
	if err := validateDisplayName(in.DisplayName); err != nil {
		return nil, err
	}

	expiresIn := in.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = defaultInviteExpiry
	}
	if expiresIn > maxInviteExpiry {
		expiresIn = maxInviteExpiry
	}

	username := deriveInviteUsername(in.Email)
	if username == "" {
		return nil, errors.New("could not derive username from email local-part")
	}

	rawToken, err := generateInviteToken()
	if err != nil {
		return nil, fmt.Errorf("generate invite token: %w", err)
	}
	hash, err := argon2pkg.Hash(rawToken)
	if err != nil {
		return nil, fmt.Errorf("hash invite token: %w", err)
	}
	expiresAt := time.Now().UTC().Add(expiresIn)

	created, err := s.users.CreateInvitedUser(ctx, repository.CreateInvitedUserRequest{
		TenantID:        in.TenantID,
		Username:        username,
		Email:           in.Email,
		DisplayName:     in.DisplayName,
		InviteTokenHash: hash,
		InviteExpiresAt: expiresAt,
	})
	if err != nil {
		return nil, err
	}

	// Optional initial role grant — fire-and-forget after the user
	// lands. A failure here leaves the invite in place; the inviter
	// can retry the grant once the user accepts.
	if in.InitialOrgRole != "" && in.InitialOrgName != "" {
		if err := s.users.GrantRole(ctx, repository.RoleAssignment{
			TenantID:   in.TenantID,
			UserID:     created.ID,
			RoleName:   in.InitialOrgRole,
			ScopeType:  "org",
			ScopeValue: in.InitialOrgName,
			GrantedBy:  in.InvitedBy,
		}); err != nil {
			slog.WarnContext(ctx, "FUT-012: initial role grant on invite failed (best-effort)",
				"err", err, "user_id", created.ID, "org", in.InitialOrgName, "role", in.InitialOrgRole)
		}
	}

	return &InviteUserResult{
		UserID:          created.ID,
		InviteToken:     rawToken,
		InviteExpiresAt: expiresAt,
	}, nil
}

// generateInviteToken returns inviteTokenRawLen bytes of crypto/rand
// hex-encoded. Same shape as the api-key secret — the copy-button UX
// already knows how to render hex strings of this length without
// wrapping.
func generateInviteToken() (string, error) {
	buf := make([]byte, inviteTokenRawLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// deriveInviteUsername extracts the local-part of an email and
// sanitises it down to the username allowlist
// (^[a-zA-Z0-9_-]{3,64}$). When the local-part has characters outside
// the allowlist (dots, plus-tags, unicode) they're replaced with '-'
// so an invite for "alice.smith+ci@example.com" still lands a usable
// "alice-smith-ci" username. Truncates to the 64-char max.
func deriveInviteUsername(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return ""
	}
	local := email[:at]
	out := make([]rune, 0, len(local))
	for _, r := range local {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			out = append(out, r)
		case r == '_' || r == '-':
			out = append(out, r)
		default:
			// Collapse runs of "outside-allowlist" chars to a single '-'
			// so "alice...smith" -> "alice-smith" instead of "alice---smith".
			if len(out) > 0 && out[len(out)-1] != '-' {
				out = append(out, '-')
			}
		}
	}
	// Trim leading/trailing dashes a tighter regex would also reject.
	for len(out) > 0 && out[0] == '-' {
		out = out[1:]
	}
	for len(out) > 0 && out[len(out)-1] == '-' {
		out = out[:len(out)-1]
	}
	if len(out) < 3 {
		return ""
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return string(out)
}

// ── SetUserDisabled ───────────────────────────────────────────────────

// SetUserDisabled flips users.status between 'active' and 'disabled'.
// On disable: revoke every active JTI for the user (via the existing
// revokeAllUserTokens helper that the password-change path uses) +
// disable every API key owned by the user. On enable: status flips,
// but disabled API keys stay disabled — the operator must re-issue
// them deliberately as a defence against re-enabling stolen credentials.
//
// Returns the resulting status string so the handler can echo it on
// the wire without a follow-up SELECT.
func (s *Service) SetUserDisabled(
	ctx context.Context,
	tenantID, userID uuid.UUID,
	disabled bool,
) (string, error) {
	// Load the current row so we can guard against 'invited' targets.
	// (SetUserStatus's WHERE clause also enforces this, but checking
	// here gives the handler a precise error code instead of "not
	// found" for an existing-but-invited user.)
	current, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return "", err
	}
	if current.TenantID != tenantID {
		return "", repository.ErrNotFound
	}
	// FUT-012: invited users are owned by the invite flow's token
	// expiry, not by this RPC. Surface a precondition error so the FE
	// can render "cancel the invite first".
	if current.Kind != "human" {
		return "", ErrInvalidStatusTransition
	}

	target := "active"
	if disabled {
		target = "disabled"
	}

	if err := s.users.SetUserStatus(ctx, tenantID, userID, target); err != nil {
		return "", err
	}

	if disabled {
		// Revoke active JTIs first (so a recently-issued token can't
		// outrun the API key disable). The helper writes
		// `jwt:revoked:<jti>` keys + clears the per-user JTI set.
		if err := s.revokeAllUserTokens(ctx, userID); err != nil {
			slog.WarnContext(ctx, "FUT-012: revoke JTIs on disable failed (best-effort)",
				"err", err, "user_id", userID)
		}
		n, err := s.users.DisableAPIKeysForUser(ctx, tenantID, userID)
		if err != nil {
			slog.WarnContext(ctx, "FUT-012: disable api keys on disable failed (best-effort)",
				"err", err, "user_id", userID)
		} else if n > 0 {
			slog.InfoContext(ctx, "FUT-012: disabled API keys for user",
				"user_id", userID, "key_count", n)
		}
	}

	return target, nil
}
