// Package sigstore holds signature records in memory.
// In production, signatures are stored as OCI artifacts pushed to registry-core.
// This in-process store serves ListSignatures without a round-trip.
package sigstore

import (
	"sync"
	"time"
)

// Record holds a signature entry.
type Record struct {
	SignerID        string
	ManifestDigest  string
	RepositoryName  string
	SignatureDigest string // sha256:<hex> of the raw signature bytes
	KeyID           string
	SigB64          string // the raw base64-encoded DER signature
	SignedAt        time.Time
}

// Store is a thread-safe in-memory index of signatures keyed by manifest digest.
type Store struct {
	mu   sync.RWMutex
	data map[string][]*Record // key: manifest digest
}

// New creates an empty Store.
func New() *Store {
	return &Store{data: make(map[string][]*Record)}
}

// Add appends a record for the given manifest digest.
func (s *Store) Add(rec *Record) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[rec.ManifestDigest] = append(s.data[rec.ManifestDigest], rec)
}

// List returns all signature records for the given manifest digest.
func (s *Store) List(manifestDigest string) []*Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Record, len(s.data[manifestDigest]))
	copy(out, s.data[manifestDigest])
	return out
}

// FindRec returns the record for a specific signer + manifest pair, or nil.
func (s *Store) FindRec(manifestDigest, signerID string) *Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.data[manifestDigest] {
		if r.SignerID == signerID {
			return r
		}
	}
	return nil
}
