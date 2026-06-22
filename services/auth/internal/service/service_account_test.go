// Package service — ServiceAccountService tests using in-memory fakes.
//
// All tests in this file use hand-written fakes and miniredis — no real
// PostgreSQL or Redis required. Tests run with plain `go test ./...`.
package service

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── Fakes ────────────────────────────────────────────────────────────────────

// capturingAuditEmitter accumulates AuditEvent values for assertion in tests.
// The zero value is ready to use.
type capturingAuditEmitter struct {
	// Events holds every AuditEvent emitted in the order they were received.
	Events []AuditEvent
	// EmitErr, when non-nil, is returned by every Emit call. Use this to
	// simulate audit backend failures.
	EmitErr error
}

// Emit records ev and returns EmitErr (nil unless the test set it).
func (a *capturingAuditEmitter) Emit(_ context.Context, ev AuditEvent) error {
	if a.EmitErr != nil {
		return a.EmitErr
	}
	a.Events = append(a.Events, ev)
	return nil
}

// fakeSARepo is an in-memory implementation of saRepo. The zero value is not
// ready to use — call newFakeSARepo() to initialise the maps.
type fakeSARepo struct {
	accounts map[uuid.UUID]*repository.ServiceAccount
}

func newFakeSARepo() *fakeSARepo {
	return &fakeSARepo{accounts: make(map[uuid.UUID]*repository.ServiceAccount)}
}

// CreateAtomic inserts the SA and a synthesised shadow user row in memory.
// It generates the SA id and shadow user id independently to mirror the real
// repo without needing a real DB transaction.
func (f *fakeSARepo) CreateAtomic(_ context.Context, in repository.CreateServiceAccountInput) (*repository.ServiceAccount, uuid.UUID, error) {
	saID := uuid.New()
	shadowID := uuid.New()
	scopes := in.AllowedScopes
	if scopes == nil {
		scopes = []string{}
	}
	sa := &repository.ServiceAccount{
		ID:            saID,
		TenantID:      in.TenantID,
		ShadowUserID:  shadowID,
		Name:          in.Name,
		Description:   in.Description,
		AllowedScopes: scopes,
		CreatedBy:     &in.CreatedBy,
		CreatedAt:     time.Now(),
	}
	f.accounts[saID] = sa
	return sa, shadowID, nil
}

func (f *fakeSARepo) Get(_ context.Context, id uuid.UUID) (*repository.ServiceAccount, error) {
	sa, ok := f.accounts[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return sa, nil
}

func (f *fakeSARepo) List(_ context.Context, tenantID uuid.UUID, includeDisabled bool, pageSize int, _ string) ([]repository.ServiceAccountWithStats, string, error) {
	var results []repository.ServiceAccountWithStats
	for _, sa := range f.accounts {
		if sa.TenantID != tenantID {
			continue
		}
		if !includeDisabled && sa.DisabledAt != nil {
			continue
		}
		results = append(results, repository.ServiceAccountWithStats{ServiceAccount: *sa})
	}
	if pageSize > 0 && len(results) > pageSize {
		results = results[:pageSize]
	}
	return results, "", nil
}

func (f *fakeSARepo) Update(_ context.Context, in repository.UpdateServiceAccountInput) (*repository.ServiceAccount, error) {
	sa, ok := f.accounts[in.ID]
	if !ok || sa.TenantID != in.TenantID {
		return nil, repository.ErrNotFound
	}
	if in.Name != nil {
		sa.Name = *in.Name
	}
	if in.Description != nil {
		sa.Description = *in.Description
	}
	if in.AllowedScopes != nil {
		sa.AllowedScopes = *in.AllowedScopes
	}
	if in.Disabled != nil {
		if *in.Disabled {
			now := time.Now()
			sa.DisabledAt = &now
		} else {
			sa.DisabledAt = nil
		}
	}
	return sa, nil
}

func (f *fakeSARepo) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := f.accounts[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.accounts, id)
	return nil
}

func (f *fakeSARepo) CountKeysAffectedByScopeShrink(_ context.Context, _ uuid.UUID, proposed []string) (int64, error) {
	// The real test for scope-shrink count uses the fakeAPIKeyRepo directly via
	// the service method; this stub is for the saRepo interface — the service
	// delegates to the real CountKeysAffectedByScopeShrink which queries keys.
	// In our fakes the SA repo doesn't hold key data, so we always return 0 here
	// and rely on fakeSARepoWithCount for the scope-shrink test.
	_ = proposed
	return 0, nil
}

