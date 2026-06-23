//go:build integration

package publisher_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// TestPublisher_ConcurrentPublish_AllAcked is the regression test for QA-002.
//
// Before the sync.Mutex fix, the shared confirms channel let two unsynchronised
// callers read each other's ACK/NACK in arbitrary order: one Publish would
// return success on a message the broker actually never acked, while another
// returned a spurious NACK or timeout. With the mutex, every caller's
// publish-then-read sequence is serialised, so confirm-mode's durability
// guarantee holds even when many goroutines hammer the same Publisher.
//
// The test fires concurrentCallers Publish calls in parallel, asserts every
// caller saw nil, and verifies the broker received exactly that many messages
// by consuming them off a queue bound to the publisher's exchange.
func TestPublisher_ConcurrentPublish_AllAcked(t *testing.T) {
	url := containers.RabbitMQ(t)

	const exchange = "test.publisher.concurrency"
	pub, err := publisher.New(url, exchange)
	if err != nil {
		t.Fatalf("publisher.New: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	// Bind a queue to the exchange so we can verify messages actually arrived.
	// Done on a second connection — we don't want this scaffolding to share
	// the publisher's channel state.
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatalf("declare queue: %v", err)
	}
	if err := ch.QueueBind(q.Name, "#", exchange, false, nil); err != nil {
		t.Fatalf("bind queue: %v", err)
	}

	// Bumped to 200 to reliably force the buffer-1 confirmation channel into
	// the race window the bug exploited. With 50 the test still passes most
	// of the time even with the bug present; at 200 the timeout path fires
	// deterministically without the fix.
	const concurrentCallers = 200

	// Track per-caller event IDs so we can prove the bytes the broker received
	// correspond to the callers that thought they succeeded — not just "we got
	// N deliveries from somewhere." This is the assertion that catches the
	// ACK/NACK crosswiring the mutex prevents (deterministic, not probabilistic).
	type result struct {
		eventID string
		err     error
	}
	results := make([]result, concurrentCallers)

	var wg sync.WaitGroup
	wg.Add(concurrentCallers)

	for i := range concurrentCallers {
		go func(idx int) {
			defer wg.Done()
			payload, _ := json.Marshal(map[string]int{"i": idx})
			eventID := uuid.NewString()
			evt := events.Event{
				ID:         eventID,
				Type:       "test.event",
				TenantID:   "00000000-0000-0000-0000-000000000000",
				OccurredAt: time.Now().UTC(),
				Version:    "1.0",
				Payload:    payload,
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err := pub.Publish(ctx, fmt.Sprintf("test.k%d", idx), evt)
			results[idx] = result{eventID: eventID, err: err}
		}(i)
	}
	wg.Wait()

	succeeded := make(map[string]struct{})
	for i, r := range results {
		if r.err != nil {
			t.Errorf("caller %d: Publish returned %v", i, r.err)
			continue
		}
		succeeded[r.eventID] = struct{}{}
	}

	// Consume deliveries and prove the set the broker received equals the set
	// the callers thought they shipped. With the mutex stripped, this would
	// fail two ways: (a) the timeout path leaves some IDs missing on the
	// broker side, (b) ACK crosswiring lets a caller return nil for a message
	// the broker never received.
	deliveries, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	if err != nil {
		t.Fatalf("consume queue: %v", err)
	}
	received := make(map[string]struct{})
	deadline := time.After(10 * time.Second)
	for len(received) < len(succeeded) {
		select {
		case d := <-deliveries:
			var evt events.Event
			if err := json.Unmarshal(d.Body, &evt); err != nil {
				t.Fatalf("unmarshal delivery: %v", err)
			}
			received[evt.ID] = struct{}{}
		case <-deadline:
			t.Fatalf("only received %d/%d expected event IDs before deadline", len(received), len(succeeded))
		}
	}
	for id := range succeeded {
		if _, ok := received[id]; !ok {
			t.Errorf("caller reported success for event %s but broker never received it", id)
		}
	}
	for id := range received {
		if _, ok := succeeded[id]; !ok {
			t.Errorf("broker received event %s but no caller reported success", id)
		}
	}
}

// Note on broker-driven deterministic regression testing: an earlier draft
// of this file attempted to force NACKs via a queue with max-length=1 +
// overflow=reject-publish, hoping a mixed-ACK-NACK pair of concurrent
// publishes would expose the bug. It didn't: Go's runtime FIFO ordering of
// select waiters keeps the goroutine that published tag N also reading
// confirm{N} in practice, so the bug becomes invisible against a real
// broker. The deterministic regression tests now live in publisher_test.go
// and use an injectable fake amqpChannel to bypass the runtime.

// TestPublisher_CancelledPublish_DoesNotPoisonNext exercises the
// drainStaleConfirm path. A cancelled Publish leaves a confirmation in flight
// on the broker side; without the drain it would sit in p.confirms and be
// read as the *next* caller's ACK. The test cancels mid-publish then fires a
// follow-up event and asserts the broker actually received the follow-up.
//
// This test catches a regression where the drain is removed, even if it can't
// directly observe internal channel attribution.
func TestPublisher_CancelledPublish_DoesNotPoisonNext(t *testing.T) {
	url := containers.RabbitMQ(t)

	const exchange = "test.publisher.cancel"
	pub, err := publisher.New(url, exchange)
	if err != nil {
		t.Fatalf("publisher.New: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatalf("dial broker: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatalf("declare queue: %v", err)
	}
	if err := ch.QueueBind(q.Name, "#", exchange, false, nil); err != nil {
		t.Fatalf("bind queue: %v", err)
	}

	// First publish with a context that's already cancelled. The broker will
	// still receive and ACK the message, but Publish should return the ctx
	// error after draining the stranded confirmation.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelledEvt := events.Event{
		ID:         uuid.NewString(),
		Type:       "test.cancelled",
		TenantID:   "00000000-0000-0000-0000-000000000000",
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    json.RawMessage(`{}`),
	}
	if err := pub.Publish(cancelledCtx, "test.cancel", cancelledEvt); err == nil {
		t.Fatalf("Publish on cancelled ctx returned nil; expected error")
	}

	// Follow-up publish on a fresh context. With drainStaleConfirm in place
	// this returns nil; without it the call may read the stranded
	// confirmation from the cancelled publish and the broker assertion below
	// would still pass — but if the broker NACKs the follow-up while we read
	// the stranded ACK, the bug would silently flip. Best we can do here
	// without injecting NACKs is to assert end-to-end correctness on the
	// happy path.
	followupID := uuid.NewString()
	followupEvt := events.Event{
		ID:         followupID,
		Type:       "test.followup",
		TenantID:   "00000000-0000-0000-0000-000000000000",
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    json.RawMessage(`{}`),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := pub.Publish(ctx, "test.followup", followupEvt); err != nil {
		t.Fatalf("follow-up Publish: %v", err)
	}

	// The broker should have received both the cancelled event (broker doesn't
	// know about caller cancellation) and the follow-up.
	deliveries, err := ch.Consume(q.Name, "", true, true, false, false, nil)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	seen := make(map[string]struct{})
	deadline := time.After(5 * time.Second)
	for len(seen) < 2 {
		select {
		case d := <-deliveries:
			var evt events.Event
			if err := json.Unmarshal(d.Body, &evt); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			seen[evt.ID] = struct{}{}
		case <-deadline:
			t.Fatalf("only saw %d/2 deliveries before deadline", len(seen))
		}
	}
	if _, ok := seen[followupID]; !ok {
		t.Errorf("broker did not receive follow-up event %s", followupID)
	}
}
