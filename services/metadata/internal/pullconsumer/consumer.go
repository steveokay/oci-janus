// Package pullconsumer hosts the FE-API-042 pull.image consumer for
// registry-metadata. Modelled after services/audit/internal/eventconsumer:
// a thin mapEvent → repository call, no business logic, no per-message
// fan-out. The consumer infrastructure (queue declare, DLX, retries) is
// reused from libs/rabbitmq/consumer.
//
// Why metadata owns this: the FE-API-043 max_idle_days retention rule needs
// a manifests.last_pulled_at column, and metadata is already the source of
// truth for manifests. Putting the consumer in audit would double the round
// trips and leak audit-owned tables back into metadata's RPC surface.
package pullconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// Consumer maps pull.image events into manifests.last_pulled_at updates.
type Consumer struct {
	repo *repository.Repository
}

// New returns a Consumer that writes via repo. The caller wires it into
// libs/rabbitmq/consumer.Consume with a queue named "metadata.pull.image"
// so a separate consumer group lives next to (and never competes with) the
// audit consumer that listens on "#".
func New(repo *repository.Repository) *Consumer {
	return &Consumer{repo: repo}
}

// HandleEvent is the libs/rabbitmq/consumer.Handler signature.
//
// On bad-shape / cross-tenant payloads we log + return nil so the message is
// ACKed and removed from the queue rather than retrying forever — the
// pull.image stream is high-volume and a poison pill should not stall it.
// Genuine transient DB errors return non-nil so the consumer NACK + retry.
func (c *Consumer) HandleEvent(ctx context.Context, event events.Event) error {
	if event.Type != events.RoutingPullImage {
		// Defensive — the subscription should only deliver pull.image, but
		// fanout misconfiguration is real and we don't want to silently apply
		// non-pull events as pulls.
		return nil
	}

	var p events.PullImagePayload
	if err := json.Unmarshal(event.Payload, &p); err != nil {
		slog.WarnContext(ctx, "metadata: pull.image payload unmarshal failed, dropping",
			"event_id", event.ID, "error", err)
		return nil
	}

	// Tenant + repo are mandatory; without them we can neither look up the
	// manifest nor enforce isolation on the UPDATE. ACK and drop.
	if p.TenantID == "" || p.RepositoryID == "" {
		slog.WarnContext(ctx, "metadata: pull.image missing tenant/repo, dropping",
			"event_id", event.ID, "tenant_id", p.TenantID, "repository_id", p.RepositoryID)
		return nil
	}

	manifestID := p.ManifestID
	if manifestID == "" {
		// services/core doesn't always have the metadata UUID handy after a
		// pull (it serves the digest from cache), so we resolve via the
		// digest path. Tenant isolation is enforced on the WHERE clause.
		if p.ManifestDigest == "" {
			slog.WarnContext(ctx, "metadata: pull.image has neither manifest_id nor manifest_digest, dropping",
				"event_id", event.ID)
			return nil
		}
		resolved, err := c.repo.FindManifestIDByDigest(ctx, p.TenantID, p.RepositoryID, p.ManifestDigest)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// Manifest was deleted between the pull (in services/core) and
				// the consumer landing the event. Not retryable.
				slog.DebugContext(ctx, "metadata: pull.image manifest no longer exists, dropping",
					"event_id", event.ID, "manifest_digest", p.ManifestDigest)
				return nil
			}
			return err // transient — let the consumer retry
		}
		manifestID = resolved
	}

	pulledAt := p.PulledAt
	if pulledAt.IsZero() {
		// Defensive fallback so a missing timestamp does not poison the DB
		// with a NULL update; use OccurredAt from the envelope.
		pulledAt = event.OccurredAt
	}

	rows, err := c.repo.UpsertManifestLastPulledAt(ctx, manifestID, p.TenantID, pulledAt)
	if err != nil {
		return err // transient DB error
	}
	if rows == 0 {
		// Inside the 24h debounce window, or manifest didn't match
		// (tenant mismatch). Both are expected; log at DEBUG.
		slog.DebugContext(ctx, "metadata: pull.image debounced or no-match",
			"manifest_id", manifestID, "tenant_id", p.TenantID)
		return nil
	}

	slog.DebugContext(ctx, "metadata: last_pulled_at updated",
		"manifest_id", manifestID, "tenant_id", p.TenantID, "pulled_at", pulledAt)
	return nil
}