// fakeSARepoWithCount wraps fakeSARepo and overrides CountKeysAffectedByScopeShrink
// so tests can set a canned count without mocking the key table.
type fakeSARepoWithCount struct {
	*fakeSARepo
	// count is returned by CountKeysAffectedByScopeShrink.
	count int64
}

func (f *fakeSARepoWithCount) CountKeysAffectedByScopeShrink(_ context.Context, _ uuid.UUID, _ []string) (int64, error) {
	return f.count, nil
}

// ── Test harness ──────────────────────────────────────────────────────────────

// saFakes bundles the fakes needed by newSAService.
type saFakes struct {
	saRepo   *fakeSARepo
	userRepo *fakeUserRepo
	keyRepo  *fakeAPIKeyRepo
	audit    *capturingAuditEmitter
	mr       *miniredis.Miniredis
	rdb      *redis.Client
	// admin is the ID of the seeded admin user across all tests.
	admin uuid.UUID
}

// newSAService constructs a ServiceAccountService backed entirely by in-memory
// fakes + miniredis. The returned saFakes carries references to all fakes so
// test cases can inspect or seed state.
func newSAService(t *testing.T) (*ServiceAccountService, *saFakes) {
	t.Helper()

	// Start an in-process Redis for revoke-key assertions.
	mr, err := miniredis.Run()
	require.NoError(t, err, "start miniredis")
	t.Cleanup(func() {
		mr.Close()
	})

	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	ur := newFakeUserRepo()
	sr := newFakeSARepo()
	ar := newFakeAPIKeyRepo()
	ae := &capturingAuditEmitter{}

	// Seed a default admin user so seedHuman and seedSA can use it without
	// setting up their own user in every test.
	adminID := uuid.New()
	tenantID := uuid.New()
	ur.users["admin@example.com"] = &repository.User{
		ID:       adminID,
		TenantID: tenantID,
		Username: "admin",
		Email:    "admin@example.com",
		IsActive: true,
		Kind:     "human",
	}

	fakes := &saFakes{
		saRepo:   sr,
		userRepo: ur,
		keyRepo:  ar,
		audit:    ae,
		mr:       mr,
		rdb:      rdb,
		admin:    adminID,
	}

	// redisAdapter wraps *redis.Client to satisfy RedisCmdable. We cannot pass
	// *redis.Client directly because the redis package's Set/Del return
	// *redis.StatusCmd / *redis.IntCmd (not the interface Err() method).
	// The adapter normalises them.
	svc := NewServiceAccountService(sr, ur, ar, ae, newRedisAdapter(rdb))

	return svc, fakes
}

// seedHuman seeds a fresh tenant + human admin user and returns (tenantID, userID).
func (f *saFakes) seedHuman(email string) (uuid.UUID, uuid.UUID) {
	tenantID := uuid.New()
	userID := uuid.New()
	displayName := "Admin User"
	f.userRepo.users[email] = &repository.User{
		ID:          userID,
		TenantID:    tenantID,
		Username:    email,
		Email:       email,
		DisplayName: &displayName,
		IsActive:    true,
		Kind:        "human",
	}
	return tenantID, userID
}

// seedSA creates an SA directly in the fake SA repo (bypassing the service) and
// returns the SA. Use this in tests that need a pre-existing SA without
// triggering Create's audit emission.
func (f *saFakes) seedSA(name string) *repository.ServiceAccount {
	tenantID := uuid.New()
	saID := uuid.New()
	shadowID := uuid.New()
	// Also register the shadow user in the user repo so Delete can find it.
	f.userRepo.users["shadow:"+shadowID.String()] = &repository.User{
		ID:       shadowID,
		TenantID: tenantID,
		Kind:     "service_account",
	}
	sa := &repository.ServiceAccount{
		ID:            saID,
		TenantID:      tenantID,
		ShadowUserID:  shadowID,
		Name:          name,
		AllowedScopes: []string{"read", "write"},
		CreatedBy:     &f.admin,
		CreatedAt:     time.Now(),
	}
	f.saRepo.accounts[saID] = sa
	return sa
}

