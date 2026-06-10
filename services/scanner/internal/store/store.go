// Package store provides an in-memory scan state tracker for the scanner service.
// State is authoritative only for the lifetime of the process; durable results
// are written to registry-metadata via gRPC after each scan completes.
package store

import (
	"sync"
	"time"
)

// Status values matching the registry-metadata scan_results table constraint.
const (
	StatusPending  = "pending"
	StatusRunning  = "running"
	StatusComplete = "complete"
	StatusFailed   = "failed"
)

// ScanRecord holds the live status of one scan job.
type ScanRecord struct {
	ScanID         string
	TenantID       string
	ManifestDigest string
	RepositoryName string
	Status         string
	SeverityCounts map[string]int
	CompletedAt    *time.Time
}

// Store is a thread-safe in-memory index of scan records keyed by scan_id.
type Store struct {
	mu sync.RWMutex
	m  map[string]*ScanRecord
}

// New returns an empty Store.
func New() *Store {
	return &Store{m: make(map[string]*ScanRecord)}
}

// Create inserts a new scan record with StatusPending.
func (s *Store) Create(id, tenantID, manifestDigest, repoName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = &ScanRecord{
		ScanID:         id,
		TenantID:       tenantID,
		ManifestDigest: manifestDigest,
		RepositoryName: repoName,
		Status:         StatusPending,
	}
}

// SetRunning marks the scan as running.
func (s *Store) SetRunning(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.m[id]; ok {
		r.Status = StatusRunning
	}
}

// SetComplete marks the scan as complete with severity counts.
func (s *Store) SetComplete(id string, counts map[string]int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.m[id]; ok {
		r.Status = StatusComplete
		r.SeverityCounts = counts
		now := time.Now()
		r.CompletedAt = &now
	}
}

// SetFailed marks the scan as failed.
func (s *Store) SetFailed(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.m[id]; ok {
		r.Status = StatusFailed
		now := time.Now()
		r.CompletedAt = &now
	}
}

// Get returns a shallow copy of the record, or false if not found.
func (s *Store) Get(id string) (ScanRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.m[id]
	if !ok {
		return ScanRecord{}, false
	}
	cp := *r
	return cp, true
}
