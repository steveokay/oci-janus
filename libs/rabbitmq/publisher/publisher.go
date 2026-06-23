// Package publisher provides a RabbitMQ publisher with confirm mode.
// Confirm mode means the broker ACKs every message before Publish returns,
// so callers know the message was persisted — not just placed in a socket buffer.
package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

// amqpChannel is the subset of *amqp.Channel that Publisher uses. Extracted
// to an interface so unit tests can substitute a fake that gives the test
// deterministic control over publish-call timing — Go's runtime FIFO channel
// fairness masks the AMQP confirmation race against a real broker, so the
// regression test for QA-002 needs to bypass the runtime.
type amqpChannel interface {
	PublishWithContext(ctx context.Context, exchange, key string, mandatory, immediate bool, msg amqp.Publishing) error
	Close() error
}

// Publisher sends events to a RabbitMQ topic exchange using confirm mode.
// Use New to create an instance; call Close when the service shuts down.
//
// Publisher is safe for concurrent use. The mu mutex serialises the publish-
// then-read-confirmation sequence: AMQP delivery tags are per-channel
// sequential and NotifyPublish delivers confirmations in tag order without
// the tag attached on the receiving side, so two unsynchronised callers
// would race and read each other's ACK/NACK — silently breaking confirm
// mode's durability guarantee (QA-002, 2026-06-23).
type Publisher struct {
	mu       sync.Mutex
	conn     *amqp.Connection
	ch       amqpChannel
	confirms chan amqp.Confirmation
	exchange string
}

// New dials the broker, opens a channel, enables confirm mode, and declares
// the exchange as durable topic. The exchange must already exist or be
// declared here; passing an existing exchange is idempotent.
func New(url, exchange string) (*Publisher, error) {
	conn, err := amqp.Dial(url)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", url, err)
	}
	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open channel: %w", err)
	}
	// Declare exchange so the publisher owns its dependency
	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("declare exchange %q: %w", exchange, err)
	}
	// Enable publisher confirms — without this, Publish is fire-and-forget
	if err := ch.Confirm(false); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("enable confirm mode: %w", err)
	}
	// Buffer 1 so the channel never blocks between Publish and the select below
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 1))

	return &Publisher{conn: conn, ch: ch, confirms: confirms, exchange: exchange}, nil
}

// Publish marshals event to JSON and sends it to the exchange with the given
// routing key. It blocks until the broker ACKs the message or ctx is cancelled.
// Messages are marked persistent (survives broker restart) and include
// tenant_id as an AMQP header for consumers that need it without deserialising.
func (p *Publisher) Publish(ctx context.Context, routingKey string, event events.Event) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	// Serialise publish+confirm so concurrent callers can't crosswire each
	// other's ACK/NACK on the shared confirms channel. See type doc.
	p.mu.Lock()
	defer p.mu.Unlock()

	err = p.ch.PublishWithContext(ctx, p.exchange, routingKey,
		false, // mandatory — don't return if no queue bound (routing is the publisher's problem)
		false, // immediate — not used with quorum queues
		amqp.Publishing{
			ContentType:  "application/json",
			DeliveryMode: amqp.Persistent, // survives broker restart
			MessageId:    event.ID,
			Timestamp:    event.OccurredAt,
			Body:         body,
			// tenant_id header lets consumers filter without deserialising the body
			Headers: amqp.Table{"tenant_id": event.TenantID},
		},
	)
	if err != nil {
		return fmt.Errorf("publish to %q/%q: %w", p.exchange, routingKey, err)
	}

	// Wait for the broker to confirm delivery. This is what makes confirm mode
	// meaningful — without this wait the call would be fire-and-forget.
	//
	// On ctx-done or timeout we drain the still-pending confirmation
	// synchronously before releasing the mutex. Without that drain a late
	// confirmation would sit in p.confirms and be misread as the *next*
	// caller's ACK, defeating the lock's whole purpose. Drain is bounded so
	// a wedged broker can't deadlock the publisher — if drain itself times
	// out the channel state is suspect and the broker is unresponsive
	// anyway, so callers see a sticky error until reconnection logic (ARCH-006)
	// resets the channel.
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	select {
	case confirm, ok := <-p.confirms:
		if !ok {
			return fmt.Errorf("confirm channel closed")
		}
		if !confirm.Ack {
			return fmt.Errorf("broker nacked message (delivery tag %d)", confirm.DeliveryTag)
		}
	case <-ctx.Done():
		drainStaleConfirm(p.confirms)
		return fmt.Errorf("context cancelled waiting for broker ack: %w", ctx.Err())
	case <-timer.C:
		drainStaleConfirm(p.confirms)
		return fmt.Errorf("timeout waiting for broker ack")
	}
	return nil
}

// drainStaleConfirm absorbs the late confirmation for an aborted Publish so it
// doesn't get misread as the next caller's ACK. Synchronous and bounded —
// must complete (or give up) before Publish releases p.mu.
func drainStaleConfirm(ch <-chan amqp.Confirmation) {
	select {
	case <-ch:
	case <-time.After(30 * time.Second):
	}
}

// Close shuts down the channel and connection. Always call on process exit.
func (p *Publisher) Close() error {
	if err := p.ch.Close(); err != nil {
		return fmt.Errorf("close channel: %w", err)
	}
	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// newWithChannel constructs a Publisher around a pre-built amqpChannel and
// confirmation receiver. It exists so unit tests can inject a fake channel
// that gives them deterministic control over publish timing. Production code
// must use New, which owns dialling the broker and enabling confirm mode.
func newWithChannel(ch amqpChannel, confirms chan amqp.Confirmation, exchange string) *Publisher {
	return &Publisher{ch: ch, confirms: confirms, exchange: exchange}
}
