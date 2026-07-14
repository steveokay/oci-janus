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
	"time"

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

func (f *fakeGlobalSSOConfigRepo) Upsert(_ context.Context, p *repository.GlobalSSOProvider) (*repository.GlobalSSOProvider, error) {
	cp := *p
	f.Providers[p.ProviderID] = &cp
	out := cp
	return &out, nil
}

func (f *fakeGlobalSSOConfigRepo) Delete(_ context.Context, providerID string) error {
	if _, ok := f.Providers[providerID]; !ok {
		return repository.ErrNotFound
	}
	delete(f.Providers, providerID)
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
	*authFakes                                // embeds userRepo, keyRepo, saRepo, audit fakes
	globalRepo  *fakeGlobalSSOConfigRepo      // fake global SSO config store
	sessionRepo *fakeSSOSessionRepo           // no-op login-session store
	tenantID    uuid.UUID                     // default tenant used for EnsureSSOUser
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

// ── REDESIGN-001 Phase 5.5: SSO subject-id binding ─────────────────────────────

// TestSSO_AutoProvision_PersistsSubject verifies that a first-time SSO login
// auto-provisions the user and persists the IdP's stable subject identifier
// onto the new row. Without this, the next login would fall back to the email
// path and lose the recycled-email defence.
func TestSSO_AutoProvision_PersistsSubject(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	const subject = "google-sub-NEW-001"
	user, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         "newhire@example.com",
		EmailVerified: true,
		DisplayName:   "New Hire",
		Subject:       subject,
	}, fakes.tenantID)

	require.NoError(t, err, "auto-provision must succeed on first login")
	require.NotNil(t, user)
	require.Equal(t, subject, user.SSOSubject,
		"provisioned user must record the IdP subject for future subject-keyed lookups")
}

// TestSSO_ReturningUser_MatchedBySubject verifies that the second login for an
// already-provisioned account is matched on (provider, subject) rather than
// email. The test changes the user's email to something unrelated and asserts
// the lookup still resolves to the same row — proving the subject path is
// primary.
func TestSSO_ReturningUser_MatchedBySubject(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	const subject = "google-sub-RETURNING-001"
	const provisioningEmail = "alice@example.com"

	// First login: provision the account.
	first, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         provisioningEmail,
		EmailVerified: true,
		Subject:       subject,
	}, fakes.tenantID)
	require.NoError(t, err)
	require.NotNil(t, first)

	// Mutate the persisted email to simulate the user updating their primary
	// IdP address — the subject must still uniquely identify them.
	first.Email = "alice-new@example.com"

	// Second login: same provider/subject pair but with an updated email
	// claim. Must resolve to the same user row via the subject lookup.
	second, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         "alice-renamed@example.com", // intentionally different
		EmailVerified: true,
		Subject:       subject,
	}, fakes.tenantID)
	require.NoError(t, err, "returning subject must resolve regardless of email")
	require.NotNil(t, second)
	require.Equal(t, first.ID, second.ID,
		"subject-keyed lookup must return the originally provisioned user")
}

// TestSSO_RecycledEmail_Rejected is the core regression test for the bug
// described in the REDESIGN-001 Phase 5.5 plan: an existing account is bound
// to subject A, but a new IdP login claims the same email with subject B
// (e.g. the email was reassigned to a new hire after the original owner
// left). The login MUST be rejected with an operator-actionable message
// rather than silently rebinding the existing account.
func TestSSO_RecycledEmail_Rejected(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	const originalSubject = "google-sub-ORIGINAL"
	const recycledEmail = "recycled@example.com"

	// First login: provision the account with the original subject.
	first, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         recycledEmail,
		EmailVerified: true,
		Subject:       originalSubject,
	}, fakes.tenantID)
	require.NoError(t, err)
	require.NotNil(t, first)

	// Second login: same email, but the IdP asserts a different subject —
	// the new hire's IdP account. Must be refused.
	_, _, err = sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         recycledEmail,
		EmailVerified: true,
		Subject:       "google-sub-NEW-HIRE",
	}, fakes.tenantID)
	require.Error(t, err, "subject mismatch on same email must be rejected")
	require.ErrorIs(t, err, ErrSSOSubjectMismatch,
		"must surface as ErrSSOSubjectMismatch so handlers can map to a clean 401")
	// SEC-042 — the rejection message is intentionally generic: it no longer
	// echoes the email back ("An account exists for `<email>`...") because
	// that gave an attacker controlling a self-hosted IdP a probe for which
	// addresses are registered. The generic phrasing must still be
	// operator-actionable.
	require.Contains(t, err.Error(), "contact your admin",
		"error must be operator-actionable")
	require.NotContains(t, err.Error(), recycledEmail,
		"SEC-042: rejection message must not leak the email back to the caller")
}

