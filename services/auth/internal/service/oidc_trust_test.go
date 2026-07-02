// Package service — oidc_trust_test.go covers OIDCTrustService CRUD and
// the ExchangeWorkloadToken flow with in-memory fakes. Integration tests
// against a real Postgres live in repository/oidc_trust_test.go; here we
// focus on the validation branches and the 7 reject reasons.

package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/services/auth/internal/repository"
)

// ── fake trustRepo ────────────────────────────────────────────────────

// fakeTrustRepo is an in-memory implementation of trustRepo. Tests use
// it to exercise the service without booting Postgres.
type fakeTrustRepo struct {
	trusts map[uuid.UUID]*repository.OIDCTrust
}

func newFakeTrustRepo() *fakeTrustRepo {
	return &fakeTrustRepo{trusts: make(map[uuid.UUID]*repository.OIDCTrust)}
}

func (f *fakeTrustRepo) Create(_ context.Context, in repository.OIDCTrust) (*repository.OIDCTrust, error) {
	// UNIQUE (tenant_id, issuer_url, subject_pattern) — emulate the DB
	// constraint so the service-level "duplicate" branch is reachable.
	for _, t := range f.trusts {
		if t.TenantID == in.TenantID && t.IssuerURL == in.IssuerURL && t.SubjectPattern == in.SubjectPattern {
			return nil, repository.ErrAlreadyExists
		}
	}
	id := uuid.New()
	now := time.Now()
	row := in
	row.ID = id
	row.CreatedAt = now
	row.UpdatedAt = now
	if row.JWKSCacheTTLSeconds == 0 {
		row.JWKSCacheTTLSeconds = 3600
	}
	f.trusts[id] = &row
	return &row, nil
}

func (f *fakeTrustRepo) GetByID(_ context.Context, tenantID, id uuid.UUID) (*repository.OIDCTrust, error) {
	t, ok := f.trusts[id]
	if !ok || t.TenantID != tenantID {
		return nil, repository.ErrNotFound
	}
	return t, nil
}