// seedSAKey creates an API key in the fake key repo belonging to the given SA
// with the specified scopes.
func (f *saFakes) seedSAKey(sa *repository.ServiceAccount, name string, scopes ...string) *repository.APIKey {
	if scopes == nil {
		scopes = []string{}
	}
	k := &repository.APIKey{
		ID:               uuid.New(),
		TenantID:         sa.TenantID,
		ServiceAccountID: &sa.ID,
		Name:             name,
		KeyHash:          "hash-" + name,
		KeyPrefix:        name[:min(len(name), 12)],
		Scopes:           scopes,
		IsActive:         true,
		CreatedAt:        time.Now(),
	}
	f.keyRepo.keys[k.ID] = k
	return k
}

// min returns the smaller of a and b.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── redisAdapter ─────────────────────────────────────────────────────────────

// redisAdapter adapts *redis.Client to the RedisCmdable interface used by
// ServiceAccountService. The adapter boxes each *redis.StatusCmd / *redis.IntCmd
// into the anonymous interface{ Err() error } that RedisCmdable returns.
type redisAdapter struct {
	rdb *redis.Client
}

// newRedisAdapter wraps a *redis.Client so ServiceAccountService can accept it
// without importing the redis package into service_account.go.
func newRedisAdapter(rdb *redis.Client) RedisCmdable {
	return &redisAdapter{rdb: rdb}
}

func (a *redisAdapter) Set(ctx context.Context, key string, value interface{}, expiration time.Duration) interface{ Err() error } {
	return a.rdb.Set(ctx, key, value, expiration)
}

func (a *redisAdapter) Del(ctx context.Context, keys ...string) interface{ Err() error } {
	return a.rdb.Del(ctx, keys...)
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestServiceAccount_Create_EmitsAudit verifies that a successful Create emits
// exactly one service_account.created event with the creator snapshot fields.
func TestServiceAccount_Create_EmitsAudit(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)

	tenant, admin := fakes.seedHuman("admin@example.com")

	sa, err := svc.Create(ctx, ServiceAccountInput{
		TenantID:      tenant,
		Name:          "ci-prod",
		AllowedScopes: []string{"pull", "push"},
		ActorUserID:   admin,
	})
	require.NoError(t, err)

	require.Len(t, fakes.audit.Events, 1, "expected exactly one audit event")
	ev := fakes.audit.Events[0]
	require.Equal(t, "service_account.created", ev.Action)
	require.Equal(t, admin.String(), ev.ActorID)
	require.Equal(t, sa.ID.String(), ev.Resource)
	require.Equal(t, "admin@example.com", ev.Fields["creator_email"],
		"creator_email snapshot must match the seeded user")
}

// TestServiceAccount_Create_NameSnapshotInAudit verifies that the SA name,
// description, and allowed_scopes are included in the audit event fields.
func TestServiceAccount_Create_NameSnapshotInAudit(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)

	tenant, admin := fakes.seedHuman("creator@example.com")

	sa, err := svc.Create(ctx, ServiceAccountInput{
		TenantID:      tenant,
		Name:          "deploy-bot",
		Description:   "CI deploy key",
		AllowedScopes: []string{"push"},
		ActorUserID:   admin,
	})
	require.NoError(t, err)

	require.Len(t, fakes.audit.Events, 1)
	ev := fakes.audit.Events[0]
	require.Equal(t, sa.ID.String(), ev.Fields["service_account_id"])
	require.Equal(t, "deploy-bot", ev.Fields["name"])
	require.Equal(t, "CI deploy key", ev.Fields["description"])
}

// TestServiceAccount_Disable_SetsRedisRevoke verifies that disabling an SA
// writes a Redis revoke key for the shadow user.
func TestServiceAccount_Disable_SetsRedisRevoke(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)
	sa := fakes.seedSA("ci-prod")

	require.NoError(t, svc.SetDisabled(ctx, sa.ID, sa.TenantID, true, fakes.admin))

	val, err := fakes.rdb.Get(ctx, "revoke:user:"+sa.ShadowUserID.String()).Result()
	require.NoError(t, err, "revoke key must be readable after disable")
	require.NotEmpty(t, val, "revoke key value must be non-empty on disable")
}

