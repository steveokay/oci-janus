package middleware

import (
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// PerUserRateLimiter caps the request rate per authenticated user.
//
// PENTEST-014: even within their own tenant, a malicious authenticated user
// can flood read endpoints (stats, repositories, tags) and drive load on
// metadata/audit. RLS still enforces data isolation, but the noisy-neighbour
// CPU/IO cost is unbounded without an application-layer cap.
//
// Implementation: in-process token bucket (golang.org/x/time/rate) keyed by
// user_id. Buckets idle for `idleTTL` are garbage-collected by a background
// sweep so the map stays bounded. With multiple management replicas the
// effective cluster-wide limit is `replicas × rps`, which is acceptable for
// a defence-in-depth LOW-severity gate; a Redis-backed limiter can replace
// this transparently later by satisfying the same Middleware signature.
type PerUserRateLimiter struct {
	rps     rate.Limit
	burst   int
	idleTTL time.Duration

	mu      sync.Mutex
	buckets map[string]*userBucket

	// stop signals the GC goroutine to exit. Closed by Stop(). PENTEST-025:
	// without this the goroutine ran for process lifetime which leaks one
	// goroutine per limiter instance in any test that recreates the limiter.
	stop chan struct{}
}

type userBucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewPerUserRateLimiter returns a limiter that permits `rps` requests per
// second per user with a `burst` allowance for short spikes. Stale buckets
// (no requests in 10 minutes) are removed by a background goroutine.
//
// Call Stop() when the limiter is no longer needed to release the GC goroutine.
// Production callers typically never stop (limiter lives for process lifetime);
// tests should defer Stop() to keep the goroutine count flat.
func NewPerUserRateLimiter(rps float64, burst int) *PerUserRateLimiter {
	l := &PerUserRateLimiter{
		rps:     rate.Limit(rps),
		burst:   burst,
		idleTTL: 10 * time.Minute,
		buckets: make(map[string]*userBucket),
		stop:    make(chan struct{}),
	}
	go l.gcLoop()
	return l
}

// Stop signals the GC goroutine to exit. Safe to call multiple times.
// PENTEST-025: required for clean shutdown of test scenarios that allocate
// many limiters; in production this only runs on process exit.
func (l *PerUserRateLimiter) Stop() {
	l.mu.Lock()
	defer l.mu.Unlock()
	select {
	case <-l.stop:
		// Already stopped — no-op so double-stop is safe.
	default:
		close(l.stop)
	}
}

// allow returns true if the user is under their per-second limit.
func (l *PerUserRateLimiter) allow(userID string) bool {
	l.mu.Lock()
	b, ok := l.buckets[userID]
	if !ok {
		b = &userBucket{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.buckets[userID] = b
	}
	b.lastSeen = time.Now()
	l.mu.Unlock()
	return b.limiter.Allow()
}

// gcLoop runs every idleTTL/2 and evicts buckets that haven't been touched
// recently, keeping the map size proportional to active users rather than
// lifetime users. Exits when Stop() is called (PENTEST-025).
func (l *PerUserRateLimiter) gcLoop() {
	ticker := time.NewTicker(l.idleTTL / 2)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-l.idleTTL)
			l.mu.Lock()
			for id, b := range l.buckets {
				if b.lastSeen.Before(cutoff) {
					delete(l.buckets, id)
				}
			}
			l.mu.Unlock()
		}
	}
}

// Middleware wraps next, returning 429 when the authenticated user has
// exceeded their rate. Requests without an authenticated user_id (e.g. the
// /healthz endpoint) are passed through unchanged — only authenticated
// callers are limited so unauth probes don't poison everyone's bucket.
func (l *PerUserRateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := UserIDFromContext(r.Context())
		if userID == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !l.allow(userID) {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
