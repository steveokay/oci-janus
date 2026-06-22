// Package service — SSO service tests (FE-API-048, Task 10).
//
// These tests verify the synthetic-email guard introduced in Task 10:
// service-account shadow emails (sa+<uuid>@internal.invalid) must never be
// used to authenticate a human SSO session, even when the IdP asserts them.
//
// All tests use in-memory fakes — no real PostgreSQL or Redis required.
package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── Minimal fakes for SSO provider + login-session repos ─────────────────────

// fakeSSOProviderRepo satisfies authProviderRepo using an in-memory map.
// Tests can pre-seed providers with seedProvider.
type fakeSSOProviderRepo struct {
	providers map[uuid.UUID]*repository.AuthProvider
}

func newFakeSSOProviderRepo() *fakeSSOProviderRepo {
	return &fakeSSOProviderRepo{providers: make(map[uuid.UUID]*repository.AuthProvider)}
}

// seedProvider inserts a provider and returns it.
func (f *fakeSSOProviderRepo) seedProvider(p *repository.AuthProvider) *repository.AuthProvider {
	f.providers[p.ID] = p
	return p
}

func (f *fakeSSOProviderRepo) Create(_ context.Context, p *repository.AuthProvider) (*repository.AuthProvider, error) {
	f.providers[p.ID] = p
	return p, nil
}

func (f *fakeSSOProviderRepo) GetByID(_ context.Context, id uuid.UUID) (*repository.AuthProvider, error) {
	p, ok := f.providers[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return p, nil
}

func (f *fakeSSOProviderRepo) ListByTenant(_ context.Context, _ uuid.UUID, _ bool) ([]*repository.AuthProvider, error) {
	return nil, nil
}

func (f *fakeSSOProviderRepo) Update(_ context.Context, id uuid.UUID, req repository.UpdateAuthProviderRequest) (*repository.AuthProvider, error) {
	p, ok := f.providers[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	// Apply partial updates mirroring the real repo (minimal — only fields
	// exercised by these tests).
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	return p, nil
}

func (f *fakeSSOProviderRepo) Delete(_ context.Context, id uuid.UUID) error {
	if _, ok := f.providers[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.providers, id)
	return nil
}

// fakeSSOSessionRepo satisfies loginSessionRepo as a no-op. SSO tests that
// only exercise EnsureSSOUser never touch login sessions.
type fakeSSOSessionRepo struct{}

func (f *fakeSSOSessionRepo) Create(_ context.Context, _ *repository.LoginSession) error { return nil }
func (f *fakeSSOSessionRepo) ConsumeByState(_ context.Context, _ string) (*repository.LoginSession, error) {
	return nil, repository.ErrNotFound
}
func (f *fakeSSOSessionRepo) DeleteExpired(_ context.Context) (int64, error) { return 0, nil }

// ── SSO test harness ──────────────────────────────────────────────────────────

// ssoFakes bundles the fakes needed by SSO tests and exposes helper methods
// for seeding providers and service accounts.
type ssoFakes struct {
	*authFakes                          // embeds userRepo, keyRepo, saRepo, audit fakes
	providerRepo *fakeSSOProviderRepo   // fake auth-provider store
	sessionRepo  *fakeSSOSessionRepo    // no-op login-session store
	tenantID     uuid.UUID              // default tenant used by seedProvider
	provider     *repository.AuthProvider // convenience: the default seeded provider
}

// newSSOService builds a *SSO and its supporting *authFakes for unit tests.
// It seeds one auto-provisioning-enabled OAuth provider in a fresh tenant.
func newSSOService(t *testing.T) (*SSO, *ssoFakes) {
	t.Helper()

	// Build the base *Service with all user / key / SA fakes wired in.
	svc, af := newAuthService(t, context.Background())

	// Build SSO-specific fakes.
	providerRepo := newFakeSSOProviderRepo()
	sessionRepo := &fakeSSOSessionRepo{}

	// newSSO requires a 32-byte credential key (AES-256).
	credKey := make([]byte, 32)
	sso, err := NewSSO(svc, providerRepo, sessionRepo, credKey)
	require.NoError(t, err, "NewSSO must succeed with a valid 32-byte key")

	tenantID := uuid.New()

	// Seed a default Google OAuth provider with auto_provision=true so most
	// tests do not need to set up their own provider.
	provider := providerRepo.seedProvider(&repository.AuthProvider{
		ID:            uuid.New(),
		TenantID:      tenantID,
		Type:          repository.AuthProviderOAuthGoogle,
		DisplayName:   "Google (test)",
		Enabled:       true,
		AutoProvision: true,
		DefaultRole:   "reader",
	})

	fakes := &ssoFakes{
		authFakes:    af,
		providerRepo: providerRepo,
		sessionRepo:  sessionRepo,
		tenantID:     tenantID,
		provider:     provider,
	}

	return sso, fakes
}

// ── Helper: isSyntheticSAEmail unit tests ────────────────────────────────────

// TestIsSyntheticSAEmail_recognisesFormat verifies that the helper correctly
// classifies synthetic and non-synthetic email addresses.
func TestIsSyntheticSAEmail_recognisesFormat(t *testing.T) {
	// Synthetic SA emails — must return true.
	syntheticEmails := []string{
		"sa+00000000-0000-0000-0000-000000000000@internal.invalid",
		"sa+" + uuid.New().String() + "@internal.invalid",
	}
	for _, email := range syntheticEmails {
		require.True(t, isSyntheticSAEmail(email),
			"isSyntheticSAEmail(%q) should be true", email)
	}

	// Real human emails — must return false.
	humanEmails := []string{
		"alice@example.com",
		"bob+filter@company.org",
		"sa@example.com",          // "sa" prefix but not the full synthetic format
		"sa+abc@example.com",      // wrong domain
		"user@internal.invalid",   // right domain but no "sa+" prefix
	}
	for _, email := range humanEmails {
		require.False(t, isSyntheticSAEmail(email),
			"isSyntheticSAEmail(%q) should be false", email)
	}
}

// ── Task 10 regression: shadow email must never authenticate ─────────────────

// TestSSO_RefusesShadowEmail verifies that EnsureSSOUser rejects an identity
// whose email matches the synthetic SA email format, even when the shadow user
// row exists in the database (i.e. GetByEmail would have returned it).
//
// Before Task 10 the SSO path called GetByEmail, which iterates all users
// regardless of kind and would have matched the shadow-user row. After Task 10
// it calls GetHumanByEmail (which filters by kind='human') AND applies the
// isSyntheticSAEmail guard before any DB call, so shadow users are blocked on
// two independent layers.
func TestSSO_RefusesShadowEmail(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	// Seed a service account in the same tenant as the provider. seedSAInTenant
	// (defined in service_repo_test.go) also registers the shadow user row in
	// the fake user repo so GetByEmail (the old path) would have matched it.
	sa := fakes.seedSAInTenant(fakes.tenantID, "ci-prod")

	// Derive the synthetic email that the shadow user row holds in the DB.
	syntheticEmail := "sa+" + sa.ID.String() + "@internal.invalid"

	// Simulate an IdP returning the synthetic email.
	_, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         syntheticEmail,
		EmailVerified: true,
		DisplayName:   "Should Not Pass",
		Subject:       "idp-sub-abc",
	})

	// Must be refused.

	// Error message must include a human-readable indicator that the email
	// domain is reserved, so operators can diagnose a misconfigured IdP.
	require.Contains(t, err.Error(), "reserved",
		"error must mention that the email domain is reserved for service accounts")
}