// TestSSO_PreMigrationUser_BackfillsSubject verifies that an existing user
// row with NULL sso_subject (i.e. provisioned before this migration) is
// transparently back-filled on first SSO login. The login succeeds and the
// next call routes through the subject-keyed fast path.
func TestSSO_PreMigrationUser_BackfillsSubject(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	// Seed a user directly — no SSOSubject set, mirroring a pre-Phase-5.5 row.
	const email = "legacy@example.com"
	const subject = "google-sub-BACKFILL"
	legacy := &repository.User{
		ID:        uuid.New(),
		TenantID:  fakes.tenantID,
		Username:  "legacy",
		Email:     email,
		IsActive:  true,
		Kind:      "human",
		CreatedAt: time.Now(),
	}
	fakes.userRepo.users[legacy.Username] = legacy

	// First SSO login post-migration. Should match by email, back-fill the
	// subject, and succeed.
	matched, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         email,
		EmailVerified: true,
		Subject:       subject,
	}, fakes.tenantID)
	require.NoError(t, err, "back-fill path must succeed without surfacing an error")
	require.Equal(t, legacy.ID, matched.ID, "must match the existing legacy row")
	require.Equal(t, subject, legacy.SSOSubject,
		"sso_subject must be back-filled on the in-memory row after first login")

	// The next login must take the subject-keyed fast path. Pair the binding
	// in the side map so GetUserBySSOSubject can find it (the fake's SetSSOSubject
	// only writes the column; the real index update is implicit in production).
	fakes.userRepo.bindSSOSubject(legacy, fakes.provider.ProviderID, subject)

	again, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         "doesnt-matter@example.com",
		EmailVerified: true,
		Subject:       subject,
	}, fakes.tenantID)
	require.NoError(t, err)
	require.Equal(t, legacy.ID, again.ID,
		"post-backfill login must resolve via subject regardless of email")
}

// TestSSO_DifferentProvider_SameSubject_Distinct verifies that the composite
// key (provider, subject) treats the same subject value across two providers
// as distinct identities. Subject strings are only unique within a single
// IdP; two IdPs may both happen to assign `sub=12345` to unrelated accounts.
func TestSSO_DifferentProvider_SameSubject_Distinct(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	// Seed a second provider so we can route a login through a non-default
	// provider id and confirm the subject lookup is provider-scoped.
	const otherProviderID = "okta_saml"
	otherProvider := &repository.GlobalSSOProvider{
		ProviderID:    otherProviderID,
		Kind:          "saml",
		DisplayName:   "Okta (test)",
		Enabled:       true,
		AutoProvision: true,
	}
	fakes.globalRepo.Providers[otherProviderID] = otherProvider

	const sharedSubject = "12345"

	// First login on provider A (Google) — provisions one account.
	a, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         "a@example.com",
		EmailVerified: true,
		Subject:       sharedSubject,
	}, fakes.tenantID)
	require.NoError(t, err)

	// First login on provider B (Okta) with the same subject value — must
	// auto-provision a new distinct account, not rebind the Google one.
	b, _, err := sso.EnsureSSOUser(ctx, otherProvider, SSOIdentity{
		Email:         "b@example.com",
		EmailVerified: true,
		Subject:       sharedSubject,
	}, fakes.tenantID)
	require.NoError(t, err)
	require.NotEqual(t, a.ID, b.ID,
		"same subject value from a different provider must produce a different account")
}

