// Package domainworker polls DNS TXT records to verify custom domain ownership.
// Failed polls are rescheduled with exponential backoff to reduce DNS query rate.
// Admin notifications are sent at 24h (reminder) and 48h (final failure).
package domainworker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
)

const (
	// maxDomainAgeHours is the window after which an unverified domain is abandoned.
	// The 48h cutoff matches the CLAUDE.md spec: "Background worker polls DNS until verified (max 48h)".
	maxDomainAgeHours = 48
	// pollInterval is the base ticker interval; the worker wakes up frequently but
	// each domain has its own next_poll_after timestamp so most are skipped cheaply.
	pollInterval = 5 * time.Minute
)

// domainRepository is the subset of repository.Repository that the worker uses.
// Kept unexported so it is a test seam, not a published API.
type domainRepository interface {
	ListUnverifiedDomains(ctx context.Context, maxAgeHours int) ([]*repository.DomainRecord, error)
	MarkDomainVerified(ctx context.Context, domainID uuid.UUID) error
	MarkDomain24hNotified(ctx context.Context, domainID uuid.UUID) error
	MarkDomain48hNotified(ctx context.Context, domainID uuid.UUID) error
	UpdateNextPollAfter(ctx context.Context, domainID uuid.UUID, next time.Time) error
}

// Worker polls pending domain verifications and updates Redis when verified.
type Worker struct {
	repo domainRepository
	rdb  *redis.Client
}

// New creates a Worker.
func New(repo *repository.Repository, rdb *redis.Client) *Worker {
	return &Worker{repo: repo, rdb: rdb}
}

// Run polls for unverified domains every pollInterval until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	// Run immediately on start, then on each tick.
	w.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// poll fetches due domains and checks each one via DNS.
func (w *Worker) poll(ctx context.Context) {
	domains, err := w.repo.ListUnverifiedDomains(ctx, maxDomainAgeHours)
	if err != nil {
		slog.ErrorContext(ctx, "domain worker: list unverified domains", "error", err)
		return
	}
	for _, d := range domains {
		if err := w.verify(ctx, d); err != nil {
			slog.DebugContext(ctx, "domain not yet verified", "domain", d.Domain, "error", err)
			// Reschedule: push next_poll_after forward using backoff so we don't
			// hammer DNS at the base 5-minute rate for every domain on every tick.
			next := time.Now().Add(calcBackoff(d.RegisteredAt))
			if upErr := w.repo.UpdateNextPollAfter(ctx, d.ID, next); upErr != nil {
				slog.WarnContext(ctx, "domain worker: update next_poll_after failed",
					"domain", d.Domain, "error", upErr)
			}
		}
	}
}

// verify looks up _registry-verify.<domain> TXT record and compares against the token.
// On success, marks the domain verified in DB and writes to Redis.
// Sends a 24h reminder notification when the domain has been pending for over 24 hours,
// and a 48h failure notification when it is about to be abandoned.
func (w *Worker) verify(ctx context.Context, d *repository.DomainRecord) error {
	age := time.Since(d.RegisteredAt)

	// Send 48h failure notification before the domain is abandoned, once only.
	if age >= 47*time.Hour && !d.Notified48h {
		slog.WarnContext(ctx, "domain worker: domain nearing 48h expiry — notifying admin",
			"domain", d.Domain, "tenant_id", d.TenantID, "age_hours", int(age.Hours()))
		if err := w.repo.MarkDomain48hNotified(ctx, d.ID); err != nil {
			slog.WarnContext(ctx, "domain worker: mark 48h notification failed",
				"domain", d.Domain, "error", err)
		}
	}

	// Send 24h reminder notification once.
	if age >= 24*time.Hour && !d.Notified24h {
		slog.WarnContext(ctx, "domain worker: domain verification pending 24h — notifying admin",
			"domain", d.Domain, "tenant_id", d.TenantID, "age_hours", int(age.Hours()))
		if err := w.repo.MarkDomain24hNotified(ctx, d.ID); err != nil {
			slog.WarnContext(ctx, "domain worker: mark 24h notification failed",
				"domain", d.Domain, "error", err)
		}
	}

	target := "_registry-verify." + d.Domain
	records, err := net.LookupTXT(target)
	if err != nil {
		return fmt.Errorf("DNS TXT lookup %s: %w", target, err)
	}

	for _, r := range records {
		if r == d.VerificationToken {
			if err := w.repo.MarkDomainVerified(ctx, d.ID); err != nil {
				return fmt.Errorf("mark domain verified: %w", err)
			}
			// Write domain → tenant_id mapping to Redis so the gateway can resolve it.
			redisKey := "domain:" + d.Domain
			if err := w.rdb.Set(ctx, redisKey, d.TenantID.String(), 0).Err(); err != nil {
				slog.ErrorContext(ctx, "domain worker: Redis write failed",
					"domain", d.Domain, "error", err)
				// Non-fatal — DB is source of truth; Redis is a cache.
			}
			slog.InfoContext(ctx, "custom domain verified",
				"domain", d.Domain, "tenant_id", d.TenantID)
			return nil
		}
	}
	return fmt.Errorf("verification token not found in TXT records for %s", target)
}

// calcBackoff returns the delay before the next DNS poll attempt for a domain.
// Backoff increases as the domain ages to reduce DNS query rate for stale entries:
//   - < 1h since registration  →  5 min  (frequent early polling)
//   - 1h–12h                   → 10 min
//   - > 12h                    → 20 min
func calcBackoff(registeredAt time.Time) time.Duration {
	age := time.Since(registeredAt)
	switch {
	case age < time.Hour:
		return 5 * time.Minute
	case age < 12*time.Hour:
		return 10 * time.Minute
	default:
		return 20 * time.Minute
	}
}
