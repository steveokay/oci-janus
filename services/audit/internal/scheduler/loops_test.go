package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/audit/internal/repository"
)

// FUT-019 Phase 3 — dispatcher email fan-out tests.
//
// These exercise the real dispatchOne against a fake repo (schedulerRepo is an
// interface for exactly this reason) + a fake EmailRecipientResolver, so the
// email enqueue path is verified without a live Postgres.

// fakeRepo is a minimal schedulerRepo that records the email fan-out calls the
// dispatcher makes. Only the methods dispatchOne touches are meaningfully
// implemented; the rest satisfy the interface with harmless zero returns.
type fakeRepo struct {
	// recipients is returned verbatim by ListEmailRecipients.
	recipients []uuid.UUID
	// listErr, if set, makes ListEmailRecipients fail.
	listErr error
	// insertErr, if set, makes the bell Insert fail.
	insertErr error
	// enqueued records every EnqueueEmailDelivery call, in call order.
	enqueued []repository.EmailDelivery
	// insertCount counts bell Insert calls.
	insertCount int
	// webhookCfg is returned verbatim by GetNotificationWebhookConfig.
	webhookCfg *repository.NotificationWebhookConfig
	// webhookEnqueued records every EnqueueWebhookDelivery call, in call order.
	webhookEnqueued []repository.WebhookDelivery
}

func (f *fakeRepo) Insert(ctx context.Context, e *repository.AuditEvent) error {
	f.insertCount++
	return f.insertErr
}

func (f *fakeRepo) ListEmailRecipients(ctx context.Context, tenantID uuid.UUID, category string) ([]uuid.UUID, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.recipients, nil
}

func (f *fakeRepo) EnqueueEmailDelivery(ctx context.Context, d repository.EmailDelivery) error {
	f.enqueued = append(f.enqueued, d)
	return nil
}

func (f *fakeRepo) GetNotificationWebhookConfig(ctx context.Context, tenantID uuid.UUID) (*repository.NotificationWebhookConfig, error) {
	return f.webhookCfg, nil
}

func (f *fakeRepo) EnqueueWebhookDelivery(ctx context.Context, d repository.WebhookDelivery) error {
	f.webhookEnqueued = append(f.webhookEnqueued, d)
	return nil
}

// ── unused-by-these-tests methods (satisfy schedulerRepo) ─────────────

func (f *fakeRepo) ListActiveTenants(ctx context.Context, window time.Duration) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeRepo) LastScheduledAt(ctx context.Context, tenantID uuid.UUID, category string) (time.Time, error) {
	return time.Time{}, nil
}

func (f *fakeRepo) ScheduleNotification(ctx context.Context, tenantID uuid.UUID, category string, dueAt time.Time, payload json.RawMessage) (bool, error) {
	return false, nil
}

func (f *fakeRepo) RevertStuckInProgress(ctx context.Context, maxAge time.Duration) (int64, error) {
	return 0, nil
}

func (f *fakeRepo) ClaimDueNotifications(ctx context.Context, now time.Time, limit int) ([]*repository.ScheduledNotification, error) {
	return nil, nil
}

func (f *fakeRepo) MarkFailed(ctx context.Context, id uuid.UUID, errMsg string) error { return nil }
func (f *fakeRepo) MarkDelivered(ctx context.Context, id uuid.UUID) error             { return nil }

// fakeResolver maps every requested user id to "<id>@example.com" unless
// resolveErr is set, in which case it fails. Recording the requested ids lets a
// test assert the resolver was queried with the recipient list.
type fakeResolver struct {
	resolveErr error
	seen       []uuid.UUID
}

func (fr *fakeResolver) ResolveEmails(ctx context.Context, tenantID uuid.UUID, userIDs []uuid.UUID) (map[uuid.UUID]string, error) {
	fr.seen = append(fr.seen, userIDs...)
	if fr.resolveErr != nil {
		return nil, fr.resolveErr
	}
	out := make(map[uuid.UUID]string, len(userIDs))
	for _, id := range userIDs {
		out[id] = id.String() + "@example.com"
	}
	return out, nil
}

// newTestRunner builds a Runner around a fake repo + the scanner_freshness
// category so dispatchOne can render a real notification. The scheduled row it
// returns carries a valid scanner_freshness payload.
func newTestRunner(repo schedulerRepo) (*Runner, *repository.ScheduledNotification, map[string]Category) {
	cfg := RunnerConfig{}
	cfg.defaults()
	r := &Runner{repo: repo, categories: Registry(), cfg: cfg}

	cat := Registry()[0] // scanner_freshness
	payload, _ := cat.Build(uuid.New(), time.Now().UTC())
	sn := &repository.ScheduledNotification{
		ID:       uuid.New(),
		TenantID: uuid.New(),
		Category: cat.Name(),
		Payload:  payload,
	}
	byName := map[string]Category{cat.Name(): cat}
	return r, sn, byName
}

