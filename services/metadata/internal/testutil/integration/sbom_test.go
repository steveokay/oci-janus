//go:build integration

// Package integration — SBOM round-trip coverage for FE-API-033.
//
// Tests in this file exercise the new UpsertScanSBOM / GetScanSBOM SQL paths
// against a real Postgres container (testcontainers) so column types,
// nullable columns, and migration ordering are all validated end-to-end.
//
// They use the repository directly (not the gRPC handler) so the test can
// seed a scan_results row through CreatePendingScanResult — the gRPC surface
// only exposes UpdateScanStatus, which targets an existing row.
package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/steveokay/oci-janus/libs/testutil/containers"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
	metadatamigrations "github.com/steveokay/oci-janus/services/metadata/migrations"
)

// buildRepo spins up a Postgres container and returns a wired repository.
// This is a slimmer counterpart to buildTestEnv — it skips the gRPC handler
// wiring because the SBOM tests need direct access to repo helpers that
// the public RPC surface doesn't expose (CreatePendingScanResult etc.).
func buildRepo(t *testing.T) *repository.Repository {
	t.Helper()
	ctx := context.Background()

	dsn := containers.Postgres(t)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)

	poolCfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig: %v", err)
	}
	sqlDB := stdlib.OpenDB(*poolCfg.ConnConfig)
	t.Cleanup(func() { _ = sqlDB.Close() })

	goose.SetBaseFS(metadatamigrations.FS)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatalf("goose.SetDialect: %v", err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatalf("goose.Up: %v", err)
	}

	return repository.New(pool)
}

// seedRepoAndScan creates an organization, repository, and pending scan
// result so SBOM upsert/get have a row to attach to. Returns the
// manifest_digest used so the test can pass it into the SBOM helpers.
func seedRepoAndScan(t *testing.T, repo *repository.Repository) (manifestDigest, repoID string) {
	t.Helper()
	ctx := context.Background()

	orgID, err := repo.GetOrCreateOrganization(ctx, devTenantID, "sbom-org")
	if err != nil {
		t.Fatalf("GetOrCreateOrganization: %v", err)
	}
	r, err := repo.CreateRepository(ctx, devTenantID, orgID, "sbom-repo", "", false, 1<<30)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}
	digest := "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if _, err := repo.CreatePendingScanResult(ctx, devTenantID, r.GetRepoId(), digest, "trivy", "0.0.0"); err != nil {
		t.Fatalf("CreatePendingScanResult: %v", err)
	}
	return digest, r.GetRepoId()
}

// TestUpsertGetScanSBOM_roundTrip verifies that an SBOM written with
// UpsertScanSBOM can be read back byte-for-byte via GetScanSBOM, including
// the format identifier.
func TestUpsertGetScanSBOM_roundTrip(t *testing.T) {
	repo := buildRepo(t)
	digest, _ := seedRepoAndScan(t, repo)
	ctx := context.Background()

	want := []byte(`{"spdxVersion":"SPDX-2.3","SPDXID":"SPDXRef-DOCUMENT","packages":[]}`)
	if err := repo.UpsertScanSBOM(ctx, devTenantID, digest, "spdx-json", want); err != nil {
		t.Fatalf("UpsertScanSBOM: %v", err)
	}

	got, err := repo.GetScanSBOM(ctx, devTenantID, digest)
	if err != nil {
		t.Fatalf("GetScanSBOM: %v", err)
	}
	if got.Format != "spdx-json" {
		t.Errorf("Format: got %q, want spdx-json", got.Format)
	}
	if string(got.SBOMJSON) != string(want) {
		t.Errorf("SBOM bytes mismatch:\n got  %s\n want %s", string(got.SBOMJSON), string(want))
	}
}

// TestGetScanSBOM_neverScanned_returnsNotFound exercises the "no scan_results
// row at all" branch — the repository must surface ErrNotFound so the
// handler can map to gRPC NotFound → 404 at the BFF.
func TestGetScanSBOM_neverScanned_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	ctx := context.Background()

	_, err := repo.GetScanSBOM(ctx, devTenantID, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if err != repository.ErrNotFound {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}

// TestGetScanSBOM_scannedButNoSBOM_returnsNotFound covers the case where
// a scan row exists but the sbom_json column is still NULL (the scan
// completed without producing an SBOM, or pre-FE-API-033 backfilled rows).
// The repository collapses this to ErrNotFound so the BFF can render the
// same "no SBOM recorded" empty state.
func TestGetScanSBOM_scannedButNoSBOM_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	digest, _ := seedRepoAndScan(t, repo)
	ctx := context.Background()

	// No UpsertScanSBOM call — sbom_json stays NULL.
	_, err := repo.GetScanSBOM(ctx, devTenantID, digest)
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if err != repository.ErrNotFound {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}

// TestUpsertScanSBOM_overwritesPreviousSBOM verifies that a second upsert
// replaces the first SBOM rather than appending — important because the
// scanner re-runs SBOM generation each scan and we want the latest blob
// always.
func TestUpsertScanSBOM_overwritesPreviousSBOM(t *testing.T) {
	repo := buildRepo(t)
	digest, _ := seedRepoAndScan(t, repo)
	ctx := context.Background()

	first := []byte(`{"spdxVersion":"SPDX-2.3","first":true}`)
	if err := repo.UpsertScanSBOM(ctx, devTenantID, digest, "spdx-json", first); err != nil {
		t.Fatalf("first UpsertScanSBOM: %v", err)
	}

	second := []byte(`{"spdxVersion":"SPDX-2.3","second":true}`)
	if err := repo.UpsertScanSBOM(ctx, devTenantID, digest, "spdx-json", second); err != nil {
		t.Fatalf("second UpsertScanSBOM: %v", err)
	}

	got, err := repo.GetScanSBOM(ctx, devTenantID, digest)
	if err != nil {
		t.Fatalf("GetScanSBOM: %v", err)
	}
	if string(got.SBOMJSON) != string(second) {
		t.Errorf("expected latest SBOM, got %s", string(got.SBOMJSON))
	}
}

// TestUpsertScanSBOM_crossTenant_returnsNotFound ensures a SBOM upsert
// against another tenant's manifest digest fails with NotFound. The scan
// row exists in the dev tenant; the upsert call uses a different tenant
// UUID so the SELECT (which filters on tenant_id) returns no row.
func TestUpsertScanSBOM_crossTenant_returnsNotFound(t *testing.T) {
	repo := buildRepo(t)
	digest, _ := seedRepoAndScan(t, repo)
	ctx := context.Background()

	otherTenant := "11111111-1111-1111-1111-111111111111"
	err := repo.UpsertScanSBOM(ctx, otherTenant, digest, "spdx-json", []byte(`{}`))
	if err == nil {
		t.Fatal("expected ErrNotFound for cross-tenant upsert, got nil")
	}
	if err != repository.ErrNotFound {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}
