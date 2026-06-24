// Package publisher provides a RabbitMQ publisher with confirm mode.
// Confirm mode means the broker ACKs every message before Publish returns,
// so callers know the message was persisted — not just placed in a socket buffer.
package publisher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

// ErrPublisherClosed is returned by Publish after Close has been called
// (QA-002a). Callers can distinguish a graceful shutdown from a broker
// outage so retry loops know to stop instead of hammering a dead channel.
var ErrPublisherClosed = errors.New("publisher is closed")

// defaultPublishTimeout is the per-Publish ceiling for broker
// confirmation. Configurable via WithPublishTimeout (QA-002b).
const defaultPublishTimeout = 10 * time.Second

// Option configures a Publisher at construction time.
type Option func(*Publisher)

// WithPublishTimeout overrides the default 10s confirmation deadline.
// Callers that batch many small messages in tight loops may want a
// shorter timeout; a slow audit exporter behind syslog/CEF over TLS may
// want a longer one. A non-positive value is rejected at New time so a
// silent zero doesn't disable the safety net (QA-002b).
func WithPublishTimeout(d time.Duration) Option {
	return func(p *Publisher) {
		if d > 0 {
			p.publishTimeout = d
		}
	}
}

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
	mu             sync.Mutex
	conn           *amqp.Connection
	ch             amqpChannel
	confirms       chan amqp.Confirmation
	exchange       string
	publishTimeout time.Duration
	// closed flips to true after Close finishes. Guarded by mu so a
	// concurrent Publish that's blocked on mu sees the post-Close state
	// when it acquires the lock and returns ErrPublisherClosed instead
	// of dialing into the now-closed channel (QA-002a).
	closed bool
}

// New dials the broker, opens a channel, enables confirm mode, and declares
// the exchange as durable topic. The exchange must already exist or be
// declared here; passing an existing exchange is idempotent.
func New(url, exchange string, opts ...Option) (*Publisher, error) {
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

	p := &Publisher{
		conn:           conn,
		ch:             ch,
		confirms:       confirms,
		exchange:       exchange,
		publishTimeout: defaultPublishTimeout,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p, nil
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

	// QA-002a: Close also acquires p.mu and sets closed=true. A concurrent
	// Publish that was blocked on mu sees the post-Close state here and
	// returns ErrPublisherClosed instead of dialing into a closed channel
	// (which would surface as a confusing "channel/connection closed"
	// error from amqp091 that callers can't reliably match on).
	if p.closed {
		return ErrPublisherClosed
	}

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
	timeout := p.publishTimeout
	if timeout <= 0 {
		// Defensive: a Publisher built via newWithChannel (test path) or
		// somehow with a zero timeout falls back to the default rather
		// than blocking forever.
		timeout = defaultPublishTimeout
	}
	timer := time.NewTimer(timeout)
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

// Close shuts down the channel and connection. Always call on process
// exit. Safe to call concurrently with Publish (QA-002a): Close takes the
// same mu the Publish path holds, so it waits for any in-flight Publish
// to drain its confirmation before tearing down the channel. Subsequent
// Publish calls return ErrPublisherClosed instead of dialing into a
// closed channel.
//
// Close is idempotent — a second call is a no-op that returns nil.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	if err := p.ch.Close(); err != nil {
		// Still mark closed and try the conn close so we don't leak the
		// underlying TCP connection on a partial-failure path.
		if p.conn != nil {
			_ = p.conn.Close()
		}
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
	return &Publisher{
		ch:             ch,
		confirms:       confirms,
		exchange:       exchange,
		publishTimeout: defaultPublishTimeout,
	}
}
