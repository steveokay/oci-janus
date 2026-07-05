// Package service — session.go: the sid lifecycle for interactive logins. A
// SessionMeta (client IP + User-Agent) captured at the HTTP edge is threaded
// into the token-issuing paths; issueSessionToken mints a sid, persists a
// user_sessions row, and embeds the sid in the JWT. List/revoke live here too.
package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

const (
	// sessionMaxAge is the absolute lifetime of a session regardless of activity.
	sessionMaxAge = 30 * 24 * time.Hour
	// sessionIdleWindow is the default idle timeout when no token policy is set.
	// (The token policy's idle_revoke_days overrides this when configured.)
	sessionIdleWindow = 14 * 24 * time.Hour
)

// SessionMeta is the client context captured at the HTTP edge for a new session.
type SessionMeta struct {
	IP        string
	UserAgent string
}

// sessionRepo is the narrow interface the service needs from SessionRepository,
// so tests can supply an in-memory fake.
type sessionRepo interface {
	Create(ctx context.Context, s repository.Session) error
	ListLive(ctx context.Context, userID uuid.UUID, idleCutoff time.Time) ([]repository.Session, error)
	RevokeOwned(ctx context.Context, userID, sid uuid.UUID) (time.Time, bool, error)
	RevokeOthers(ctx context.Context, userID, keepSID uuid.UUID) ([]repository.Session, error)
	TouchLastActive(ctx context.Context, sid uuid.UUID, at time.Time) error
}

// SetSessionRepo wires the user_sessions repository. Kept as a setter so the
// JWT-posture constructors stay signature-stable (mirrors SetMFAKEK).
func (s *Service) SetSessionRepo(r sessionRepo) { s.sessions = r }

// issueSessionToken mints a sid, persists the session row, and issues an access
// token carrying the sid. Used only by the interactive login paths (password,
// MFA completion, SSO). A row-insert failure is fatal to the login — we must
// never hand out a sid claim without a backing row.
//
// When the session repo is not wired (s.sessions == nil), sessions are disabled:
// issue a plain token with no sid. This mirrors the codebase's optional-dependency
// idiom (tokenPolicy / lastUsed / redis are all nil-tolerant) and keeps every
// existing login/MFA/SSO unit test — which does not wire a session repo — green.
func (s *Service) issueSessionToken(ctx context.Context, userID, tenantID uuid.UUID, roles []string, isGlobalAdmin bool, kind string, amr []string, meta SessionMeta) (string, error) {
	if s.sessions == nil {
		return s.IssueToken(ctx, userID.String(), tenantID.String(), nil, roles, isGlobalAdmin, kind, amr, "")
	}
	sid := uuid.New()
	// Persist the session row first: a sid claim must never outlive a missing
	// backing row, so a Create failure aborts the login.
	if err := s.sessions.Create(ctx, repository.Session{
		SID:         sid,
		UserID:      userID,
		TenantID:    tenantID,
		DeviceLabel: parseDeviceLabel(meta.UserAgent),
		UserAgent:   meta.UserAgent,
		IP:          meta.IP,
		ExpiresAt:   s.now().Add(sessionMaxAge),
	}); err != nil {
		return "", err
	}
	return s.IssueToken(ctx, userID.String(), tenantID.String(), nil, roles, isGlobalAdmin, kind, amr, sid.String())
}

// sessionRevokeKey is the Redis key that marks a session revoked. Consulted
// fail-closed by ValidateToken, mirroring revoke:user.
func sessionRevokeKey(sid string) string { return "revoke:sid:" + sid }

// idleCutoff returns now()-idleWindow, honouring the tenant token policy's
// idle_revoke_days when configured, else the default sessionIdleWindow.
func (s *Service) idleCutoff(ctx context.Context, tenantID uuid.UUID) time.Time {
	window := sessionIdleWindow
	// A configured, positive idle_revoke_days on the tenant token policy overrides
	// the default idle window. GetOrDefault never errors on a missing row (it
	// returns a zero-valued policy), so any error path just falls back to default.
	if s.tokenPolicy != nil {
		if p, err := s.tokenPolicy.GetOrDefault(ctx, tenantID); err == nil && p.IdleRevokeDays != nil && *p.IdleRevokeDays > 0 {
			window = time.Duration(*p.IdleRevokeDays) * 24 * time.Hour
		}
	}
	return s.now().Add(-window)
}

// sessionActiveWindow is the debounce interval for last_active writes — one DB
// write per session per minute at most, keeping ValidateToken hot.
const sessionActiveWindow = 60 * time.Second

// sessionActiveUpdater debounces user_sessions.last_active_at writes through
// Redis (SETNX), fail-OPEN on Redis error. Fire-and-forget, exactly like the
// FUT-003 lastUsedUpdater for API keys. last_active is telemetry, not a
// security boundary, so Redis-down here degrades to an inline write, never a deny.
type sessionActiveUpdater struct {
	redis  lastUsedRedis
	repo   sessionTouchRepo
	logger *slog.Logger
}

// sessionTouchRepo is the narrow interface the debouncer needs — just the
// last_active bump. Satisfied by the SessionRepository and by the in-memory
// fake used in unit tests.
type sessionTouchRepo interface {
	TouchLastActive(ctx context.Context, sid uuid.UUID, at time.Time) error
}

// newSessionActiveUpdater constructs a sessionActiveUpdater. logger may be nil —
// slog.Default() is substituted so warn/info paths never panic in dev-mode tests.
func newSessionActiveUpdater(rd lastUsedRedis, repo sessionTouchRepo, logger *slog.Logger) *sessionActiveUpdater {
	if logger == nil {
		logger = slog.Default()
	}
	return &sessionActiveUpdater{redis: rd, repo: repo, logger: logger}
}

// NewSessionActiveUpdater is the exported constructor used by server wiring.
func NewSessionActiveUpdater(rd lastUsedRedis, repo sessionTouchRepo, logger *slog.Logger) *sessionActiveUpdater {
	return newSessionActiveUpdater(rd, repo, logger)
}

// SetSessionActiveUpdater wires the debouncer onto the Service (mirrors SetMFAKEK).
func (s *Service) SetSessionActiveUpdater(u *sessionActiveUpdater) { s.sessionActive = u }

// Touch fire-and-forgets a debounced last_active bump. Callers pass
// context.Background() (the request context would cancel on client disconnect).
func (u *sessionActiveUpdater) Touch(ctx context.Context, sid uuid.UUID) { go u.touchNow(ctx, sid) }

// touchNow is called synchronously by tests + async by Touch. It SET NX EX's a
// per-session debounce key; on a win (or when Redis is unwired/erroring) it
// writes the DB row, on a loss it skips. Redis errors fail-OPEN because
// last_active is telemetry, not a security boundary.
func (u *sessionActiveUpdater) touchNow(ctx context.Context, sid uuid.UUID) {
	now := time.Now().UTC()
	if u.redis != nil {
		set, err := u.redis.SetNX(ctx, "sid_active:"+sid.String(), "1", sessionActiveWindow).Result()
		if err == nil && !set {
			return // another tick claimed this window
		}
		if err != nil && !errors.Is(err, redis.Nil) {
			u.logger.Info("session last_active debounce redis error; falling open", "err", err)
		}
	}
	if err := u.repo.TouchLastActive(ctx, sid, now); err != nil {
		u.logger.Warn("session last_active UPDATE failed", "sid", sid, "err", err)
	}
}
