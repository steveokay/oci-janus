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

// enrolMFAUserForSession seeds an active human user and drives the full TOTP
// enrolment flow, returning the user + tenant ids AND the base32 secret so the
// caller can compute a valid OTP. It mirrors enrolMFAUser (mfa_test.go) but
// returns the tenant id + secret and derives the enrolment code from svc.now()
// (the harness pins the clock via loginMFAFixedNow, so codeForSecret is
// deterministic).
func enrolMFAUserForSession(t *testing.T, svc *Service, users *fakeUserRepo) (userID, tenantID uuid.UUID, secretBase32 string) {
	t.Helper()
	userID = uuid.New()
	tenantID = uuid.New()
	users.addUser(&repository.User{
		ID:       userID,
		TenantID: tenantID,
		Username: "u-" + userID.String()[:8],
		Email:    userID.String()[:8] + "@example.com",
		IsActive: true,
		Kind:     "human",
	})
	// Enrol under a back-dated clock so CompleteMFAEnrollment consumes an EARLIER
	// TOTP counter than the one the caller spends at svc.now(). Without this the
	// replay guard (verifyTOTP: counter must strictly advance past LastUsedCounter)
	// would reject the identical current-window code when it is re-presented at
	// VerifyLoginMFA. 90s back = two 30s steps earlier, well outside the accepted
	// window at enrol time but strictly below the current counter at verify time.
	realNowFn := svc.nowFn
	enrolAt := loginMFAFixedNow.Add(-90 * time.Second)
	svc.nowFn = func() time.Time { return enrolAt }
	secret, _, err := svc.BeginMFAEnrollment(context.Background(), userID)
	if err != nil {
		t.Fatalf("BeginMFAEnrollment: %v", err)
	}
	code := codeForSecret(t, secret, enrolAt)
	if _, err := svc.CompleteMFAEnrollment(context.Background(), userID, code); err != nil {
		t.Fatalf("CompleteMFAEnrollment: %v", err)
	}
	// Restore the pinned clock so the caller's codeForSecret(secret, svc.now())
	// lands on the current window.
	svc.nowFn = realNowFn
	return userID, tenantID, secret
}

func TestVerifyLoginMFA_createsSession(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc, users := newSessionLoginTestService(t, rdb, sessions)
	ctx := context.Background()

	userID, tenantID, secret := enrolMFAUserForSession(t, svc, users)
	ct, _ := svc.IssueMFAChallengeToken(ctx, userID.String(), tenantID.String())
	tok, err := svc.VerifyLoginMFA(ctx, ct, codeForSecret(t, secret, svc.now()),
		SessionMeta{IP: "198.51.100.4", UserAgent: "docker/24.0"})
	if err != nil {
		t.Fatalf("VerifyLoginMFA: %v", err)
	}
	claims, _ := svc.ValidateToken(ctx, tok)
	if claims.Sid == "" || sessions.bySID[claims.Sid] == nil {
		t.Fatal("MFA login must create a session")
	}
}

// TestIssueSSOToken_createsSession asserts the SSO login path mints a session
// row + sid. It wraps the shared session harness's *Service in a minimal SSO
// (nil provider/session repos, a throwaway 32-byte credential key) since
// IssueSSOToken only needs the underlying auth service to issue the token.
func TestIssueSSOToken_createsSession(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc, _ := newSessionLoginTestService(t, rdb, sessions)
	ctx := context.Background()

	sso, err := NewSSO(svc, nil, nil, make([]byte, 32))
	if err != nil {
		t.Fatalf("NewSSO: %v", err)
	}
	user := &repository.User{ID: uuid.New(), TenantID: uuid.New(), Kind: "human", IsActive: true}
	tok, err := sso.IssueSSOToken(ctx, user, []string{"reader"},
		SessionMeta{IP: "203.0.113.7", UserAgent: "Mozilla/5.0"})
	if err != nil {
		t.Fatalf("IssueSSOToken: %v", err)
	}
	claims, _ := svc.ValidateToken(ctx, tok)
	if claims.Sid == "" || sessions.bySID[claims.Sid] == nil {
		t.Fatal("SSO login must create a session")
	}
}