// TestSSO_SEC040_TenantFilterOnSubjectLookup is the regression test for
// SEC-040 — the Phase 5.5 partial index was missing tenant_id, so a recycled
// IdP subject reachable from two tenants sharing one provider could resolve to
// a user in the wrong tenant. The lookup is now (tenant_id, provider, subject);
// a subject that exists in tenant A must NOT be visible to an EnsureSSOUser
// call scoped to tenant B. The tenant_id column stays a defence-in-depth
// boundary even though the platform hosts a single tenant (ADR-0031).
func TestSSO_SEC040_TenantFilterOnSubjectLookup(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	const sharedSubject = "google-sub-cross-tenant"
	const sharedEmail = "alice@example.com"

	// First login: provision Alice in tenant A.
	tenantA := fakes.tenantID
	a, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         sharedEmail,
		EmailVerified: true,
		Subject:       sharedSubject,
	}, tenantA)
	require.NoError(t, err)
	require.Equal(t, tenantA, a.TenantID, "tenant A user must be in tenant A")

	// Second login: same IdP, same subject, same email — but the callback is
	// scoped to tenant B. With SEC-040's tenant filter, the subject lookup
	// in Step 1 must miss; the auto-provision path then creates a SECOND
	// distinct user in tenant B. Without the filter, the lookup would
	// resolve to Alice in tenant A and return a session for the wrong
	// tenant.
	tenantB := uuid.New()
	b, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         sharedEmail,
		EmailVerified: true,
		Subject:       sharedSubject,
	}, tenantB)
	require.NoError(t, err)
	require.NotEqual(t, a.ID, b.ID,
		"SEC-040: subject lookup must be tenant-scoped — the same (provider, subject) in a different tenant must NOT resolve to the tenant-A user")
	require.Equal(t, tenantB, b.TenantID,
		"the freshly provisioned tenant-B user must belong to tenant B")
}

// TestSSO_SEC041_RaceRecoveryRefusesSubjectMismatch is the regression test
// for SEC-041 — when CreateSSOUser loses the unique-index race AND the
// subject-keyed lookup misses (subject was never persisted on the
// race-winning row), the recovery path falls back to GetHumanByEmail. The
// recovered row may carry a DIFFERENT sso_subject already; accepting it
// blind would hand the caller a session for an identity the IdP did not
// assert in this request.
//
// We simulate the race outcome by seeding a row that has the email AND a
// subject that differs from the one EnsureSSOUser is asserting now, then
// rigging the CreateSSOUser fake to surface ErrAlreadyExists. The
// subject-lookup will miss (different subject) and the email-fallback
// path must refuse with ErrSSOSubjectMismatch + the generic SEC-042
// message rather than handing back the wrong row.
func TestSSO_SEC041_RaceRecoveryRefusesSubjectMismatch(t *testing.T) {
	ctx := context.Background()
	sso, fakes := newSSOService(t)

	const sharedEmail = "race@example.com"
	const previouslyBoundSubject = "google-sub-race-winner"
	const newCallerSubject = "google-sub-race-loser"

	// Seed the race-winning row: same email, but a different subject. The
	// real DB would have a unique-index on email so the next
	// CreateSSOUser would surface ErrAlreadyExists. The fake mirrors that
	// via the failCreateSSOWith hook.
	racePartner := &repository.User{
		ID:         uuid.New(),
		TenantID:   fakes.tenantID,
		Username:   "race",
		Email:      sharedEmail,
		Kind:       "human",
		IsActive:   true,
		SSOSubject: previouslyBoundSubject,
		CreatedAt:  time.Now(),
	}
	fakes.userRepo.addUser(racePartner)
	fakes.userRepo.bindSSOSubject(racePartner, fakes.provider.ProviderID, previouslyBoundSubject)
	fakes.userRepo.failCreateSSOWith = repository.ErrAlreadyExists

	// Second caller arrives asserting newCallerSubject. The subject lookup
	// must miss (we bound previouslyBoundSubject, not newCallerSubject).
	// CreateSSOUser then trips ErrAlreadyExists (rigged above), the
	// subject-fallback again misses, and the email-fallback finds the row
	// — at which point the SEC-041 mismatch check must fire.
	_, _, err := sso.EnsureSSOUser(ctx, fakes.provider, SSOIdentity{
		Email:         sharedEmail,
		EmailVerified: true,
		Subject:       newCallerSubject,
	}, fakes.tenantID)

	require.Error(t, err, "race-recovery via email with a mismatched subject must be rejected")
	require.ErrorIs(t, err, ErrSSOSubjectMismatch,
		"SEC-041: rejection must surface as ErrSSOSubjectMismatch so handlers map to a clean 401")
	require.Contains(t, err.Error(), "contact your admin",
		"SEC-042: race-recovery rejection must use the same generic, non-enumerating phrasing")
	require.NotContains(t, err.Error(), sharedEmail,
		"SEC-042: race-recovery rejection must not leak the email")
}
