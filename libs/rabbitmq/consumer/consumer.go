// Package consumer provides a RabbitMQ consumer with manual ACK and dead-letter routing.
// Manual ACK means a message is only removed from the queue after the handler
// returns nil — if the handler fails or the process crashes, RabbitMQ re-delivers.
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

// Handler is the function signature for event processors. Return nil to ACK,
// return an error to NACK (which triggers retry or dead-letter routing).
type Handler func(ctx context.Context, event events.Event) error

// Config describes a queue binding. Every consumer declares its own queue so
// services are decoupled — adding a new consumer does not require changing publishers.
type Config struct {
	// Queue is the durable queue name this consumer owns (e.g. "scanner.push.completed")
	Queue string
	// RoutingKey is the pattern to bind to (e.g. "push.completed", "scan.*")
	RoutingKey string
	// Exchange to bind against (default: events.ExchangeEvents)
	Exchange string
	// DLX is the dead-letter exchange for undeliverable messages (default: events.ExchangeDLX)
	DLX string
	// MaxRetries: after this many NACK-without-requeue, the message routes to DLX.
	// 0 means "always requeue" which risks infinite loops on permanent errors.
	MaxRetries int
}

// Consumer wraps a single AMQP channel in a long-lived consumer loop.
type Consumer struct {
	conn *amqp.Connection
	ch   *amqp.Channel
	cfg  Config

	// retries tracks per-DeliveryTag attempt counts so that an in-memory
	// counter survives across NACK(requeue=true) cycles on the same channel.
	//
	// Why not x-death? RabbitMQ only adds the x-death header when a message
	// is actually dead-lettered (NACK with requeue=false on a queue that has
	// x-dead-letter-exchange set). NACK(requeue=true) puts the message back on
	// the same queue without touching x-death, so a header-based counter would
	// stay at 0 forever and the message would loop between consumer + queue
	// indefinitely on any persistent handler error (REM-015).
	//
	// DeliveryTag is a uint64 monotonic counter scoped to the AMQP channel; it
	// is stable across redeliveries of the *same* message on the *same*
	// channel, and that is the only scope we need — if the channel drops, the
	// new channel gets fresh tags and the counter is irrelevant (RabbitMQ
	// itself redelivers the message, which we want to count as attempt 0).
	//
	// Memory is bounded by prefetch_count × MaxRetries entries (a few KB in
	// the worst case), and entries are cleared on every terminal state (ACK
	// success, NACK-to-DLX, or unparseable payload).
	retries sync.Map // key: uint64 DeliveryTag, value: int attempt count
}

// New connects to the broker, declares the queue with DLX routing, and binds
// the routing key against the exchange. The caller owns the returned Consumer
// and must call Close when done.
func New(url string, cfg Config) (*Consumer, error) {
	if cfg.Exchange == "" {
		cfg.Exchange = events.ExchangeEvents
	}
	if cfg.DLX == "" {
		cfg.DLX = events.ExchangeDLX
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}

	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}

	// Prefetch 1: process one message at a time so a slow handler doesn't accumulate
	// unacknowledged messages. Increase for embarrassingly parallel consumers.
	if err := ch.Qos(1, 0, false); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("set qos: %w", err)
	}

	// Declare the exchange so it exists even if no publisher has run yet
	if err := ch.ExchangeDeclare(cfg.Exchange, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare exchange: %w", err)
	}
	// DLX exchange receives messages that exhaust their retry budget
	if err := ch.ExchangeDeclare(cfg.DLX, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare dlx: %w", err)
	}

	// Queue declared with dead-letter routing and a 7-day message TTL
	queueArgs := amqp.Table{
		"x-dead-letter-exchange": cfg.DLX,
		"x-message-ttl":          int64(7 * 24 * 60 * 60 * 1000), // 7 days in ms
		"x-queue-type":           "quorum",                       // durable, replicated
	}
	if _, err := ch.QueueDeclare(cfg.Queue, true, false, false, false, queueArgs); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare queue %q: %w", cfg.Queue, err)
	}
	if err := ch.QueueBind(cfg.Queue, cfg.RoutingKey, cfg.Exchange, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("bind queue: %w", err)
	}

	return &Consumer{conn: conn, ch: ch, cfg: cfg}, nil
}

