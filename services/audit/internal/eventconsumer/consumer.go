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

	// FE-API-042: each successful manifest GET on services/core publishes a
	// pull.image event so the analytics `metric=pulls` query (FE-API-030) has
	// concrete audit_events rows to bucket. ActorID is empty for anonymous
	// public pulls — we record "anonymous" so the dashboard's actor filter
	// has a stable value rather than a blank cell. Resource mirrors push.image
	// so push + pull events group together in the activity feed.
	case events.RoutingPullImage:
		var p events.PullImagePayload
		_ = json.Unmarshal(event.Payload, &p)
		actor := p.ActorID
		actorType := "user"
		if actor == "" {
			actor = "anonymous"
			actorType = "anonymous"
		}
		resource := p.RepositoryName
		if p.Tag != "" {
			resource = p.RepositoryName + ":" + p.Tag
		} else if p.ManifestDigest != "" {
			resource = p.RepositoryName + "@" + p.ManifestDigest
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "pull.image",
			Resource:   resource,
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

	// FE-API-041: retention lifecycle. All three are system-actor events
	// because the executor is gc's cron loop; when a user triggers
	// TriggerRetentionRun the payload's triggered_by carries the user_id
	// in metadata.raw so the dashboard can still attribute the sweep.
	// Resource is the repository_id (or empty for cross-tenant grace
	// sweeps) so the activity feed groups them next to push events.
	case events.RoutingRetentionEvaluated:
		var p events.RetentionEvaluatedPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "retention.evaluated",
			Resource:   p.RepositoryID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingRetentionApplied:
		var p events.RetentionAppliedPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "retention.applied",
			Resource:   p.RepositoryID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingRetentionGraceCompleted:
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "retention.grace_completed",
			Resource:   "", // cross-tenant or tenant-wide, no specific resource
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FE-API-048 FUT-007: every SA lifecycle mutation lands on this single
	// routing key — services/auth's rabbitMQAuditEmitter packs the
	// spec-§5.7 action (e.g. "service_account.disabled") into the payload
	// rather than fanning out per action. We propagate the payload action
	// verbatim into audit_events.action so the activity feed (FE-API-048
	// FUT-005) groups SA events alongside push/pull.
	//
	// Per-action context (creator_email, key_id, key_prefix, scope diffs)
	// is already preserved inside meta's raw envelope under the `raw.fields`
	// key — no merge needed at this layer; the notifications handler can
	// surface specific fields when it builds the dashboard rows.
	case events.RoutingServiceAccountLifecycle:
		var p events.ServiceAccountLifecyclePayload
		_ = json.Unmarshal(event.Payload, &p)
		actor := p.ActorID
		actorType := "user"
		if actor == "" || actor == "system" {
			actor = "system"
			actorType = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     p.Action,
			Resource:   p.Resource,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}
	}

	return nil
}
