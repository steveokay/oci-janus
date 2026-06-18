// Package sigstore manages signature records for registry-signer.
//
// In production, records are persisted to PostgreSQL via a sigstoreRepo so that
// signature metadata survives process restarts, rolling deploys, and OOM kills
// (SEC-015 remediation). In local development (or unit tests) a nil repo falls
// back to the in-memory map so no database is required.
package sigstore

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Record holds a signature entry.
//
// SigB64 contains the raw base64-encoded DER signature bytes used for in-memory
// verification. It is populated on sign and kept only in the in-process cache —
// it is NEVER written to the database (SEC-015: do not persist SigB64 in
// cleartext).
type Record struct {
	SignerID        string
	ManifestDigest  string
	RepositoryName  string
	SignatureDigest string // sha256:<hex> of the raw signature bytes
	KeyID           string
	SigB64          string // raw base64-encoded DER signature — NOT stored in DB
	SignedAt        time.Time
}

// sigstoreRepo is the persistence interface that the DB-backed repository
// implements. Keeping it here (rather than in the repository package) avoids
// an import cycle and allows unit tests to inject lightweight fakes without
// pulling in pgx.
type sigstoreRepo interface {
	// Store upserts a Record. Called after every successful sign operation.
	Store(ctx context.Context, rec *Record) error
	// List returns all records for the given manifest digest.
	List(ctx context.Context, manifestDigest string) ([]*Record, error)
	// FindRec returns the record for (manifestDigest, signerID), or (nil, nil)
	// when no match exists.
	FindRec(ctx context.Context, manifestDigest, signerID string) (*Record, error)
}

// Store is a thread-safe signature index.
//
// The in-memory map acts as a write-through cache: every Add call writes to
// both the map and the DB repo (when one is configured). Reads hit the map
// first; on a cache miss the DB is consulted so that records written by other
// replicas (or before a restart) are found.
type Store struct {
	mu   sync.RWMutex
	data map[string][]*Record // key: manifest digest

	repo sigstoreRepo // nil when running without a database
}

// New creates an in-memory-only Store. Suitable for unit tests and local
// development. Signature records are lost on process restart.
func New() *Store {
	return &Store{data: make(map[string][]*Record)}
}

// NewWithDB creates a Store backed by the provided repository for durable
// persistence. The in-memory map is still maintained as a read-through cache
// so hot paths avoid a DB round-trip on every lookup.
func NewWithDB(repo sigstoreRepo) *Store {
	return &Store{
		data: make(map[string][]*Record),
		repo: repo,
	}
}

// Add persists a signature record.
//
// The record is always written to the in-memory cache so that the same process
// can find it immediately without a DB round-trip. When a repo is configured
// the record is also upserted to PostgreSQL (excluding SigB64 — SEC-015). A DB
// write failure is logged but does not fail the sign operation: the record
// remains in-memory for the lifetime of the process so the caller can still
// verify it.
//
// PENTEST-022: the DB write runs with a bounded `context.Background()` rather
// than the caller's request context — this is intentional because the gRPC
// response has already returned by the time we get here in the typical "fast
// path" usage, so propagating a cancelled context would always fail. We do
// cap with a 5-second deadline so a stalled DB cannot pile up unbounded
// blocked goroutines under burst load.
func (s *Store) Add(rec *Record) {
	s.mu.Lock()
	s.data[rec.ManifestDigest] = append(s.data[rec.ManifestDigest], rec)
	s.mu.Unlock()

	if s.repo != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.repo.Store(ctx, rec); err != nil {
			slog.ErrorContext(ctx, "failed to persist signature record to database",
				"manifest_digest", rec.ManifestDigest,
				"signer_id", rec.SignerID,
				"error", err,
			)
		}
	}
}

// List returns all signature records for the given manifest digest.
//
// The in-memory cache is checked first. On a miss the DB is queried (when
// configured) so that records from other replicas or previous process
// incarnations are found. Results from a DB hit are merged back into the cache.
//
// PENTEST-022: takes the caller's ctx so a cancelled gRPC request stops
// waiting on the DB rather than pinning a connection until the query returns.
func (s *Store) List(ctx context.Context, manifestDigest string) []*Record {
	s.mu.RLock()
	cached := s.data[manifestDigest]
	s.mu.RUnlock()

	// Return a copy of the cached slice so callers cannot mutate internal state.
	if len(cached) > 0 {
		out := make([]*Record, len(cached))
		copy(out, cached)
		return out
	}

	if s.repo == nil {
		return nil
	}

	// Cache miss — query DB and warm the local cache.
	recs, err := s.repo.List(ctx, manifestDigest)
	if err != nil {
		slog.ErrorContext(ctx, "failed to list signatures from database",
			"manifest_digest", manifestDigest,
			"error", err,
		)
		return nil
	}

	if len(recs) > 0 {
		s.mu.Lock()
		// Only populate cache if it is still empty to avoid stomping a concurrent Add.
		if len(s.data[manifestDigest]) == 0 {
			s.data[manifestDigest] = recs
		}
		s.mu.Unlock()
	}
	return recs
}

// FindRec returns the record for a specific signer + manifest pair, or nil when
// no record exists.
//
// Lookup order: in-memory cache → DB (when configured). A DB hit warms the
// cache entry so subsequent lookups from this replica are served from memory.
//
// PENTEST-022: takes the caller's ctx so a cancelled gRPC request releases
// the DB connection promptly.
func (s *Store) FindRec(ctx context.Context, manifestDigest, signerID string) *Record {
	s.mu.RLock()
	for _, r := range s.data[manifestDigest] {
		if r.SignerID == signerID {
			s.mu.RUnlock()
			return r
		}
	}
	s.mu.RUnlock()

	if s.repo == nil {
		return nil
	}

	// Cache miss — consult the database.
	rec, err := s.repo.FindRec(ctx, manifestDigest, signerID)
	if err != nil {
		slog.ErrorContext(ctx, "failed to find signature in database",
			"manifest_digest", manifestDigest,
			"signer_id", signerID,
			"error", err,
		)
		return nil
	}

	if rec != nil {
		// Warm the in-memory cache with the DB result.
		s.mu.Lock()
		s.data[manifestDigest] = append(s.data[manifestDigest], rec)
		s.mu.Unlock()
	}
	return rec
}