// Consume starts the consumer loop. It blocks until ctx is cancelled or the
// AMQP channel closes. The handler is called synchronously for each delivery.
//
// Retry logic (REM-015): each Delivery carries a monotonic DeliveryTag.
// We track per-tag attempt counts in c.retries. On handler failure we NACK
// with requeue=true (incrementing the in-memory counter) until the counter
// reaches MaxRetries, at which point we NACK with requeue=false and the
// broker routes the message to the DLX. Counter entries are cleared on
// every terminal state to keep memory bounded.
func (c *Consumer) Consume(ctx context.Context, handler Handler) error {
	deliveries, err := c.ch.ConsumeWithContext(ctx, c.cfg.Queue,
		"",    // consumer tag — auto-generated
		false, // autoAck=false — we ACK manually after successful processing
		false, false, false, nil,
	)
	if err != nil {
		return fmt.Errorf("start consuming %q: %w", c.cfg.Queue, err)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed")
			}
			c.handle(ctx, d, handler)
		}
	}
}

// handle processes a single delivery and ACK/NACKs it.
func (c *Consumer) handle(ctx context.Context, d amqp.Delivery, handler Handler) {
	var event events.Event
	if err := json.Unmarshal(d.Body, &event); err != nil {
		// Unparseable messages can never succeed — send straight to DLX.
		// No counter entry exists yet for this tag (we only ever insert one
		// after a *handler* failure), so nothing to clean up.
		slog.ErrorContext(ctx, "consumer: unparseable message, routing to DLX",
			"queue", c.cfg.Queue,
			"error", err.Error(),
		)
		_ = d.Nack(false, false)
		return
	}

	if err := handler(ctx, event); err != nil {
		// Read current attempt count; first failure starts at 0.
		// Note: d.Redelivered is true on the second-and-later delivery of the
		// same message. We don't rely on it to *count* (the broker doesn't
		// expose a tag-level redelivery count), but the field is useful as a
		// sanity-check in logs and as a hint when the counter map is empty
		// (e.g. after a process restart that drained the channel — the broker
		// re-delivers but our counter resets to 0, which is the correct
		// behaviour: a brand-new process gets a fresh retry budget).
		var retries int
		if v, ok := c.retries.Load(d.DeliveryTag); ok {
			retries = v.(int)
		}

		slog.WarnContext(ctx, "consumer: handler error",
			"queue", c.cfg.Queue,
			"event_id", event.ID,
			"event_type", event.Type,
			"retry", retries,
			"max_retries", c.cfg.MaxRetries,
			"redelivered", d.Redelivered,
			"error", err.Error(),
		)

		if retries >= c.cfg.MaxRetries {
			// Exhausted retries — NACK without requeue routes via x-dead-letter-exchange to the DLX.
			c.retries.Delete(d.DeliveryTag)
			_ = d.Nack(false, false)
			return
		}

		// Increment the attempt count *before* the NACK so a fast redelivery
		// observes the new value. Storing retries+1 is correct because we
		// just consumed one attempt; the next handler call will be attempt
		// number retries+1.
		c.retries.Store(d.DeliveryTag, retries+1)
		_ = d.Nack(false, true) // requeue for immediate retry
		return
	}

	// Success — drop any counter entry from prior failed attempts and ACK.
	c.retries.Delete(d.DeliveryTag)
	_ = d.Ack(false)
}

// Close shuts down the channel and connection. Call on service shutdown.
func (c *Consumer) Close() error {
	if err := c.ch.Close(); err != nil {
		return fmt.Errorf("close channel: %w", err)
	}
	return c.conn.Close()
}
