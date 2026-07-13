package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/steveokay/oci-janus/libs/crypto/aes"
	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// FE-API-034 — SSO admin-config service.
//
// A global-admin editable surface over global_sso_config (deployment-wide, not
// per-tenant — the per-tenant admin surface was the RM-003 §A1 gate flaw). v1
// covers the OAuth/OIDC kinds; SAML editing stays env/SQL-only. The OAuth
// client secret is sealed under the SSO credential key and is write-only: it is
// never returned, and an empty secret on update preserves the stored value.

// Sentinel errors surfaced to the HTTP layer (mapped to 400).
var (
	// ErrProviderConfigInvalid is returned for a malformed provider payload
	// (bad id, unknown kind, missing required field, non-https issuer).
	ErrProviderConfigInvalid = errors.New("invalid provider configuration")
	// ErrClientSecretRequired is returned when creating an OAuth provider
	// without a client secret (there is nothing to fall back to).
	ErrClientSecretRequired = errors.New("oauth client secret required")
	// ErrSAMLNotEditable is returned when the admin surface is asked to write a
	// SAML provider — deferred to a follow-up; configure SAML via env/SQL.
	ErrSAMLNotEditable = errors.New("SAML providers are not editable from the dashboard yet")
)

// providerIDPattern bounds provider_id to a URL-safe slug: lowercase, starts
// with a letter, 2–64 chars. Matches the stable ids the login flow expects
// ("google", "github", "corp-okta", …).
var providerIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)

// UpsertProviderInput is the admin-supplied provider configuration. ClientSecret
// is plaintext; an empty value on update preserves the stored ciphertext.
type UpsertProviderInput struct {
	ProviderID     string
	Kind           string
	DisplayName    string
	Enabled        bool
	OAuthClientID  string
	OAuthIssuerURL string
	OAuthScopes    []string
	ClientSecret   string
	AutoProvision  bool
}

// AdminProviderView is the admin-facing projection of a provider row. The
// client secret ciphertext never leaves the service — HasSecret reports its
// presence instead.
type AdminProviderView struct {
	ProviderID     string
	Kind           string
	DisplayName    string
	Enabled        bool
	OAuthClientID  string
	OAuthIssuerURL string
	OAuthScopes    []string
	HasSecret      bool
	AutoProvision  bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func toAdminView(p *repository.GlobalSSOProvider) AdminProviderView {
	return AdminProviderView{
		ProviderID:     p.ProviderID,
		Kind:           p.Kind,
		DisplayName:    p.DisplayName,
		Enabled:        p.Enabled,
		OAuthClientID:  p.OAuthClientID,
		OAuthIssuerURL: p.OAuthIssuerURL,
		OAuthScopes:    p.OAuthScopes,
		HasSecret:      len(p.OAuthClientSecretEnc) > 0,
		AutoProvision:  p.AutoProvision,
		CreatedAt:      p.CreatedAt,
		UpdatedAt:      p.UpdatedAt,
	}
}

// ListAllProviders returns every configured provider (enabled + disabled) for
// the admin surface, with the secret ciphertext replaced by a HasSecret flag.
func (s *SSO) ListAllProviders(ctx context.Context) ([]AdminProviderView, error) {
	ps, err := s.providers.List(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]AdminProviderView, 0, len(ps))
	for _, p := range ps {
		out = append(out, toAdminView(p))
	}
	return out, nil
}

// UpsertProvider validates + persists an OAuth provider configuration. The
// client secret is sealed under the SSO credential key; an empty ClientSecret
// on update preserves the stored ciphertext (creating without one is rejected).
// Returns the canonical row as an AdminProviderView (secret stripped).
func (s *SSO) UpsertProvider(ctx context.Context, in UpsertProviderInput) (*AdminProviderView, error) {
	if !providerIDPattern.MatchString(in.ProviderID) {
		return nil, fmt.Errorf("%w: provider_id must match %s", ErrProviderConfigInvalid, providerIDPattern.String())
	}
	kind := repository.AuthProviderType(in.Kind)
	if kind == repository.AuthProviderSAML {
		return nil, ErrSAMLNotEditable
	}
	if !kind.IsOAuth() {
		return nil, fmt.Errorf("%w: unknown kind %q", ErrProviderConfigInvalid, in.Kind)
	}
	if name := strings.TrimSpace(in.DisplayName); name == "" || len(in.DisplayName) > 128 {
		return nil, fmt.Errorf("%w: display_name is required and must be <= 128 chars", ErrProviderConfigInvalid)
	}
	if strings.TrimSpace(in.OAuthClientID) == "" {
		return nil, fmt.Errorf("%w: oauth_client_id is required", ErrProviderConfigInvalid)
	}
	if kind == repository.AuthProviderOAuthGeneric && !isHTTPSURL(in.OAuthIssuerURL) {
		return nil, fmt.Errorf("%w: oauth_issuer_url must be an https URL for generic OIDC", ErrProviderConfigInvalid)
	}

	// Resolve the secret ciphertext: seal a new one, or keep the existing one
	// on update. Distinguish create vs update via the existing row.
	existing, err := s.providers.Get(ctx, in.ProviderID)
	creating := errors.Is(err, repository.ErrNotFound)
	if err != nil && !creating {
		return nil, err
	}
	var secretEnc []byte
	switch {
	case in.ClientSecret != "":
		ct, encErr := aes.Encrypt([]byte(in.ClientSecret), s.credentialKey)
		if encErr != nil {
			return nil, fmt.Errorf("seal client secret: %w", encErr)
		}
		secretEnc = ct
	case creating:
		return nil, ErrClientSecretRequired
	default:
		secretEnc = existing.OAuthClientSecretEnc
	}

	// oauth_scopes is a NOT NULL column and the repo binds it explicitly, so a
	// nil slice would violate the constraint (23502). Persist a non-nil empty
	// array instead — the login flow falls back to per-kind default scopes when
	// the stored array is empty, so this preserves the intended behaviour.
	scopes := in.OAuthScopes
	if scopes == nil {
		scopes = []string{}
	}
	row := &repository.GlobalSSOProvider{
		ProviderID:           in.ProviderID,
		Kind:                 in.Kind,
		DisplayName:          in.DisplayName,
		Enabled:              in.Enabled,
		OAuthClientID:        in.OAuthClientID,
		OAuthClientSecretEnc: secretEnc,
		OAuthIssuerURL:       in.OAuthIssuerURL,
		OAuthScopes:          scopes,
		AutoProvision:        in.AutoProvision,
	}
	saved, err := s.providers.Upsert(ctx, row)
	if err != nil {
		return nil, err
	}
	view := toAdminView(saved)
	return &view, nil
}

// DeleteProvider removes a provider by id, returning repository.ErrNotFound when
// no such provider exists.
func (s *SSO) DeleteProvider(ctx context.Context, providerID string) error {
	return s.providers.Delete(ctx, providerID)
}

// isHTTPSURL reports whether raw parses as an absolute https URL with a host.
func isHTTPSURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.Host != ""
}
