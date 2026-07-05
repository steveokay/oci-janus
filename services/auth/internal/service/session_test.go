package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// fakeSessionRepo is an in-memory sessionRepo for unit tests.
type fakeSessionRepo struct {
	bySID      map[string]*repository.Session
	touchCount map[uuid.UUID]int
}

func newFakeSessionRepo() *fakeSessionRepo {
	return &fakeSessionRepo{bySID: map[string]*repository.Session{}, touchCount: map[uuid.UUID]int{}}
}

func (f *fakeSessionRepo) Create(_ context.Context, s repository.Session) error {
	c := s
	f.bySID[s.SID.String()] = &c
	return nil
}
func (f *fakeSessionRepo) ListLive(_ context.Context, userID uuid.UUID, _ time.Time) ([]repository.Session, error) {
	var out []repository.Session
	for _, s := range f.bySID {
		if s.UserID == userID && s.RevokedAt == nil {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (f *fakeSessionRepo) RevokeOwned(_ context.Context, userID, sid uuid.UUID) (time.Time, bool, error) {
	s, ok := f.bySID[sid.String()]
	if !ok || s.UserID != userID || s.RevokedAt != nil {
		return time.Time{}, false, nil
	}
	now := time.Now()
	s.RevokedAt = &now
	return s.ExpiresAt, true, nil
}
func (f *fakeSessionRepo) RevokeOthers(_ context.Context, userID, keepSID uuid.UUID) ([]repository.Session, error) {
	var out []repository.Session
	for _, s := range f.bySID {
		if s.UserID == userID && s.SID != keepSID && s.RevokedAt == nil {
			now := time.Now()
			s.RevokedAt = &now
			out = append(out, *s)
		}
	}
	return out, nil
}
func (f *fakeSessionRepo) TouchLastActive(_ context.Context, sid uuid.UUID, _ time.Time) error {
	f.touchCount[sid]++
	return nil
}

// newSessionTestService builds a Service with a single-key ring, miniredis, and
// the given fake session repo wired via SetSessionRepo.
func newSessionTestService(t *testing.T, rdb redisClient, sessions sessionRepo) *Service {
	t.Helper()
	svc := newTokenTestService(t, rdb) // reuse the helper added in Task 4 (session_token_test.go)
	svc.SetSessionRepo(sessions)
	return svc
}

func TestIssueSessionToken_createsRowAndSid(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc := newSessionTestService(t, rdb, sessions)
	ctx := context.Background()

	userID, tenantID := uuid.New(), uuid.New()
	tok, err := svc.issueSessionToken(ctx, userID, tenantID, nil, false, "human",
		[]string{"pwd"}, SessionMeta{IP: "203.0.113.9", UserAgent: "docker/24.0"})
	if err != nil {
		t.Fatalf("issueSessionToken: %v", err)
	}
	claims, _ := svc.ValidateToken(ctx, tok)
	if claims.Sid == "" {
		t.Fatal("expected a non-empty sid claim")
	}
	row, ok := sessions.bySID[claims.Sid]
	if !ok {
		t.Fatal("expected a session row keyed by the minted sid")
	}
	if row.DeviceLabel != "Docker CLI" || row.IP != "203.0.113.9" {
		t.Fatalf("row metadata wrong: %+v", row)
	}
}
