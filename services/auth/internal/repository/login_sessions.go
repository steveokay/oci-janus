// REDESIGN-001 RM-003 / RM-004 — short-lived login session repository for the
// OAuth/SAML redirect dance. Each row is single-use: it is inserted on /start
// and deleted (via ConsumeByState) on /callback so a captured CSRF token
// cannot be replayed.
//
// Changes from FE-API-034:
//   - TenantID removed (RM-004): sessions are deployment-wide; the callback
//     resolves the tenant from the authenticated user's row.
//   - ProviderID changed from UUID to string (RM-003): providers are now
//     identified by their stable string id (e.g. "google", "okta_saml").
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoginSession is the in-flight state for an SSO redirect.
//
// PKCEVerifier holds the OAuth PKCE code_verifier (or, for SAML, the
// AuthnRequest ID stashed so the ACS handler can enforce InResponseTo).
// RedirectURL is the intra-app path the user is sent to after a successful
// callback. ExpiresAt bounds the lifetime; the periodic cleanup sweep
// deletes rows past expiry.
type LoginSession struct {
	State        string
	ProviderID   string // stable string id matching global_sso_config.provider_id
	PKCEVerifier string
	RedirectURL  string
	ExpiresAt    time.Time
	CreatedAt    time.Time
}

// LoginSessionRepository owns the auth_login_sessions table.
type LoginSessionRepository struct {
	pool *pgxpool.Pool
}

// NewLoginSessionRepository builds the repository against the given pool.
func NewLoginSessionRepository(pool *pgxpool.Pool) *LoginSessionRepository {
	return &LoginSessionRepository{pool: pool}
}

// Create inserts a new login session. Callers must generate the state token
// (32 bytes of crypto/rand, base64url) and the PKCE verifier outside this
// repository so the values appear in only one place in the codebase.
func (r *LoginSessionRepository) Create(ctx context.Context, s *LoginSession) error {
	const q = `
		INSERT INTO auth_login_sessions
		    (state, provider_id, pkce_verifier, redirect_url, expires_at)
		VALUES ($1, $2, $3, $4, $5)`
	_, err := r.pool.Exec(ctx, q,
		s.State, s.ProviderID, nullIfEmpty(s.PKCEVerifier),
		s.RedirectURL, s.ExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("create login session: %w", err)
	}
	return nil
}

// ConsumeByState fetches the row matching state AND deletes it in one
// statement. This enforces the single-use rule: a second call with the same
// state returns ErrNotFound (no row found by the time RETURNING runs).
//
// Expired rows are treated as missing — even if cleanup hasn't run yet, an
// expired session must not authenticate the caller.
func (r *LoginSessionRepository) ConsumeByState(ctx context.Context, state string) (*LoginSession, error) {
	const q = `
		DELETE FROM auth_login_sessions
		WHERE state = $1
		  AND expires_at > NOW()
		RETURNING state, provider_id,
		          COALESCE(pkce_verifier, ''), redirect_url, expires_at, created_at`

	var s LoginSession
	err := r.pool.QueryRow(ctx, q, state).Scan(
		&s.State, &s.ProviderID,
		&s.PKCEVerifier, &s.RedirectURL, &s.ExpiresAt, &s.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("consume login session: %w", err)
	}
	return &s, nil
}

// DeleteExpired removes all rows past expiry. Returns the number of deleted
// rows so the caller can log housekeeping volume. Called by the background
// cleanup goroutine started by the server.
func (r *LoginSessionRepository) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM auth_login_sessions WHERE expires_at <= NOW()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired login sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