// TestSSO_RefusesShadowEmail_AutoProvisionDisabled verifies that the synthetic
// email guard fires even when auto_provision=false on the provider. Previously
// the early-return for auto_provision=false happened before the DB lookup, so
// a shadow email could slip through if the server was configured with
// auto_provision=false. The guard now fires before any flag check.
func TestSSO_RefusesShadowEmail_AutoProvisionDisabled(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	// Override the default provider to disable auto-provisioning.
	fakes.provider.AutoProvision = false

	sa := fakes.seedSAInTenant(fakes.tenantID, "deploy-bot")
	syntheticEmail := "sa+" + sa.ID.String() + "@internal.invalid"

	_, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         syntheticEmail,
		EmailVerified: true,
		Subject:       "idp-sub-xyz",
	})

	require.Error(t, err, "must reject synthetic email regardless of auto_provision flag")
	require.Contains(t, err.Error(), "reserved",
		"must indicate reserved domain, not auto-provision error")
}

// TestSSO_HumanEmailStillWorks is a smoke test confirming that normal human
// emails still flow through EnsureSSOUser correctly after the Task 10 changes.
// Auto-provision path is exercised because no human user with this email exists
// in the fake store.
func TestSSO_HumanEmailStillWorks(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	// Pick an email that is guaranteed to not exist in the user repo.
	humanEmail := "new-sso-user@example.com"

	user, roles, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         humanEmail,
		EmailVerified: true,
		DisplayName:   "New SSO User",
		Subject:       "google-sub-123",
	})

	require.NoError(t, err, "normal human email must succeed when auto_provision=true")
	require.NotNil(t, user, "must return the provisioned user")
	require.Equal(t, humanEmail, user.Email, "provisioned user email must match IdP identity")
	// roles may be empty (fake GrantRole is a no-op + fake GetUserRoles returns nil)
	_ = roles
}

// TestSSO_UnverifiedEmailRefused verifies the existing EmailVerified gate
// is unaffected by Task 10 changes.
func TestSSO_UnverifiedEmailRefused(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	_, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         "unverified@example.com",
		EmailVerified: false,
		Subject:       "idp-sub-unverified",
	})

	require.ErrorIs(t, err, ErrEmailNotVerified,
		"unverified email must still return ErrEmailNotVerified")
}
