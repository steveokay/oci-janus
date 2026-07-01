// Package worker — idle_revoke.go implements the FUT-003 background
// worker that sweeps API keys whose last_used_at is older than the
// tenant's configured idle_revoke_days threshold.
//
// Flow per tick (default: hourly):
//  1. Enumerate tenants with idle_revoke_days configured (worker-scoped;
//     tenants with no policy do zero work here).
//  2. For each tenant:
//     a. Acquire pg_try_advisory_lock keyed on the tenant id (FNV-64a hash).
//        Skip if another auth replica already holds it — the lock is the
//        multi-replica safety guard.
//     b. Load the tenant's policy (idle_revoke_days).
//     c. ListIdleKeys with cutoff = now - idle_revoke_days.
//     d. For each returned key: RevokeWithReason(id, "idle_revoked") +
//        publish auth.key_revoked with reason="idle_revoked".
//
// Failure semantics:
//   - Any error on tenant N never blocks tenants N+1..M. The loop logs
//     and continues.
//   - A revoke failure per-key logs + continues so one bad row doesn't
//     freeze the whole sweep.
//   - A publish failure per-key logs + continues so a broker hiccup
//     doesn't roll back a revoke that already succeeded (the DB row is
//     the source of truth; the audit gap is visible via the missing
//     audit event).
//
// Fake-clock friendly: `now func() time.Time` and `tickPeriod` are
// injectable for tests.
package worker

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// Publisher is the narrow interface IdleRevoke uses to emit
// auth.key_revoked events. Kept small so tests can supply a fake without
// pulling in the full RabbitMQ publisher stack.
type Publisher interface {
	// PublishKeyRevoked publishes an auth.key_revoked envelope carrying
	// the KeyRevokedPayload with the given reason. Errors are surfaced
	// so the worker can log per-key, but the DB revoke has already
	// happened by the time this is called.
	PublishKeyRevoked(ctx context.Context, tenantID uuid.UUID, payload events.KeyRevokedPayload) error
}

// apiKeyRepo is the narrow interface the worker uses for API-key ops.
// Satisfied by *repository.APIKeyRepository.
type apiKeyRepo interface {
	ListIdleKeys(ctx context.Context, tenantID uuid.UUID, cutoff time.Time) ([]repository.IdleKey, error)
	RevokeWithReason(ctx context.Context, id uuid.UUID, reason string) error
}

// policyRepo is the narrow interface the worker uses for policy reads.
// Satisfied by *repository.TokenPolicyRepo.
type policyRepo interface {
	GetOrDefault(ctx context.Context, tenantID uuid.UUID) (*repository.TokenPolicy, error)
	ListTenantsWithIdleRevoke(ctx context.Context) ([]uuid.UUID, error)
}

// IdleRevoke is the background worker. Constructed at server startup;
// safe to call Run in a goroutine and cancel via context.
type IdleRevoke struct {
	pool       *pgxpool.Pool
	apiKeys    apiKeyRepo
	policies   policyRepo
	pub        Publisher
	now        func() time.Time
	logger     *slog.Logger
	tickPeriod time.Duration
}

// New constructs an IdleRevoke. pub may be nil (no broker) — the worker
// still revokes but the audit trail loses the auth.key_revoked event.
// logger nil falls back to slog.Default. tickPeriod defaults to 1 hour.
func New(pool *pgxpool.Pool, apiKeys apiKeyRepo, policies policyRepo, pub Publisher, logger *slog.Logger) *IdleRevoke {
	if logger == nil {
		logger = slog.Default()
	}
	return &IdleRevoke{
		pool:       pool,
		apiKeys:    apiKeys,
		policies:   policies,
		pub:        pub,
		now:        time.Now,
		logger:     logger,
		tickPeriod: time.Hour,
	}
}

// WithClock replaces the wall-clock reader (tests use this to pin now).
// Chained builder to keep the New signature stable.
func (w *IdleRevoke) WithClock(fn func() time.Time) *IdleRevoke {
	if fn != nil {
		w.now = fn
	}
	return w
}

// WithTickPeriod replaces the default 1-hour cadence (tests use short
// values so a full loop runs inside a bounded context).
func (w *IdleRevoke) WithTickPeriod(d time.Duration) *IdleRevoke {
	if d > 0 {
		w.tickPeriod = d
	}
	return w
}

// Run blocks until ctx is cancelled. Ticks at the configured cadence
// (1h by default; overridable via WithTickPeriod). An IMMEDIATE first
// tick fires so a fresh boot doesn't leave idle keys lingering for up to
// tickPeriod before the sweeper takes its first pass.
func (w *IdleRevoke) Run(ctx context.Context) {
	w.tickOnce(ctx)
	ticker := time.NewTicker(w.tickPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tickOnce(ctx)
		}
	}
}

// TickOnce runs one sweep. Exposed for testing so tests can drive the
// loop without spinning a goroutine + ticker.
func (w *IdleRevoke) TickOnce(ctx context.Context) {
	w.tickOnce(ctx)
}

