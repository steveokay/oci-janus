package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	argon2pkg "github.com/steveokay/oci-janus/libs/crypto/argon2"
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

// newSessionLoginTestService builds a Service backed by an in-memory fakeUserRepo
// plus the caller's miniredis client and fake session repo. Unlike
// newSessionTestService (which uses a nil user repo), this harness can seed real
// password + MFA users so the interactive login paths (Login / VerifyLoginMFA)
// can be exercised end-to-end. The clock is pinned to loginMFAFixedNow and a
// 32-byte MFA KEK is wired so enrolMFAUserForSession's TOTP maths is
// deterministic and reusable across Task 6 and Task 7.
func newSessionLoginTestService(t *testing.T, rdb redisClient, sessions sessionRepo) (*Service, *fakeUserRepo) {
	t.Helper()
	priv, _ := genTestKey(t)
	ring, err := newKeyRing([]signingKey{
		{kid: "kid-test", privateKey: priv, publicKey: &priv.PublicKey},
	}, "kid-test")
	if err != nil {
		t.Fatalf("newKeyRing: %v", err)
	}
	users := newFakeUserRepo()
	svc, err := NewWithFakesAndRing(users, nil, nil, nil, rdb, ring)
	if err != nil {
		t.Fatalf("NewWithFakesAndRing: %v", err)
	}
	// Deterministic 32-byte AES-256 KEK + pinned clock so MFA enrolment/verify is
	// reproducible; harmless for the plain password path.
	svc.SetMFAKEK([]byte("0123456789abcdef0123456789abcdef"))
	svc.nowFn = func() time.Time { return loginMFAFixedNow }
	// Wire the fake session repo so the login paths mint + persist a sid.
	svc.SetSessionRepo(sessions)
	return svc, users
}

// seedPasswordUserForSession seeds an active human user with a valid argon2 hash
// of the given password and returns its tenant + user ids. It reuses the
// argon2pkg helper (never hand-rolls a hash) so AuthenticateUser accepts the
// password on the login path.
func seedPasswordUserForSession(t *testing.T, users *fakeUserRepo, username, password string) (tenantID, userID uuid.UUID) {
	t.Helper()
	pwHash, err := argon2pkg.Hash(password)
	if err != nil {
		t.Fatalf("argon2pkg.Hash: %v", err)
	}
	tenantID = uuid.New()
	userID = uuid.New()
	users.addUser(&repository.User{
		ID:           userID,
		TenantID:     tenantID,
		Username:     username,
		IsActive:     true,
		PasswordHash: pwHash,
		Kind:         "human",
	})
	return tenantID, userID
}

func TestLogin_noMFA_createsSession(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc, users := newSessionLoginTestService(t, rdb, sessions)
	ctx := context.Background()

	tenantID, _ := seedPasswordUserForSession(t, users, "alice", "Str0ng!Password123")
	res, err := svc.Login(ctx, tenantID, "alice", "Str0ng!Password123",
		SessionMeta{IP: "203.0.113.5", UserAgent: "Mozilla/5.0 (Macintosh) Chrome/125.0 Safari/537.36"})
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if res.Token == "" {
		t.Fatal("expected an access token")
	}
	claims, _ := svc.ValidateToken(ctx, res.Token)
	if claims.Sid == "" || sessions.bySID[claims.Sid] == nil {
		t.Fatal("login must create a session row and stamp its sid")
	}
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
