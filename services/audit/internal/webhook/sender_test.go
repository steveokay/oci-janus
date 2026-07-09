package webhook

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// fakeSenderRepo is an in-memory senderRepo used to drive the send loop without
// Postgres. It returns its pending slice on the first claim (then none), hands
// back a configurable webhook config, and records the Mark* calls so the test
// can assert on the delivery outcome.
type fakeSenderRepo struct {
	cfg      *repository.NotificationWebhookConfig
	cfgErr   error
	pending  []*repository.WebhookDelivery
	claimErr error
	claimed  bool

	deliveredIDs []uuid.UUID
	failedIDs    []uuid.UUID
	failedLast   struct {
		attempts int
		next     time.Time
		failed   bool
		code     int
		msg      string
	}
}

func (f *fakeSenderRepo) GetNotificationWebhookConfig(_ context.Context, _ uuid.UUID) (*repository.NotificationWebhookConfig, error) {
	return f.cfg, f.cfgErr
}

func (f *fakeSenderRepo) ClaimPendingWebhookDeliveries(_ context.Context, _ time.Time, _ int) ([]*repository.WebhookDelivery, error) {
	if f.claimErr != nil {
		return nil, f.claimErr
	}
	// Only the first claim returns rows so runTick drains once per test.
	if f.claimed {
		return nil, nil
	}
	f.claimed = true
	return f.pending, nil
}

func (f *fakeSenderRepo) MarkWebhookDelivered(_ context.Context, id uuid.UUID, _ int) error {
	f.deliveredIDs = append(f.deliveredIDs, id)
	return nil
}

func (f *fakeSenderRepo) MarkWebhookFailed(_ context.Context, id uuid.UUID, attempts int, next time.Time, failed bool, responseStatus int, errMsg string) error {
	f.failedIDs = append(f.failedIDs, id)
	f.failedLast.attempts = attempts
	f.failedLast.next = next
	f.failedLast.failed = failed
	f.failedLast.code = responseStatus
	f.failedLast.msg = errMsg
	return nil
}

// key32 returns a 32-byte all-zero KEK (a valid AES-256 key for the tests).
func key32() []byte { return make([]byte, 32) }

// enabledConfig returns an enabled webhook config for tenantID whose SecretEnc
// is "shh" sealed under kek.
func enabledConfig(t *testing.T, tenantID uuid.UUID, kek []byte) *repository.NotificationWebhookConfig {
	t.Helper()
	enc, err := aes.Encrypt([]byte("shh"), kek)
	if err != nil {
		t.Fatalf("seal test secret: %v", err)
	}
	return &repository.NotificationWebhookConfig{
		TenantID:  tenantID,
		URL:       "https://x",
		SecretEnc: enc,
		Enabled:   true,
	}
}

// pendingDelivery returns one pending row with the given attempt count.
func pendingDelivery(tenantID uuid.UUID, attempts int) *repository.WebhookDelivery {
	return &repository.WebhookDelivery{
		ID:          uuid.New(),
		TenantID:    tenantID,
		Category:    "digest",
		Subject:     "Weekly digest",
		BodySummary: "3 new pushes",
		Link:        "/repos",
		Status:      "pending",
		Attempts:    attempts,
	}
}

// TestSender_runTick_delivers: an enabled config + one due delivery + a stubbed
// post returning 200 must mark exactly one delivery delivered.
func TestSender_runTick_delivers(t *testing.T) {
	tid := uuid.New()
	kek := key32()
	repo := &fakeSenderRepo{
		cfg:     enabledConfig(t, tid, kek),
		pending: []*repository.WebhookDelivery{pendingDelivery(tid, 0)},
	}
	s := NewSender(repo, kek, "")
	posted := 0
	s.post = func(_ context.Context, _ string, _, _ []byte) (int, error) {
		posted++
		return 200, nil
	}

	s.runTick(context.Background())

	if posted != 1 {
		t.Fatalf("expected post called once, got %d", posted)
	}
	if len(repo.deliveredIDs) != 1 {
		t.Fatalf("expected 1 delivered id, got %d", len(repo.deliveredIDs))
	}
	if len(repo.failedIDs) != 0 {
		t.Fatalf("did not expect any failed ids on a successful send, got %d", len(repo.failedIDs))
	}
}

// TestSender_runTick_idlesWithoutKEK: a nil KEK disables the channel — no claim,
// no send, no mark.
func TestSender_runTick_idlesWithoutKEK(t *testing.T) {
	tid := uuid.New()
	repo := &fakeSenderRepo{
		pending: []*repository.WebhookDelivery{pendingDelivery(tid, 0)},
	}
	s := NewSender(repo, nil, "")
	s.post = func(_ context.Context, _ string, _, _ []byte) (int, error) {
		t.Fatalf("post must not be called when KEK is unset")
		return 0, nil
	}

	s.runTick(context.Background())

	if repo.claimed {
		t.Fatalf("expected no claim when KEK is empty (webhook disabled)")
	}
	if len(repo.deliveredIDs) != 0 || len(repo.failedIDs) != 0 {
		t.Fatalf("expected no send/mark activity when webhook disabled")
	}
}

// TestSender_runTick_disabledConfigAgesToTerminal: a disabled config on a leased
// row must age it toward a terminal state via MarkWebhookFailed (never left
// pending to re-claim forever). At MaxAttempts-1 this tick flips it to 'failed'.
func TestSender_runTick_disabledConfigAgesToTerminal(t *testing.T) {
	tid := uuid.New()
	cfg := enabledConfig(t, tid, key32())
	cfg.Enabled = false // config exists but disabled
	repo := &fakeSenderRepo{
		cfg:     cfg,
		pending: []*repository.WebhookDelivery{pendingDelivery(tid, MaxAttempts-1)},
	}
	s := NewSender(repo, key32(), "")
	s.post = func(_ context.Context, _ string, _, _ []byte) (int, error) {
		t.Fatalf("post must not be called when config is disabled")
		return 0, nil
	}

	s.runTick(context.Background())

	if len(repo.deliveredIDs) != 0 {
		t.Fatalf("did not expect any delivered ids when config is disabled")
	}
	if len(repo.failedIDs) != 1 {
		t.Fatalf("expected exactly 1 failed id, got %d", len(repo.failedIDs))
	}
	if repo.failedLast.attempts != MaxAttempts {
		t.Fatalf("expected attempts=%d, got %d", MaxAttempts, repo.failedLast.attempts)
	}
	if !repo.failedLast.failed {
		t.Fatalf("expected failed=true on the MaxAttempts-th disabled tick")
	}
}
