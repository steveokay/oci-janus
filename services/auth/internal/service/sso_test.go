// REDESIGN-001 RM-003 — SSO service tests.
//
// Changes from FE-API-034 / FE-API-048 Task 10:
//   - fakeSSOProviderRepo replaced by fakeGlobalSSOConfigRepo (implements globalSSOConfigRepo).
//   - ssoFakes.provider is now *repository.GlobalSSOProvider.
//   - EnsureSSOUser now takes (ctx, *GlobalSSOProvider, SSOIdentity, tenantID).
//   - Stable string ProviderID instead of UUID.
package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── Minimal fakes for global SSO config + login-session repos ─────────────────

// fakeGlobalSSOConfigRepo satisfies globalSSOConfigRepo using an in-memory map.
// Tests seed providers via the Providers field directly.
type fakeGlobalSSOConfigRepo struct {
	Providers map[string]*repository.GlobalSSOProvider
}

func newFakeGlobalSSOConfigRepo() *fakeGlobalSSOConfigRepo {
	return &fakeGlobalSSOConfigRepo{Providers: make(map[string]*repository.GlobalSSOProvider)}
}

func (f *fakeGlobalSSOConfigRepo) Get(_ context.Context, providerID string) (*repository.GlobalSSOProvider, error) {
	p, ok := f.Providers[providerID]
	if !ok {
		return nil, repository.ErrNotFound
	}
	out := *p
	return &out, nil
}

func (f *fakeGlobalSSOConfigRepo) List(_ context.Context, enabledOnly bool) ([]*repository.GlobalSSOProvider, error) {
	out := make([]*repository.GlobalSSOProvider, 0, len(f.Providers))
	for _, p := range f.Providers {
		if enabledOnly && !p.Enabled {
			continue
		}
		cp := *p
		out = append(out, &cp)
	}
	return out, nil
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
	*authFakes                               // embeds userRepo, keyRepo, saRepo, audit fakes
	globalRepo  *fakeGlobalSSOConfigRepo    // fake global SSO config store
	sessionRepo *fakeSSOSessionRepo          // no-op login-session store
	tenantID    uuid.UUID                    // default tenant used for EnsureSSOUser
	provider    *repository.GlobalSSOProvider // convenience: the default seeded provider
}

// newSSOService builds a *SSO and its supporting *authFakes for unit tests.
// It seeds one auto-provisioning-enabled OAuth provider in a fresh tenant.
//
// REDESIGN-001 RM-003: uses GlobalSSOProvider with string providerID.
func newSSOService(t *testing.T) (*SSO, *ssoFakes) {
	t.Helper()

	// Build the base *Service with all user / key / SA fakes wired in.
	svc, af := newAuthService(t, context.Background())

	// Build SSO-specific fakes.
	globalRepo := newFakeGlobalSSOConfigRepo()
	sessionRepo := &fakeSSOSessionRepo{}

	// NewSSO requires a 32-byte credential key (AES-256).
	credKey := make([]byte, 32)
	sso, err := NewSSO(svc, globalRepo, sessionRepo, credKey)
	require.NoError(t, err, "NewSSO must succeed with a valid 32-byte key")

	tenantID := uuid.New()
	sso.WithDefaultTenantID(tenantID)

	// Seed a default Google OAuth provider with auto_provision=true so most
	// tests do not need to set up their own provider.
	const defaultProviderID = "google"
	provider := &repository.GlobalSSOProvider{
		ProviderID:    defaultProviderID,
		Kind:          "oauth_google",
		DisplayName:   "Google (test)",
		Enabled:       true,
		AutoProvision: true,
	}
	globalRepo.Providers[defaultProviderID] = provider

	fakes := &ssoFakes{
		authFakes:   af,
		globalRepo:  globalRepo,
		sessionRepo: sessionRepo,
		tenantID:    tenantID,
		provider:    provider,
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
		"sa@example.com",        // "sa" prefix but not the full synthetic format
		"sa+abc@example.com",    // wrong domain
		"user@internal.invalid", // right domain but no "sa+" prefix
	}
	for _, email := range humanEmails {
		require.False(t, isSyntheticSAEmail(email),
			"isSyntheticSAEmail(%q) should be false", email)
	}
}

// ── Task 10 regression: shadow email must never authenticate ─────────────────

// TestSSO_RefusesShadowEmail verifies that EnsureSSOUser rejects an identity
// whose email matches the synthetic SA email format, even when the shadow user
// row exists in the database.
func TestSSO_RefusesShadowEmail(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	// Seed a service account in the same tenant as the provider.
	sa := fakes.seedSAInTenant(fakes.tenantID, "ci-prod")

	// Derive the synthetic email that the shadow user row holds in the DB.
	syntheticEmail := "sa+" + sa.ID.String() + "@internal.invalid"

	// Simulate an IdP returning the synthetic email.
	// REDESIGN-001 RM-003: EnsureSSOUser now takes (ctx, *GlobalSSOProvider, SSOIdentity, tenantID).
	_, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         syntheticEmail,
		EmailVerified: true,
		DisplayName:   "Should Not Pass",
		Subject:       "idp-sub-abc",
	}, fakes.tenantID)

	// Must be refused with a message indicating the domain is reserved.
	require.Error(t, err, "must reject synthetic email")
	require.Contains(t, err.Error(), "reserved",
		"error must mention that the email domain is reserved for service accounts")
}

// TestSSO_RefusesShadowEmail_AutoProvisionDisabled verifies that the synthetic
// email guard fires even when auto_provision=false on the provider.
func TestSSO_RefusesShadowEmail_AutoProvisionDisabled(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	// Override the default provider to disable auto-provisioning.
	fakes.provider.AutoProvision = false
	fakes.globalRepo.Providers[fakes.provider.ProviderID] = fakes.provider

	sa := fakes.seedSAInTenant(fakes.tenantID, "deploy-bot")
	syntheticEmail := "sa+" + sa.ID.String() + "@internal.invalid"

	_, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         syntheticEmail,
		EmailVerified: true,
		Subject:       "idp-sub-xyz",
	}, fakes.tenantID)

	require.Error(t, err, "must reject synthetic email regardless of auto_provision flag")
	require.Contains(t, err.Error(), "reserved",
		"must indicate reserved domain, not auto-provision error")
}

// TestSSO_HumanEmailStillWorks is a smoke test confirming that normal human
// emails still flow through EnsureSSOUser correctly after the RM-003 changes.
// Auto-provision path is exercised because no human user with this email exists
// in the fake store.
func TestSSO_HumanEmailStillWorks(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	// Pick an email that is guaranteed not to exist in the user repo.
	humanEmail := "new-sso-user@example.com"

	user, roles, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         humanEmail,
		EmailVerified: true,
		DisplayName:   "New SSO User",
		Subject:       "google-sub-123",
	}, fakes.tenantID)

	require.NoError(t, err, "normal human email must succeed when auto_provision=true")
	require.NotNil(t, user, "must return the provisioned user")
	require.Equal(t, humanEmail, user.Email, "provisioned user email must match IdP identity")
	// roles may be empty (fake GrantRole is a no-op + fake GetUserRoles returns nil)
	_ = roles
}

// TestSSO_UnverifiedEmailRefused verifies the existing EmailVerified gate
// is unaffected by RM-003 changes.
func TestSSO_UnverifiedEmailRefused(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	_, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         "unverified@example.com",
		EmailVerified: false,
		Subject:       "idp-sub-unverified",
	}, fakes.tenantID)

	require.ErrorIs(t, err, ErrEmailNotVerified,
		"unverified email must still return ErrEmailNotVerified")
}
