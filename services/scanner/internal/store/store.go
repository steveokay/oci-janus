// Package store provides an in-memory scan state tracker for the scanner service.
// State is authoritative only for the lifetime of the process; durable results
// are written to registry-metadata via gRPC after each scan completes.
package store

import (
	"context"
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

// HasRecentScan reports whether a scan exists for (tenantID,
// manifestDigest) that is either still in flight (pending|running) or
// completed within recentWindow. The pull-through cache consumer
// (worker.HandleCachePopulated) uses this to avoid stacking redundant
// scans when a popular image is fetched repeatedly inside a short
// window — the metadata service stores the authoritative scan result,
// but in-memory dedup here keeps the worker pool from being flooded
// before the broker even sees the next event.
//
// recentWindow is interpreted from the record's CompletedAt; an
// in-flight scan (CompletedAt == nil) is always considered "recent"
// regardless of the window. Pass a positive duration; zero or negative
// disables the recent-completion check (in-flight matches still hit).
//
// FUT-017 — added so cache.populated → enqueue stays idempotent without
// reaching for the metadata service on every event. Cross-process
// deduplication still falls back to scan_results uniqueness in
// metadata; this in-memory check is just the cheap first line.
func (s *Store) HasRecentScan(tenantID, manifestDigest string, recentWindow time.Duration) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cutoff := time.Now().Add(-recentWindow)
	for _, r := range s.m {
		if r.TenantID != tenantID || r.ManifestDigest != manifestDigest {
			continue
		}
		// In-flight scan — always a match, regardless of window.
		if r.Status == StatusPending || r.Status == StatusRunning {
			return true
		}
		// Completed scan — only a match when CompletedAt is inside the
		// caller's window. recentWindow <= 0 disables this branch.
		if recentWindow > 0 && r.CompletedAt != nil && r.CompletedAt.After(cutoff) {
			return true
		}
	}
	return false
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

// Sweep removes terminal-status records (complete / failed) whose CompletedAt
// is older than maxAge. Returns the number of records dropped (QA-005).
//
// The metadata service is the system of record for scan results — this
// in-memory map is purely a process-lifetime convenience for live status
// reads. Long-running workers accumulate completed entries forever without
// a sweep; one entry per scan, growing without bound. 24h is a reasonable
// default retention: the dashboard polls scan status every few seconds
// while a scan is active, and once a scan completes the FE switches to
// fetching from metadata so the in-memory row is only useful briefly.
func (s *Store) Sweep(maxAge time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	dropped := 0
	for id, r := range s.m {
		if r.CompletedAt == nil {
			continue
		}
		if r.Status != StatusComplete && r.Status != StatusFailed {
			continue
		}
		if r.CompletedAt.Before(cutoff) {
			delete(s.m, id)
			dropped++
		}
	}
	return dropped
}

// StartSweeper runs Sweep on the given interval until ctx is cancelled.
// Intended to be invoked once at service startup as `go store.StartSweeper(
// ctx, time.Hour, 24*time.Hour)`. Returns when ctx is done.
func (s *Store) StartSweeper(ctx context.Context, interval, maxAge time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(maxAge)
		}
	}
}