func TestValidateToken_revokedSid_denied(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc := newSessionTestService(t, rdb, sessions)
	ctx := context.Background()

	tok, _ := svc.issueSessionToken(ctx, uuid.New(), uuid.New(), nil, false, "human",
		[]string{"pwd"}, SessionMeta{IP: "203.0.113.1", UserAgent: "x"})
	claims, _ := svc.ValidateToken(ctx, tok)

	if err := rdb.Set(ctx, "revoke:sid:"+claims.Sid, "1", time.Hour).Err(); err != nil {
		t.Fatalf("seed revoke:sid: %v", err)
	}
	if _, err := svc.ValidateToken(ctx, tok); err == nil {
		t.Fatal("ValidateToken must reject a token whose sid is revoked")
	}
}

func TestRevokeSession_setsGateAndRow(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc := newSessionTestService(t, rdb, sessions)
	ctx := context.Background()

	userID := uuid.New()
	sid := uuid.New()
	sessions.bySID[sid.String()] = &repository.Session{SID: sid, UserID: userID, ExpiresAt: time.Now().Add(time.Hour)}

	ok, err := svc.RevokeSession(ctx, userID, sid)
	if err != nil || !ok {
		t.Fatalf("RevokeSession: ok=%v err=%v", ok, err)
	}
	if v, _ := rdb.Get(ctx, sessionRevokeKey(sid.String())).Result(); v == "" {
		t.Fatal("RevokeSession must set the revoke:sid gate")
	}
	if ok, _ := svc.RevokeSession(ctx, uuid.New(), sid); ok {
		t.Fatal("cross-user RevokeSession must return ok=false")
	}
}

// TestRevokeOtherSessions_keepsCurrent seeds two live sessions for one user,
// revokes all but the kept sid, and asserts the count is 1 and the kept session's
// gate is NOT set (it must remain valid).
func TestRevokeOtherSessions_keepsCurrent(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	svc := newSessionTestService(t, rdb, sessions)
	ctx := context.Background()

	userID := uuid.New()
	keepSID := uuid.New()
	otherSID := uuid.New()
	sessions.bySID[keepSID.String()] = &repository.Session{SID: keepSID, UserID: userID, ExpiresAt: time.Now().Add(time.Hour)}
	sessions.bySID[otherSID.String()] = &repository.Session{SID: otherSID, UserID: userID, ExpiresAt: time.Now().Add(time.Hour)}

	n, err := svc.RevokeOtherSessions(ctx, userID, keepSID)
	if err != nil {
		t.Fatalf("RevokeOtherSessions: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 revoked, got %d", n)
	}
	// The kept session's gate must NOT be set — it stays valid.
	if v, _ := rdb.Get(ctx, sessionRevokeKey(keepSID.String())).Result(); v != "" {
		t.Fatal("kept session must not be gated")
	}
	// The other session's gate must be set.
	if v, _ := rdb.Get(ctx, sessionRevokeKey(otherSID.String())).Result(); v == "" {
		t.Fatal("revoked session must be gated")
	}
}

func TestSessionActiveUpdater_debounces(t *testing.T) {
	rdb := newTestRedis(t)
	sessions := newFakeSessionRepo()
	u := newSessionActiveUpdater(rdb, sessions, nil)
	ctx := context.Background()
	sid := uuid.New()

	u.touchNow(ctx, sid) // first: SETNX wins -> write
	u.touchNow(ctx, sid) // second inside window: SETNX loses -> skip
	if sessions.touchCount[sid] != 1 {
		t.Fatalf("expected exactly 1 debounced touch, got %d", sessions.touchCount[sid])
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
