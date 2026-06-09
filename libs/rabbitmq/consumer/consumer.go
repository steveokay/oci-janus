// Package consumer provides a RabbitMQ consumer with manual ACK and dead-letter routing.
// Manual ACK means a message is only removed from the queue after the handler
// returns nil — if the handler fails or the process crashes, RabbitMQ re-delivers.
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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
		"x-queue-type":           "quorum",                        // durable, replicated
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
// Retry logic: each failed delivery is NACKed with requeue=true until the
// x-death count (set by RabbitMQ) reaches MaxRetries, at which point it is
// NACKed with requeue=false and routes to the DLX.
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
		// Unparseable messages can never succeed — send straight to DLX
		slog.ErrorContext(ctx, "consumer: unparseable message, routing to DLX",
			"queue", c.cfg.Queue,
			"error", err.Error(),
		)
		_ = d.Nack(false, false)
		return
	}

	if err := handler(ctx, event); err != nil {
		retries := deathCount(d)
		slog.WarnContext(ctx, "consumer: handler error",
			"queue", c.cfg.Queue,
			"event_id", event.ID,
			"event_type", event.Type,
			"retry", retries,
			"max_retries", c.cfg.MaxRetries,
			"error", err.Error(),
		)
		if retries >= c.cfg.MaxRetries {
			// Exhausted retries — route to DLX for human inspection
			_ = d.Nack(false, false)
		} else {
			_ = d.Nack(false, true) // requeue for immediate retry
		}
		return
	}

	_ = d.Ack(false)
}

// deathCount reads the x-death count header added by RabbitMQ each time a
// message is dead-lettered and re-routed back. Returns 0 if the header is absent.
func deathCount(d amqp.Delivery) int {
	deaths, ok := d.Headers["x-death"].([]any)
	if !ok {
		return 0
	}
	total := 0
	for _, entry := range deaths {
		if table, ok := entry.(amqp.Table); ok { //nolint:gocritic
			if count, ok := table["count"].(int64); ok {
				total += int(count)
			}
		}
	}
	return total
}

// Close shuts down the channel and connection. Call on service shutdown.
func (c *Consumer) Close() error {
	if err := c.ch.Close(); err != nil {
		return fmt.Errorf("close channel: %w", err)
	}
	return c.conn.Close()
}
