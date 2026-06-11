// Package advisory provides PostgreSQL advisory locking for GC coordination.
// Each GC worker acquires a per-tenant advisory lock before starting a GC run,
// preventing concurrent workers from racing on the same tenant's data.
package advisory

import (
	"context"
	"fmt"
	"hash/fnv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Locker acquires PostgreSQL session-level advisory locks keyed by tenant UUID.
type Locker struct {
	pool *pgxpool.Pool
}

// New returns a Locker backed by the given connection pool.
func New(pool *pgxpool.Pool) *Locker {
	return &Locker{pool: pool}
}

// TryLock attempts to acquire a non-blocking advisory lock for tenantID.
// Returns (unlock func, true, nil) on success or (nil, false, nil) if the lock
// is already held by another session. Returns (nil, false, err) on DB error.
// The caller must invoke unlock() when the protected work is done; unlock
// releases the lock and the pinned connection back to the pool.
func (l *Locker) TryLock(ctx context.Context, tenantID uuid.UUID) (unlock func(), acquired bool, err error) {
	// Pin a single connection — advisory locks are session-scoped.
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("acquire connection: %w", err)
	}

	key := lockKey(tenantID)
	row := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", key)
	if err := row.Scan(&acquired); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("pg_try_advisory_lock: %w", err)
	}

	if !acquired {
		conn.Release()
		return nil, false, nil
	}

	unlock = func() {
		// Explicit unlock so the next session can acquire immediately (vs waiting
		// for the connection to be closed/returned to the pool).
		_, _ = conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", key)
		conn.Release()
	}
	return unlock, true, nil
}

// lockKey derives a stable int64 advisory lock key from a tenant UUID using FNV-64a.
// FNV-64a distributes UUIDs uniformly across the int8 key space PostgreSQL requires.
func lockKey(id uuid.UUID) int64 {
	h := fnv.New64a()
	h.Write(id[:])
	return int64(h.Sum64())
}