func TestDispatchOne_EmailFanOut_TwoRecipients(t *testing.T) {
	repo := &fakeRepo{recipients: []uuid.UUID{uuid.New(), uuid.New()}}
	r, sn, byName := newTestRunner(repo)
	r.WithEmailResolver(&fakeResolver{})

	if err := r.dispatchOne(context.Background(), sn, byName); err != nil {
		t.Fatalf("dispatchOne returned error: %v", err)
	}
	if repo.insertCount != 1 {
		t.Fatalf("expected 1 bell insert, got %d", repo.insertCount)
	}
	if len(repo.enqueued) != 2 {
		t.Fatalf("expected 2 email deliveries enqueued, got %d", len(repo.enqueued))
	}
	// Every enqueued row must carry the scheduled id + rendered subject.
	for _, d := range repo.enqueued {
		if d.SourceScheduledID != sn.ID {
			t.Errorf("enqueued row has wrong source_scheduled_id: %s", d.SourceScheduledID)
		}
		if d.TenantID != sn.TenantID {
			t.Errorf("enqueued row has wrong tenant_id: %s", d.TenantID)
		}
		if d.Subject == "" || d.BodySummary == "" {
			t.Errorf("enqueued row missing subject/summary: %+v", d)
		}
		if d.ToAddress == "" {
			t.Errorf("enqueued row missing to_address")
		}
	}
}

func TestDispatchOne_EmailFanOut_NilResolver(t *testing.T) {
	repo := &fakeRepo{recipients: []uuid.UUID{uuid.New(), uuid.New()}}
	r, sn, byName := newTestRunner(repo)
	// No WithEmailResolver → email disabled.

	if err := r.dispatchOne(context.Background(), sn, byName); err != nil {
		t.Fatalf("dispatchOne returned error: %v", err)
	}
	if len(repo.enqueued) != 0 {
		t.Fatalf("expected no email deliveries with nil resolver, got %d", len(repo.enqueued))
	}
}

func TestDispatchOne_EmailFanOut_ResolverErrorDoesNotFailBell(t *testing.T) {
	repo := &fakeRepo{recipients: []uuid.UUID{uuid.New()}}
	r, sn, byName := newTestRunner(repo)
	r.WithEmailResolver(&fakeResolver{resolveErr: errors.New("auth unreachable")})

	// The bell write succeeded, so dispatchOne must still return nil even
	// though the email resolve failed.
	if err := r.dispatchOne(context.Background(), sn, byName); err != nil {
		t.Fatalf("dispatchOne must not fail on resolver error, got: %v", err)
	}
	if repo.insertCount != 1 {
		t.Fatalf("expected bell insert to still happen, got %d", repo.insertCount)
	}
	if len(repo.enqueued) != 0 {
		t.Fatalf("expected no deliveries when resolver errors, got %d", len(repo.enqueued))
	}
}

func TestDispatchOne_EmailFanOut_ListRecipientsErrorDoesNotFailBell(t *testing.T) {
	repo := &fakeRepo{listErr: errors.New("db down")}
	r, sn, byName := newTestRunner(repo)
	r.WithEmailResolver(&fakeResolver{})

	if err := r.dispatchOne(context.Background(), sn, byName); err != nil {
		t.Fatalf("dispatchOne must not fail on list-recipients error, got: %v", err)
	}
	if len(repo.enqueued) != 0 {
		t.Fatalf("expected no deliveries, got %d", len(repo.enqueued))
	}
}

// ── FUT-019 webhook channel fan-out tests ────────────────────────────

func TestDispatchOne_WebhookFanOut_EnabledCategoryEnqueues(t *testing.T) {
	repo := &fakeRepo{}
	r, sn, byName := newTestRunner(repo)
	// Config enabled + this notification's category in the enabled set.
	repo.webhookCfg = &repository.NotificationWebhookConfig{
		Enabled:           true,
		EnabledCategories: []string{sn.Category},
	}
	r.WithWebhookEnabled()

	if err := r.dispatchOne(context.Background(), sn, byName); err != nil {
		t.Fatalf("dispatchOne returned error: %v", err)
	}
	if len(repo.webhookEnqueued) != 1 {
		t.Fatalf("expected 1 webhook delivery enqueued, got %d", len(repo.webhookEnqueued))
	}
	d := repo.webhookEnqueued[0]
	if d.SourceScheduledID != sn.ID {
		t.Errorf("webhook row has wrong source_scheduled_id: %s", d.SourceScheduledID)
	}
	if d.TenantID != sn.TenantID {
		t.Errorf("webhook row has wrong tenant_id: %s", d.TenantID)
	}
	if d.Subject == "" || d.BodySummary == "" {
		t.Errorf("webhook row missing subject/summary: %+v", d)
	}
}

func TestDispatchOne_WebhookFanOut_CategoryNotEnabledSkips(t *testing.T) {
	repo := &fakeRepo{}
	r, sn, byName := newTestRunner(repo)
	// Config enabled but this notification's category is NOT in the set.
	repo.webhookCfg = &repository.NotificationWebhookConfig{
		Enabled:           true,
		EnabledCategories: []string{"some_other_category"},
	}
	r.WithWebhookEnabled()

	if err := r.dispatchOne(context.Background(), sn, byName); err != nil {
		t.Fatalf("dispatchOne returned error: %v", err)
	}
	if len(repo.webhookEnqueued) != 0 {
		t.Fatalf("expected no webhook deliveries when category not enabled, got %d", len(repo.webhookEnqueued))
	}
}
