// Package exportworker is the Phase 2 async-queue layer for audit-log
// streaming to SIEM (futures.md Tier 1 #4).
//
// Phase 1 shipped the renderer + delivery + per-tenant config + an
// inline fire-and-forget goroutine from the eventconsumer. That was
// honest but lossy on exhausted retries — `dlx_depth` is incremented
// on the config row but the event itself is gone unless you scan the
// audit DB.
//
// Phase 2 promotes dispatch to a real RabbitMQ queue with proper DLX:
//
//   eventconsumer (after INSERT audit_events)
//      │  Publish(AuditExportTask{tenant_id, event}) → audit.export
//      ▼
//   audit.export queue (durable, quorum)        ─┬─ NACK → x-death++ → retry
//      │                                          │
//      │ exportworker.Consumer                     │ ≥ MaxRetries
//      ├─ load config + filter + render            ▼
//      ├─ ship via export.Deliver              dlx.audit-export queue
//      │  success → ACK + TouchSuccess              (parked)
//      │  failure → NACK (broker handles retry)     │
//      ▼                                            │ admin Drain action
//   SIEM                                            ▼
//                                            re-publish onto audit.export
//
// The producer side becomes near-instant — the publish is a synchronous
// RabbitMQ confirm but doesn't wait for SIEM ACK. A downstream outage
// fills `dlx.audit-export`, and the operator drains explicitly from
// the dashboard once they've fixed the receiver.
//
// The package exposes:
//
//   - Publisher.Enqueue(task) — used by eventconsumer's dispatcher.
//   - Consumer.Run(ctx)        — main worker loop, blocks until ctx done.
//   - Drain(ctx)              — admin one-shot that drains dlx.audit-export
//                                back onto audit.export.
//   - DLXDepth(ctx)           — real-time queue depth via the RabbitMQ
//                                Management HTTP API (the Phase 1 row
//                                counter is now treated as a cumulative
//                                "events ever parked" rather than a
//                                live depth).
package exportworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/services/audit/internal/export"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// Queue + exchange names. Kept as package constants so callers (drain
// RPC, FE depth fetcher) can refer to the same strings without typos.
const (
	ExchangeAuditExport    = "audit.export"      // topic exchange
	QueueAuditExport       = "audit.export.tasks" // primary worker queue
	QueueAuditExportDLX    = "audit.export.dlx"   // park-on-exhaustion queue
	ExchangeAuditExportDLX = "dlx.audit-export"   // dead-letter exchange
	RoutingKeyTask         = "task"
	MaxRetries             = 3
)

// AuditExportTask is the message body. We wrap the on-the-wire audit
// event in our own envelope so renderers + filters can run without a
// repository load on every retry — the consumer still hits the DB to
// look up the config (filter + secrets) but the event itself is
// self-contained.
type AuditExportTask struct {
	TenantID string         `json:"tenant_id"`
	Event    export.Event   `json:"event"`
	// EnqueuedAt is set by the producer at publish time; the consumer
	// emits it in observability logs so the operator can spot how
	// long a parked event sat in DLX before drain.
	EnqueuedAt time.Time     `json:"enqueued_at"`
}

// ─── Publisher ─────────────────────────────────────────────────────────────

// Publisher is the producer side wired into eventconsumer. Wraps the
// shared libs/rabbitmq/publisher so we get publisher-confirms + the
// `audit.export` topic exchange declared up front.
type Publisher struct {
	p *publisher.Publisher
}

// NewPublisher dials RabbitMQ and declares the audit.export exchange.
// Returns nil + nil when the URL is empty (Phase 1 / dev fallback —
// the eventconsumer detects this and falls back to inline dispatch).
func NewPublisher(rabbitURL string) (*Publisher, error) {
	if rabbitURL == "" {
		return nil, nil
	}
	p, err := publisher.New(rabbitURL, ExchangeAuditExport)
	if err != nil {
		return nil, fmt.Errorf("audit-export publisher: %w", err)
	}
	return &Publisher{p: p}, nil
}

// Enqueue publishes an AuditExportTask. Synchronous via publisher-
// confirms so the eventconsumer learns about RabbitMQ outages
// immediately (it logs + falls back to the legacy in-process retry
// path so an audit event is never silently lost). Routing key is the
// constant `task` — the queue is bound on `task.#` so we can grow
// per-tenant or per-format routing patterns without re-declaring.
func (pub *Publisher) Enqueue(ctx context.Context, task AuditExportTask) error {
	if pub == nil || pub.p == nil {
		return errors.New("audit-export publisher not configured")
	}
	body, err := json.Marshal(task)
	if err != nil {
		return fmt.Errorf("marshal task: %w", err)
	}
	envelope := events.Event{
		ID:         uuid.NewString(),
		Type:       "audit.export.task",
		TenantID:   task.TenantID,
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    body,
	}
	return pub.p.Publish(ctx, RoutingKeyTask, envelope)
}