func (w *IdleRevoke) tickOnce(ctx context.Context) {
	tenantIDs, err := w.policies.ListTenantsWithIdleRevoke(ctx)
	if err != nil {
		w.logger.WarnContext(ctx, "idle_revoke: list tenants failed", "err", err)
		return
	}
	for _, tenantID := range tenantIDs {
		w.tickTenant(ctx, tenantID)
	}
}

// tickTenant handles ONE tenant per invocation. pg_try_advisory_lock
// prevents multi-replica double-work: if another auth replica already
// holds the lock, we skip immediately (the other replica will do the
// work on this tick).
//
// All errors log-and-continue — this loop must not starve other tenants.
func (w *IdleRevoke) tickTenant(ctx context.Context, tenantID uuid.UUID) {
	// Acquire a session-scoped advisory lock. We need to pin a single
	// connection because advisory locks are per-session in Postgres — if
	// the pool hands us a different connection for the unlock, the lock
	// stays held (until the connection is closed).
	conn, err := w.pool.Acquire(ctx)
	if err != nil {
		w.logger.WarnContext(ctx, "idle_revoke: acquire conn failed", "tenant_id", tenantID, "err", err)
		return
	}
	defer conn.Release()

	key := idleRevokeLockKey(tenantID)
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key).Scan(&locked); err != nil {
		w.logger.WarnContext(ctx, "idle_revoke: lock query failed", "tenant_id", tenantID, "err", err)
		return
	}
	if !locked {
		// Another replica already holds the tenant lock — skip silently.
		return
	}
	defer func() {
		if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", key); err != nil {
			w.logger.WarnContext(ctx, "idle_revoke: unlock failed", "tenant_id", tenantID, "err", err)
		}
	}()

	policy, err := w.policies.GetOrDefault(ctx, tenantID)
	if err != nil {
		w.logger.WarnContext(ctx, "idle_revoke: get policy failed", "tenant_id", tenantID, "err", err)
		return
	}
	if policy.IdleRevokeDays == nil {
		// Policy could have been cleared between the enumerate + the
		// per-tenant read — treat as "no work" rather than an error.
		return
	}
	cutoff := w.now().Add(-time.Duration(*policy.IdleRevokeDays) * 24 * time.Hour)
	keys, err := w.apiKeys.ListIdleKeys(ctx, tenantID, cutoff)
	if err != nil {
		w.logger.WarnContext(ctx, "idle_revoke: list idle keys failed", "tenant_id", tenantID, "err", err)
		return
	}
	for _, k := range keys {
		if err := w.apiKeys.RevokeWithReason(ctx, k.ID, "idle_revoked"); err != nil {
			w.logger.WarnContext(ctx, "idle_revoke: revoke failed", "key_id", k.ID, "err", err)
			continue
		}
		w.emit(ctx, tenantID, k)
	}
}

// emit publishes an auth.key_revoked event carrying the FUT-003 reason.
// pub may be nil (no broker) — the worker still revoked the DB row, we
// just lose the audit event. Best-effort per key so a broker hiccup
// doesn't skip the remaining keys in the sweep.
func (w *IdleRevoke) emit(ctx context.Context, tenantID uuid.UUID, k repository.IdleKey) {
	if w.pub == nil {
		return
	}
	owner := ""
	switch {
	case k.UserID != nil:
		owner = k.UserID.String()
	case k.ServiceAccountID != nil:
		owner = k.ServiceAccountID.String()
	}
	payload := events.KeyRevokedPayload{
		TenantID:    tenantID.String(),
		KeyID:       k.ID.String(),
		OwnerUserID: owner,
		Reason:      "idle_revoked",
	}
	if err := w.pub.PublishKeyRevoked(ctx, tenantID, payload); err != nil {
		w.logger.WarnContext(ctx, "idle_revoke: publish failed", "key_id", k.ID, "err", err)
	}
}

// idleRevokeLockKey derives a stable int64 advisory-lock key from a
// tenant UUID. FNV-64a distributes UUIDs uniformly across the int8 key
// space Postgres requires (same technique as services/gc — see Decision
// #16 in CLAUDE.md §14).
//
// We salt the hash with a per-worker prefix ("idle-revoke:") so a
// tenant's idle-revoke lock never collides with the same tenant's GC
// lock — the two workers should never block each other.
func idleRevokeLockKey(id uuid.UUID) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("idle-revoke:"))
	_, _ = h.Write(id[:])
	return int64(h.Sum64())
}

// RunningKeyLabel returns a debug label describing the current
// configuration — surfaced by callers that want a boot-time slog line
// so operators can spot "1h cadence, 3 tenants configured" in logs
// without a separate metric.
func (w *IdleRevoke) RunningKeyLabel() string {
	return fmt.Sprintf("idle_revoke tick=%s", w.tickPeriod)
}