func (f *fakeTrustRepo) List(_ context.Context, tenantID uuid.UUID) ([]*repository.OIDCTrust, error) {
	var out []*repository.OIDCTrust
	for _, t := range f.trusts {
		if t.TenantID == tenantID {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeTrustRepo) ListByIssuer(_ context.Context, issuerURL string) ([]*repository.OIDCTrust, error) {
	var out []*repository.OIDCTrust
	for _, t := range f.trusts {
		if t.IssuerURL == issuerURL {
			out = append(out, t)
		}
	}
	return out, nil
}

func (f *fakeTrustRepo) Update(_ context.Context, in repository.OIDCTrust) (*repository.OIDCTrust, error) {
	t, ok := f.trusts[in.ID]
	if !ok || t.TenantID != in.TenantID {
		return nil, repository.ErrNotFound
	}
	t.DisplayName = in.DisplayName
	t.SubjectPattern = in.SubjectPattern
	if in.JWKSCacheTTLSeconds > 0 {
		t.JWKSCacheTTLSeconds = in.JWKSCacheTTLSeconds
	}
	t.UpdatedAt = time.Now()
	return t, nil
}

func (f *fakeTrustRepo) Delete(_ context.Context, tenantID, id uuid.UUID) error {
	t, ok := f.trusts[id]
	if !ok || t.TenantID != tenantID {
		return repository.ErrNotFound
	}
	delete(f.trusts, id)
	return nil
}

func (f *fakeTrustRepo) MarkUsed(_ context.Context, id uuid.UUID) error {
	t, ok := f.trusts[id]
	if !ok {
		return repository.ErrNotFound
	}
	now := time.Now()
	t.LastUsedAt = &now
	return nil
}

// ── helpers ───────────────────────────────────────────────────────────

// newTrustServiceFakes builds an OIDCTrustService backed by in-memory
// fakes + a real auth.Service constructed with a 1-key ring. Reuses
// newSAService to get a miniredis-backed *redis.Client so the auth
// Service's recordIssuedJTI call succeeds.
func newTrustServiceFakes(t *testing.T, allowedIssuers []string) (*OIDCTrustService, *fakeTrustRepo, *fakeSARepo, *capturingAuditEmitter) {
	t.Helper()

	_, saFakes := newSAService(t)
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	pubB64 := base64.StdEncoding.EncodeToString(testPublicKeyPEM(t, &priv.PublicKey))
	privB64 := base64.StdEncoding.EncodeToString(testPrivateKeyPEM(t, priv))
	authSvc, err := New(nil, nil, nil, nil, saFakes.rdb, privB64, pubB64, "test-kid")
	require.NoError(t, err)

	trustRepo := newFakeTrustRepo()
	audit := &capturingAuditEmitter{}
	svc := NewOIDCTrustService(trustRepo, saFakes.saRepo, authSvc, audit, allowedIssuers)
	return svc, trustRepo, saFakes.saRepo, audit
}

// testPublicKeyPEM marshals an RSA public key to PKIX PEM. Test-only.
func testPublicKeyPEM(t *testing.T, pub *rsa.PublicKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// testPrivateKeyPEM marshals an RSA private key to PKCS8 PEM. Test-only.
func testPrivateKeyPEM(t *testing.T, priv *rsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// requireCode asserts that err is a gRPC status with the given code.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	require.Error(t, err)
	s, ok := status.FromError(err)
	require.True(t, ok, "error must be a gRPC status: %v", err)
	require.Equal(t, want, s.Code(), "got code %v, want %v (msg: %s)", s.Code(), want, s.Message())
}

// requireRejectReason asserts the most recent audit event is a
// workload_token.rejected with the expected `reason` field.
func requireRejectReason(t *testing.T, audit *capturingAuditEmitter, wantReason string) {
	t.Helper()
	require.NotEmpty(t, audit.Events, "expected at least one audit event")
	ev := audit.Events[len(audit.Events)-1]
	require.Equal(t, "auth.workload_token.rejected", ev.Action)
	require.Equal(t, wantReason, ev.Fields["reason"], "wrong reject reason")
}

// ── TestOIDCTrustService_Create ───────────────────────────────────────

// TestOIDCTrustService_Create_Validations exercises every validation
// branch in Create.
func TestOIDCTrustService_Create_Validations(t *testing.T) {
	ctx := context.Background()
	allowed := []string{"https://token.actions.githubusercontent.com"}

	svc, _, saRepo, _ := newTrustServiceFakes(t, allowed)

	// Seed an SA in tenant A.
	tenantA := uuid.New()
	saA, _, err := saRepo.CreateAtomic(ctx, repository.CreateServiceAccountInput{
		TenantID: tenantA,
		Name:     "ci-prod",
	})
	require.NoError(t, err)

	// Seed an SA in a DIFFERENT tenant for the cross-tenant test.
	tenantB := uuid.New()
	saB, _, err := saRepo.CreateAtomic(ctx, repository.CreateServiceAccountInput{
		TenantID: tenantB,
		Name:     "ci-prod-b",
	})
	require.NoError(t, err)

	mkBase := func() CreateOIDCTrustInput {
		return CreateOIDCTrustInput{
			TenantID:         tenantA,
			ServiceAccountID: saA.ID,
			DisplayName:      "GH Actions",
			IssuerURL:        "https://token.actions.githubusercontent.com",
			Audience:         "registry.example.com",
			SubjectPattern:   "repo:steveokay/oci-janus:ref:refs/heads/main",
		}
	}

	t.Run("happy path", func(t *testing.T) {
		row, err := svc.Create(ctx, mkBase())
		require.NoError(t, err)
		require.NotEqual(t, uuid.Nil, row.ID)
	})

	t.Run("empty display_name rejected", func(t *testing.T) {
		in := mkBase()
		in.DisplayName = ""
		in.SubjectPattern = "repo:other:ref:refs/heads/empty-name"
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("empty audience rejected", func(t *testing.T) {
		in := mkBase()
		in.Audience = ""
		in.SubjectPattern = "repo:other:ref:refs/heads/empty-aud"
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("issuer not in allowlist rejected", func(t *testing.T) {
		in := mkBase()
		in.IssuerURL = "https://attacker.example.com"
		in.SubjectPattern = "repo:other:ref:refs/heads/bad-issuer"
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("invalid glob rejected", func(t *testing.T) {
		in := mkBase()
		in.SubjectPattern = "repo:***"
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("cross-tenant SA rejected", func(t *testing.T) {
		in := mkBase()
		in.ServiceAccountID = saB.ID
		in.SubjectPattern = "repo:other:ref:refs/heads/cross-tenant"
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("unknown SA rejected", func(t *testing.T) {
		in := mkBase()
		in.ServiceAccountID = uuid.New()
		in.SubjectPattern = "repo:other:ref:refs/heads/unknown-sa"
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("duplicate subject rejected", func(t *testing.T) {
		// The first happy-path sub-test already inserted this exact
		// (tenant, issuer, subject_pattern) tuple. Re-insert must collide.
		_, err := svc.Create(ctx, mkBase())
		requireCode(t, err, codes.AlreadyExists)
	})

	// SEC-060: JWKS cache TTL must be bounded so a compromised/malicious
	// admin cannot set a negative/tiny TTL (JWKS-refetch amplifier against
	// the upstream IdP) or an enormous one (pin a transiently-controlled
	// key set for the deployment lifetime). 0 stays valid (repo default).
	t.Run("TTL zero is accepted (repo default)", func(t *testing.T) {
		in := mkBase()
		in.SubjectPattern = "repo:other:ref:refs/heads/ttl-zero"
		in.JWKSCacheTTLSeconds = 0
		_, err := svc.Create(ctx, in)
		require.NoError(t, err)
	})

	t.Run("TTL below floor rejected", func(t *testing.T) {
		in := mkBase()
		in.SubjectPattern = "repo:other:ref:refs/heads/ttl-low"
		in.JWKSCacheTTLSeconds = 59
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("TTL negative rejected", func(t *testing.T) {
		in := mkBase()
		in.SubjectPattern = "repo:other:ref:refs/heads/ttl-neg"
		in.JWKSCacheTTLSeconds = -1
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("TTL above ceiling rejected", func(t *testing.T) {
		in := mkBase()
		in.SubjectPattern = "repo:other:ref:refs/heads/ttl-high"
		in.JWKSCacheTTLSeconds = 86401
		_, err := svc.Create(ctx, in)
		requireCode(t, err, codes.InvalidArgument)
	})

	t.Run("TTL at bounds accepted", func(t *testing.T) {
		in := mkBase()
		in.SubjectPattern = "repo:other:ref:refs/heads/ttl-floor"
		in.JWKSCacheTTLSeconds = 60
		_, err := svc.Create(ctx, in)
		require.NoError(t, err)

		in2 := mkBase()
		in2.SubjectPattern = "repo:other:ref:refs/heads/ttl-ceil"
		in2.JWKSCacheTTLSeconds = 86400
		_, err = svc.Create(ctx, in2)
		require.NoError(t, err)
	})
}

// TestOIDCTrustService_Update_Delete exercises the mutation paths so the
// "happy path returns the updated row" + "scope is tenant-bound" branches
// are covered.
func TestOIDCTrustService_Update_Delete(t *testing.T) {
	ctx := context.Background()
	allowed := []string{"https://token.actions.githubusercontent.com"}
	svc, _, saRepo, audit := newTrustServiceFakes(t, allowed)

	tenantA := uuid.New()
	saA, _, err := saRepo.CreateAtomic(ctx, repository.CreateServiceAccountInput{
		TenantID: tenantA,
		Name:     "ci-prod",
	})
	require.NoError(t, err)

	created, err := svc.Create(ctx, CreateOIDCTrustInput{
		TenantID:         tenantA,
		ServiceAccountID: saA.ID,
		DisplayName:      "original",
		IssuerURL:        "https://token.actions.githubusercontent.com",
		Audience:         "registry.example.com",
		SubjectPattern:   "repo:org/r:ref:refs/heads/main",
		ActorID:          "admin",
	})
	require.NoError(t, err)

	// Update happy.
	audit.Events = nil
	updated, err := svc.Update(ctx, UpdateOIDCTrustInput{
		ID:             created.ID,
		TenantID:       tenantA,
		DisplayName:    "renamed",
		SubjectPattern: "repo:org/r:ref:refs/heads/feature",
		ActorID:        "admin",
	})
	require.NoError(t, err)
	require.Equal(t, "renamed", updated.DisplayName)
	require.NotEmpty(t, audit.Events)
	require.Equal(t, "auth.oidc_trust.updated", audit.Events[len(audit.Events)-1].Action)

	// Update with invalid glob rejected.
	_, err = svc.Update(ctx, UpdateOIDCTrustInput{
		ID:             created.ID,
		TenantID:       tenantA,
		DisplayName:    "x",
		SubjectPattern: "***",
		ActorID:        "admin",
	})
	requireCode(t, err, codes.InvalidArgument)

	// Update wrong tenant returns NotFound.
	_, err = svc.Update(ctx, UpdateOIDCTrustInput{
		ID:             created.ID,
		TenantID:       uuid.New(),
		DisplayName:    "x",
		SubjectPattern: "valid",
		ActorID:        "admin",
	})
	requireCode(t, err, codes.NotFound)

	// Delete happy.
	audit.Events = nil
	require.NoError(t, svc.Delete(ctx, tenantA, created.ID, "admin"))
	require.NotEmpty(t, audit.Events)
	require.Equal(t, "auth.oidc_trust.deleted", audit.Events[len(audit.Events)-1].Action)

	// Delete idempotent miss → NotFound.
	err = svc.Delete(ctx, tenantA, created.ID, "admin")
	requireCode(t, err, codes.NotFound)
}

// ── ExchangeWorkloadToken ─────────────────────────────────────────────

// signTestJWT signs an RS256 JWT for the stub IdP. Callers tweak the
// claims via the mutator function to produce variants for each reject
// reason.
func signTestJWT(t *testing.T, priv *rsa.PrivateKey, kid string, mut func(jwt.MapClaims)) string {
	t.Helper()
	now := time.Now()
	claims := jwt.MapClaims{
		"iss": "PLACEHOLDER",
		"sub": "repo:steveokay/oci-janus:ref:refs/heads/main",
		"aud": "registry.example.com",
		"iat": now.Unix(),
		"exp": now.Add(5 * time.Minute).Unix(),
	}
	if mut != nil {
		mut(claims)
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = kid
	signed, err := tok.SignedString(priv)
	require.NoError(t, err)
	return signed
}

// TestExchangeWorkloadToken covers the happy path + every reject reason.
func TestExchangeWorkloadToken(t *testing.T) {
	ctx := context.Background()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	srv, _ := startStubIdP(t, priv, "test-kid")
	defer srv.Close()

	allowed := []string{srv.URL}
	svc, trustRepo, saRepo, audit := newTrustServiceFakes(t, allowed)

	// Seed an SA with allowed_scopes so IssueWorkloadToken returns a
	// real Access list.
	tenantA := uuid.New()
	sa, _, err := saRepo.CreateAtomic(ctx, repository.CreateServiceAccountInput{
		TenantID:      tenantA,
		Name:          "ci-prod",
		AllowedScopes: []string{"push", "pull"},
	})
	require.NoError(t, err)

	// Seed a trust pointing at that SA via the fake repo directly. We
	// bypass svc.Create here because the SEC-063 HTTPS-issuer validator
	// (PR #224) legitimately rejects the stub IdP's http://127.0.0.1
	// URL — that create-time invariant is exercised by
	// TestOIDCTrustService_Create_Validations. This test is about the
	// EXCHANGE path, so we skip the front door and seed a valid row.
	_, err = trustRepo.Create(ctx, repository.OIDCTrust{
		TenantID:            tenantA,
		ServiceAccountID:    sa.ID,
		DisplayName:         "GH Actions",
		IssuerURL:           srv.URL,
		Audience:            "registry.example.com",
		SubjectPattern:      "repo:steveokay/oci-janus:ref:refs/heads/main",
		JWKSCacheTTLSeconds: 3600,
	})
	require.NoError(t, err)

	t.Run("Happy", func(t *testing.T) {
		audit.Events = nil
		raw := signTestJWT(t, priv, "test-kid", func(c jwt.MapClaims) {
			c["iss"] = srv.URL
		})
		res, err := svc.ExchangeWorkloadToken(ctx, raw)
		require.NoError(t, err)
		require.NotEmpty(t, res.AccessToken)
		require.Equal(t, int32(WorkloadTokenLifetimeSeconds), res.ExpiresIn)
		require.Equal(t, "Bearer", res.TokenType)
		require.NotEmpty(t, audit.Events)
		require.Equal(t, "auth.workload_token.exchanged", audit.Events[len(audit.Events)-1].Action)
	})

	t.Run("Rejects_IssuerNotAllowed", func(t *testing.T) {
		audit.Events = nil
		raw := signTestJWT(t, priv, "test-kid", func(c jwt.MapClaims) {
			c["iss"] = "https://attacker.example.com"
		})
		_, err := svc.ExchangeWorkloadToken(ctx, raw)
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "issuer_not_allowed")
	})

	t.Run("Rejects_AudienceMismatch", func(t *testing.T) {
		audit.Events = nil
		raw := signTestJWT(t, priv, "test-kid", func(c jwt.MapClaims) {
			c["iss"] = srv.URL
			c["aud"] = "wrong.example.com"
		})
		_, err := svc.ExchangeWorkloadToken(ctx, raw)
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "audience_mismatch")
	})

	t.Run("Rejects_SubjectMismatch", func(t *testing.T) {
		audit.Events = nil
		raw := signTestJWT(t, priv, "test-kid", func(c jwt.MapClaims) {
			c["iss"] = srv.URL
			c["sub"] = "repo:steveokay/oci-janus:ref:refs/heads/wrong"
		})
		_, err := svc.ExchangeWorkloadToken(ctx, raw)
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "subject_mismatch")
	})

	t.Run("Rejects_SignatureInvalid", func(t *testing.T) {
		audit.Events = nil
		otherPriv, err := rsa.GenerateKey(rand.Reader, 2048)
		require.NoError(t, err)
		raw := signTestJWT(t, otherPriv, "test-kid", func(c jwt.MapClaims) {
			c["iss"] = srv.URL
		})
		_, err = svc.ExchangeWorkloadToken(ctx, raw)
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "signature_invalid")
	})

	t.Run("Rejects_Expired", func(t *testing.T) {
		audit.Events = nil
		raw := signTestJWT(t, priv, "test-kid", func(c jwt.MapClaims) {
			c["iss"] = srv.URL
			c["exp"] = time.Now().Add(-1 * time.Minute).Unix()
			c["iat"] = time.Now().Add(-2 * time.Minute).Unix()
		})
		_, err := svc.ExchangeWorkloadToken(ctx, raw)
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "expired")
	})

	t.Run("Rejects_NotYetValid", func(t *testing.T) {
		audit.Events = nil
		raw := signTestJWT(t, priv, "test-kid", func(c jwt.MapClaims) {
			c["iss"] = srv.URL
			future := time.Now().Add(5 * time.Minute).Unix()
			c["nbf"] = future
			c["iat"] = future
			c["exp"] = time.Now().Add(10 * time.Minute).Unix()
		})
		_, err := svc.ExchangeWorkloadToken(ctx, raw)
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "not_yet_valid")
	})

	t.Run("Rejects_SAdisabled", func(t *testing.T) {
		audit.Events = nil
		disabled := true
		_, err := saRepo.Update(ctx, repository.UpdateServiceAccountInput{
			ID:       sa.ID,
			TenantID: tenantA,
			Disabled: &disabled,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			enabled := false
			_, _ = saRepo.Update(ctx, repository.UpdateServiceAccountInput{
				ID:       sa.ID,
				TenantID: tenantA,
				Disabled: &enabled,
			})
		})
		raw := signTestJWT(t, priv, "test-kid", func(c jwt.MapClaims) {
			c["iss"] = srv.URL
		})
		_, err = svc.ExchangeWorkloadToken(ctx, raw)
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "sa_disabled")
	})

	t.Run("Rejects_MalformedJWT", func(t *testing.T) {
		audit.Events = nil
		_, err := svc.ExchangeWorkloadToken(ctx, "not.a.jwt")
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "signature_invalid")
	})

	t.Run("Rejects_EmptyJWT", func(t *testing.T) {
		audit.Events = nil
		_, err := svc.ExchangeWorkloadToken(ctx, "")
		requireCode(t, err, codes.Unauthenticated)
		requireRejectReason(t, audit, "signature_invalid")
	})
}
