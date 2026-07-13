package service

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// FE-API-034 — SSO admin-config service methods.
//
// UpsertProvider validates + persists an OAuth provider, sealing the client
// secret under the SSO credential key; an empty secret on update preserves the
// stored ciphertext. ListAllProviders returns enabled + disabled providers with
// the ciphertext stripped and a HasSecret flag. SAML editing is deferred.

// freshAdmin returns an SSO service with an empty provider store (the harness
// seeds a default "google" row we don't want interfering with admin CRUD).
func freshAdmin(t *testing.T) (*SSO, *ssoFakes) {
	t.Helper()
	sso, f := newSSOService(t)
	f.globalRepo.Providers = map[string]*repository.GlobalSSOProvider{}
	return sso, f
}

func TestUpsertProvider_createOAuth_sealsSecret(t *testing.T) {
	sso, f := freshAdmin(t)
	ctx := context.Background()

	view, err := sso.UpsertProvider(ctx, UpsertProviderInput{
		ProviderID:    "github",
		Kind:          "oauth_github",
		DisplayName:   "GitHub",
		Enabled:       true,
		OAuthClientID: "client-abc",
		ClientSecret:  "s3cr3t",
		AutoProvision: true,
	})
	require.NoError(t, err)
	require.Equal(t, "github", view.ProviderID)
	require.True(t, view.HasSecret, "created provider must report a secret")
	require.True(t, view.Enabled)

	// The stored ciphertext must decrypt back to the plaintext, and the plaintext
	// must never appear on the returned view.
	stored := f.globalRepo.Providers["github"]
	require.NotEmpty(t, stored.OAuthClientSecretEnc)
	pt, err := sso.DecryptClientSecret(stored)
	require.NoError(t, err)
	require.Equal(t, "s3cr3t", pt)
}

func TestUpsertProvider_updateEmptySecret_keepsExisting(t *testing.T) {
	sso, f := freshAdmin(t)
	ctx := context.Background()

	_, err := sso.UpsertProvider(ctx, UpsertProviderInput{
		ProviderID: "github", Kind: "oauth_github", DisplayName: "GitHub",
		Enabled: true, OAuthClientID: "client-abc", ClientSecret: "original",
	})
	require.NoError(t, err)
	originalEnc := f.globalRepo.Providers["github"].OAuthClientSecretEnc

	// Update with an empty secret — display name changes, secret is preserved.
	view, err := sso.UpsertProvider(ctx, UpsertProviderInput{
		ProviderID: "github", Kind: "oauth_github", DisplayName: "GitHub Enterprise",
		Enabled: true, OAuthClientID: "client-abc", ClientSecret: "",
	})
	require.NoError(t, err)
	require.True(t, view.HasSecret)
	require.Equal(t, "GitHub Enterprise", view.DisplayName)
	require.Equal(t, originalEnc, f.globalRepo.Providers["github"].OAuthClientSecretEnc,
		"empty secret on update must preserve the stored ciphertext")
}

func TestUpsertProvider_createWithoutSecret_errors(t *testing.T) {
	sso, _ := freshAdmin(t)
	_, err := sso.UpsertProvider(context.Background(), UpsertProviderInput{
		ProviderID: "github", Kind: "oauth_github", DisplayName: "GitHub",
		Enabled: true, OAuthClientID: "client-abc", ClientSecret: "",
	})
	require.ErrorIs(t, err, ErrClientSecretRequired)
}

func TestUpsertProvider_rejectsSAML(t *testing.T) {
	sso, _ := freshAdmin(t)
	_, err := sso.UpsertProvider(context.Background(), UpsertProviderInput{
		ProviderID: "okta", Kind: "saml", DisplayName: "Okta",
	})
	require.ErrorIs(t, err, ErrSAMLNotEditable)
}

func TestUpsertProvider_genericRequiresHTTPSIssuer(t *testing.T) {
	sso, _ := freshAdmin(t)
	base := UpsertProviderInput{
		ProviderID: "corp", Kind: "oauth_generic", DisplayName: "Corp SSO",
		OAuthClientID: "cid", ClientSecret: "sec",
	}
	// Missing issuer.
	_, err := sso.UpsertProvider(context.Background(), base)
	require.ErrorIs(t, err, ErrProviderConfigInvalid)
	// Plain http issuer.
	base.OAuthIssuerURL = "http://idp.corp.example"
	_, err = sso.UpsertProvider(context.Background(), base)
	require.ErrorIs(t, err, ErrProviderConfigInvalid)
	// Valid https issuer.
	base.OAuthIssuerURL = "https://idp.corp.example"
	_, err = sso.UpsertProvider(context.Background(), base)
	require.NoError(t, err)
}

func TestUpsertProvider_invalidProviderID(t *testing.T) {
	sso, _ := freshAdmin(t)
	for _, bad := range []string{"", "A", "1google", "has space", "has_underscore"} {
		_, err := sso.UpsertProvider(context.Background(), UpsertProviderInput{
			ProviderID: bad, Kind: "oauth_google", DisplayName: "X",
			OAuthClientID: "cid", ClientSecret: "sec",
		})
		require.ErrorIsf(t, err, ErrProviderConfigInvalid, "provider_id %q must be rejected", bad)
	}
}

func TestListAllProviders_stripsSecret_includesDisabled(t *testing.T) {
	sso, _ := freshAdmin(t)
	ctx := context.Background()

	_, err := sso.UpsertProvider(ctx, UpsertProviderInput{
		ProviderID: "github", Kind: "oauth_github", DisplayName: "GitHub",
		Enabled: true, OAuthClientID: "cid", ClientSecret: "sec",
	})
	require.NoError(t, err)
	_, err = sso.UpsertProvider(ctx, UpsertProviderInput{
		ProviderID: "google", Kind: "oauth_google", DisplayName: "Google",
		Enabled: false, OAuthClientID: "cid2", ClientSecret: "sec2",
	})
	require.NoError(t, err)

	views, err := sso.ListAllProviders(ctx)
	require.NoError(t, err)
	require.Len(t, views, 2, "admin list includes disabled providers")
	for _, v := range views {
		require.True(t, v.HasSecret, "%s should report has_secret", v.ProviderID)
	}
}

func TestDeleteProvider(t *testing.T) {
	sso, _ := freshAdmin(t)
	ctx := context.Background()
	_, err := sso.UpsertProvider(ctx, UpsertProviderInput{
		ProviderID: "github", Kind: "oauth_github", DisplayName: "GitHub",
		Enabled: true, OAuthClientID: "cid", ClientSecret: "sec",
	})
	require.NoError(t, err)

	require.NoError(t, sso.DeleteProvider(ctx, "github"))
	require.True(t, errors.Is(sso.DeleteProvider(ctx, "github"), repository.ErrNotFound),
		"deleting a missing provider returns ErrNotFound")
}
