// Package scheduler holds the FUT-019 Phase 2 scheduled-notifications
// infrastructure: a per-category registry, the scheduler loop that
// inserts due rows into scheduled_notifications, and the dispatcher
// loop that drains them and writes notification_events into the bell
// feed.
//
// Each category implements the Category interface:
//
//	Name      stable wire-key the FE preferences matrix targets
//	Cadence   how often to schedule for an active tenant
//	Build     builds the payload for ScheduleNotification
//	Render    turns a delivered scheduled_notifications row into the
//	          title/summary/link the bell renders
//
// New categories add an entry to the Registry slice below. No other
// code needs to change.
package scheduler

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Category is the per-category contract. Stable wire-keys keep the
// FE preferences matrix decoupled from the Go type names.
type Category interface {
	Name() string
	Cadence() time.Duration
	Build(tenantID uuid.UUID, now time.Time) (json.RawMessage, error)
	Render(payload json.RawMessage) (RenderedNotification, error)
}

// RenderedNotification is the bell-side wire shape. Mirrors what
// services/audit's renderNotification switch already produces for
// every other category. Action is fixed to "notification.scheduled"
// at the dispatcher layer — categories only fill the visible body.
type RenderedNotification struct {
	Title    string
	Summary  string
	Link     string
	Metadata map[string]string
}

// ── scanner_freshness ────────────────────────────────────────────────

// ScannerFreshnessCategory is the first concrete category — pings each
// active tenant once every 30 days if their Trivy adapter is behind
// the latest known release.
//
// Phase 2 hardcodes LatestKnownTrivyVersion. Phase 3+ will wire a
// real GitHub release poller that updates the constant nightly. The
// hardcoded value is enough to prove the architecture end-to-end on
// the dev stack — the trivy adapter image bundled in docker-compose
// reports a fixed version too.
type ScannerFreshnessCategory struct {
	// LatestKnownTrivyVersion is the version the scheduler considers
	// "current". When the operator's adapter is behind, the message
	// fires. Edit this constant + the release date below as part of
	// the periodic version sweep (Phase 3 wires automation).
	LatestKnownTrivyVersion string
	LatestKnownReleasedAt   time.Time
}

func (c *ScannerFreshnessCategory) Name() string {
	return "scanner_freshness"
}

func (c *ScannerFreshnessCategory) Cadence() time.Duration {
	return 30 * 24 * time.Hour // monthly
}

// Build assembles the payload the dispatcher reads at render time.
// The current_version field is left blank for Phase 2 — the dispatcher
// can't easily query services/scanner from here without a runtime
// dep, and the message stays useful as a generic "verify your
// adapter is current" nudge. Phase 3+ will inject the live version
// via an HTTP probe to services/scanner.
func (c *ScannerFreshnessCategory) Build(tenantID uuid.UUID, now time.Time) (json.RawMessage, error) {
	body := struct {
		LatestVersion string    `json:"latest_known_version"`
		ReleasedAt    time.Time `json:"latest_released_at"`
		ScheduledAt   time.Time `json:"scheduled_at"`
	}{
		LatestVersion: c.LatestKnownTrivyVersion,
		ReleasedAt:    c.LatestKnownReleasedAt,
		ScheduledAt:   now,
	}
	return json.Marshal(body)
}

func (c *ScannerFreshnessCategory) Render(payload json.RawMessage) (RenderedNotification, error) {
	var p struct {
		LatestVersion string    `json:"latest_known_version"`
		ReleasedAt    time.Time `json:"latest_released_at"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return RenderedNotification{}, fmt.Errorf("render scanner_freshness payload: %w", err)
	}
	// Tone discipline (locked in futures.md): first sentence carries
	// the actionable noun + verb. No "you have a notification"
	// vagueness.
	return RenderedNotification{
		Title: "Verify your Trivy adapter is current",
		Summary: fmt.Sprintf(
			"Latest known Trivy release: %s (released %s). Open /admin/scanner to compare against your active adapter version.",
			p.LatestVersion, p.ReleasedAt.Format("2006-01-02"),
		),
		Link: "/admin/scanner",
		Metadata: map[string]string{
			"latest_known_version": p.LatestVersion,
		},
	}, nil
}

// Registry is the list of categories the scheduler/dispatcher rotates
// through. Adding a new category is one entry here + the Category
// implementation. Order doesn't matter — the scheduler iterates the
// slice per tenant per tick.
//
// FUT-019 Phase 2 ships with scanner_freshness only. Phase 3+ adds
// the other 6 categories.
func Registry() []Category {
	return []Category{
		&ScannerFreshnessCategory{
			LatestKnownTrivyVersion: "v0.58.0",
			LatestKnownReleasedAt:   time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		},
	}
}
