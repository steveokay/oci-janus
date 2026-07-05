// Package worker — session_sweep.go: a ticker that garbage-collects expired /
// long-idle user_sessions rows so the table and the session list stay bounded.
// Mirrors runLoginSessionCleanup but for interactive sessions.
package worker

import (
	"context"
	"log/slog"
	"time"
)

// sessionSweepRepo is the narrow interface the sweeper needs. Satisfied by
// *repository.SessionRepository via DeleteExpired. Kept small so tests can
// supply a fake without pulling in the full pgx stack.
type sessionSweepRepo interface {
	DeleteExpired(ctx context.Context, idleCutoff time.Time) (int64, error)
}

// SessionSweeper deletes rows past their absolute expiry or older than
// idleWindow since last activity.
type SessionSweeper struct {
	repo       sessionSweepRepo
	idleWindow time.Duration
	period     time.Duration
	logger     *slog.Logger
}

// NewSessionSweeper constructs a SessionSweeper. logger may be nil.
func NewSessionSweeper(repo sessionSweepRepo, idleWindow, period time.Duration, logger *slog.Logger) *SessionSweeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionSweeper{repo: repo, idleWindow: idleWindow, period: period, logger: logger}
}

// TickOnce runs one sweep. Exposed so tests drive it deterministically.
func (w *SessionSweeper) TickOnce(ctx context.Context) error {
	// idleCutoff = now - idleWindow; DeleteExpired also enforces the absolute
	// expires_at column so this handles both the idle-timeout and hard-expiry
	// cases in a single statement.
	n, err := w.repo.DeleteExpired(ctx, time.Now().Add(-w.idleWindow))
	if err != nil {
		return err
	}
	if n > 0 {
		w.logger.Debug("session sweep: deleted expired sessions", "n", n)
	}
	return nil
}

// Run loops until ctx is cancelled.
func (w *SessionSweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(w.period)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.TickOnce(ctx); err != nil {
				w.logger.Warn("session sweep failed", "err", err)
			}
		}
	}
}