// Close releases the underlying AMQP channel + connection.
func (pub *Publisher) Close() error {
	if pub == nil || pub.p == nil {
		return nil
	}
	return pub.p.Close()
}

// ─── Consumer ─────────────────────────────────────────────────────────────

// Consumer drains audit.export.tasks, applies the filter, decrypts
// secrets, renders, and ships. Owns the lifetime of a single goroutine
// that blocks on Consumer.Run(ctx).
//
// We don't use libs/rabbitmq/consumer here because its `x-death`-based
// retry counter only increments on dead-lettering (not on requeue),
// which produces an infinite retry loop in our case. Instead we let
// export.Deliver's in-process 3-attempt retry handle "transient
// network flap" cases, and NACK-without-requeue on the FIRST failure
// after that — the message routes straight to dlx.audit-export for
// operator drain. The trade-off: we don't get RabbitMQ-managed
// delayed retries, but the operator has explicit DLX → drain
// control which is exactly what Phase 2 promised.
type Consumer struct {
	conn       *amqp.Connection
	ch         *amqp.Channel
	repo       *repository.Repository
	secretsKey []byte
}

// NewConsumer connects to the broker + declares the audit.export
// queue with the dlx.audit-export dead-letter binding. Returns nil
// when rabbitURL is empty so the audit server's Run() can boot without
// the worker — inline-dispatch fallback rides on top.
//
// Critical: also declares + binds the audit.export.dlx queue against
// the dlx.audit-export exchange. Without this binding, messages that
// NACK out of audit.export.tasks (after MaxRetries) route to the DLX
// exchange but no queue picks them up → they're dropped silently.
// Doing the bind here at boot (not lazily in Drain) means the DLX
// is always reachable even before the operator triggers a drain.
func NewConsumer(rabbitURL string, repo *repository.Repository, secretsKey []byte) (*Consumer, error) {
	if rabbitURL == "" || repo == nil {
		return nil, nil
	}
	conn, err := amqp.Dial(rabbitURL)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("channel: %w", err)
	}
	// Declare the main exchange + DLX exchange + DLX queue +
	// audit.export.tasks queue with dead-letter routing.
	if err := ch.ExchangeDeclare(ExchangeAuditExport, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare main exchange: %w", err)
	}
	if err := ch.ExchangeDeclare(ExchangeAuditExportDLX, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare dlx exchange: %w", err)
	}
	if _, err := ch.QueueDeclare(QueueAuditExportDLX, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum",
	}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare dlx queue: %w", err)
	}
	if err := ch.QueueBind(QueueAuditExportDLX, "#", ExchangeAuditExportDLX, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bind dlx queue: %w", err)
	}
	if _, err := ch.QueueDeclare(QueueAuditExport, true, false, false, false, amqp.Table{
		"x-dead-letter-exchange": ExchangeAuditExportDLX,
		"x-queue-type":           "quorum",
	}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare tasks queue: %w", err)
	}
	if err := ch.QueueBind(QueueAuditExport, RoutingKeyTask+".#", ExchangeAuditExport, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bind tasks queue: %w", err)
	}
	// Bind on plain "task" too (publisher uses that exact key).
	if err := ch.QueueBind(QueueAuditExport, RoutingKeyTask, ExchangeAuditExport, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bind tasks queue (plain): %w", err)
	}
	return &Consumer{conn: conn, ch: ch, repo: repo, secretsKey: secretsKey}, nil
}

// declareDLX makes sure the audit.export.dlx queue exists and is
// bound to the dlx.audit-export topic exchange on `#`. Called once
// per server boot. Idempotent — uses RabbitMQ's "declare with same
// args = no-op" semantic.
func declareDLX(rabbitURL string) error {
	conn, err := amqp.Dial(rabbitURL)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("channel: %w", err)
	}
	defer ch.Close()
	if err := ch.ExchangeDeclare(ExchangeAuditExportDLX, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("declare dlx exchange: %w", err)
	}
	if _, err := ch.QueueDeclare(QueueAuditExportDLX, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum",
	}); err != nil {
		return fmt.Errorf("declare dlx queue: %w", err)
	}
	if err := ch.QueueBind(QueueAuditExportDLX, "#", ExchangeAuditExportDLX, false, nil); err != nil {
		return fmt.Errorf("bind dlx queue: %w", err)
	}
	return nil
}

// Run blocks on the consume loop until ctx is cancelled. Closes the
// AMQP channel + connection on exit.
func (c *Consumer) Run(ctx context.Context) error {
	if c == nil || c.ch == nil {
		return nil
	}
	defer func() {
		_ = c.ch.Close()
		_ = c.conn.Close()
	}()
	deliveries, err := c.ch.ConsumeWithContext(ctx, QueueAuditExport,
		"", false, false, false, false, nil,
	)
	if err != nil {
		return fmt.Errorf("consume: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("delivery channel closed")
			}
			c.handle(ctx, d)
		}
	}
}

