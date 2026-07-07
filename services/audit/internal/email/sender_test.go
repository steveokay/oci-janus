package email

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// fakeSenderRepo is an in-memory senderRepo used to drive the send loop without
// Postgres. It returns a single pending delivery on the first claim (then none),
// hands back a configurable transport config, and records the Mark* calls so the
// test can assert on the delivery outcome.
type fakeSenderRepo struct {
	cfg       *repository.EmailTransportConfig
	cfgErr    error
	pending   []*repository.EmailDelivery
	claimErr  error
	claimed   bool

	sentID       uuid.UUID
	sentProvider string
	sentCalled   bool

	failedID       uuid.UUID
	failedAttempts int
	failedNext     time.Time
	failedFlag     bool
	failedMsg      string
	failedCalled   bool
}

func (f *fakeSenderRepo) GetEmailTransportConfig(_ context.Context, _ uuid.UUID) (*repository.EmailTransportConfig, error) {
	return f.cfg, f.cfgErr
}

func (f *fakeSenderRepo) ClaimPendingEmailDeliveries(_ context.Context, _ time.Time, _ int) ([]*repository.EmailDelivery, error) {
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

func (f *fakeSenderRepo) MarkEmailSent(_ context.Context, id uuid.UUID, provider string) error {
	f.sentCalled = true
	f.sentID = id
	f.sentProvider = provider
	return nil
}

func (f *fakeSenderRepo) MarkEmailFailed(_ context.Context, id uuid.UUID, attempts int, next time.Time, failed bool, errMsg string) error {
	f.failedCalled = true
	f.failedID = id
	f.failedAttempts = attempts
	f.failedNext = next
	f.failedFlag = failed
	f.failedMsg = errMsg
	return nil
}

// fakeTransport is a Transport whose Send outcome is controlled by sendErr.
type fakeTransport struct {
	name    string
	sendErr error
	sent    []Message
}

func (t *fakeTransport) Send(_ context.Context, msg Message) error {
	t.sent = append(t.sent, msg)
	return t.sendErr
}

func (t *fakeTransport) Name() string { return t.name }

// enabledConfig returns a minimal enabled transport config (no secret columns —
// decryptConfig leaves the secret fields empty when ciphertext is nil).
func enabledConfig(tenantID uuid.UUID) *repository.EmailTransportConfig {
	return &repository.EmailTransportConfig{
		TenantID:    tenantID,
		Provider:    "resend",
		Enabled:     true,
		FromAddress: "noreply@example.com",
		FromName:    "Janus",
	}
}

// pendingDelivery returns one pending row with the given attempt count.
func pendingDelivery(tenantID uuid.UUID, attempts int) *repository.EmailDelivery {
	return &repository.EmailDelivery{
		ID:          uuid.New(),
		TenantID:    tenantID,
		UserID:      uuid.New(),
		ToAddress:   "user@example.com",
		Category:    "digest",
		Subject:     "Weekly digest",
		BodySummary: "3 new pushes",
		Link:        "/repos",
		Status:      "pending",
		Attempts:    attempts,
	}
}

// newTestSender builds a Sender wired to the fake repo + a fixed fake transport,
// bypassing NewTransport so no real network transport is constructed.
func newTestSender(repo senderRepo, tr Transport) *Sender {
	s := NewSender(repo, []byte("test-kek-not-actually-used-here!"), "https://janus.example.com")
	s.buildTransport = func(DecryptedConfig) (Transport, error) { return tr, nil }
	return s
}

func TestSender_runTick_success(t *testing.T) {
	tid := uuid.New()
	repo := &fakeSenderRepo{
		cfg:     enabledConfig(tid),
		pending: []*repository.EmailDelivery{pendingDelivery(tid, 0)},
	}
	tr := &fakeTransport{name: "resend"}
	s := newTestSender(repo, tr)

	s.runTick(context.Background())

	if !repo.sentCalled {
		t.Fatalf("expected MarkEmailSent to be called")
	}
	if repo.sentProvider != "resend" {
		t.Fatalf("expected provider %q, got %q", "resend", repo.sentProvider)
	}
	if repo.failedCalled {
		t.Fatalf("did not expect MarkEmailFailed on a successful send")
	}
	if len(tr.sent) != 1 {
		t.Fatalf("expected transport.Send called once, got %d", len(tr.sent))
	}
	// Sanity-check that the rendered message carried the absolute CTA link.
	if got := tr.sent[0].To; got != "user@example.com" {
		t.Fatalf("expected To user@example.com, got %q", got)
	}
}

func TestSender_runTick_failureRetries(t *testing.T) {
	tid := uuid.New()
	// attempts starts at 0 → this failure bumps it to 1, well under MaxAttempts.
	d := pendingDelivery(tid, 0)
	repo := &fakeSenderRepo{
		cfg:     enabledConfig(tid),
		pending: []*repository.EmailDelivery{d},
	}
	tr := &fakeTransport{name: "resend", sendErr: errors.New("smtp 500")}
	s := newTestSender(repo, tr)

	before := time.Now().UTC()
	s.runTick(context.Background())

	if repo.sentCalled {
		t.Fatalf("did not expect MarkEmailSent on a failed send")
	}
	if !repo.failedCalled {
		t.Fatalf("expected MarkEmailFailed to be called")
	}
	if repo.failedAttempts != 1 {
		t.Fatalf("expected attempts=1, got %d", repo.failedAttempts)
	}
	if repo.failedFlag {
		t.Fatalf("expected failed=false when attempts < MaxAttempts")
	}
	// next ≈ now + Backoff(1); allow a generous window for test execution time.
	wantMin := before.Add(Backoff(1))
	wantMax := time.Now().UTC().Add(Backoff(1) + time.Second)
	if repo.failedNext.Before(wantMin.Add(-time.Second)) || repo.failedNext.After(wantMax) {
		t.Fatalf("next_attempt_at %v out of expected window [%v, %v]", repo.failedNext, wantMin, wantMax)
	}
}

func TestSender_runTick_failureExhaustsBudget(t *testing.T) {
	tid := uuid.New()
	// attempts at MaxAttempts-1 → this failure bumps it to MaxAttempts → failed.
	d := pendingDelivery(tid, MaxAttempts-1)
	repo := &fakeSenderRepo{
		cfg:     enabledConfig(tid),
		pending: []*repository.EmailDelivery{d},
	}
	tr := &fakeTransport{name: "resend", sendErr: errors.New("smtp 500")}
	s := newTestSender(repo, tr)

	s.runTick(context.Background())

	if !repo.failedCalled {
		t.Fatalf("expected MarkEmailFailed to be called")
	}
	if repo.failedAttempts != MaxAttempts {
		t.Fatalf("expected attempts=%d, got %d", MaxAttempts, repo.failedAttempts)
	}
	if !repo.failedFlag {
		t.Fatalf("expected failed=true on the MaxAttempts-th failure")
	}
}

func TestSender_runTick_idlesWithoutKEK(t *testing.T) {
	tid := uuid.New()
	repo := &fakeSenderRepo{
		cfg:     enabledConfig(tid),
		pending: []*repository.EmailDelivery{pendingDelivery(tid, 0)},
	}
	tr := &fakeTransport{name: "resend"}
	s := newTestSender(repo, tr)
	s.kek = nil // simulate email disabled

	s.runTick(context.Background())

	if repo.claimed {
		t.Fatalf("expected no claim when KEK is empty (email disabled)")
	}
	if repo.sentCalled || repo.failedCalled {
		t.Fatalf("expected no send/mark activity when email disabled")
	}
}

// FIX 2: a disabled/misconfigured transport must age the leased row toward a
// terminal state (via fail()), NOT leave it pending — otherwise the row is
// re-claimed every minute forever. This tick bumps attempts (0→1) and, because
// the config is disabled, records the failure without ever sending.
func TestSender_runTick_disabledConfigFailsRow(t *testing.T) {
	tid := uuid.New()
	cfg := enabledConfig(tid)
	cfg.Enabled = false // config exists but disabled
	repo := &fakeSenderRepo{
		cfg:     cfg,
		pending: []*repository.EmailDelivery{pendingDelivery(tid, 0)},
	}
	tr := &fakeTransport{name: "resend"}
	s := newTestSender(repo, tr)

	s.runTick(context.Background())

	if repo.sentCalled {
		t.Fatalf("did not expect MarkEmailSent when config is disabled")
	}
	if !repo.failedCalled {
		t.Fatalf("expected MarkEmailFailed to age the row when config is disabled")
	}
	if repo.failedAttempts != 1 {
		t.Fatalf("expected attempts=1 after one disabled tick, got %d", repo.failedAttempts)
	}
	if len(tr.sent) != 0 {
		t.Fatalf("expected no transport.Send when config disabled")
	}
}

// FIX 2 (terminal): once a disabled row has burned its retry budget the failure
// must flip it to 'failed' so it stops being re-claimed.
func TestSender_runTick_disabledConfigTerminatesAtMaxAttempts(t *testing.T) {
	tid := uuid.New()
	cfg := enabledConfig(tid)
	cfg.Enabled = false
	repo := &fakeSenderRepo{
		cfg:     cfg,
		pending: []*repository.EmailDelivery{pendingDelivery(tid, MaxAttempts-1)},
	}
	tr := &fakeTransport{name: "resend"}
	s := newTestSender(repo, tr)

	s.runTick(context.Background())

	if !repo.failedCalled {
		t.Fatalf("expected MarkEmailFailed to be called")
	}
	if repo.failedAttempts != MaxAttempts {
		t.Fatalf("expected attempts=%d, got %d", MaxAttempts, repo.failedAttempts)
	}
	if !repo.failedFlag {
		t.Fatalf("expected failed=true on the MaxAttempts-th disabled tick")
	}
}

// TestRenderMessage_escapesHTML verifies the security-relevant escape: a crafted
// event summary / category must appear HTML-escaped in the HTML body, never as
// live markup (FIX 4 test gap).
func TestRenderMessage_escapesHTML(t *testing.T) {
	tid := uuid.New()
	d := pendingDelivery(tid, 0)
	d.BodySummary = "<script>alert(1)</script>"
	d.Category = "<b>digest</b>"

	msg := renderMessage("https://janus.example.com", d)

	if strings.Contains(msg.HTMLBody, "<script>") {
		t.Fatalf("HTML body contains raw <script>: %q", msg.HTMLBody)
	}
	if !strings.Contains(msg.HTMLBody, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Fatalf("HTML body missing escaped summary: %q", msg.HTMLBody)
	}
	if strings.Contains(msg.HTMLBody, "<b>digest</b>") {
		t.Fatalf("HTML body contains raw category markup: %q", msg.HTMLBody)
	}
	if !strings.Contains(msg.HTMLBody, "&lt;b&gt;digest&lt;/b&gt;") {
		t.Fatalf("HTML body missing escaped category: %q", msg.HTMLBody)
	}
}

// TestRenderMessage_absoluteFooterLink verifies the footer "Manage preferences"
// link is absolutized against platformHost the same way the CTA is (FIX 4).
func TestRenderMessage_absoluteFooterLink(t *testing.T) {
	tid := uuid.New()
	d := pendingDelivery(tid, 0)

	msg := renderMessage("https://janus.example.com", d)
	if !strings.Contains(msg.HTMLBody, `href="https://janus.example.com/settings/notifications"`) {
		t.Fatalf("expected absolute footer link, got %q", msg.HTMLBody)
	}
	if !strings.Contains(msg.TextBody, "https://janus.example.com/settings/notifications") {
		t.Fatalf("expected absolute footer link in text body, got %q", msg.TextBody)
	}

	// With no host configured the footer falls back to the relative path.
	rel := renderMessage("", d)
	if !strings.Contains(rel.HTMLBody, `href="/settings/notifications"`) {
		t.Fatalf("expected relative footer link when host empty, got %q", rel.HTMLBody)
	}
}