// TestServiceAccount_Enable_DeletesRedisRevoke verifies that enabling an SA
// removes any existing revoke key so JWT validation resumes.
func TestServiceAccount_Enable_DeletesRedisRevoke(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)
	sa := fakes.seedSA("ci-prod")

	// Disable first so there's a revoke key to clear.
	require.NoError(t, svc.SetDisabled(ctx, sa.ID, sa.TenantID, true, fakes.admin))
	// Verify the key was set.
	_, err := fakes.rdb.Get(ctx, "revoke:user:"+sa.ShadowUserID.String()).Result()
	require.NoError(t, err, "revoke key must exist after disable")

	// Now enable and verify the key is gone.
	require.NoError(t, svc.SetDisabled(ctx, sa.ID, sa.TenantID, false, fakes.admin))
	_, err = fakes.rdb.Get(ctx, "revoke:user:"+sa.ShadowUserID.String()).Result()
	require.ErrorIs(t, err, redis.Nil, "revoke key must be deleted after enable")
}

// TestServiceAccount_Disable_EmitsAudit verifies that SetDisabled emits
// service_account.disabled when disabled=true and service_account.enabled when
// disabled=false.
func TestServiceAccount_Disable_EmitsAudit(t *testing.T) {
	ctx := context.Background()

	t.Run("disable emits service_account.disabled", func(t *testing.T) {
		svc, fakes := newSAService(t)
		sa := fakes.seedSA("bot")

		require.NoError(t, svc.SetDisabled(ctx, sa.ID, sa.TenantID, true, fakes.admin))

		require.Len(t, fakes.audit.Events, 1)
		require.Equal(t, "service_account.disabled", fakes.audit.Events[0].Action)
		require.Equal(t, sa.ID.String(), fakes.audit.Events[0].Resource)
	})

	t.Run("enable emits service_account.enabled", func(t *testing.T) {
		svc, fakes := newSAService(t)
		sa := fakes.seedSA("bot")

		require.NoError(t, svc.SetDisabled(ctx, sa.ID, sa.TenantID, false, fakes.admin))

		require.Len(t, fakes.audit.Events, 1)
		require.Equal(t, "service_account.enabled", fakes.audit.Events[0].Action)
	})
}

// TestServiceAccount_Delete_Cascades verifies that Delete removes the SA from
// the repo and also removes the shadow user from the user repo (in production
// the FK cascade handles this; in the fake we verify the service calls Delete
// which removes the SA — the shadow-user cleanup is a DB concern tested via
// integration tests).
func TestServiceAccount_Delete_Cascades(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)
	sa := fakes.seedSA("doomed")
	// Seed a key on the SA so the fake key repo has something to check.
	fakes.seedSAKey(sa, "k1")

	require.NoError(t, svc.Delete(ctx, sa.ID, fakes.admin))

	// SA is gone from the fake repo.
	_, err := fakes.saRepo.Get(ctx, sa.ID)
	require.ErrorIs(t, err, repository.ErrNotFound, "SA must be removed after Delete")
}

// TestServiceAccount_Delete_EmitsAudit verifies that Delete emits
// service_account.deleted with the SA name snapshot so the audit trail is
// useful even after the row is gone.
func TestServiceAccount_Delete_EmitsAudit(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)
	sa := fakes.seedSA("doomed")

	require.NoError(t, svc.Delete(ctx, sa.ID, fakes.admin))

	require.Len(t, fakes.audit.Events, 1)
	ev := fakes.audit.Events[0]
	require.Equal(t, "service_account.deleted", ev.Action)
	require.Equal(t, sa.ID.String(), ev.Resource)
	require.Equal(t, "doomed", ev.Fields["name"], "name snapshot must survive deletion")
}

// TestServiceAccount_Delete_RevokeShadowKey verifies that Delete clears the
// Redis revoke key for the shadow user so there is no stale entry.
func TestServiceAccount_Delete_RevokeShadowKey(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)
	sa := fakes.seedSA("gone")

	// Pre-seed a revoke key as if the SA was disabled before deletion.
	require.NoError(t, fakes.rdb.Set(ctx, "revoke:user:"+sa.ShadowUserID.String(), "1", time.Minute).Err())

	require.NoError(t, svc.Delete(ctx, sa.ID, fakes.admin))

	// The revoke key should be cleared.
	_, err := fakes.rdb.Get(ctx, "revoke:user:"+sa.ShadowUserID.String()).Result()
	require.ErrorIs(t, err, redis.Nil, "stale revoke key must be deleted on SA delete")
}

