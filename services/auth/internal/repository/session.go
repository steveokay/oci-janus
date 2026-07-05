// Package repository — session.go: all SQL for the user_sessions table
// (migration 20260706000001). Sessions anchor the stable sid embedded in the
// JWT; the service layer owns the sid lifecycle. No SQL for this table lives
// outside this file (CLAUDE.md §11).
package repository

import (
	"context"
	"fmt"
	"net/netip"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Session is one row of user_sessions.
type Session struct {
	SID          uuid.UUID
	UserID       uuid.UUID
	TenantID     uuid.UUID
	DeviceLabel  string
	UserAgent    string
	IP           string // stringified INET
	CreatedAt    time.Time
	LastActiveAt time.Time
	ExpiresAt    time.Time
	RevokedAt    *time.Time
}

// SessionRepository owns user_sessions.
type SessionRepository struct {
	pool *pgxpool.Pool
}

// NewSessionRepository constructs a SessionRepository.
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{pool: pool}
}

// Create inserts a new session row.
func (r *SessionRepository) Create(ctx context.Context, s Session) error {
	// Validate the IP parses as an inet so a malformed value can't be stored.
	if _, err := netip.ParseAddr(s.IP); err != nil {
		return fmt.Errorf("session ip %q: %w", s.IP, err)
	}
	const q = `INSERT INTO user_sessions
		(sid, user_id, tenant_id, device_label, user_agent, ip, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6::inet, $7)`
	_, err := r.pool.Exec(ctx, q, s.SID, s.UserID, s.TenantID, s.DeviceLabel, s.UserAgent, s.IP, s.ExpiresAt)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// ListLive returns the user's non-revoked, non-expired, non-idle sessions,
// newest-active first. idleCutoff is now()-idleWindow; rows with
// last_active_at at or before it are treated as dead and excluded.
func (r *SessionRepository) ListLive(ctx context.Context, userID uuid.UUID, idleCutoff time.Time) ([]Session, error) {
	const q = `SELECT sid, user_id, tenant_id, device_label, user_agent, host(ip),
		       created_at, last_active_at, expires_at, revoked_at
		FROM user_sessions
		WHERE user_id = $1 AND revoked_at IS NULL
		  AND expires_at > now() AND last_active_at > $2
		ORDER BY last_active_at DESC`
	rows, err := r.pool.Query(ctx, q, userID, idleCutoff)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.SID, &s.UserID, &s.TenantID, &s.DeviceLabel, &s.UserAgent,
			&s.IP, &s.CreatedAt, &s.LastActiveAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RevokeOwned marks one session revoked, but only if it belongs to userID.
// Returns the session's expires_at (for the Redis gate TTL) and true when the
// row exists, is owned, and is still within its absolute lifetime; ok=false
// means the sid was absent, not owned, or already past expiry.
//
// The update is idempotent (SEC-081): `revoked_at = COALESCE(revoked_at, now())`
// leaves an already-revoked row's timestamp intact but still RETURNs expires_at,
// so a caller retrying after a transient Redis-gate write failure re-obtains the
// TTL and can re-drive the gate. The `revoked_at IS NULL` filter is intentionally
// dropped for that reason — matched by `expires_at > now()` so we never resurrect
// a TTL for a dead row.
func (r *SessionRepository) RevokeOwned(ctx context.Context, userID, sid uuid.UUID) (expiresAt time.Time, ok bool, err error) {
	const q = `UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, now())
		WHERE sid = $1 AND user_id = $2 AND expires_at > now()
		RETURNING expires_at`
	err = r.pool.QueryRow(ctx, q, sid, userID).Scan(&expiresAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return time.Time{}, false, nil
		}
		return time.Time{}, false, fmt.Errorf("revoke session: %w", err)
	}
	return expiresAt, true, nil
}

// RevokeOthers marks all of the user's still-live sessions revoked EXCEPT keepSID,
// returning the (sid, expires_at) of each so the caller can set the Redis gate.
// Like RevokeOwned this is idempotent (SEC-081): it COALESCEs revoked_at and
// selects every non-kept unexpired row (revoked or not), so a retry after a
// transient gate-write failure re-returns the same set and re-drives every gate.
func (r *SessionRepository) RevokeOthers(ctx context.Context, userID, keepSID uuid.UUID) ([]Session, error) {
	const q = `UPDATE user_sessions SET revoked_at = COALESCE(revoked_at, now())
		WHERE user_id = $1 AND sid <> $2 AND expires_at > now()
		RETURNING sid, expires_at`
	rows, err := r.pool.Query(ctx, q, userID, keepSID)
	if err != nil {
		return nil, fmt.Errorf("revoke other sessions: %w", err)
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.SID, &s.ExpiresAt); err != nil {
			return nil, fmt.Errorf("scan revoked session: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// TouchLastActive bumps last_active_at to now() for a live session. A no-op row
// count (revoked/expired/absent sid) is not an error — the debouncer swallows it.
func (r *SessionRepository) TouchLastActive(ctx context.Context, sid uuid.UUID, at time.Time) error {
	const q = `UPDATE user_sessions SET last_active_at = $2
		WHERE sid = $1 AND revoked_at IS NULL`
	_, err := r.pool.Exec(ctx, q, sid, at)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// DeleteExpired garbage-collects rows past their absolute expiry or older than
// the idle cutoff. Returns the number of rows deleted. Called by the sweep.
func (r *SessionRepository) DeleteExpired(ctx context.Context, idleCutoff time.Time) (int64, error) {
	const q = `DELETE FROM user_sessions
		WHERE expires_at < now() OR last_active_at < $1`
	tag, err := r.pool.Exec(ctx, q, idleCutoff)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
