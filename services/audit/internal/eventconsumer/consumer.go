// Package eventconsumer maps incoming RabbitMQ events to audit_events rows.
package eventconsumer

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// Consumer maps platform events to audit records.
type Consumer struct {
	repo *repository.Repository
}

// New creates a Consumer.
func New(repo *repository.Repository) *Consumer {
	return &Consumer{repo: repo}
}

// HandleEvent is the consumer.Handler for all subscribed event types.
// It derives an AuditEvent from the platform event and inserts it.
func (c *Consumer) HandleEvent(ctx context.Context, event events.Event) error {
	tenantID, err := uuid.Parse(event.TenantID)
	if err != nil {
		slog.WarnContext(ctx, "audit: invalid tenant_id in event, skipping", "event_id", event.ID, "type", event.Type)
		return nil // don't NACK — unparseable tenant is a permanent error
	}

	ae := mapEvent(tenantID, event)
	if ae == nil {
		return nil // unknown event type — ignore
	}

	if err := c.repo.Insert(ctx, ae); err != nil {
		return err // NACK → retry
	}

	slog.DebugContext(ctx, "audit event recorded",
		"event_id", event.ID,
		"action", ae.Action,
		"tenant_id", ae.TenantID,
	)
	return nil
}

// mapEvent converts a platform RabbitMQ event into an AuditEvent row.
func mapEvent(tenantID uuid.UUID, event events.Event) *repository.AuditEvent {
	now := event.OccurredAt
	if now.IsZero() {
		now = time.Now()
	}

	meta, _ := json.Marshal(map[string]any{"event_id": event.ID, "raw": json.RawMessage(event.Payload)})

	switch event.Type {
	case events.RoutingPushCompleted:
		var p events.PushCompletedPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    p.PushedBy,
			ActorType:  "user",
			Action:     "push.image",
			Resource:   p.RepositoryName + ":" + p.Tag,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingPushFailed:
		// FE-API-008: surfaced in the notifications bell so users see a failed
		// push without tailing logs. Reuses PushCompletedPayload since the
		// publisher carries the same identifying fields plus an optional reason
		// in the raw envelope.
		var p events.PushCompletedPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    p.PushedBy,
			ActorType:  "user",
			Action:     "push.failed",
			Resource:   p.RepositoryName + ":" + p.Tag,
			Outcome:    "failure",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingWebhookFailed:
		// FE-API-008: webhook delivery failures bubble up to the bell so an
		// operator can spot a misconfigured endpoint without polling the
		// webhook deliveries table. The action name aligns with the
		// dashboard's notification vocabulary ("webhook.delivery_failed")
		// rather than the routing key so the frontend filter is intuitive.
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "webhook.delivery_failed",
			Resource:   string(event.Payload),
			Outcome:    "failure",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingManifestDeleted:
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "delete.manifest",
			Resource:   string(event.Payload),
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingTagDeleted:
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "delete.tag",
			Resource:   string(event.Payload),
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingScanCompleted:
		var p events.ScanCompletedPayload
		_ = json.Unmarshal(event.Payload, &p)
		outcome := "success"
		if p.PolicyViolation {
			outcome = "failure"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "scan.completed",
			Resource:   p.ManifestDigest,
			Outcome:    outcome,
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingScanPolicyBlocked:
		var p events.ScanCompletedPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "scan.policy_blocked",
			Resource:   p.ManifestDigest,
			Outcome:    "failure",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingImageSigned:
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "image.signed",
			Resource:   string(event.Payload),
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingTenantCreated:
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "tenant.created",
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}
	}

	return nil
}
