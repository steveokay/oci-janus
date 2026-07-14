// Package eventconsumer maps incoming RabbitMQ events to audit_events rows.
package eventconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/services/audit/internal/export"
	"github.com/steveokay/oci-janus/services/audit/internal/exportworker"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// aesDecrypt aliases libs/crypto/aes.Decrypt so the consumer file
// reads cleanly without an alias import on every call site.
var aesDecrypt = aes.Decrypt

// Consumer maps platform events to audit records.
type Consumer struct {
	repo *repository.Repository
	// exporter is the optional SIEM-streaming hook (futures.md Tier 1 #4).
	// Phase 1 wired an in-process fire-and-forget dispatcher; Phase 2
	// replaces it with a RabbitMQ publish to the audit.export queue.
	// Both paths still go through this Consumer — the choice is
	// determined by `publisher` below. When neither is wired, the
	// audit INSERT happens as usual and SIEM streaming is disabled.
	exporter  *exportDispatcher       // Phase 1 fallback (inline retry, no DLX)
	publisher *exportworker.Publisher // Phase 2 (durable queue + real DLX)
}

// New creates a Consumer.
func New(repo *repository.Repository) *Consumer {
	return &Consumer{repo: repo}
}

// WithExporter wires the Phase 1 in-process dispatcher as a fallback
// for environments without RabbitMQ (legacy or unit-test setups).
// In production, WithExportPublisher is preferred — see
// services/audit/internal/exportworker for the queue-based path.
func (c *Consumer) WithExporter(d *exportDispatcher) *Consumer {
	c.exporter = d
	return c
}

// WithExportPublisher wires the Phase 2 RabbitMQ-queue publisher
// (futures.md Tier 1 #4). When set, HandleEvent enqueues an
// AuditExportTask after each successful audit_events INSERT and the
// exportworker.Consumer drains + ships independently. The producer
// path becomes near-instant (publisher-confirms only) so a slow SIEM
// no longer back-pressures the audit DB write path.
//
// When both WithExporter + WithExportPublisher are wired,
// WithExportPublisher wins — the queue is the preferred path. The
// inline dispatcher stays as a no-op fallback the operator can revert
// to by unsetting the publisher env var.
func (c *Consumer) WithExportPublisher(p *exportworker.Publisher) *Consumer {
	c.publisher = p
	return c
}

// exportDispatcher carries the bits we need from the gRPC handler
// (cipher key + repository methods) so the consumer's hot path can
// dispatch a single event without re-decrypting on every send. v1
// loads the config + decrypts on every event; a per-tenant LRU cache
// is an obvious Phase 2 optimisation once we observe a real
// throughput problem.
type exportDispatcher struct {
	repo       *repository.Repository
	secretsKey []byte
}

// NewExportDispatcher constructs the optional dispatcher. Returns nil
// when secretsKey is empty — callers can pass the result straight to
// WithExporter without a nil check (WithExporter handles the no-op
// case).
func NewExportDispatcher(repo *repository.Repository, secretsKey []byte) *exportDispatcher {
	if repo == nil {
		return nil
	}
	return &exportDispatcher{repo: repo, secretsKey: secretsKey}
}

