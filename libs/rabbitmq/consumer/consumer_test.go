//go:build integration

// Integration tests for libs/rabbitmq/consumer covering the REM-015 retry-counter
// fix. The tests boot a RabbitMQ container via testcontainers and exercise the
// real broker semantics — anything less couldn't catch the original bug (the
// requeue loop only happens against a real broker that respects x-dead-letter
// routing and Nack(requeue=true)).
package consumer_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/steveokay/oci-janus/libs/rabbitmq/consumer"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// testFixture wires a fresh publisher + consumer pair against a one-shot
// RabbitMQ container. Each test gets its own queue/routing-key so tests
// can run in parallel without cross-talk.
type testFixture struct {
	url         string
	exchange    string
	queue       string
	dlx         string
	dlxQueue    string
	routingKey  string
	pub         *publisher.Publisher
	dlxConn     *amqp.Connection
	dlxChannel  *amqp.Channel
	dlxBindings <-chan amqp.Delivery
}

// setupFixture creates a fresh broker + bindings and registers cleanup.
// Naming uses t.Name() so a failure points back at the test that produced it.
func setupFixture(t *testing.T) *testFixture {
	t.Helper()
	url := containers.RabbitMQ(t)

	// Unique names per test — t.Name() is already unique across subtests.
	exchange := "test.events." + safeName(t.Name())
	queue := "test.q." + safeName(t.Name())
	dlx := "test.dlx." + safeName(t.Name())
	dlxQueue := "test.dlxq." + safeName(t.Name())
	routingKey := "push.completed"

	pub, err := publisher.New(url, exchange)
	if err != nil {
		t.Fatalf("create publisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	// Wire a separate connection that owns the DLX queue + a delivery channel.
	// We bind the DLX queue here (not via the Consumer) because the Consumer
	// only declares its own queue + DLX exchange — the operator is normally
	// responsible for the DLX *queue* binding.
	dlxConn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial dlx connection: %v", err)
	}
	t.Cleanup(func() { _ = dlxConn.Close() })

	dlxCh, err := dlxConn.Channel()
	if err != nil {
		t.Fatalf("open dlx channel: %v", err)
	}
	t.Cleanup(func() { _ = dlxCh.Close() })

	// Declare the DLX exchange ourselves so it exists before the consumer
	// boots — avoids a race where the test's DLX queue binding fires before
	// the consumer has declared the exchange.
	if err := dlxCh.ExchangeDeclare(dlx, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare dlx exchange: %v", err)
	}
	// DLX queue is a normal classic queue — quorum isn't needed for tests
	// and avoids a 7-day TTL surprise.
	if _, err := dlxCh.QueueDeclare(dlxQueue, true, false, false, false, nil); err != nil {
		t.Fatalf("declare dlx queue: %v", err)
	}
	// Bind to "#" so any routing key on the DLX lands here.
	if err := dlxCh.QueueBind(dlxQueue, "#", dlx, false, nil); err != nil {
		t.Fatalf("bind dlx queue: %v", err)
	}
	dlxDeliveries, err := dlxCh.Consume(dlxQueue, "", true, false, false, false, nil)
	if err != nil {
		t.Fatalf("consume dlx queue: %v", err)
	}

	return &testFixture{
		url:         url,
		exchange:    exchange,
		queue:       queue,
		dlx:         dlx,
		dlxQueue:    dlxQueue,
		routingKey:  routingKey,
		pub:         pub,
		dlxConn:     dlxConn,
		dlxChannel:  dlxCh,
		dlxBindings: dlxDeliveries,
	}
}

// safeName flattens t.Name() into something RabbitMQ accepts as an entity name.
// Slashes (subtests use "Parent/Child") aren't legal in routing keys but are
// fine in queue/exchange names; we still replace them for consistency.
func safeName(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			out = append(out, byte(r))
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}

// makeConsumer creates the consumer under test against the fixture.
func makeConsumer(t *testing.T, f *testFixture, maxRetries int) *consumer.Consumer {
	t.Helper()
	c, err := consumer.New(f.url, consumer.Config{
		Queue:      f.queue,
		RoutingKey: f.routingKey,
		Exchange:   f.exchange,
		DLX:        f.dlx,
		MaxRetries: maxRetries,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// publish sends a single push.completed event so the consumer can pick it up.
// The payload is irrelevant for these tests — we only care about delivery flow.
func publish(t *testing.T, f *testFixture, id string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.pub.Publish(ctx, f.routingKey, events.Event{
		ID:         id,
		Type:       f.routingKey,
		TenantID:   "tenant-test",
		OccurredAt: time.Now().UTC(),
		Version:    "1",
	}); err != nil {
		t.Fatalf("publish %s: %v", id, err)
	}
}

// runConsumer kicks the consumer in a goroutine and returns a cancel func.
// We deliberately use a separate context so the test can let the consumer drain
// and shut it down cleanly via cancel().
func runConsumer(t *testing.T, c *consumer.Consumer, h consumer.Handler) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		// We expect ctx.Err() at shutdown; anything else is a real failure.
		if err := c.Consume(ctx, h); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("consumer exited with non-cancel error: %v", err)
		}
	}()
	return cancel
}

// TestHappyPath_HandlerSucceeds_ACK confirms a successful handler causes the
// message to be ACKed exactly once and never lands on the DLX.
func TestHappyPath_HandlerSucceeds_ACK(t *testing.T) {
	f := setupFixture(t)
	c := makeConsumer(t, f, 3)

	var calls atomic.Int32
	done := make(chan struct{}, 1)
	stop := runConsumer(t, c, func(ctx context.Context, e events.Event) error {
		calls.Add(1)
		select {
		case done <- struct{}{}:
		default:
		}
		return nil
	})
	defer stop()

	publish(t, f, "happy-1")

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("handler never called")
	}

	// Give RabbitMQ a moment to settle; assert nothing landed on the DLX.
	if got := drainDLX(t, f, 1*time.Second); got != 0 {
		t.Fatalf("expected 0 DLX deliveries, got %d", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 handler call, got %d", got)
	}
}

// TestTransientFailure_RecoversOnRetry confirms that a handler which fails
// once and then succeeds drives exactly one NACK(requeue=true) + one ACK,
// and the message does NOT land on the DLX.
func TestTransientFailure_RecoversOnRetry(t *testing.T) {
	f := setupFixture(t)
	c := makeConsumer(t, f, 3)

	var calls atomic.Int32
	succeeded := make(chan struct{}, 1)
	stop := runConsumer(t, c, func(ctx context.Context, e events.Event) error {
		n := calls.Add(1)
		if n == 1 {
			return errors.New("transient")
		}
		select {
		case succeeded <- struct{}{}:
		default:
		}
		return nil
	})
	defer stop()

	publish(t, f, "transient-1")

	select {
	case <-succeeded:
	case <-time.After(15 * time.Second):
		t.Fatalf("handler never succeeded; calls=%d", calls.Load())
	}

	if got := drainDLX(t, f, 1*time.Second); got != 0 {
		t.Fatalf("expected 0 DLX deliveries, got %d", got)
	}
	if got := calls.Load(); got < 2 {
		t.Fatalf("expected >= 2 handler calls, got %d", got)
	}
}

// TestExhaustion_RoutesToDLX confirms that a permanently failing handler
// reaches MaxRetries and then routes the message to the DLX exactly once.
func TestExhaustion_RoutesToDLX(t *testing.T) {
	f := setupFixture(t)
	const maxRetries = 2
	c := makeConsumer(t, f, maxRetries)

	var calls atomic.Int32
	stop := runConsumer(t, c, func(ctx context.Context, e events.Event) error {
		calls.Add(1)
		return errors.New("permanent")
	})
	defer stop()

	publish(t, f, "exhaust-1")

	// Wait for the DLX delivery. The fix should deliver exactly one DLX
	// message after maxRetries+1 handler attempts (attempt 0 + MaxRetries
	// retries = MaxRetries+1 total calls before the broker dead-letters).
	got := waitForDLX(t, f, 1, 30*time.Second)
	if got != 1 {
		t.Fatalf("expected 1 DLX delivery, got %d (handler calls=%d)", got, calls.Load())
	}
	// Handler should have run exactly MaxRetries+1 times — attempt zero plus
	// MaxRetries requeues. Tolerate "at least" because the broker is allowed
	// to interleave but the floor is MaxRetries+1.
	if calls.Load() < int32(maxRetries+1) {
		t.Fatalf("expected at least %d handler calls, got %d", maxRetries+1, calls.Load())
	}
}

// TestRegression_NoRequeueLoop is the regression test for the original
// REM-015 bug: before the fix, a permanently failing handler caused the
// message to bounce between the consumer + queue indefinitely (x-death is
// never incremented by NACK(requeue=true), so deathCount stayed at 0). The
// fix tracks retries in-memory keyed by DeliveryTag, so the message MUST
// reach the DLX within a bounded number of handler calls.
//
// We assert two things:
//  1. The DLX receives the message (i.e. the consumer actually escapes the
//     retry loop).
//  2. The total number of handler invocations is bounded by MaxRetries+1
//     plus a small slack. Before the fix this number was unbounded.
func TestRegression_NoRequeueLoop(t *testing.T) {
	f := setupFixture(t)
	const maxRetries = 3
	c := makeConsumer(t, f, maxRetries)

	var (
		mu    sync.Mutex
		calls int
	)
	stop := runConsumer(t, c, func(ctx context.Context, e events.Event) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return errors.New("never succeeds — regression for REM-015")
	})
	defer stop()

	publish(t, f, "regression-1")

	// The fix must route to DLX within a bounded window. If the bug is back,
	// the DLX stays empty forever and we time out.
	if got := waitForDLX(t, f, 1, 20*time.Second); got != 1 {
		t.Fatalf("REM-015 regression — expected 1 DLX delivery, got %d; the consumer is looping", got)
	}

	// Give the loop a moment to settle, then assert the handler did NOT
	// keep firing. Before the fix this number kept climbing forever; after
	// the fix it should plateau at MaxRetries+1.
	time.Sleep(2 * time.Second)
	mu.Lock()
	total := calls
	mu.Unlock()

	if total > maxRetries+3 { // small slack for in-flight scheduling
		t.Fatalf("REM-015 regression — handler called %d times, expected ~%d (bug: would be unbounded)",
			total, maxRetries+1)
	}
}

// drainDLX collects all currently-available DLX deliveries without blocking
// beyond max. Returns the count drained.
func drainDLX(t *testing.T, f *testFixture, max time.Duration) int {
	t.Helper()
	deadline := time.After(max)
	count := 0
	for {
		select {
		case _, ok := <-f.dlxBindings:
			if !ok {
				return count
			}
			count++
		case <-deadline:
			return count
		}
	}
}

// waitForDLX waits until n deliveries land on the DLX or the timeout fires.
// Returns the count actually received.
func waitForDLX(t *testing.T, f *testFixture, n int, timeout time.Duration) int {
	t.Helper()
	deadline := time.After(timeout)
	count := 0
	for count < n {
		select {
		case _, ok := <-f.dlxBindings:
			if !ok {
				return count
			}
			count++
		case <-deadline:
			return count
		}
	}
	return count
}
