// Package domainworker polls DNS TXT records to verify custom domain ownership.
package domainworker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
)

const (
	maxDomainAgeHours = 48
	pollInterval      = 5 * time.Minute
)

// Worker polls pending domain verifications and updates Redis when verified.
type Worker struct {
	repo *repository.Repository
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

// poll fetches pending domains and checks each one via DNS.
func (w *Worker) poll(ctx context.Context) {
	domains, err := w.repo.ListUnverifiedDomains(ctx, maxDomainAgeHours)
	if err != nil {
		slog.ErrorContext(ctx, "domain worker: list unverified domains", "error", err)
		return
	}
	for _, d := range domains {
		if err := w.verify(ctx, d); err != nil {
			slog.DebugContext(ctx, "domain not yet verified", "domain", d.Domain, "error", err)
		}
	}
}

// verify looks up _registry-verify.<domain> TXT record and compares against the token.
// On success, marks the domain verified in DB and writes to Redis.
func (w *Worker) verify(ctx context.Context, d *repository.DomainRecord) error {
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
					"domain", d.Domain,
					"error", err,
				)
				// Don't return error — DB is the source of truth, Redis is cache.
			}
			slog.Info("custom domain verified",
				"domain", d.Domain,
				"tenant_id", d.TenantID,
			)
			return nil
		}
	}
	return fmt.Errorf("verification token not found in TXT records for %s", target)
}