// HandleEvent is the consumer.Handler for all subscribed event types.
// It derives an AuditEvent from the platform event and inserts it.
func (c *Consumer) HandleEvent(ctx context.Context, event events.Event) error {
	tenantID, err := uuid.Parse(event.TenantID)
	if err != nil {
		slog.WarnContext(ctx, "audit: invalid tenant_id in event, skipping", "event_id", event.ID, "type", event.Type)
		// Intentional: return nil (not err) so RabbitMQ ACKs the message and
		// drops it rather than NACK + requeue, which would loop forever on
		// a permanent parse error.
		return nil //nolint:nilerr
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

	// Audit-log streaming to SIEM (futures.md Tier 1 #4). After the
	// audit DB write succeeds, hand the event off to whichever
	// transport the operator wired:
	//
	//   • Phase 2 (preferred): publish to the audit.export RabbitMQ
	//     queue. Synchronous via publisher-confirms, so we learn
	//     about broker outages immediately and don't silently drop
	//     events. The exportworker.Consumer drains + ships
	//     independently with full DLX semantics + replay support.
	//
	//   • Phase 1 (fallback): in-process fire-and-forget goroutine.
	//     Used when no broker URL is configured (legacy / unit tests).
	//     Exhausted retries bump the cumulative dlx_depth counter on
	//     the config row but the event itself is gone — Phase 2
	//     supersedes this path.
	//
	// We never NACK the original audit RabbitMQ message based on
	// streaming outcome — the DB write is already durable and a
	// retry would duplicate the audit_events row.
	if c.publisher != nil {
		c.enqueueExportTask(ae)
	} else if c.exporter != nil {
		go c.exporter.dispatch(context.Background(), ae)
	}
	return nil
}

// enqueueExportTask publishes an AuditExportTask onto audit.export.
// Synchronous (publisher-confirms) but bounded by a short context
// timeout so a hung broker doesn't stall the audit consumer's main
// loop. On failure we log + bump the failure counter so the operator
// sees the gap on the dashboard — and fall through to the legacy
// inline dispatcher when it's wired (defence in depth).
func (c *Consumer) enqueueExportTask(ae *repository.AuditEvent) {
	pubCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	task := exportworker.AuditExportTask{
		TenantID:   ae.TenantID.String(),
		EnqueuedAt: time.Now().UTC(),
		Event: export.Event{
			ID:         ae.ID.String(),
			TenantID:   ae.TenantID.String(),
			ActorID:    ae.ActorID,
			ActorType:  ae.ActorType,
			ActorIP:    ae.ActorIP,
			Action:     ae.Action,
			Resource:   ae.Resource,
			Outcome:    ae.Outcome,
			Metadata:   ae.Metadata,
			OccurredAt: ae.OccurredAt,
		},
	}
	if err := c.publisher.Enqueue(pubCtx, task); err != nil {
		slog.WarnContext(pubCtx, "audit export: enqueue failed",
			"tenant_id", ae.TenantID, "action", ae.Action, "err", err)
		_ = c.repo.TouchAuditExportFailure(pubCtx, ae.TenantID,
			"enqueue: "+truncateError(err))
		// Fall through to the Phase 1 dispatcher if it's wired — a
		// broker outage shouldn't take the SIEM stream down too.
		if c.exporter != nil {
			go c.exporter.dispatch(context.Background(), ae)
		}
	}
}

// truncateError clips an error string to fit the last_error column.
func truncateError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

// dispatch loads the tenant config, applies the include/exclude filter,
// decrypts the per-tenant secret material, and runs export.Deliver.
// Background goroutine so the consumer's NACK signal isn't gated on a
// slow SIEM. Errors are logged + persisted on the config row; the
// caller never sees them.
func (d *exportDispatcher) dispatch(ctx context.Context, ae *repository.AuditEvent) {
	cfg, err := d.repo.GetAuditExportConfig(ctx, ae.TenantID)
	if err != nil {
		if !errors.Is(err, repository.ErrExportConfigNotFound) {
			slog.WarnContext(ctx, "audit export: load config failed", "tenant_id", ae.TenantID, "err", err)
		}
		return
	}
	if !cfg.Enabled {
		return
	}
	if !matchesFilter(cfg.EventFilters, ae.Action) {
		return
	}

	hmacSecret, err := openSecretBytes(d.secretsKey, cfg.HMACSecret)
	if err != nil {
		slog.WarnContext(ctx, "audit export: decrypt hmac_secret failed", "tenant_id", ae.TenantID, "err", err)
		return
	}
	bearerToken, err := openSecretBytes(d.secretsKey, cfg.BearerToken)
	if err != nil {
		slog.WarnContext(ctx, "audit export: decrypt bearer_token failed", "tenant_id", ae.TenantID, "err", err)
		return
	}

	exp := export.Config{
		TenantID:    cfg.TenantID.String(),
		Format:      cfg.Format,
		TargetURL:   cfg.TargetURL,
		HMACSecret:  hmacSecret,
		BearerToken: bearerToken,
	}
	evt := export.Event{
		ID:         ae.ID.String(),
		TenantID:   ae.TenantID.String(),
		ActorID:    ae.ActorID,
		ActorType:  ae.ActorType,
		ActorIP:    ae.ActorIP,
		Action:     ae.Action,
		Resource:   ae.Resource,
		Outcome:    ae.Outcome,
		Metadata:   ae.Metadata,
		OccurredAt: ae.OccurredAt,
	}

	if _, err := export.Deliver(ctx, exp, evt); err != nil {
		// Persist the failure for operator visibility. Cap the error
		// string + bump the dlx_depth so the "stuck events" pill on
		// the FE config page renders. We deliberately don't push to
		// a real DLX queue in v1 (see package-level comment).
		msg := err.Error()
		if len(msg) > 512 {
			msg = msg[:512]
		}
		_ = d.repo.TouchAuditExportFailure(ctx, ae.TenantID, msg)
		_ = d.repo.IncrementAuditExportDLX(ctx, ae.TenantID, 1)
		slog.WarnContext(ctx, "audit export: delivery exhausted",
			"tenant_id", ae.TenantID, "format", cfg.Format, "err", err)
		return
	}
	_ = d.repo.TouchAuditExportSuccess(ctx, ae.TenantID)
}

// matchesFilter applies the JSON event_filters expression. Shape:
//
//	{"include": ["push.completed", "scan.*"], "exclude": ["webhook.*"]}
//
// `exclude` wins over `include`. Empty / null filters means "send
// everything." Wildcards: trailing `.*` matches any suffix.
func matchesFilter(raw json.RawMessage, action string) bool {
	if len(raw) == 0 {
		return true
	}
	var f struct {
		Include []string `json:"include"`
		Exclude []string `json:"exclude"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return true // malformed filter → fail open (best-effort)
	}
	for _, p := range f.Exclude {
		if matchPattern(p, action) {
			return false
		}
	}
	if len(f.Include) == 0 {
		return true
	}
	for _, p := range f.Include {
		if matchPattern(p, action) {
			return true
		}
	}
	return false
}

// matchPattern is the simplest practical pattern matcher: exact match
// or `prefix.*` suffix wildcard. We don't need regex — operators write
// their filters by hand and prefer the predictable shape.
func matchPattern(pattern, s string) bool {
	if pattern == s {
		return true
	}
	if strings.HasSuffix(pattern, ".*") {
		return strings.HasPrefix(s, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

// openSecretBytes is the consumer-side decrypt helper. Returns "" for
// nil/empty input so the renderer can branch on `secret == ""`.
// Duplicated from services/audit/internal/handler/audit_export.go to
// avoid the handler ↔ consumer import (consumer is upstream of handler
// in the dependency graph today). Both paths use the same AES-256-GCM
// envelope so the ciphertext format stays consistent.
func openSecretBytes(key, ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", nil
	}
	if len(key) == 0 {
		return "", errors.New("audit-export secrets key not configured")
	}
	plain, err := aesDecrypt(ciphertext, key)
	if err != nil {
		return "", err
	}
	return string(plain), nil
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
		// FUT-081: registry-core now publishes a typed ManifestDeletedPayload
		// carrying the deleter + repo + digest, so the audit row records WHO
		// deleted WHAT instead of a raw JSON blob.
		var p events.ManifestDeletedPayload
		_ = json.Unmarshal(event.Payload, &p)
		actor, actorType := p.DeletedBy, "user"
		if actor == "" {
			actor, actorType = "system", "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "delete.manifest",
			Resource:   p.RepositoryName + "@" + p.Digest,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingTagDeleted:
		// FUT-081: typed TagDeletedPayload — see manifest.deleted above.
		var p events.TagDeletedPayload
		_ = json.Unmarshal(event.Payload, &p)
		actor, actorType := p.DeletedBy, "user"
		if actor == "" {
			actor, actorType = "system", "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "delete.tag",
			Resource:   p.RepositoryName + ":" + p.Tag,
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

	// FUT-020: image promotion. Actor comes from the payload (JWT-derived
	// user_id from the BFF) rather than being system-attributed — a
	// promotion is an explicit operator action; the audit trail is
	// meaningless if it always attributes to "system". Empty actor
	// (bot / SA / CLI) falls back to system so the actor_type column has
	// a stable value.
	//
	// Resource pins to the destination side ("dst_org/dst_repo:dst_tag")
	// because operators reading /activity ask "what got promoted INTO
	// this repo?" — the source is captured in metadata.raw for auditors
	// who need the full picture without a separate query.
	case events.RoutingImagePromoted:
		var p events.ImagePromotedPayload
		_ = json.Unmarshal(event.Payload, &p)
		actor := p.ActorUserID
		actorType := "user"
		if actor == "" {
			actor = "system"
			actorType = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "image.promoted",
			Resource:   p.DstOrg + "/" + p.DstRepo + ":" + p.DstTag,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FUT-021 — CVSS-gated admission policy flip. Actor is empty when
	// the change came from a service-account API key not attached to a
	// shadow user; treat empty as "system" for the actor_type column so
	// the activity feed stays legible.
	case events.RoutingRepoCVSSPolicyChanged:
		var p events.RepoCVSSPolicyChangedPayload
		_ = json.Unmarshal(event.Payload, &p)
		actor := p.ActorID
		actorType := "user"
		if actor == "" {
			actor = "system"
			actorType = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "repo.cvss_policy.changed",
			Resource:   p.Org + "/" + p.Repo,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FUT-023 — ephemeral PR-scoped registries. services/metadata emits
	// pr.namespace.provisioned when a per-PR org namespace is created and
	// pr.namespace.torn_down when it is GC'd (or promoted on merge). Both
	// are system-actor events — the namespace lifecycle is driven by the
	// PR-registry reconciler, not a direct operator action; the initiating
	// PR (provider/source_repo/pr_number) is preserved in metadata.raw so
	// the activity feed can attribute the change to a specific PR. Action
	// mirrors the routing key so the audit vocabulary stays stable.
	case events.RoutingPRNamespaceProvisioned:
		var p events.PRNamespaceProvisionedPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "pr.namespace.provisioned",
			Resource:   p.OrgName,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingPRNamespaceTornDown:
		var p events.PRNamespaceTornDownPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "pr.namespace.torn_down",
			Resource:   p.OrgName,
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

	// --- Phase 6.3: previously unmapped events ---

	// scan.queued is emitted by registry-management when a user manually
	// triggers a scan via the API. Auditing it closes the gap between
	// "scan requested" and "scan.completed" in the activity feed.
	case events.RoutingScanQueued:
		var p events.ScanQueuedPayload
		_ = json.Unmarshal(event.Payload, &p)
		// Build the object from repo+tag when available, fall back to digest.
		object := p.RepositoryName
		if p.TagName != "" {
			object = p.RepositoryName + ":" + p.TagName
		} else if p.ManifestDigest != "" {
			object = p.RepositoryName + "@" + p.ManifestDigest
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "scan.queue",
			Resource:   object,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// webhook.queued is emitted when a delivery record is created. No
	// explicit payload struct exists yet — the raw envelope is stored in
	// metadata for operator inspection. Resource is the raw payload so the
	// audit row is still identifiable without a typed field.
	case events.RoutingWebhookQueued:
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "webhook.queue",
			Resource:   string(event.Payload),
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// webhook.delivered records a successful HTTP delivery. No explicit
	// payload struct exists yet — details (status code, latency) are
	// carried in the raw envelope inside metadata.
	case events.RoutingWebhookDelivered:
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "webhook.deliver",
			Resource:   string(event.Payload),
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// gc.run.started / gc.run.completed — GC events are system-actor
	// events. The event envelope ID serves as the run identifier since
	// GCRunStartedPayload only carries the mode, not a run UUID.
	case events.RoutingGCRunStarted:
		var p events.GCRunStartedPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "gc.start",
			Resource:   event.ID, // event envelope ID is the run identifier
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingGCRunCompleted:
		var p events.GCRunCompletedPayload
		_ = json.Unmarshal(event.Payload, &p)
		// Encode run stats (bytes_reclaimed, manifests_swept, blobs_swept)
		// into a dedicated details JSON blob so operators can inspect them
		// without re-parsing the full raw envelope.
		details, _ := json.Marshal(map[string]any{
			"mode":              p.Mode,
			"manifests_deleted": p.ManifestsDeleted,
			"blobs_deleted":     p.BlobsDeleted,
			"bytes_freed":       p.BytesFreed,
			"dry_run":           p.DryRun,
		})
		gcMeta, _ := json.Marshal(map[string]any{
			"event_id": event.ID,
			"raw":      json.RawMessage(event.Payload),
			"details":  json.RawMessage(details),
		})
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "gc.complete",
			Resource:   event.ID, // event envelope ID matches the gc.start row
			Outcome:    "success",
			Metadata:   gcMeta,
			OccurredAt: now,
		}

	// tenant.deleted is high-impact — audit it with the actor id from the
	// event payload (set by management's publishTenantEvent helper).
	case events.RoutingTenantDeleted:
		var raw struct {
			TenantID string `json:"tenant_id"`
			ActorID  string `json:"actor_id"`
		}
		_ = json.Unmarshal(event.Payload, &raw)
		actor := raw.ActorID
		actorType := "user"
		if actor == "" {
			actor = "system"
			actorType = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "tenant.delete",
			Resource:   raw.TenantID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// tenant.renamed — the payload carries {tenant_id, name, actor_id} from
	// management's publishTenantEvent. "name" is the NEW name after the rename;
	// the old name is not available in the current payload shape.
	case events.RoutingTenantRenamed:
		var raw struct {
			TenantID string `json:"tenant_id"`
			Name     string `json:"name"`
			ActorID  string `json:"actor_id"`
		}
		_ = json.Unmarshal(event.Payload, &raw)
		actor := raw.ActorID
		actorType := "user"
		if actor == "" {
			actor = "system"
			actorType = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "tenant.rename",
			Resource:   raw.TenantID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// tenant.plan_changed — the payload carries {tenant_id, plan, actor_id}.
	// "plan" is the NEW plan after the change.
	case events.RoutingTenantPlanChanged:
		var raw struct {
			TenantID string `json:"tenant_id"`
			Plan     string `json:"plan"`
			ActorID  string `json:"actor_id"`
		}
		_ = json.Unmarshal(event.Payload, &raw)
		actor := raw.ActorID
		actorType := "user"
		if actor == "" {
			actor = "system"
			actorType = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "tenant.plan_change",
			Resource:   raw.TenantID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FUT-081: the tenant.domain.verified case was removed here along with its
	// routing-key constant — custom domains were removed in RM-001, so the
	// event can never be published.

	// store.queued is emitted by registry-proxy when a background blob
	// store fails and needs a retry. Auditing it gives operators a trail
	// of proxy retry events. Resource is built from upstream + image when
	// available, falling back to the raw payload.
	case events.RoutingStoreQueued:
		var p events.StoreQueuedPayload
		_ = json.Unmarshal(event.Payload, &p)
		object := p.UpstreamName
		if p.Image != "" {
			object = p.UpstreamName + "/" + p.Image
		}
		if object == "" {
			object = string(event.Payload)
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "proxy.store.queue",
			Resource:   object,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// cache.populated (FUT-017) — emitted by services/proxy after a
	// successful pull-through cache write. Auditing it gives operators
	// a trail of proxy cache population events.
	case events.RoutingCachePopulated:
		var p events.CachePopulatedPayload
		_ = json.Unmarshal(event.Payload, &p)
		object := p.UpstreamName + "/" + p.Image
		if p.Reference != "" {
			object = object + ":" + p.Reference
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "proxy.cache.populate",
			Resource:   object,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// rbac.role_granted / rbac.role_revoked are SECURITY-CRITICAL — they
	// record every privilege escalation and revocation in the system.
	// Subject is the granter (the human admin who granted the role), object
	// encodes the full assignment in a stable "<grantee>:<role>:<scope_type>:<scope_value>"
	// key so the audit feed can group by assignee without JSON parsing.
	case events.RoutingRBACRoleGranted:
		var p events.RoleGrantedPayload
		_ = json.Unmarshal(event.Payload, &p)
		// Build a stable composite key for the granted assignment so it is
		// identifiable at a glance in the audit feed.
		object := p.UserID + ":" + p.Role + ":" + p.ScopeType + ":" + p.ScopeValue
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    p.GrantedBy,
			ActorType:  "user",
			Action:     "rbac.grant",
			Resource:   object,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// rbac.role_revoked — the payload is leaner than RoleGrantedPayload
	// (carries assignment_id + revoker) because the assignment row is gone
	// by the time the event fires. The assignment_id is the resource.
	case events.RoutingRBACRoleRevoked:
		var p events.RoleRevokedPayload
		_ = json.Unmarshal(event.Payload, &p)
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    p.RevokedBy,
			ActorType:  "user",
			Action:     "rbac.revoke",
			Resource:   p.AssignmentID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FUT-001 — federated workload identity. Three admin events (the
	// trust create/update/delete) plus the two exchange outcomes
	// (exchanged/rejected). The admin events carry the actor_id from
	// the JWT-derived admin; rejection events use "anonymous" because
	// the rejection happened BEFORE we could authenticate the caller.
	case events.RoutingOIDCTrustCreated, events.RoutingOIDCTrustUpdated, events.RoutingOIDCTrustDeleted:
		var p events.OIDCTrustPayload
		_ = json.Unmarshal(event.Payload, &p)
		actor := p.ActorID
		actorType := "user"
		if actor == "" {
			actor = "system"
			actorType = "system"
		}
		// Map the routing key to the audit action verbatim — the
		// activity feed already knows how to group on the "auth."
		// prefix; we don't translate to a shorter verb here because
		// the routing key is the most stable identifier.
		action := event.Type
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     action,
			Resource:   p.TrustID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingWorkloadTokenExchanged:
		var p events.WorkloadTokenPayload
		_ = json.Unmarshal(event.Payload, &p)
		// Exchanged events carry the SA's shadow user id (set by the
		// emitter); fall back to "system" if the publisher didn't set it
		// (defence in depth — should not happen in practice).
		actor := p.ServiceAccountID
		actorType := "service_account"
		if actor == "" {
			actor = "system"
			actorType = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "auth.workload_token.exchanged",
			Resource:   p.TrustID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	case events.RoutingWorkloadTokenRejected:
		var p events.WorkloadTokenPayload
		_ = json.Unmarshal(event.Payload, &p)
		// Rejections are anonymous by definition — the rejection
		// happened before we could authenticate the caller.
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "anonymous",
			ActorType:  "anonymous",
			Action:     "auth.workload_token.rejected",
			Resource:   p.TrustID,
			Outcome:    "failure",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FUT-003 — workspace-wide token policy changes. Emitted by
	// services/auth's TokenPolicyService on every successful
	// PutTokenPolicy. Carries the before/after diff so the activity
	// feed can render "max_ttl_days: 90 → 60" without a callback.
	// The full diff (before + after JSON) lives inside meta.raw so
	// analytics consumers can reconstruct it without a schema change.
	case events.RoutingTokenPolicyChanged:
		var p events.TokenPolicyChangedPayload
		_ = json.Unmarshal(event.Payload, &p)
		actor := p.ActorID
		actorType := "user"
		if actor == "" {
			actor = "system"
			actorType = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "auth.token_policy.changed",
			Resource:   p.TenantID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FUT-003 — key revocation. Fires from manual admin revoke paths and
	// from the FUT-003 idle-revoke background worker. Reason distinguishes
	// "manual" from "idle_revoked" (and reserved "rotation_lapsed" for
	// FUT-004). Actor is "system" for the worker's revocations because the
	// worker has no operator identity.
	case events.RoutingKeyRevoked:
		var p events.KeyRevokedPayload
		_ = json.Unmarshal(event.Payload, &p)
		// Actor is always "system": manual revokes carry the OWNER id (not
		// the acting operator) and the idle-revoke worker has no operator
		// identity at all, so there is no trustworthy actor to stamp. The
		// key id lands in Resource; downstream can join to the users table
		// if it needs the owner. (A future auth change that adds ActorID to
		// the payload can branch on p.Reason == "manual" here.)
		actor := "system"
		actorType := "system"
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  actorType,
			Action:     "auth.key_revoked",
			Resource:   p.KeyID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FUT-004 — access-review "this key is due for review" nudge. Fired
	// per stale key by the weekly worker; the notification bell + the
	// /api-keys/review panel both consume this feed. Actor is "system"
	// because the worker has no operator identity.
	//
	// SEC-070: a malformed payload is dropped (nil → ACK, no insert)
	// instead of stamping a blank-Resource row into audit_events. The
	// same hardening is pending for the older mapEvent cases — tracked
	// as a consumer-wide follow-up in security.md#SEC-070.
	case events.RoutingAccessReviewDue:
		var p events.AccessReviewDuePayload
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			slog.Warn("audit: malformed access_review.due payload — dropping",
				"event_id", event.ID, "err", err)
			return nil
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    "system",
			ActorType:  "system",
			Action:     "auth.access_review.due",
			Resource:   p.KeyID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}

	// FUT-004 — operator explicitly snoozed a stale-key review. Actor
	// comes from the SnoozeAPIKeyReview caller's JWT sub (plumbed
	// through the BFF); Resource is the key id so /activity groups the
	// snooze next to the key's other events.
	case events.RoutingAccessReviewSnoozed:
		var p events.AccessReviewSnoozedPayload
		// SEC-070: drop malformed payloads (see access_review.due above).
		if err := json.Unmarshal(event.Payload, &p); err != nil {
			slog.Warn("audit: malformed access_review.snoozed payload — dropping",
				"event_id", event.ID, "err", err)
			return nil
		}
		actor := p.ActorID
		if actor == "" {
			actor = "system"
		}
		return &repository.AuditEvent{
			TenantID:   tenantID,
			ActorID:    actor,
			ActorType:  "user",
			Action:     "auth.access_review.snoozed",
			Resource:   p.KeyID,
			Outcome:    "success",
			Metadata:   meta,
			OccurredAt: now,
		}
	}

	return nil
}