// handle is the per-message worker. export.Deliver runs the 3-attempt
// in-process retry internally; if it returns an error we route the
// message straight to DLX (NACK with requeue=false). Permanent errors
// (unparseable payload, bad tenant_id, decrypt failure) ACK so they
// don't loop or fill DLX with un-recoverable garbage.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery) {
	var env events.Event
	if err := json.Unmarshal(d.Body, &env); err != nil {
		slog.WarnContext(ctx, "audit-export: unparseable envelope", "err", err)
		_ = d.Ack(false)
		return
	}
	if env.Type != "audit.export.task" {
		slog.WarnContext(ctx, "audit-export: unexpected envelope type", "type", env.Type)
		_ = d.Ack(false)
		return
	}
	var task AuditExportTask
	if err := json.Unmarshal(env.Payload, &task); err != nil {
		slog.WarnContext(ctx, "audit-export: unparseable task payload", "err", err)
		_ = d.Ack(false)
		return
	}
	tenantID, err := uuid.Parse(task.TenantID)
	if err != nil {
		_ = d.Ack(false)
		return
	}

	cfg, err := c.repo.GetAuditExportConfig(ctx, tenantID)
	if err != nil {
		if errors.Is(err, repository.ErrExportConfigNotFound) {
			_ = d.Ack(false) // config was deleted — drop
			return
		}
		// Transient DB error → NACK with requeue so we retry on the
		// same connection. This is the ONE place we requeue rather
		// than DLX — DB blips are usually transient.
		_ = d.Nack(false, true)
		return
	}
	if !cfg.Enabled || !matchesFilter(cfg.EventFilters, task.Event.Action) {
		_ = d.Ack(false)
		return
	}

	hmacSecret, err := openSecret(c.secretsKey, cfg.HMACSecret)
	if err != nil {
		_ = c.repo.TouchAuditExportFailure(ctx, tenantID, "decrypt hmac_secret: "+truncErr(err))
		_ = d.Ack(false) // permanent
		return
	}
	bearerToken, err := openSecret(c.secretsKey, cfg.BearerToken)
	if err != nil {
		_ = c.repo.TouchAuditExportFailure(ctx, tenantID, "decrypt bearer_token: "+truncErr(err))
		_ = d.Ack(false)
		return
	}

	exp := export.Config{
		TenantID:    cfg.TenantID.String(),
		Format:      cfg.Format,
		TargetURL:   cfg.TargetURL,
		HMACSecret:  hmacSecret,
		BearerToken: bearerToken,
	}
	if _, err := export.Deliver(ctx, exp, task.Event); err != nil {
		msg := truncErr(err)
		_ = c.repo.TouchAuditExportFailure(ctx, tenantID, msg)
		_ = c.repo.IncrementAuditExportDLX(ctx, tenantID, 1)
		// NACK without requeue → routes to dlx.audit-export
		// (declared above with x-dead-letter-exchange binding).
		slog.WarnContext(ctx, "audit-export: routing to DLX after retry exhaustion",
			"tenant_id", tenantID, "format", cfg.Format, "err", msg)
		_ = d.Nack(false, false)
		return
	}
	_ = c.repo.TouchAuditExportSuccess(ctx, tenantID)
	_ = d.Ack(false)
}

// truncErr clips the error string to fit the last_error column.
func truncErr(err error) string {
	s := err.Error()
	if len(s) > 512 {
		s = s[:512]
	}
	return s
}

// ─── Drain (admin) ─────────────────────────────────────────────────────────

