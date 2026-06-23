package publisher

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
)

// fakeChannel is a test amqpChannel that lets the test control the timing of
// PublishWithContext returns. Each call signals on publishStarted then blocks
// on publishReturn until the test releases it. This is what gives the
// regression test deterministic control over a race that Go's runtime would
// otherwise hide.
type fakeChannel struct {
	publishStarted chan struct{}
	publishReturn  chan error
	closed         bool
}

func newFakeChannel() *fakeChannel {
	return &fakeChannel{
		publishStarted: make(chan struct{}, 16),
		publishReturn:  make(chan error, 16),
	}
}

func (f *fakeChannel) PublishWithContext(_ context.Context, _, _ string, _, _ bool, _ amqp.Publishing) error {
	f.publishStarted <- struct{}{}
	return <-f.publishReturn
}

func (f *fakeChannel) Close() error {
	f.closed = true
	return nil
}

// TestPublisher_MutexSerialisesConcurrentCallers is the deterministic
// regression test for QA-002 the integration test couldn't be.
//
// The QA-002 bug is that two unsynchronised callers race on the shared
// confirms channel and could read each other's confirmations. Empirically,
// against a real broker this race is masked by Go's runtime FIFO ordering of
// select waiters — the natural goroutine scheduling order matches the
// publish order, so the bug becomes invisible.
//
// To prove the mutex is actually doing its job, we bypass the runtime: a
// fakeChannel signals when PublishWithContext is entered, and the test
// observes that the SECOND caller's PublishWithContext is NOT entered while
// the FIRST caller is still mid-publish. With the mutex stripped, both
// PublishWithContext entries would happen back-to-back; this assertion
// fires and the test fails.
//
// The test then pumps explicit per-call confirmations and verifies each
// caller's reported result correlates with what we told it.
func TestPublisher_MutexSerialisesConcurrentCallers(t *testing.T) {
	fake := newFakeChannel()
	confirms := make(chan amqp.Confirmation, 4)
	p := newWithChannel(fake, confirms, "test-exchange")

	var errA, errB error
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		errA = p.Publish(context.Background(), "ka", makeEvent("A"))
	}()

	// Wait for A to be inside PublishWithContext. With the mutex, A holds it.
	select {
	case <-fake.publishStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("A never entered PublishWithContext")
	}

	go func() {
		defer wg.Done()
		errB = p.Publish(context.Background(), "kb", makeEvent("B"))
	}()

	// B should be blocked on the mutex. If it weren't, we'd see a second
	// publishStarted signal arrive quickly. This is the assertion that fails
	// when the mutex is removed.
	select {
	case <-fake.publishStarted:
		t.Fatal("B entered PublishWithContext while A still held the mutex — mutex broken")
	case <-time.After(100 * time.Millisecond):
	}

	// Release A: PublishWithContext returns, then A reads its confirmation.
	fake.publishReturn <- nil
	confirms <- amqp.Confirmation{DeliveryTag: 1, Ack: true}

	// Now B should be free to enter PublishWithContext.
	select {
	case <-fake.publishStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("B never entered PublishWithContext after A released the mutex")
	}

	// Give B a NACK to read. Each caller must observe its own confirmation.
	fake.publishReturn <- nil
	confirms <- amqp.Confirmation{DeliveryTag: 2, Ack: false}

	wg.Wait()

	if errA != nil {
		t.Errorf("A: got %v, want nil (A was given an ACK)", errA)
	}
	if errB == nil || !strings.Contains(errB.Error(), "broker nacked") {
		t.Errorf("B: got %v, want NACK error (B was given a NACK)", errB)
	}
}

// TestPublisher_DrainStaleConfirm verifies the ctx-cancel branch drains the
// pending confirmation before releasing the mutex, so a subsequent caller
// doesn't read it as their own.
//
// Real-world sequence the drain prevents:
//
//  1. A publishes (tag 1).
//  2. A's ctx is cancelled while waiting for the confirmation.
//  3. The broker's confirm{1, ACK} arrives at p.confirms after A returns the
//     ctx error.
//  4. Without the drain, that confirmation sits in p.confirms.
//  5. B acquires the mutex, publishes (tag 2), and reads confirm{1, ACK} as
//     its own — reporting success for a message that wasn't actually
//     confirmed.
//
// With the drain, A consumes the late ACK inside the ctx.Done() branch
// before releasing the mutex, so B reads its own confirmation.
//
// Important: we cancel A's ctx *after* A is inside the select (not before
// PublishWithContext). Pre-cancelling makes both select branches ready and
// Go's select picks randomly. The realistic bug scenario has the broker
// confirmation arrive *after* the cancellation, which is what we model
// below.
func TestPublisher_DrainStaleConfirm(t *testing.T) {
	fake := newFakeChannel()
	confirms := make(chan amqp.Confirmation, 4)
	p := newWithChannel(fake, confirms, "test-exchange")

	ctxA, cancelA := context.WithCancel(context.Background())

	var errA, errB error
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		errA = p.Publish(ctxA, "ka", makeEvent("A"))
	}()

	// Wait for A to be inside PublishWithContext, then let it return so A
	// enters the wait-for-confirm select. The select observes only ctx-done
	// or confirms; we provide neither yet.
	<-fake.publishStarted
	fake.publishReturn <- nil

	// Brief pause to let A reach the select. There is no observable signal
	// for "this goroutine is now blocked in select"; 50 ms is generous on a
	// developer machine and Go runtime fairness keeps the sleep tight.
	time.Sleep(50 * time.Millisecond)

	// Now cancel A's ctx. A wakes on ctx.Done() and enters drainStaleConfirm.
	cancelA()

	// "Broker" sends the late ACK. With the drain in place, A consumes it
	// before releasing the mutex; without the drain, it would sit in the
	// channel and the next caller (B) would read it as their own.
	confirms <- amqp.Confirmation{DeliveryTag: 1, Ack: true}

	wg.Wait()
	if errA == nil || !strings.Contains(errA.Error(), "context cancelled") {
		t.Fatalf("A: got %v, want ctx-cancel error", errA)
	}

	// Fire B. With drain: confirms is empty, B blocks until we supply its
	// confirmation. Without drain: A's stranded ACK is still in confirms
	// and B reads it before publishStarted even fires for tag 2.
	wg.Add(1)
	go func() {
		defer wg.Done()
		errB = p.Publish(context.Background(), "kb", makeEvent("B"))
	}()

	<-fake.publishStarted
	fake.publishReturn <- nil
	// Give B a NACK so the test can tell the difference between "B read its
	// own confirmation" (= NACK) and "B read A's stranded ACK" (= nil).
	confirms <- amqp.Confirmation{DeliveryTag: 2, Ack: false}
	wg.Wait()

	if errB == nil || !strings.Contains(errB.Error(), "broker nacked") {
		t.Errorf("B: got %v, want NACK error (drain did not consume A's stranded ACK)", errB)
	}
}

func makeEvent(id string) events.Event {
	return events.Event{
		ID:         id,
		Type:       "test.event",
		TenantID:   "00000000-0000-0000-0000-000000000000",
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    json.RawMessage(`{}`),
	}
}
