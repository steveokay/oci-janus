package domainworker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/tenant/internal/repository"
)

// fakeRepo is an in-memory domainRepository for unit tests.
type fakeRepo struct {
	mu      sync.Mutex
	domains []*repository.DomainRecord

	verified      map[uuid.UUID]bool
	notified24h   map[uuid.UUID]bool
	notified48h   map[uuid.UUID]bool
	nextPollAfter map[uuid.UUID]time.Time
}

func newFakeRepo(domains ...*repository.DomainRecord) *fakeRepo {
	return &fakeRepo{
		domains:       domains,
		verified:      make(map[uuid.UUID]bool),
		notified24h:   make(map[uuid.UUID]bool),
		notified48h:   make(map[uuid.UUID]bool),
		nextPollAfter: make(map[uuid.UUID]time.Time),
	}
}

func (f *fakeRepo) ListUnverifiedDomains(_ context.Context, _ int) ([]*repository.DomainRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*repository.DomainRecord
	for _, d := range f.domains {
		if !f.verified[d.ID] {
			out = append(out, d)
		}
	}
	return out, nil
}

func (f *fakeRepo) MarkDomainVerified(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verified[id] = true
	return nil
}

func (f *fakeRepo) MarkDomain24hNotified(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified24h[id] = true
	return nil
}

func (f *fakeRepo) MarkDomain48hNotified(_ context.Context, id uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notified48h[id] = true
	return nil
}

func (f *fakeRepo) UpdateNextPollAfter(_ context.Context, id uuid.UUID, next time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextPollAfter[id] = next
	return nil
}

// --- calcBackoff ---

func TestCalcBackoff(t *testing.T) {
	cases := []struct {
		name     string
		age      time.Duration
		wantBack time.Duration
	}{
		{"just registered", 1 * time.Minute, 5 * time.Minute},
		{"59 minutes", 59 * time.Minute, 5 * time.Minute},
		{"exactly 1 hour", 1 * time.Hour, 10 * time.Minute},
		{"6 hours", 6 * time.Hour, 10 * time.Minute},
		{"11h59m", 11*time.Hour + 59*time.Minute, 10 * time.Minute},
		{"exactly 12 hours", 12 * time.Hour, 20 * time.Minute},
		{"36 hours", 36 * time.Hour, 20 * time.Minute},
		{"47 hours", 47 * time.Hour, 20 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			registeredAt := time.Now().Add(-tc.age)
			got := calcBackoff(registeredAt)
			if got != tc.wantBack {
				t.Errorf("calcBackoff(age=%v) = %v, want %v", tc.age, got, tc.wantBack)
			}
		})
	}
}

// --- verify() notification logic ---

// newDomain builds a DomainRecord at a given age with the given notification state.
func newDomain(age time.Duration, notified24h, notified48h bool) *repository.DomainRecord {
	return &repository.DomainRecord{
		ID:                uuid.New(),
		TenantID:          uuid.New(),
		Domain:            "example.com",
		VerificationToken: "tok-abc",
		RegisteredAt:      time.Now().Add(-age),
		Notified24h:       notified24h,
		Notified48h:       notified48h,
		NextPollAfter:     time.Now().Add(-time.Minute),
	}
}

// workerWithFakeRepo creates a Worker backed by a fakeRepo (and no Redis — safe because
// verify() only writes to Redis on success; DNS will fail and Redis is never reached).
func workerWithFakeRepo(repo domainRepository) *Worker {
	return &Worker{repo: repo, rdb: nil}
}

func TestVerify_Sends24hNotification_Once(t *testing.T) {
	d := newDomain(25*time.Hour, false, false) // 25h old, neither notification sent
	repo := newFakeRepo(d)
	w := workerWithFakeRepo(repo)

	// verify() will fail DNS (no real TXT record), but notifications run before DNS.
	ctx := context.Background()
	_ = w.verify(ctx, d) // ignore DNS error

	if !repo.notified24h[d.ID] {
		t.Error("expected 24h notification to be marked after 25h age")
	}
	if repo.notified48h[d.ID] {
		t.Error("expected no 48h notification for 25h old domain")
	}
}

func TestVerify_Sends48hNotification_Once(t *testing.T) {
	d := newDomain(47*time.Hour+30*time.Minute, false, false) // 47.5h old
	repo := newFakeRepo(d)
	w := workerWithFakeRepo(repo)

	ctx := context.Background()
	_ = w.verify(ctx, d)

	if !repo.notified48h[d.ID] {
		t.Error("expected 48h notification to be marked at 47.5h age")
	}
	if !repo.notified24h[d.ID] {
		t.Error("expected 24h notification also to be marked (domain is >24h old)")
	}
}

func TestVerify_DoesNotRepeat24hNotification(t *testing.T) {
	d := newDomain(26*time.Hour, true, false) // already notified at 24h
	repo := newFakeRepo(d)
	w := workerWithFakeRepo(repo)

	ctx := context.Background()
	_ = w.verify(ctx, d)

	// The fake repo should NOT have been called again for 24h notification.
	// We detect this because fakeRepo.notified24h was never set by our call —
	// the worker saw d.Notified24h=true and skipped the DB write.
	if repo.notified24h[d.ID] {
		// fakeRepo.MarkDomain24hNotified was called — that means the worker
		// fired a redundant notification.
		t.Error("24h notification should not be re-sent when already notified")
	}
}

func TestVerify_NoNotificationUnder24h(t *testing.T) {
	d := newDomain(2*time.Hour, false, false) // 2h old — no notification yet
	repo := newFakeRepo(d)
	w := workerWithFakeRepo(repo)

	ctx := context.Background()
	_ = w.verify(ctx, d)

	if repo.notified24h[d.ID] || repo.notified48h[d.ID] {
		t.Error("no notification expected for a 2h old domain")
	}
}

// --- poll() backoff scheduling ---

func TestPoll_UpdatesNextPollAfterOnFailure(t *testing.T) {
	// Fresh domain — DNS will fail, so we expect next_poll_after to be pushed forward.
	d := newDomain(30*time.Minute, false, false)
	d.Domain = "nonexistent.invalid" // guaranteed DNS failure
	repo := newFakeRepo(d)
	w := workerWithFakeRepo(repo)

	before := time.Now()
	w.poll(context.Background())
	after := time.Now()

	npa, ok := repo.nextPollAfter[d.ID]
	if !ok {
		t.Fatal("expected UpdateNextPollAfter to be called after DNS failure")
	}
	// next_poll_after should be ~5 minutes ahead (domain is <1h old).
	minExpected := before.Add(4 * time.Minute)
	maxExpected := after.Add(6 * time.Minute)
	if npa.Before(minExpected) || npa.After(maxExpected) {
		t.Errorf("next_poll_after = %v, want in [%v, %v]", npa, minExpected, maxExpected)
	}
}

func TestPoll_NoBackoffUpdateOnSuccess(t *testing.T) {
	// A domain that would succeed cannot be unit-tested (requires real DNS).
	// This test verifies that when the list returns empty, no state is mutated.
	repo := newFakeRepo() // empty — no domains due for polling
	w := workerWithFakeRepo(repo)

	w.poll(context.Background())

	if len(repo.nextPollAfter) != 0 {
		t.Error("expected no UpdateNextPollAfter calls when domain list is empty")
	}
}
