//go:build integration

// Package repository — oidc_trust_test.go exercises the migration round-trip
// + CRUD repository for the FUT-001 oidc_trust_configs table against a real
// PostgreSQL 16 container via testcontainers.
//
// Covers:
//   - migration up creates the table + indexes + UNIQUE constraint cleanly
//   - Create / GetByID / List / ListByIssuer / Update / Delete happy paths
//   - UNIQUE (tenant_id, issuer_url, subject_pattern) rejects duplicates
//   - ON DELETE CASCADE from service_accounts removes trusts when the SA
//     is deleted

package repository

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
)

// TestOIDCTrustRepo exercises every public method of OIDCTrustRepo against
// a real Postgres instance. Sub-tests share the container + pool so total
// runtime stays under ~10s on a developer laptop.
func TestOIDCTrustRepo(t *testing.T) {
	ctx := context.Background()

	dsn := containers.Postgres(t)

	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err, "pgxpool.New")
	t.Cleanup(pool.Close)

	// Migrate up to and including the FUT-001 trust-configs migration.
	gooseUpTo(t, dsn, "20260701000001")

	repo := NewOIDCTrustRepo(pool)

	// Seed an SA + shadow user to act as the trust's FK target.
	tenantID := uuid.New()

	mkServiceAccount := func(name string) uuid.UUID {
		t.Helper()
		// Shadow user first (the SA's backing identity).
		var shadow uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `
			INSERT INTO users (tenant_id, username, email, password_hash, kind)
			VALUES ($1, 'sa-'||$2, 'sa+'||$2||'@internal.invalid', '', 'service_account')
			RETURNING id`, tenantID, name).Scan(&shadow))
		var sa uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `
			INSERT INTO service_accounts (tenant_id, shadow_user_id, name)
			VALUES ($1, $2, $3) RETURNING id`, tenantID, shadow, name).Scan(&sa))
		return sa
	}

	saA := mkServiceAccount("ci-prod-a")

	t.Run("Create_HappyPath", func(t *testing.T) {
		got, err := repo.Create(ctx, OIDCTrust{
			TenantID:            tenantID,
			ServiceAccountID:    saA,
			DisplayName:         "GitHub Actions — main branch",
			IssuerURL:           "https://token.actions.githubusercontent.com",
			Audience:            "registry.example.com",
			SubjectPattern:      "repo:steveokay/oci-janus:ref:refs/heads/main",
			JWKSCacheTTLSeconds: 0, // exercise the default-3600 path
		})
		require.NoError(t, err)
		require.NotEqual(t, uuid.Nil, got.ID)
		require.Equal(t, int32(3600), got.JWKSCacheTTLSeconds, "default TTL should be 3600 when caller passes 0")
		require.Nil(t, got.LastUsedAt, "newly-created row must have NULL last_used_at")
	})

	t.Run("GetByID_RoundTrip", func(t *testing.T) {
		created, err := repo.Create(ctx, OIDCTrust{
			TenantID:         tenantID,
			ServiceAccountID: saA,
			DisplayName:      "GitLab CI",
			IssuerURL:        "https://gitlab.com",
			Audience:         "registry.example.com",
			SubjectPattern:   "project_path:group/project:ref_type:branch:ref:main",
		})
		require.NoError(t, err)

		got, err := repo.GetByID(ctx, tenantID, created.ID)
		require.NoError(t, err)
		require.Equal(t, created.ID, got.ID)
		require.Equal(t, "GitLab CI", got.DisplayName)
	})

	t.Run("GetByID_ScopesToTenant", func(t *testing.T) {
		// Cross-tenant read must return ErrNotFound rather than the row.
		otherTenant := uuid.New()
		created, err := repo.Create(ctx, OIDCTrust{
			TenantID:         tenantID,
			ServiceAccountID: saA,
			DisplayName:      "Buildkite",
			IssuerURL:        "https://agent.buildkite.com",
			Audience:         "registry.example.com",
			SubjectPattern:   "organization-slug:org:pipeline-slug:pipe:ref:refs/heads/main",
		})
		require.NoError(t, err)

		_, err = repo.GetByID(ctx, otherTenant, created.ID)
		require.ErrorIs(t, err, ErrNotFound, "other-tenant lookup must miss")
	})

	t.Run("List_ScopesToTenant", func(t *testing.T) {
		got, err := repo.List(ctx, tenantID)
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(got), 3, "should see all 3 trusts created so far in this tenant")

		// Cross-tenant list must return empty (not error).
		empty, err := repo.List(ctx, uuid.New())
		require.NoError(t, err)
		require.Empty(t, empty)
	})

	t.Run("ListByIssuer_AcrossTenants", func(t *testing.T) {
		// Create one more trust in a different tenant against the same issuer
		// so we can verify ListByIssuer sees BOTH (it is NOT tenant-scoped).
		otherTenant := uuid.New()
		var otherShadow uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `
			INSERT INTO users (tenant_id, username, email, password_hash, kind)
			VALUES ($1, 'sa-other', 'sa+other@internal.invalid', '', 'service_account')
			RETURNING id`, otherTenant).Scan(&otherShadow))
		var otherSA uuid.UUID
		require.NoError(t, pool.QueryRow(ctx, `
			INSERT INTO service_accounts (tenant_id, shadow_user_id, name)
			VALUES ($1, $2, 'ci-other') RETURNING id`, otherTenant, otherShadow).Scan(&otherSA))

		_, err := repo.Create(ctx, OIDCTrust{
			TenantID:         otherTenant,
			ServiceAccountID: otherSA,
			DisplayName:      "Other tenant GH",
			IssuerURL:        "https://token.actions.githubusercontent.com",
			Audience:         "other.example.com",
			SubjectPattern:   "repo:other/repo:ref:refs/heads/main",
		})
		require.NoError(t, err)

		got, err := repo.ListByIssuer(ctx, "https://token.actions.githubusercontent.com")
		require.NoError(t, err)
		require.GreaterOrEqual(t, len(got), 2, "should see both tenants' GH trusts")
	})

	t.Run("Create_RejectsDuplicateSubject", func(t *testing.T) {
		// Same (tenant, issuer, subject_pattern) must collide.
		_, err := repo.Create(ctx, OIDCTrust{
			TenantID:         tenantID,
			ServiceAccountID: saA,
			DisplayName:      "duplicate",
			IssuerURL:        "https://token.actions.githubusercontent.com",
			Audience:         "registry.example.com",
			SubjectPattern:   "repo:steveokay/oci-janus:ref:refs/heads/main", // already used
		})
		require.ErrorIs(t, err, ErrAlreadyExists)
	})

	t.Run("Update_MutatesAllowedFieldsOnly", func(t *testing.T) {
		created, err := repo.Create(ctx, OIDCTrust{
			TenantID:         tenantID,
			ServiceAccountID: saA,
			DisplayName:      "Update target",
			IssuerURL:        "https://token.actions.githubusercontent.com",
			Audience:         "registry.example.com",
			SubjectPattern:   "repo:steveokay/oci-janus:environment:staging",
		})
		require.NoError(t, err)

		// Try to change display_name + subject_pattern + ttl. The Update
		// method should NOT mutate issuer_url / audience / service_account_id.
		updated, err := repo.Update(ctx, OIDCTrust{
			ID:                  created.ID,
			TenantID:            tenantID,
			DisplayName:         "renamed",
			SubjectPattern:      "repo:steveokay/oci-janus:environment:production",
			JWKSCacheTTLSeconds: 7200,
		})
		require.NoError(t, err)
		require.Equal(t, "renamed", updated.DisplayName)
		require.Equal(t, "repo:steveokay/oci-janus:environment:production", updated.SubjectPattern)
		require.Equal(t, int32(7200), updated.JWKSCacheTTLSeconds)
		// Untouched fields preserved.
		require.Equal(t, created.IssuerURL, updated.IssuerURL)
		require.Equal(t, created.Audience, updated.Audience)
		require.Equal(t, created.ServiceAccountID, updated.ServiceAccountID)
	})

	t.Run("Delete_RoundTrip", func(t *testing.T) {
		created, err := repo.Create(ctx, OIDCTrust{
			TenantID:         tenantID,
			ServiceAccountID: saA,
			DisplayName:      "Delete target",
			IssuerURL:        "https://example-idp.invalid",
			Audience:         "registry.example.com",
			SubjectPattern:   "user:to-delete",
		})
		require.NoError(t, err)

		require.NoError(t, repo.Delete(ctx, tenantID, created.ID))

		_, err = repo.GetByID(ctx, tenantID, created.ID)
		require.ErrorIs(t, err, ErrNotFound)

		// Second delete is idempotent miss → ErrNotFound.
		err = repo.Delete(ctx, tenantID, created.ID)
		require.ErrorIs(t, err, ErrNotFound)
	})

	t.Run("MarkUsed_UpdatesLastUsedAt", func(t *testing.T) {
		created, err := repo.Create(ctx, OIDCTrust{
			TenantID:         tenantID,
			ServiceAccountID: saA,
			DisplayName:      "MarkUsed target",
			IssuerURL:        "https://example-idp-2.invalid",
			Audience:         "registry.example.com",
			SubjectPattern:   "user:mark-used",
		})
		require.NoError(t, err)
		require.Nil(t, created.LastUsedAt, "freshly created row has NULL last_used_at")

		require.NoError(t, repo.MarkUsed(ctx, created.ID))

		got, err := repo.GetByID(ctx, tenantID, created.ID)
		require.NoError(t, err)
		require.NotNil(t, got.LastUsedAt, "MarkUsed should populate last_used_at")
	})

	t.Run("CascadeDeleteOnServiceAccount", func(t *testing.T) {
		// New SA so we can delete it without breaking earlier sub-tests.
		saB := mkServiceAccount("ci-cascade")
		created, err := repo.Create(ctx, OIDCTrust{
			TenantID:         tenantID,
			ServiceAccountID: saB,
			DisplayName:      "Cascade target",
			IssuerURL:        "https://example-idp-3.invalid",
			Audience:         "registry.example.com",
			SubjectPattern:   "user:cascade",
		})
		require.NoError(t, err)

		// Delete the SA's shadow user → cascades to service_accounts →
		// cascades to oidc_trust_configs.
		_, err = pool.Exec(ctx, `
			DELETE FROM users
			WHERE  id = (SELECT shadow_user_id FROM service_accounts WHERE id = $1)`,
			saB)
		require.NoError(t, err)

		_, err = repo.GetByID(ctx, tenantID, created.ID)
		require.ErrorIs(t, err, ErrNotFound, "trust should cascade-delete with its SA")
	})
}