// TestServiceAccount_Delete_NotFound verifies that deleting a non-existent SA
// returns ErrNotFound and emits no audit event.
func TestServiceAccount_Delete_NotFound(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)

	err := svc.Delete(ctx, uuid.New(), fakes.admin)
	require.ErrorIs(t, err, repository.ErrNotFound)
	require.Empty(t, fakes.audit.Events, "no audit event on not-found delete")
}

// TestServiceAccount_ScopeShrinkPreflight verifies that CountKeysAffectedByScopeShrink
// returns the number of keys that have at least one scope not in the proposed set.
// Because the fakeSARepo.CountKeysAffectedByScopeShrink returns 0, we test the
// method using a fakeSARepoWithCount wrapper.
func TestServiceAccount_ScopeShrinkPreflight(t *testing.T) {
	ctx := context.Background()
	_, fakes := newSAService(t)
	sa := fakes.seedSA("c") // allowed_scopes={read,write}

	// Build a service wired to fakeSARepoWithCount so we can control the count.
	saWithCount := &fakeSARepoWithCount{fakeSARepo: fakes.saRepo, count: 1}
	svc := NewServiceAccountService(saWithCount, fakes.userRepo, fakes.keyRepo, fakes.audit, newRedisAdapter(fakes.rdb))

	n, err := svc.CountKeysAffectedByScopeShrink(ctx, sa.ID, sa.TenantID, []string{"read"})
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "count from repo must be forwarded unchanged")
}

// TestServiceAccount_ScopeShrinkPreflight_TenantMismatch verifies that
// CountKeysAffectedByScopeShrink returns ErrNotFound when the supplied tenantID
// does not match the SA's stored tenant.
func TestServiceAccount_ScopeShrinkPreflight_TenantMismatch(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)
	sa := fakes.seedSA("victim")
	wrongTenant := uuid.New()

	_, err := svc.CountKeysAffectedByScopeShrink(ctx, sa.ID, wrongTenant, []string{"read"})
	require.ErrorIs(t, err, repository.ErrNotFound, "mismatched tenant must return ErrNotFound")
}

// TestServiceAccount_Update_EmitsAudit verifies that Update emits a
// service_account.updated audit event with the changed fields.
func TestServiceAccount_Update_EmitsAudit(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)
	sa := fakes.seedSA("original")

	newName := "renamed"
	_, err := svc.Update(ctx, UpdateServiceAccountInput{
		ID:          sa.ID,
		TenantID:    sa.TenantID,
		Name:        &newName,
		ActorUserID: fakes.admin,
	})
	require.NoError(t, err)

	require.Len(t, fakes.audit.Events, 1)
	ev := fakes.audit.Events[0]
	require.Equal(t, "service_account.updated", ev.Action)
	require.Equal(t, sa.ID.String(), ev.Resource)
	require.Equal(t, "renamed", ev.Fields["name"])
}

// TestServiceAccount_Get_ReturnsNotFound verifies that Get returns ErrNotFound
// for an unknown ID.
func TestServiceAccount_Get_ReturnsNotFound(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSAService(t)

	_, err := svc.Get(ctx, uuid.New())
	require.ErrorIs(t, err, repository.ErrNotFound)
}

// TestServiceAccount_List_FiltersByTenant verifies that List only returns SAs
// belonging to the requested tenant.
func TestServiceAccount_List_FiltersByTenant(t *testing.T) {
	ctx := context.Background()
	svc, fakes := newSAService(t)

	targetTenant := uuid.New()
	otherTenant := uuid.New()

	// Seed two SAs in the target tenant and one in another tenant.
	for _, name := range []string{"sa-a", "sa-b"} {
		id := uuid.New()
		shadowID := uuid.New()
		fakes.saRepo.accounts[id] = &repository.ServiceAccount{
			ID: id, TenantID: targetTenant, ShadowUserID: shadowID,
			Name: name, AllowedScopes: []string{}, CreatedAt: time.Now(),
		}
	}
	id := uuid.New()
	shadowID := uuid.New()
	fakes.saRepo.accounts[id] = &repository.ServiceAccount{
		ID: id, TenantID: otherTenant, ShadowUserID: shadowID,
		Name: "other-sa", AllowedScopes: []string{}, CreatedAt: time.Now(),
	}

	results, _, err := svc.List(ctx, targetTenant, false, 20, "")
	require.NoError(t, err)
	require.Len(t, results, 2, "only SAs from the target tenant should be returned")
}