// Drain consumes every message currently sitting in dlx.audit-export
// for the given tenant and re-publishes onto audit.export. Used by
// the dashboard's `Drain` admin button after the operator confirms
// their SIEM is reachable again.
//
// Implementation: we open a one-shot consumer on the DLX queue, drain
// each delivery, filter by tenant (the DLX queue is shared so we have
// to selectively re-publish), and ACK regardless. Messages from other
// tenants get re-published back to the DLX as-is so we don't strip
// them from other tenants' queues.
//
// Returns the number of messages re-published for this tenant, plus
// any error from the underlying AMQP operations.
func Drain(ctx context.Context, rabbitURL string, tenantID uuid.UUID) (int, error) {
	if rabbitURL == "" {
		return 0, errors.New("rabbit URL not configured")
	}
	conn, err := amqp.Dial(rabbitURL)
	if err != nil {
		return 0, fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	ch, err := conn.Channel()
	if err != nil {
		return 0, fmt.Errorf("channel: %w", err)
	}
	defer ch.Close()

	// Declare the DLX queue with the SAME args the consumer uses —
	// idempotent. Bound to the dlx.audit-export exchange below.
	if err := ch.ExchangeDeclare(ExchangeAuditExportDLX, "topic", true, false, false, false, nil); err != nil {
		return 0, fmt.Errorf("declare dlx exchange: %w", err)
	}
	if _, err := ch.QueueDeclare(QueueAuditExportDLX, true, false, false, false, amqp.Table{
		"x-queue-type": "quorum",
	}); err != nil {
		return 0, fmt.Errorf("declare dlx queue: %w", err)
	}
	if err := ch.QueueBind(QueueAuditExportDLX, "#", ExchangeAuditExportDLX, false, nil); err != nil {
		return 0, fmt.Errorf("bind dlx queue: %w", err)
	}

	// Drain via Get rather than Consume so the loop terminates as
	// soon as the queue is empty. Bounded loop with a hard cap to
	// avoid runaway drain when an operator triggers it on a queue
	// with millions of parked messages.
	const maxDrain = 10_000
	republished := 0
	for i := 0; i < maxDrain; i++ {
		msg, ok, err := ch.Get(QueueAuditExportDLX, false)
		if err != nil {
			return republished, fmt.Errorf("get dlx: %w", err)
		}
		if !ok {
			break // queue empty
		}
		// Tenant filter — only re-publish messages that belong to
		// the calling tenant. Other tenants' messages get NACK-with-
		// requeue so they stay in DLX for their own drain action.
		matches := false
		var env events.Event
		if jerr := json.Unmarshal(msg.Body, &env); jerr == nil {
			if env.TenantID == tenantID.String() {
				matches = true
			}
		}
		if !matches {
			_ = msg.Nack(false, true) // requeue=true → back to DLX
			continue
		}
		// Re-publish onto the main exchange. Use the same routing
		// key the original task used (`task` — the const).
		if err := ch.PublishWithContext(ctx, ExchangeAuditExport, RoutingKeyTask, false, false, amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent,
			Body:         msg.Body,
		}); err != nil {
			// Republish failed — requeue so we don't drop the event.
			_ = msg.Nack(false, true)
			return republished, fmt.Errorf("republish: %w", err)
		}
		_ = msg.Ack(false)
		republished++
	}
	return republished, nil
}

// ─── DLX depth (real-time) ────────────────────────────────────────────────

// MgmtClient queries the RabbitMQ Management HTTP API for live
// queue-depth observability. The Mgmt API ships with RabbitMQ's
// `rabbitmq_management` plugin (already enabled on the dev compose
// stack via :15672). In prod, the plugin runs on the broker; the
// credentials come from the same env vars the AMQP URL uses.
type MgmtClient struct {
	baseURL string
	user    string
	pass    string
	hc      *http.Client
}

// NewMgmtClient parses the RabbitMQ AMQP URL + a management port to
// build a Management API base URL. RabbitMQ's default is :15672 for
// Mgmt; we accept an override env var so a customer running on a
// non-default port can plug it in without changing code.
func NewMgmtClient(rabbitURL, mgmtURL string) (*MgmtClient, error) {
	if rabbitURL == "" {
		return nil, errors.New("rabbit URL required")
	}
	u, err := url.Parse(rabbitURL)
	if err != nil {
		return nil, fmt.Errorf("parse rabbit url: %w", err)
	}
	if mgmtURL == "" {
		// Default — same host, port 15672, http://. Production
		// deployments behind TLS should set the override.
		host := u.Hostname()
		mgmtURL = "http://" + host + ":15672"
	}
	mc := &MgmtClient{
		baseURL: strings.TrimRight(mgmtURL, "/"),
		hc:      &http.Client{Timeout: 3 * time.Second},
	}
	if u.User != nil {
		mc.user = u.User.Username()
		mc.pass, _ = u.User.Password()
	}
	return mc, nil
}

// QueueDepth returns the message count on the given queue. Default
// vhost (`/`). Returns 0 + nil when the queue doesn't exist yet (so a
// freshly-booted stack with no DLX messages renders 0 rather than
// surfacing an error to the operator).
func (mc *MgmtClient) QueueDepth(ctx context.Context, queueName string) (int, error) {
	if mc == nil {
		return 0, nil
	}
	endpoint := fmt.Sprintf("%s/api/queues/%%2F/%s", mc.baseURL, url.PathEscape(queueName))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	if mc.user != "" {
		req.SetBasicAuth(mc.user, mc.pass)
	}
	resp, err := mc.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("mgmt api: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return 0, nil // queue not yet declared
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("mgmt api: status %d", resp.StatusCode)
	}
	var body struct {
		Messages int `json:"messages"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode mgmt response: %w", err)
	}
	return body.Messages, nil
}
