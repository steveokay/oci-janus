// Package handler — unit tests for MetadataHandler.
//
// All tests use a hand-written fakeRepo that implements metadataRepo.
// No real PostgreSQL or network connections are required (CLAUDE.md §18).
package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/metadata/internal/repository"
)

// ── fakeRepo ─────────────────────────────────────────────────────────────────

// fakeRepo is a controllable in-memory stub for metadataRepo.
// Each group of fields corresponds to one RPC family. Tests set the fields they
// need and leave the rest at their zero values (nil error, nil proto pointer).
type fakeRepo struct {
	// CreateRepository
	createRepoResult *metadatav1.Repository
	createRepoErr    error

	// GetOrCreateOrganization (used by CreateRepository when OrgId is absent)
	getOrCreateOrgID  string
	getOrCreateOrgErr error

	// GetRepository
	getRepoResult *metadatav1.Repository
	getRepoErr    error

	// GetRepositoryByName / GetRepositoryByFullName
	getRepoByNameResult *metadatav1.Repository
	getRepoByNameErr    error

	getRepoByFullNameResult *metadatav1.Repository
	getRepoByFullNameErr    error

	// ListRepositories
	listReposResult []*metadatav1.Repository
	listReposErr    error

	// DeleteRepository
	deleteRepoErr error

	// UpdateRepositoryQuota
	updateQuotaResult *metadatav1.Repository
	updateQuotaErr    error

	// GetTenantStorageBreakdown (FE-API-031)
	storageBreakdownResp *metadatav1.GetTenantStorageBreakdownResponse
	storageBreakdownErr  error

	// GetTenantUsage (FE-API-028)
	tenantUsageResp *metadatav1.TenantUsage
	tenantUsageErr  error

	// PutTag
	putTagResult *metadatav1.Tag
	putTagErr    error

	// GetTag
	getTagResult *metadatav1.Tag
	getTagErr    error

	// ListTags
	listTagsResult []*metadatav1.Tag
	listTagsErr    error

	// DeleteTag
	deleteTagErr error

	// PutManifest
	putManifestResult *metadatav1.Manifest
	putManifestErr    error

	// GetManifest
	getManifestResult *metadatav1.Manifest
	getManifestErr    error

	// DeleteManifest
	deleteManifestErr error

	// ListUntaggedManifests
	listUntaggedResult []*metadatav1.Manifest
	listUntaggedErr    error

	// LinkBlob / UnlinkBlob
	linkBlobErr   error
	unlinkBlobErr error

	// ListOrphanedBlobs
	listOrphanedResult []*metadatav1.BlobRef
	listOrphanedErr    error

	// Quota
	quotaUsageResult *metadatav1.QuotaUsage
	quotaUsageErr    error
	incrStorageErr   error
	decrStorageErr   error

	// Scan results
	upsertScanErr error
	getScanResult *metadatav1.ScanResult
	getScanErr    error
	// SBOM (FE-API-033)
	upsertSBOMErr      error
	upsertSBOMCalls    []upsertSBOMCallArgs
	getSBOMResult      *repository.SBOMResult
	getSBOMErr         error
	getSBOMCalls       []getSBOMCallArgs
	vulnTotal     int64
	vulnCritical  int64
	vulnHigh      int64
	vulnMedium    int64
	vulnLow       int64
	vulnNeg       int64
	vulnErr       error
	// Security overview (FE-API-020)
	securityOverview    *repository.SecurityOverview
	securityOverviewErr error
	// FE-API-014: vulnerability list
	listVulnsRows  []repository.VulnerabilityRow
	listVulnsNext  string
	listVulnsErr   error
	listVulnsCalls []listVulnsCallArgs
	// FE-API-015: scan history
	listScansRows  []repository.ScanHistoryRow
	listScansNext  string
	listScansErr   error
	listScansCalls []listScansCallArgs
	// FE-API-017: remediation suggestions
	listRemRows  []repository.RemediationRow
	listRemNext  string
	listRemErr   error
	listRemCalls []listRemCallArgs
	// UpdateRepository
	updateRepoResult *metadatav1.Repository
	updateRepoErr    error
	// Repository count
	repoCount    int64
	repoCountErr error
	// FE-API-037: per-repo retention policy CRUD
	getRetentionResult *metadatav1.RetentionPolicy
	getRetentionErr    error
	getRetentionCalls  []retentionGetCallArgs

	upsertRetentionResult *metadatav1.RetentionPolicy
	upsertRetentionErr    error
	upsertRetentionCalls  []retentionUpsertCallArgs

	deleteRetentionErr   error
	deleteRetentionCalls []retentionDeleteCallArgs

	// FE-API-038: retention policy dry-run evaluator. Tests can stub the
	// repository's evaluation result + error, and assert on the captured
	// call args (cap clamping, candidate forwarding).
	evalRetentionResult *repository.EvaluationResult
	evalRetentionErr    error
	evalRetentionCalls  []retentionEvalCallArgs

	// FE-API-039: per-org default retention policy.
	getOrgRetentionResult *metadatav1.RetentionPolicy
	getOrgRetentionErr    error
	getOrgRetentionCalls  []orgRetentionGetCallArgs

	upsertOrgRetentionResult *metadatav1.RetentionPolicy
	upsertOrgRetentionErr    error
	upsertOrgRetentionCalls  []orgRetentionUpsertCallArgs

	deleteOrgRetentionErr   error
	deleteOrgRetentionCalls []orgRetentionDeleteCallArgs

	// FE-API-039: effective policy resolution.
	effectiveRetentionResult *repository.EffectivePolicyResult
	effectiveRetentionErr    error
	effectiveRetentionCalls  []effectivePolicyCallArgs

	// FE-API-039: org name → org_id lookup.
	lookupOrgIDResult string
	lookupOrgIDErr    error
	lookupOrgIDCalls  []lookupOrgIDCallArgs

	// FE-API-040: retention executor primitives.
	markPendingErr    error
	markPendingCalls  []pendingCallArgs
	clearPendingErr   error
	clearPendingCalls []pendingCallArgs
	listPendingResult []*metadatav1.PendingDeleteManifest
	listPendingErr    error
	listPendingCalls  []listPendingCallArgs
}

// retentionEvalCallArgs records what EvaluateRetention forwarded so the
// handler tests can verify cap clamping + candidate wiring without standing
// up a real database.
type retentionEvalCallArgs struct {
	tenantID            string
	repoID              string
	candidate           *metadatav1.RetentionPolicyCandidate
	maxDeleteResults    int
	maxProtectedResults int
}

// retentionGetCallArgs / retentionUpsertCallArgs / retentionDeleteCallArgs
// capture what the handler forwarded so retention tests can assert wiring
// without a real database.
type retentionGetCallArgs struct {
	tenantID string
	repoID   string
}

type retentionUpsertCallArgs struct {
	tenantID  string
	repoID    string
	enabled   bool
	rules     []*metadatav1.RetentionRule
	patterns  []string
	updatedBy string
}

type retentionDeleteCallArgs struct {
	tenantID string
	repoID   string
}

// FE-API-039 — per-org default retention call args. Same shape as the
// per-repo equivalents, swapping repo_id for org_id.
type orgRetentionGetCallArgs struct {
	tenantID string
	orgID    string
}

type orgRetentionUpsertCallArgs struct {
	tenantID  string
	orgID     string
	enabled   bool
	rules     []*metadatav1.RetentionRule
	patterns  []string
	updatedBy string
}

type orgRetentionDeleteCallArgs struct {
	tenantID string
	orgID    string
}

// effectivePolicyCallArgs captures the (tenant, repo) tuple passed to
// GetEffectiveRetentionPolicy so tests can assert the lookup was scoped
// correctly.
type effectivePolicyCallArgs struct {
	tenantID string
	repoID   string
}

// pendingCallArgs / listPendingCallArgs capture what the FE-API-040 retention
// executor primitives forwarded so tests can assert tenant scoping + clamping.
type pendingCallArgs struct {
	tenantID   string
	manifestID string
}

type listPendingCallArgs struct {
	tenantID        string
	graceWindowSecs int64
	limit           int
}

// lookupOrgIDCallArgs captures the (tenant, name) tuple passed to
// LookupOrgIDByName so tests can assert the BFF forwarded the correct org.
type lookupOrgIDCallArgs struct {
	tenantID string
	name     string
}

// listVulnsCallArgs / listScansCallArgs capture what the handler forwards
// so the tests can assert validation + cursor wiring.
type listVulnsCallArgs struct {
	tenantID  string
	severity  string
	pageToken string
	limit     int
}

type listScansCallArgs struct {
	tenantID  string
	since     time.Time
	pageToken string
	limit     int
}

// listRemCallArgs records what ListTenantRemediations forwarded so the
// handler tests can assert tenant_id / pagination wiring without a real
// database.
type listRemCallArgs struct {
	tenantID  string
	pageToken string
	limit     int
}

// upsertSBOMCallArgs records what UpsertScanSBOM forwarded so the handler
// tests can assert payload wiring without a real database.
type upsertSBOMCallArgs struct {
	tenantID       string
	manifestDigest string
	format         string
	sbomLen        int
}

// getSBOMCallArgs mirrors upsertSBOMCallArgs for the read path.
type getSBOMCallArgs struct {
	tenantID       string
	manifestDigest string
}

// ── metadataRepo implementation on fakeRepo ───────────────────────────────────

func (f *fakeRepo) GetOrCreateOrganization(_ context.Context, _, _ string) (string, error) {
	return f.getOrCreateOrgID, f.getOrCreateOrgErr
}

func (f *fakeRepo) CreateRepository(_ context.Context, _, _, _, _ string, _ bool, _ int64) (*metadatav1.Repository, error) {
	return f.createRepoResult, f.createRepoErr
}

func (f *fakeRepo) UpdateRepository(_ context.Context, _, _, _ string) (*metadatav1.Repository, error) {
	return f.updateRepoResult, f.updateRepoErr
}

func (f *fakeRepo) GetRepository(_ context.Context, _, _ string) (*metadatav1.Repository, error) {
	return f.getRepoResult, f.getRepoErr
}

func (f *fakeRepo) GetRepositoryByName(_ context.Context, _, _, _ string) (*metadatav1.Repository, error) {
	return f.getRepoByNameResult, f.getRepoByNameErr
}

func (f *fakeRepo) GetRepositoryByFullName(_ context.Context, _, _ string) (*metadatav1.Repository, error) {
	return f.getRepoByFullNameResult, f.getRepoByFullNameErr
}

func (f *fakeRepo) ListRepositories(_ context.Context, _, _ string) ([]*metadatav1.Repository, error) {
	return f.listReposResult, f.listReposErr
}

func (f *fakeRepo) DeleteRepository(_ context.Context, _, _ string) error {
	return f.deleteRepoErr
}

func (f *fakeRepo) UpdateRepositoryQuota(_ context.Context, _, _ string, _ int64) (*metadatav1.Repository, error) {
	return f.updateQuotaResult, f.updateQuotaErr
}

func (f *fakeRepo) PutTag(_ context.Context, _, _, _, _ string) (*metadatav1.Tag, error) {
	return f.putTagResult, f.putTagErr
}

func (f *fakeRepo) GetTag(_ context.Context, _, _, _ string) (*metadatav1.Tag, error) {
	return f.getTagResult, f.getTagErr
}

func (f *fakeRepo) ListTags(_ context.Context, _, _ string, _ int32, _ string) ([]*metadatav1.Tag, error) {
	return f.listTagsResult, f.listTagsErr
}

func (f *fakeRepo) DeleteTag(_ context.Context, _, _, _ string) error {
	return f.deleteTagErr
}

func (f *fakeRepo) PutManifest(_ context.Context, _, _, _, _ string, _ []byte, _ int64) (*metadatav1.Manifest, error) {
	return f.putManifestResult, f.putManifestErr
}

func (f *fakeRepo) GetManifest(_ context.Context, _, _, _ string) (*metadatav1.Manifest, error) {
	return f.getManifestResult, f.getManifestErr
}

func (f *fakeRepo) DeleteManifest(_ context.Context, _, _, _ string) error {
	return f.deleteManifestErr
}

func (f *fakeRepo) ListUntaggedManifests(_ context.Context, _, _ string) ([]*metadatav1.Manifest, error) {
	return f.listUntaggedResult, f.listUntaggedErr
}

func (f *fakeRepo) LinkBlob(_ context.Context, _, _, _ string, _ int64) error {
	return f.linkBlobErr
}

func (f *fakeRepo) UnlinkBlob(_ context.Context, _, _ string) error {
	return f.unlinkBlobErr
}

func (f *fakeRepo) ListOrphanedBlobs(_ context.Context) ([]*metadatav1.BlobRef, error) {
	return f.listOrphanedResult, f.listOrphanedErr
}

func (f *fakeRepo) GetTenantStorageBreakdown(_ context.Context, _ string) (*metadatav1.GetTenantStorageBreakdownResponse, error) {
	return f.storageBreakdownResp, f.storageBreakdownErr
}

func (f *fakeRepo) GetTenantUsage(_ context.Context, _ string) (*metadatav1.TenantUsage, error) {
	return f.tenantUsageResp, f.tenantUsageErr
}

func (f *fakeRepo) GetTenantQuotaUsage(_ context.Context, _ string) (*metadatav1.QuotaUsage, error) {
	return f.quotaUsageResult, f.quotaUsageErr
}

func (f *fakeRepo) UpdateTenantQuota(_ context.Context, _ string, _ int64) (*metadatav1.QuotaUsage, error) {
	return f.quotaUsageResult, f.quotaUsageErr
}

func (f *fakeRepo) IncrementTenantStorage(_ context.Context, _ string, _ int64) error {
	return f.incrStorageErr
}

func (f *fakeRepo) DecrementTenantStorage(_ context.Context, _ string, _ int64) error {
	return f.decrStorageErr
}

func (f *fakeRepo) UpsertScanResult(_ context.Context, _, _, _ string, _ []byte, _ map[string]int32, _, _, _, _ string) error {
	return f.upsertScanErr
}

func (f *fakeRepo) GetScanResult(_ context.Context, _, _ string) (*metadatav1.ScanResult, error) {
	return f.getScanResult, f.getScanErr
}

func (f *fakeRepo) UpsertScanSBOM(_ context.Context, tenantID, manifestDigest, format string, sbom []byte) error {
	f.upsertSBOMCalls = append(f.upsertSBOMCalls, upsertSBOMCallArgs{
		tenantID: tenantID, manifestDigest: manifestDigest, format: format, sbomLen: len(sbom),
	})
	return f.upsertSBOMErr
}

func (f *fakeRepo) GetScanSBOM(_ context.Context, tenantID, manifestDigest string) (*repository.SBOMResult, error) {
	f.getSBOMCalls = append(f.getSBOMCalls, getSBOMCallArgs{
		tenantID: tenantID, manifestDigest: manifestDigest,
	})
	return f.getSBOMResult, f.getSBOMErr
}

func (f *fakeRepo) GetTenantVulnerabilityCount(_ context.Context, _ string) (int64, int64, int64, int64, int64, int64, error) {
	return f.vulnTotal, f.vulnCritical, f.vulnHigh, f.vulnMedium, f.vulnLow, f.vulnNeg, f.vulnErr
}

func (f *fakeRepo) GetSecurityOverview(_ context.Context, _ string) (*repository.SecurityOverview, error) {
	return f.securityOverview, f.securityOverviewErr
}

func (f *fakeRepo) ListTenantVulnerabilities(_ context.Context, tenantID, sev, token string, limit int) ([]repository.VulnerabilityRow, string, error) {
	f.listVulnsCalls = append(f.listVulnsCalls, listVulnsCallArgs{
		tenantID: tenantID, severity: sev, pageToken: token, limit: limit,
	})
	return f.listVulnsRows, f.listVulnsNext, f.listVulnsErr
}

func (f *fakeRepo) ListScanHistory(_ context.Context, tenantID string, since time.Time, token string, limit int) ([]repository.ScanHistoryRow, string, error) {
	f.listScansCalls = append(f.listScansCalls, listScansCallArgs{
		tenantID: tenantID, since: since, pageToken: token, limit: limit,
	})
	return f.listScansRows, f.listScansNext, f.listScansErr
}

func (f *fakeRepo) ListTenantRemediations(_ context.Context, tenantID, token string, limit int) ([]repository.RemediationRow, string, error) {
	f.listRemCalls = append(f.listRemCalls, listRemCallArgs{
		tenantID: tenantID, pageToken: token, limit: limit,
	})
	return f.listRemRows, f.listRemNext, f.listRemErr
}

func (f *fakeRepo) CountRepositories(_ context.Context, _ string) (int64, error) {
	return f.repoCount, f.repoCountErr
}

// FE-API-037 fake repo methods. Tests assert on the *Calls slices and set
// the *Result / *Err fields to drive each branch (happy path, NotFound,
// internal error). Pointer-typed proto results so a nil return is unambiguous.
func (f *fakeRepo) GetRepoRetentionPolicy(_ context.Context, tenantID, repoID string) (*metadatav1.RetentionPolicy, error) {
	f.getRetentionCalls = append(f.getRetentionCalls, retentionGetCallArgs{tenantID: tenantID, repoID: repoID})
	return f.getRetentionResult, f.getRetentionErr
}

func (f *fakeRepo) UpsertRepoRetentionPolicy(
	_ context.Context,
	tenantID, repoID string,
	enabled bool,
	rules []*metadatav1.RetentionRule,
	patterns []string,
	updatedBy string,
) (*metadatav1.RetentionPolicy, error) {
	f.upsertRetentionCalls = append(f.upsertRetentionCalls, retentionUpsertCallArgs{
		tenantID: tenantID, repoID: repoID, enabled: enabled,
		rules: rules, patterns: patterns, updatedBy: updatedBy,
	})
	return f.upsertRetentionResult, f.upsertRetentionErr
}

func (f *fakeRepo) DeleteRepoRetentionPolicy(_ context.Context, tenantID, repoID string) error {
	f.deleteRetentionCalls = append(f.deleteRetentionCalls, retentionDeleteCallArgs{tenantID: tenantID, repoID: repoID})
	return f.deleteRetentionErr
}

// EvaluateRetention captures the clamped caps + candidate so tests can
// assert the handler actually clamped over-large values and forwarded the
// candidate verbatim.
func (f *fakeRepo) EvaluateRetention(
	_ context.Context,
	tenantID, repoID string,
	candidate *metadatav1.RetentionPolicyCandidate,
	maxDeleteResults, maxProtectedResults int,
) (*repository.EvaluationResult, error) {
	f.evalRetentionCalls = append(f.evalRetentionCalls, retentionEvalCallArgs{
		tenantID:            tenantID,
		repoID:              repoID,
		candidate:           candidate,
		maxDeleteResults:    maxDeleteResults,
		maxProtectedResults: maxProtectedResults,
	})
	return f.evalRetentionResult, f.evalRetentionErr
}

// FE-API-039 fake repo methods. Same pattern as the per-repo fakes — tests
// set the *Result / *Err fields and assert on the captured *Calls slices.

func (f *fakeRepo) GetOrgRetentionPolicy(_ context.Context, tenantID, orgID string) (*metadatav1.RetentionPolicy, error) {
	f.getOrgRetentionCalls = append(f.getOrgRetentionCalls, orgRetentionGetCallArgs{tenantID: tenantID, orgID: orgID})
	return f.getOrgRetentionResult, f.getOrgRetentionErr
}

func (f *fakeRepo) UpsertOrgRetentionPolicy(
	_ context.Context,
	tenantID, orgID string,
	enabled bool,
	rules []*metadatav1.RetentionRule,
	patterns []string,
	updatedBy string,
) (*metadatav1.RetentionPolicy, error) {
	f.upsertOrgRetentionCalls = append(f.upsertOrgRetentionCalls, orgRetentionUpsertCallArgs{
		tenantID: tenantID, orgID: orgID, enabled: enabled,
		rules: rules, patterns: patterns, updatedBy: updatedBy,
	})
	return f.upsertOrgRetentionResult, f.upsertOrgRetentionErr
}

func (f *fakeRepo) DeleteOrgRetentionPolicy(_ context.Context, tenantID, orgID string) error {
	f.deleteOrgRetentionCalls = append(f.deleteOrgRetentionCalls, orgRetentionDeleteCallArgs{tenantID: tenantID, orgID: orgID})
	return f.deleteOrgRetentionErr
}

func (f *fakeRepo) GetEffectiveRetentionPolicy(_ context.Context, tenantID, repoID string) (*repository.EffectivePolicyResult, error) {
	f.effectiveRetentionCalls = append(f.effectiveRetentionCalls, effectivePolicyCallArgs{tenantID: tenantID, repoID: repoID})
	return f.effectiveRetentionResult, f.effectiveRetentionErr
}

func (f *fakeRepo) LookupOrgIDByName(_ context.Context, tenantID, name string) (string, error) {
	f.lookupOrgIDCalls = append(f.lookupOrgIDCalls, lookupOrgIDCallArgs{tenantID: tenantID, name: name})
	return f.lookupOrgIDResult, f.lookupOrgIDErr
}

// FE-API-040 retention-pending stubs. Each method captures its call args so a
// test can assert the handler forwarded the intended tenant/manifest IDs, and
// the configurable error / result fields let cases simulate "no row" /
// repository errors without standing up Postgres.
func (f *fakeRepo) MarkManifestRetentionPending(_ context.Context, tenantID, manifestID string) error {
	f.markPendingCalls = append(f.markPendingCalls, pendingCallArgs{tenantID: tenantID, manifestID: manifestID})
	return f.markPendingErr
}

func (f *fakeRepo) ClearManifestRetentionPending(_ context.Context, tenantID, manifestID string) error {
	f.clearPendingCalls = append(f.clearPendingCalls, pendingCallArgs{tenantID: tenantID, manifestID: manifestID})
	return f.clearPendingErr
}

func (f *fakeRepo) ListPendingDeleteManifests(_ context.Context, tenantID string, graceWindowSecs int64, limit int) ([]*metadatav1.PendingDeleteManifest, error) {
	f.listPendingCalls = append(f.listPendingCalls, listPendingCallArgs{tenantID: tenantID, graceWindowSecs: graceWindowSecs, limit: limit})
	return f.listPendingResult, f.listPendingErr
}

// ── test helpers ──────────────────────────────────────────────────────────────

// newHandler wires a fakeRepo into a MetadataHandler for testing.
func newHandler(r metadataRepo) *MetadataHandler {
	return &MetadataHandler{repo: r}
}

// requireCode asserts that err carries the expected gRPC status code.
func requireCode(t *testing.T, err error, want codes.Code) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with code %v, got nil", want)
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status error, got %T: %v", err, err)
	}
	if st.Code() != want {
		t.Errorf("status code: got %v, want %v", st.Code(), want)
	}
}

// requireNoErr fails the test if err is non-nil.
func requireNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// ── CreateRepository ──────────────────────────────────────────────────────────

// TestCreateRepository_withOrgID_returnsRepository verifies the happy path when
// OrgId is explicitly provided (no org resolution required).
func TestCreateRepository_withOrgID_returnsRepository(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "repo-1", TenantId: "t1", OrgId: "org-1", Name: "myrepo"}
	h := newHandler(&fakeRepo{createRepoResult: want})

	got, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		OrgId:    "org-1",
		Name:     "myrepo",
	})
	requireNoErr(t, err)
	if got.RepoId != want.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, want.RepoId)
	}
}

// TestCreateRepository_withFullName_resolvesOrg verifies that when OrgId is
// absent the handler parses the "org/repo" name and calls GetOrCreateOrganization.
func TestCreateRepository_withFullName_resolvesOrg(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "repo-2", TenantId: "t1", OrgId: "org-auto", Name: "myrepo"}
	h := newHandler(&fakeRepo{
		getOrCreateOrgID: "org-auto",
		createRepoResult: want,
	})

	got, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		Name:     "myorg/myrepo",
	})
	requireNoErr(t, err)
	if got.RepoId != want.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, want.RepoId)
	}
}

// TestCreateRepository_singleComponentName_returnsInvalidArgument ensures names
// without a "/" are rejected when OrgId is not set.
func TestCreateRepository_singleComponentName_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		Name:     "noslash",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestCreateRepository_orgResolutionError_returnsInternal verifies that a
// failure in GetOrCreateOrganization surfaces as codes.Internal.
func TestCreateRepository_orgResolutionError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getOrCreateOrgErr: errors.New("db down")})

	_, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		Name:     "myorg/myrepo",
	})
	requireCode(t, err, codes.Internal)
}

// TestCreateRepository_alreadyExists_returnsExistingRepo verifies that when the
// repo already exists the handler falls back to GetRepositoryByName and returns
// the existing record without an error.
func TestCreateRepository_alreadyExists_returnsExistingRepo(t *testing.T) {
	existing := &metadatav1.Repository{RepoId: "repo-existing", Name: "myrepo"}
	h := newHandler(&fakeRepo{
		createRepoErr:       repository.ErrAlreadyExists,
		getRepoByNameResult: existing,
	})

	got, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		OrgId:    "org-1",
		Name:     "myrepo",
	})
	requireNoErr(t, err)
	if got.RepoId != existing.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, existing.RepoId)
	}
}

// TestCreateRepository_repoError_returnsInternal verifies that unexpected repo
// errors are translated to codes.Internal.
func TestCreateRepository_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{createRepoErr: errors.New("unexpected")})

	_, err := h.CreateRepository(context.Background(), &metadatav1.CreateRepositoryRequest{
		TenantId: "t1",
		OrgId:    "org-1",
		Name:     "myrepo",
	})
	requireCode(t, err, codes.Internal)
}

// ── GetRepository ─────────────────────────────────────────────────────────────

// TestGetRepository_found_returnsRepository verifies the happy path.
func TestGetRepository_found_returnsRepository(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "r1", TenantId: "t1"}
	h := newHandler(&fakeRepo{getRepoResult: want})

	got, err := h.GetRepository(context.Background(), &metadatav1.GetRepositoryRequest{TenantId: "t1", RepoId: "r1"})
	requireNoErr(t, err)
	if got.RepoId != want.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, want.RepoId)
	}
}

// TestGetRepository_notFound_returnsNotFound verifies ErrNotFound maps to
// codes.NotFound.
func TestGetRepository_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getRepoErr: repository.ErrNotFound})

	_, err := h.GetRepository(context.Background(), &metadatav1.GetRepositoryRequest{TenantId: "t1", RepoId: "missing"})
	requireCode(t, err, codes.NotFound)
}

// TestGetRepository_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestGetRepository_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getRepoErr: errors.New("db fail")})

	_, err := h.GetRepository(context.Background(), &metadatav1.GetRepositoryRequest{TenantId: "t1", RepoId: "r1"})
	requireCode(t, err, codes.Internal)
}

// ── GetRepositoryByName ───────────────────────────────────────────────────────

// TestGetRepositoryByName_found_returnsRepository verifies the happy path.
func TestGetRepositoryByName_found_returnsRepository(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "r2", Name: "myorg/myrepo"}
	h := newHandler(&fakeRepo{getRepoByFullNameResult: want})

	got, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "t1",
		Name:     "myorg/myrepo",
	})
	requireNoErr(t, err)
	if got.RepoId != want.RepoId {
		t.Errorf("RepoId: got %q, want %q", got.RepoId, want.RepoId)
	}
}

// TestGetRepositoryByName_emptyTenantID_returnsInvalidArgument verifies that a
// missing tenant_id is rejected early.
func TestGetRepositoryByName_emptyTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "",
		Name:     "myorg/myrepo",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetRepositoryByName_emptyName_returnsInvalidArgument verifies that a
// missing name is rejected early.
func TestGetRepositoryByName_emptyName_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})

	_, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "t1",
		Name:     "",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetRepositoryByName_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestGetRepositoryByName_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getRepoByFullNameErr: repository.ErrNotFound})

	_, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "t1",
		Name:     "myorg/missing",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetRepositoryByName_repoError_returnsInternal verifies unexpected errors
// map to codes.Internal.
func TestGetRepositoryByName_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getRepoByFullNameErr: errors.New("timeout")})

	_, err := h.GetRepositoryByName(context.Background(), &metadatav1.GetRepositoryByNameRequest{
		TenantId: "t1",
		Name:     "myorg/myrepo",
	})
	requireCode(t, err, codes.Internal)
}

// ── DeleteRepository ──────────────────────────────────────────────────────────

// TestDeleteRepository_success_returnsEmpty verifies that a successful delete
// returns an empty proto with no error.
func TestDeleteRepository_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.DeleteRepository(context.Background(), &metadatav1.DeleteRepositoryRequest{TenantId: "t1", RepoId: "r1"})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestDeleteRepository_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestDeleteRepository_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{deleteRepoErr: repository.ErrNotFound})

	_, err := h.DeleteRepository(context.Background(), &metadatav1.DeleteRepositoryRequest{TenantId: "t1", RepoId: "missing"})
	requireCode(t, err, codes.NotFound)
}

// TestDeleteRepository_repoError_returnsInternal verifies unexpected errors map
// to codes.Internal.
func TestDeleteRepository_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{deleteRepoErr: errors.New("db error")})

	_, err := h.DeleteRepository(context.Background(), &metadatav1.DeleteRepositoryRequest{TenantId: "t1", RepoId: "r1"})
	requireCode(t, err, codes.Internal)
}

// ── PutTag ────────────────────────────────────────────────────────────────────

// TestPutTag_success_returnsTag verifies the happy path.
func TestPutTag_success_returnsTag(t *testing.T) {
	want := &metadatav1.Tag{TagId: "tag-1", Name: "v1.0.0", ManifestDigest: "sha256:abc"}
	h := newHandler(&fakeRepo{putTagResult: want})

	got, err := h.PutTag(context.Background(), &metadatav1.PutTagRequest{
		TenantId:       "t1",
		RepoId:         "r1",
		Name:           "v1.0.0",
		ManifestDigest: "sha256:abc",
	})
	requireNoErr(t, err)
	if got.TagId != want.TagId {
		t.Errorf("TagId: got %q, want %q", got.TagId, want.TagId)
	}
	if got.ManifestDigest != want.ManifestDigest {
		t.Errorf("ManifestDigest: got %q, want %q", got.ManifestDigest, want.ManifestDigest)
	}
}

// TestPutTag_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestPutTag_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{putTagErr: errors.New("constraint violation")})

	_, err := h.PutTag(context.Background(), &metadatav1.PutTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "v1.0.0", ManifestDigest: "sha256:abc",
	})
	requireCode(t, err, codes.Internal)
}

// TestPutTag_alreadyExists_returnsAlreadyExists verifies ErrAlreadyExists maps
// to codes.AlreadyExists.
func TestPutTag_alreadyExists_returnsAlreadyExists(t *testing.T) {
	h := newHandler(&fakeRepo{putTagErr: repository.ErrAlreadyExists})

	_, err := h.PutTag(context.Background(), &metadatav1.PutTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "v1.0.0", ManifestDigest: "sha256:abc",
	})
	requireCode(t, err, codes.AlreadyExists)
}

// ── GetTag ────────────────────────────────────────────────────────────────────

// TestGetTag_found_returnsTag verifies the happy path.
func TestGetTag_found_returnsTag(t *testing.T) {
	want := &metadatav1.Tag{TagId: "tag-2", Name: "latest"}
	h := newHandler(&fakeRepo{getTagResult: want})

	got, err := h.GetTag(context.Background(), &metadatav1.GetTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "latest",
	})
	requireNoErr(t, err)
	if got.TagId != want.TagId {
		t.Errorf("TagId: got %q, want %q", got.TagId, want.TagId)
	}
}

// TestGetTag_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestGetTag_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getTagErr: repository.ErrNotFound})

	_, err := h.GetTag(context.Background(), &metadatav1.GetTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "ghost",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetTag_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestGetTag_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getTagErr: errors.New("timeout")})

	_, err := h.GetTag(context.Background(), &metadatav1.GetTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "latest",
	})
	requireCode(t, err, codes.Internal)
}

// ── DeleteTag ─────────────────────────────────────────────────────────────────

// TestDeleteTag_success_returnsEmpty verifies the happy path.
func TestDeleteTag_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.DeleteTag(context.Background(), &metadatav1.DeleteTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "v1.0.0",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestDeleteTag_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestDeleteTag_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{deleteTagErr: repository.ErrNotFound})

	_, err := h.DeleteTag(context.Background(), &metadatav1.DeleteTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "gone",
	})
	requireCode(t, err, codes.NotFound)
}

// TestDeleteTag_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestDeleteTag_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{deleteTagErr: errors.New("db error")})

	_, err := h.DeleteTag(context.Background(), &metadatav1.DeleteTagRequest{
		TenantId: "t1", RepoId: "r1", Name: "v1.0.0",
	})
	requireCode(t, err, codes.Internal)
}

// ── PutManifest ───────────────────────────────────────────────────────────────

// TestPutManifest_success_returnsManifest verifies the happy path.
func TestPutManifest_success_returnsManifest(t *testing.T) {
	want := &metadatav1.Manifest{
		ManifestId: "m-1",
		Digest:     "sha256:deadbeef",
		MediaType:  "application/vnd.oci.image.manifest.v1+json",
		SizeBytes:  512,
	}
	h := newHandler(&fakeRepo{putManifestResult: want})

	got, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId:  "t1",
		RepoId:    "r1",
		Digest:    "sha256:deadbeef",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		RawJson:   []byte(`{}`),
		SizeBytes: 512,
	})
	requireNoErr(t, err)
	if got.ManifestId != want.ManifestId {
		t.Errorf("ManifestId: got %q, want %q", got.ManifestId, want.ManifestId)
	}
	if got.Digest != want.Digest {
		t.Errorf("Digest: got %q, want %q", got.Digest, want.Digest)
	}
}

// TestPutManifest_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestPutManifest_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{putManifestErr: errors.New("storage full")})

	_, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:abc", MediaType: "application/json",
	})
	requireCode(t, err, codes.Internal)
}

// TestPutManifest_alreadyExists_returnsAlreadyExists verifies ErrAlreadyExists
// maps to codes.AlreadyExists.
func TestPutManifest_alreadyExists_returnsAlreadyExists(t *testing.T) {
	h := newHandler(&fakeRepo{putManifestErr: repository.ErrAlreadyExists})

	_, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:abc", MediaType: "application/json",
	})
	requireCode(t, err, codes.AlreadyExists)
}

// TestPutManifest_nilRawJSON_stillSucceeds verifies that nil RawJson does not
// cause a handler panic (nil slice is valid protobuf bytes field).
func TestPutManifest_nilRawJSON_stillSucceeds(t *testing.T) {
	want := &metadatav1.Manifest{ManifestId: "m-nil", Digest: "sha256:aaa"}
	h := newHandler(&fakeRepo{putManifestResult: want})

	got, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId:  "t1",
		RepoId:    "r1",
		Digest:    "sha256:aaa",
		MediaType: "application/json",
		RawJson:   nil,
	})
	requireNoErr(t, err)
	if got.ManifestId != want.ManifestId {
		t.Errorf("ManifestId: got %q, want %q", got.ManifestId, want.ManifestId)
	}
}

// TestPutManifest_oversizeJSON_returnsInvalidArgument verifies that raw_json
// payloads above the 4 MiB cap are rejected before reaching the repository.
func TestPutManifest_oversizeJSON_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{}) // repo should never be called

	_, err := h.PutManifest(context.Background(), &metadatav1.PutManifestRequest{
		TenantId:  "t1",
		RepoId:    "r1",
		Digest:    "sha256:big",
		MediaType: "application/vnd.oci.image.manifest.v1+json",
		RawJson:   make([]byte, 4<<20+1), // one byte over the 4 MiB limit
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ── GetManifest ───────────────────────────────────────────────────────────────

// TestGetManifest_found_returnsManifest verifies the happy path.
func TestGetManifest_found_returnsManifest(t *testing.T) {
	want := &metadatav1.Manifest{ManifestId: "m-2", Digest: "sha256:cafebabe"}
	h := newHandler(&fakeRepo{getManifestResult: want})

	got, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "sha256:cafebabe",
	})
	requireNoErr(t, err)
	if got.ManifestId != want.ManifestId {
		t.Errorf("ManifestId: got %q, want %q", got.ManifestId, want.ManifestId)
	}
}

// TestGetManifest_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestGetManifest_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getManifestErr: repository.ErrNotFound})

	_, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "sha256:missing",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetManifest_tagReference_resolvedByRepo verifies that a non-digest
// reference (tag name) is passed straight to the repo layer, which handles
// resolution internally.
func TestGetManifest_tagReference_resolvedByRepo(t *testing.T) {
	want := &metadatav1.Manifest{ManifestId: "m-3", Digest: "sha256:resolved"}
	h := newHandler(&fakeRepo{getManifestResult: want})

	got, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "latest",
	})
	requireNoErr(t, err)
	if got.ManifestId != want.ManifestId {
		t.Errorf("ManifestId: got %q, want %q", got.ManifestId, want.ManifestId)
	}
}

// TestGetManifest_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestGetManifest_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getManifestErr: errors.New("timeout")})

	_, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "sha256:abc",
	})
	requireCode(t, err, codes.Internal)
}

// TestGetManifest_tagNotFound_returnsNotFound verifies that when tag resolution
// fails with ErrNotFound the handler propagates codes.NotFound.
func TestGetManifest_tagNotFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getManifestErr: repository.ErrNotFound})

	_, err := h.GetManifest(context.Background(), &metadatav1.GetManifestRequest{
		TenantId: "t1", RepoId: "r1", Reference: "nonexistent-tag",
	})
	requireCode(t, err, codes.NotFound)
}

// ── DeleteManifest ────────────────────────────────────────────────────────────

// TestDeleteManifest_success_returnsEmpty verifies the happy path.
func TestDeleteManifest_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.DeleteManifest(context.Background(), &metadatav1.DeleteManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:abc",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestDeleteManifest_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestDeleteManifest_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{deleteManifestErr: repository.ErrNotFound})

	_, err := h.DeleteManifest(context.Background(), &metadatav1.DeleteManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:gone",
	})
	requireCode(t, err, codes.NotFound)
}

// TestDeleteManifest_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestDeleteManifest_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{deleteManifestErr: errors.New("foreign key")})

	_, err := h.DeleteManifest(context.Background(), &metadatav1.DeleteManifestRequest{
		TenantId: "t1", RepoId: "r1", Digest: "sha256:abc",
	})
	requireCode(t, err, codes.Internal)
}

// ── LinkBlob / UnlinkBlob ─────────────────────────────────────────────────────

// TestLinkBlob_success_returnsEmpty verifies the happy path.
func TestLinkBlob_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.LinkBlob(context.Background(), &metadatav1.LinkBlobRequest{
		RepoId:     "r1",
		BlobDigest: "sha256:abc",
		StorageKey: "blobs/t1/sha256/ab/abc",
		SizeBytes:  1024,
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestLinkBlob_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestLinkBlob_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{linkBlobErr: errors.New("tx failed")})

	_, err := h.LinkBlob(context.Background(), &metadatav1.LinkBlobRequest{
		RepoId:     "r1",
		BlobDigest: "sha256:abc",
		StorageKey: "blobs/t1/sha256/ab/abc",
		SizeBytes:  1024,
	})
	requireCode(t, err, codes.Internal)
}

// TestUnlinkBlob_success_returnsEmpty verifies the happy path.
func TestUnlinkBlob_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.UnlinkBlob(context.Background(), &metadatav1.UnlinkBlobRequest{
		RepoId:     "r1",
		BlobDigest: "sha256:abc",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestUnlinkBlob_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestUnlinkBlob_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{unlinkBlobErr: repository.ErrNotFound})

	_, err := h.UnlinkBlob(context.Background(), &metadatav1.UnlinkBlobRequest{
		RepoId:     "r1",
		BlobDigest: "sha256:gone",
	})
	requireCode(t, err, codes.NotFound)
}

// ── GetTenantQuotaUsage ───────────────────────────────────────────────────────

// TestGetTenantQuotaUsage_success_returnsUsage verifies the happy path.
func TestGetTenantQuotaUsage_success_returnsUsage(t *testing.T) {
	want := &metadatav1.QuotaUsage{TenantId: "t1", UsedBytes: 512, QuotaBytes: 10240}
	h := newHandler(&fakeRepo{quotaUsageResult: want})

	got, err := h.GetTenantQuotaUsage(context.Background(), &metadatav1.GetTenantQuotaUsageRequest{TenantId: "t1"})
	requireNoErr(t, err)
	if got.UsedBytes != want.UsedBytes {
		t.Errorf("UsedBytes: got %d, want %d", got.UsedBytes, want.UsedBytes)
	}
	if got.QuotaBytes != want.QuotaBytes {
		t.Errorf("QuotaBytes: got %d, want %d", got.QuotaBytes, want.QuotaBytes)
	}
}

// TestGetTenantQuotaUsage_repoError_returnsInternal verifies unexpected errors
// map to codes.Internal.
func TestGetTenantQuotaUsage_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{quotaUsageErr: errors.New("db fail")})

	_, err := h.GetTenantQuotaUsage(context.Background(), &metadatav1.GetTenantQuotaUsageRequest{TenantId: "t1"})
	requireCode(t, err, codes.Internal)
}

// ── IncrementTenantStorage / DecrementTenantStorage ──────────────────────────

// TestIncrementTenantStorage_success_returnsEmpty verifies the happy path.
func TestIncrementTenantStorage_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.IncrementTenantStorage(context.Background(), &metadatav1.IncrementTenantStorageRequest{
		TenantId: "t1",
		Bytes:    1024,
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestIncrementTenantStorage_repoError_returnsInternal verifies unexpected
// errors map to codes.Internal.
func TestIncrementTenantStorage_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{incrStorageErr: errors.New("db error")})

	_, err := h.IncrementTenantStorage(context.Background(), &metadatav1.IncrementTenantStorageRequest{
		TenantId: "t1",
		Bytes:    512,
	})
	requireCode(t, err, codes.Internal)
}

// TestDecrementTenantStorage_success_returnsEmpty verifies the happy path.
func TestDecrementTenantStorage_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.DecrementTenantStorage(context.Background(), &metadatav1.DecrementTenantStorageRequest{
		TenantId: "t1",
		Bytes:    256,
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestDecrementTenantStorage_repoError_returnsInternal verifies unexpected
// errors map to codes.Internal.
func TestDecrementTenantStorage_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{decrStorageErr: errors.New("db error")})

	_, err := h.DecrementTenantStorage(context.Background(), &metadatav1.DecrementTenantStorageRequest{
		TenantId: "t1",
		Bytes:    256,
	})
	requireCode(t, err, codes.Internal)
}

// ── UpdateScanStatus ──────────────────────────────────────────────────────────

// TestUpdateScanStatus_success_returnsEmpty verifies the happy path.
func TestUpdateScanStatus_success_returnsEmpty(t *testing.T) {
	h := newHandler(&fakeRepo{})

	resp, err := h.UpdateScanStatus(context.Background(), &metadatav1.UpdateScanStatusRequest{
		ScanId:   "scan-1",
		TenantId: "t1",
		Status:   "complete",
	})
	requireNoErr(t, err)
	if resp == nil {
		t.Error("expected non-nil empty response")
	}
}

// TestUpdateScanStatus_notFound_returnsNotFound verifies that a missing scan ID
// maps to codes.NotFound.
func TestUpdateScanStatus_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{upsertScanErr: repository.ErrNotFound})

	_, err := h.UpdateScanStatus(context.Background(), &metadatav1.UpdateScanStatusRequest{
		ScanId:   "scan-missing",
		TenantId: "t1",
		Status:   "complete",
	})
	requireCode(t, err, codes.NotFound)
}

// TestUpdateScanStatus_repoError_returnsInternal verifies unexpected errors map
// to codes.Internal.
func TestUpdateScanStatus_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{upsertScanErr: errors.New("db fail")})

	_, err := h.UpdateScanStatus(context.Background(), &metadatav1.UpdateScanStatusRequest{
		ScanId:   "scan-1",
		TenantId: "t1",
		Status:   "running",
	})
	requireCode(t, err, codes.Internal)
}

// ── GetScanResult ─────────────────────────────────────────────────────────────

// TestGetScanResult_found_returnsScanResult verifies the happy path including
// that SeverityCounts fields are passed through.
func TestGetScanResult_found_returnsScanResult(t *testing.T) {
	want := &metadatav1.ScanResult{
		ScanId:         "scan-1",
		ManifestDigest: "sha256:abc",
		Status:         "complete",
		SeverityCounts: map[string]int32{"CRITICAL": 2, "HIGH": 5},
	}
	h := newHandler(&fakeRepo{getScanResult: want})

	got, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:abc",
	})
	requireNoErr(t, err)
	if got.ScanId != want.ScanId {
		t.Errorf("ScanId: got %q, want %q", got.ScanId, want.ScanId)
	}
	if got.SeverityCounts["CRITICAL"] != 2 {
		t.Errorf("CRITICAL count: got %d, want 2", got.SeverityCounts["CRITICAL"])
	}
}

// TestGetScanResult_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestGetScanResult_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getScanErr: repository.ErrNotFound})

	_, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:missing",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetScanResult_repoError_returnsInternal verifies unexpected errors map to
// codes.Internal.
func TestGetScanResult_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{getScanErr: errors.New("timeout")})

	_, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:abc",
	})
	requireCode(t, err, codes.Internal)
}

// TestGetScanResult_pendingStatus_returnsScanResult verifies that a scan in
// pending state is returned without error.
func TestGetScanResult_pendingStatus_returnsScanResult(t *testing.T) {
	want := &metadatav1.ScanResult{ScanId: "scan-2", Status: "pending"}
	h := newHandler(&fakeRepo{getScanResult: want})

	got, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:def",
	})
	requireNoErr(t, err)
	if got.Status != "pending" {
		t.Errorf("Status: got %q, want pending", got.Status)
	}
}

// TestGetScanResult_emptySeverityCounts_returnsResult verifies that a scan
// result with no severity counts is returned cleanly.
func TestGetScanResult_emptySeverityCounts_returnsResult(t *testing.T) {
	want := &metadatav1.ScanResult{ScanId: "scan-3", Status: "complete", SeverityCounts: map[string]int32{}}
	h := newHandler(&fakeRepo{getScanResult: want})

	got, err := h.GetScanResult(context.Background(), &metadatav1.GetScanResultRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:clean",
	})
	requireNoErr(t, err)
	if got.ScanId != "scan-3" {
		t.Errorf("ScanId: got %q, want scan-3", got.ScanId)
	}
}

// ── GetTenantVulnerabilityCount ───────────────────────────────────────────────

// TestGetTenantVulnerabilityCount_success_returnsCorrectTotals verifies the
// happy path and that total = critical + high.
func TestGetTenantVulnerabilityCount_success_returnsCorrectTotals(t *testing.T) {
	h := newHandler(&fakeRepo{vulnTotal: 7, vulnCritical: 3, vulnHigh: 4})

	got, err := h.GetTenantVulnerabilityCount(context.Background(), &metadatav1.GetTenantVulnerabilityCountRequest{
		TenantId: "t1",
	})
	requireNoErr(t, err)
	if got.Total != 7 {
		t.Errorf("Total: got %d, want 7", got.Total)
	}
	if got.CriticalCount != 3 {
		t.Errorf("CriticalCount: got %d, want 3", got.CriticalCount)
	}
	if got.HighCount != 4 {
		t.Errorf("HighCount: got %d, want 4", got.HighCount)
	}
}

// TestGetTenantVulnerabilityCount_repoError_returnsInternal verifies unexpected
// errors map to codes.Internal.
func TestGetTenantVulnerabilityCount_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{vulnErr: errors.New("db fail")})

	_, err := h.GetTenantVulnerabilityCount(context.Background(), &metadatav1.GetTenantVulnerabilityCountRequest{
		TenantId: "t1",
	})
	requireCode(t, err, codes.Internal)
}

// ── GetSecurityOverview (FE-API-020) ──────────────────────────────────────────

// TestGetSecurityOverview_emptyTenant_returnsZeroValues verifies that a tenant
// with no scans, no tags, and no recent activity returns a fully zero-valued
// SecurityOverview — the frontend distinguishes "never scanned" from "clean"
// via tags_scanned + recent_scans_24h, both zero here.
func TestGetSecurityOverview_emptyTenant_returnsZeroValues(t *testing.T) {
	h := newHandler(&fakeRepo{
		securityOverview: &repository.SecurityOverview{},
	})

	got, err := h.GetSecurityOverview(context.Background(), &metadatav1.GetSecurityOverviewRequest{
		TenantId: "t1",
	})
	requireNoErr(t, err)
	if got.GetOpenVulnerabilitiesTotal() != 0 {
		t.Errorf("OpenVulnerabilitiesTotal: got %d, want 0", got.GetOpenVulnerabilitiesTotal())
	}
	if got.GetSeverityCounts().GetCritical() != 0 {
		t.Errorf("Critical: got %d, want 0", got.GetSeverityCounts().GetCritical())
	}
	if got.GetScanCoverage().GetTagsTotal() != 0 {
		t.Errorf("TagsTotal: got %d, want 0", got.GetScanCoverage().GetTagsTotal())
	}
	if got.GetScanCoverage().GetPercent() != 0 {
		t.Errorf("Percent: got %f, want 0", got.GetScanCoverage().GetPercent())
	}
	if got.GetRecentScans_24H() != 0 {
		t.Errorf("RecentScans24h: got %d, want 0", got.GetRecentScans_24H())
	}
}

// TestGetSecurityOverview_populated_returnsAggregatedView verifies the full
// happy path: counts flow through to the proto, percent is computed correctly,
// and partial coverage (3 of 4 tags scanned = 75%) is preserved.
func TestGetSecurityOverview_populated_returnsAggregatedView(t *testing.T) {
	h := newHandler(&fakeRepo{
		securityOverview: &repository.SecurityOverview{
			OpenVulnerabilitiesTotal: 12,
			Critical:                 2,
			High:                     3,
			Medium:                   4,
			Low:                      2,
			Negligible:               1,
			TagsTotal:                4,
			TagsScanned:              3,
			RecentScans24h:           5,
			DaysSinceLastScan:        2,
		},
	})

	got, err := h.GetSecurityOverview(context.Background(), &metadatav1.GetSecurityOverviewRequest{
		TenantId: "t1",
	})
	requireNoErr(t, err)
	if got.GetOpenVulnerabilitiesTotal() != 12 {
		t.Errorf("Total: got %d, want 12", got.GetOpenVulnerabilitiesTotal())
	}
	if got.GetSeverityCounts().GetCritical() != 2 || got.GetSeverityCounts().GetHigh() != 3 {
		t.Errorf("severity: got %+v, want C=2 H=3", got.GetSeverityCounts())
	}
	if got.GetScanCoverage().GetTagsScanned() != 3 || got.GetScanCoverage().GetTagsTotal() != 4 {
		t.Errorf("coverage: got %+v, want 3/4", got.GetScanCoverage())
	}
	if got.GetScanCoverage().GetPercent() != 75.0 {
		t.Errorf("percent: got %f, want 75.0", got.GetScanCoverage().GetPercent())
	}
	if got.GetRecentScans_24H() != 5 {
		t.Errorf("RecentScans24h: got %d, want 5", got.GetRecentScans_24H())
	}
	if got.GetDaysSinceLastScan() != 2 {
		t.Errorf("DaysSinceLastScan: got %d, want 2", got.GetDaysSinceLastScan())
	}
}

// TestGetSecurityOverview_emptyTenantID_returnsInvalidArgument enforces the
// CLAUDE.md §7 input-validation rule at the handler boundary.
func TestGetSecurityOverview_emptyTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetSecurityOverview(context.Background(), &metadatav1.GetSecurityOverviewRequest{TenantId: ""})
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetSecurityOverview_repoError_returnsInternal verifies unexpected errors
// from the repository map to codes.Internal (and never leak the underlying
// driver text via the gRPC status).
func TestGetSecurityOverview_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{securityOverviewErr: errors.New("db fail")})
	_, err := h.GetSecurityOverview(context.Background(), &metadatav1.GetSecurityOverviewRequest{TenantId: "t1"})
	requireCode(t, err, codes.Internal)
}

// ── UpdateRepositoryQuota ─────────────────────────────────────────────────────

// TestUpdateRepositoryQuota_success_returnsRepository verifies the happy path.
func TestUpdateRepositoryQuota_success_returnsRepository(t *testing.T) {
	want := &metadatav1.Repository{RepoId: "r1", StorageQuota: 20 << 30}
	h := newHandler(&fakeRepo{updateQuotaResult: want})

	got, err := h.UpdateRepositoryQuota(context.Background(), &metadatav1.UpdateRepositoryQuotaRequest{
		TenantId:     "t1",
		RepoId:       "r1",
		StorageQuota: 20 << 30,
	})
	requireNoErr(t, err)
	if got.StorageQuota != want.StorageQuota {
		t.Errorf("StorageQuota: got %d, want %d", got.StorageQuota, want.StorageQuota)
	}
}

// TestUpdateRepositoryQuota_notFound_returnsNotFound verifies ErrNotFound mapping.
func TestUpdateRepositoryQuota_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{updateQuotaErr: repository.ErrNotFound})

	_, err := h.UpdateRepositoryQuota(context.Background(), &metadatav1.UpdateRepositoryQuotaRequest{
		TenantId: "t1", RepoId: "missing", StorageQuota: 1024,
	})
	requireCode(t, err, codes.NotFound)
}

// ── mapErr (internal helper) ──────────────────────────────────────────────────

// TestMapErr_nil_returnsNil verifies that mapErr passes through nil unchanged.
func TestMapErr_nil_returnsNil(t *testing.T) {
	if mapErr(nil) != nil {
		t.Error("expected mapErr(nil) == nil")
	}
}

// TestMapErr_notFound_returnsNotFoundCode verifies ErrNotFound sentinel mapping.
func TestMapErr_notFound_returnsNotFoundCode(t *testing.T) {
	requireCode(t, mapErr(repository.ErrNotFound), codes.NotFound)
}

// TestMapErr_alreadyExists_returnsAlreadyExistsCode verifies ErrAlreadyExists
// sentinel mapping.
func TestMapErr_alreadyExists_returnsAlreadyExistsCode(t *testing.T) {
	requireCode(t, mapErr(repository.ErrAlreadyExists), codes.AlreadyExists)
}

// TestMapErr_unknownError_returnsInternalCode verifies that any non-sentinel
// error maps to codes.Internal.
func TestMapErr_unknownError_returnsInternalCode(t *testing.T) {
	requireCode(t, mapErr(errors.New("random")), codes.Internal)
}

// ── Streaming stream fakes ────────────────────────────────────────────────────

// baseServerStream is a minimal grpc.ServerStream implementation shared by all
// typed stream fakes below. It satisfies the interface without real gRPC
// transport — it stores a background context and discards all control calls.
type baseServerStream struct{}

func (b *baseServerStream) SetHeader(metadata.MD) error  { return nil }
func (b *baseServerStream) SendHeader(metadata.MD) error { return nil }
func (b *baseServerStream) SetTrailer(metadata.MD)       {}
func (b *baseServerStream) Context() context.Context     { return context.Background() }
func (b *baseServerStream) SendMsg(m any) error          { return nil }
func (b *baseServerStream) RecvMsg(m any) error          { return nil }

// fakeListReposStream implements metadatav1.MetadataService_ListRepositoriesServer.
// It captures every Send call so tests can inspect the items streamed.
type fakeListReposStream struct {
	baseServerStream
	sent    []*metadatav1.Repository
	sendErr error
}

func (f *fakeListReposStream) Send(r *metadatav1.Repository) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, r)
	return nil
}

// Compile-time check: fakeListReposStream satisfies the generated stream interface.
var _ metadatav1.MetadataService_ListRepositoriesServer = (*fakeListReposStream)(nil)

// fakeListTagsStream implements metadatav1.MetadataService_ListTagsServer.
type fakeListTagsStream struct {
	baseServerStream
	sent    []*metadatav1.Tag
	sendErr error
}

func (f *fakeListTagsStream) Send(t *metadatav1.Tag) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, t)
	return nil
}

var _ metadatav1.MetadataService_ListTagsServer = (*fakeListTagsStream)(nil)

// fakeListUntaggedStream implements metadatav1.MetadataService_ListUntaggedManifestsServer.
type fakeListUntaggedStream struct {
	baseServerStream
	sent    []*metadatav1.Manifest
	sendErr error
}

func (f *fakeListUntaggedStream) Send(m *metadatav1.Manifest) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, m)
	return nil
}

var _ metadatav1.MetadataService_ListUntaggedManifestsServer = (*fakeListUntaggedStream)(nil)

// fakeListOrphanedStream implements metadatav1.MetadataService_ListOrphanedBlobsServer.
type fakeListOrphanedStream struct {
	baseServerStream
	sent    []*metadatav1.BlobRef
	sendErr error
}

func (f *fakeListOrphanedStream) Send(b *metadatav1.BlobRef) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sent = append(f.sent, b)
	return nil
}

var _ metadatav1.MetadataService_ListOrphanedBlobsServer = (*fakeListOrphanedStream)(nil)

// ── ListRepositories (streaming) ──────────────────────────────────────────────

// TestListRepositories_success_sendsAllRepos verifies that every repository
// returned by the repo layer is forwarded to the stream.
func TestListRepositories_success_sendsAllRepos(t *testing.T) {
	repos := []*metadatav1.Repository{
		{RepoId: "r1", Name: "myorg/a"},
		{RepoId: "r2", Name: "myorg/b"},
	}
	h := newHandler(&fakeRepo{listReposResult: repos})
	stream := &fakeListReposStream{}

	err := h.ListRepositories(&metadatav1.ListRepositoriesRequest{TenantId: "t1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d repos, want 2", len(stream.sent))
	}
}

// TestListRepositories_repoError_returnsInternal verifies that a repo layer
// error stops the stream with codes.Internal.
func TestListRepositories_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{listReposErr: errors.New("db fail")})
	stream := &fakeListReposStream{}

	err := h.ListRepositories(&metadatav1.ListRepositoriesRequest{TenantId: "t1"}, stream)
	requireCode(t, err, codes.Internal)
}

// TestListRepositories_empty_sendsNothing verifies that an empty result
// completes without error and without sending any messages.
func TestListRepositories_empty_sendsNothing(t *testing.T) {
	h := newHandler(&fakeRepo{listReposResult: nil})
	stream := &fakeListReposStream{}

	err := h.ListRepositories(&metadatav1.ListRepositoriesRequest{TenantId: "t1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 0 {
		t.Errorf("expected 0 sent, got %d", len(stream.sent))
	}
}

// TestListRepositories_sendError_propagates verifies that a stream Send error
// is returned to the caller.
func TestListRepositories_sendError_propagates(t *testing.T) {
	repos := []*metadatav1.Repository{{RepoId: "r1"}}
	h := newHandler(&fakeRepo{listReposResult: repos})
	stream := &fakeListReposStream{sendErr: errors.New("client disconnected")}

	err := h.ListRepositories(&metadatav1.ListRepositoriesRequest{TenantId: "t1"}, stream)
	if err == nil {
		t.Fatal("expected error from send failure, got nil")
	}
}

// ── ListTags (streaming) ──────────────────────────────────────────────────────

// TestListTags_success_sendsAllTags verifies that every tag is forwarded.
func TestListTags_success_sendsAllTags(t *testing.T) {
	tags := []*metadatav1.Tag{
		{TagId: "t1", Name: "latest"},
		{TagId: "t2", Name: "v1.0.0"},
	}
	h := newHandler(&fakeRepo{listTagsResult: tags})
	stream := &fakeListTagsStream{}

	err := h.ListTags(&metadatav1.ListTagsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d tags, want 2", len(stream.sent))
	}
}

// TestListTags_repoError_returnsInternal verifies a repo error stops the stream.
func TestListTags_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{listTagsErr: errors.New("db fail")})
	stream := &fakeListTagsStream{}

	err := h.ListTags(&metadatav1.ListTagsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireCode(t, err, codes.Internal)
}

// TestListTags_empty_sendsNothing verifies an empty result completes cleanly.
func TestListTags_empty_sendsNothing(t *testing.T) {
	h := newHandler(&fakeRepo{listTagsResult: nil})
	stream := &fakeListTagsStream{}

	err := h.ListTags(&metadatav1.ListTagsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 0 {
		t.Errorf("expected 0 sent, got %d", len(stream.sent))
	}
}

// ── ListUntaggedManifests (streaming) ─────────────────────────────────────────

// TestListUntaggedManifests_success_sendsAll verifies that all untagged
// manifests are forwarded to the stream.
func TestListUntaggedManifests_success_sendsAll(t *testing.T) {
	manifests := []*metadatav1.Manifest{
		{ManifestId: "m1", Digest: "sha256:aaa"},
		{ManifestId: "m2", Digest: "sha256:bbb"},
	}
	h := newHandler(&fakeRepo{listUntaggedResult: manifests})
	stream := &fakeListUntaggedStream{}

	err := h.ListUntaggedManifests(&metadatav1.ListUntaggedManifestsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d manifests, want 2", len(stream.sent))
	}
}

// TestListUntaggedManifests_repoError_returnsInternal verifies a repo error
// stops the stream.
func TestListUntaggedManifests_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{listUntaggedErr: errors.New("db fail")})
	stream := &fakeListUntaggedStream{}

	err := h.ListUntaggedManifests(&metadatav1.ListUntaggedManifestsRequest{TenantId: "t1", RepoId: "r1"}, stream)
	requireCode(t, err, codes.Internal)
}

// ── ListOrphanedBlobs (streaming) ─────────────────────────────────────────────

// TestListOrphanedBlobs_success_sendsAll verifies that all orphaned blobs are
// forwarded to the stream.
func TestListOrphanedBlobs_success_sendsAll(t *testing.T) {
	blobs := []*metadatav1.BlobRef{
		{Digest: "sha256:orphan1", SizeBytes: 512},
		{Digest: "sha256:orphan2", SizeBytes: 1024},
	}
	h := newHandler(&fakeRepo{listOrphanedResult: blobs})
	stream := &fakeListOrphanedStream{}

	err := h.ListOrphanedBlobs(&metadatav1.ListOrphanedBlobsRequest{}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 2 {
		t.Errorf("sent %d blobs, want 2", len(stream.sent))
	}
}

// TestListOrphanedBlobs_repoError_returnsInternal verifies a repo error stops
// the stream.
func TestListOrphanedBlobs_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{listOrphanedErr: errors.New("db fail")})
	stream := &fakeListOrphanedStream{}

	err := h.ListOrphanedBlobs(&metadatav1.ListOrphanedBlobsRequest{}, stream)
	requireCode(t, err, codes.Internal)
}

// TestListOrphanedBlobs_empty_sendsNothing verifies an empty result completes
// cleanly.
func TestListOrphanedBlobs_empty_sendsNothing(t *testing.T) {
	h := newHandler(&fakeRepo{listOrphanedResult: nil})
	stream := &fakeListOrphanedStream{}

	err := h.ListOrphanedBlobs(&metadatav1.ListOrphanedBlobsRequest{}, stream)
	requireNoErr(t, err)
	if len(stream.sent) != 0 {
		t.Errorf("expected 0 sent, got %d", len(stream.sent))
	}
}

// ─── FE-API-014: ListTenantVulnerabilities ──────────────────────────────────

// TestListTenantVulnerabilities_missingTenant_returnsInvalidArgument verifies
// the guard rejects empty tenant_id before touching the repository.
func TestListTenantVulnerabilities_missingTenant_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.ListTenantVulnerabilities(context.Background(), &metadatav1.ListTenantVulnerabilitiesRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

// TestListTenantVulnerabilities_invalidSeverity_returnsInvalidArgument
// verifies an out-of-allowlist severity is rejected at the gRPC layer so the
// repository never sees junk values.
func TestListTenantVulnerabilities_invalidSeverity_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.ListTenantVulnerabilities(context.Background(), &metadatav1.ListTenantVulnerabilitiesRequest{
		TenantId: "t1",
		Severity: "SEVERE",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestListTenantVulnerabilities_happyPath_mapsRowsToProto verifies one CVE
// rolls up to one TenantVulnerability with its affected tags + timestamps.
func TestListTenantVulnerabilities_happyPath_mapsRowsToProto(t *testing.T) {
	first := time.Now().Add(-3 * time.Hour)
	last := time.Now().Add(-1 * time.Hour)
	fake := &fakeRepo{
		listVulnsRows: []repository.VulnerabilityRow{
			{
				CVE: "CVE-2024-1", Severity: "CRITICAL",
				PackageName: "openssl", PackageVersion: "1.0.0", FixedIn: "1.0.1",
				FirstSeen: first, LastSeen: last,
			},
		},
		listVulnsNext: "cursor-2",
	}
	h := newHandler(fake)
	resp, err := h.ListTenantVulnerabilities(context.Background(), &metadatav1.ListTenantVulnerabilitiesRequest{
		TenantId: "t1", Severity: "critical", PageSize: 25,
	})
	requireNoErr(t, err)
	if len(resp.Vulnerabilities) != 1 || resp.Vulnerabilities[0].CveId != "CVE-2024-1" {
		t.Fatalf("expected 1 vuln CVE-2024-1, got %+v", resp.Vulnerabilities)
	}
	if resp.NextPageToken != "cursor-2" {
		t.Errorf("NextPageToken: got %q, want cursor-2", resp.NextPageToken)
	}
	// Severity is uppercased before being forwarded.
	if got := fake.listVulnsCalls[0].severity; got != "CRITICAL" {
		t.Errorf("severity forwarded: got %q, want CRITICAL", got)
	}
}

// TestListTenantVulnerabilities_pageTokenErrorBubblesAsInvalidArgument
// verifies a malformed cursor surfaces as InvalidArgument so the BFF can
// return 400 rather than 500.
func TestListTenantVulnerabilities_pageTokenErrorBubblesAsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{listVulnsErr: errors.New("decode page_token: bad")})
	_, err := h.ListTenantVulnerabilities(context.Background(), &metadatav1.ListTenantVulnerabilitiesRequest{
		TenantId: "t1", PageToken: "***",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ─── FE-API-015: ListScanHistory ────────────────────────────────────────────

// TestListScanHistory_missingTenant_returnsInvalidArgument verifies the
// guard rejects empty tenant_id before touching the repository.
func TestListScanHistory_missingTenant_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.ListScanHistory(context.Background(), &metadatav1.ListScanHistoryRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

// TestListScanHistory_defaultsSince30DaysAgo verifies the default time window
// is applied when the request omits `since`.
func TestListScanHistory_defaultsSince30DaysAgo(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake)
	_, err := h.ListScanHistory(context.Background(), &metadatav1.ListScanHistoryRequest{TenantId: "t1"})
	requireNoErr(t, err)
	if len(fake.listScansCalls) != 1 {
		t.Fatalf("expected one repo call, got %d", len(fake.listScansCalls))
	}
	cutoff := fake.listScansCalls[0].since
	age := time.Since(cutoff)
	if age < 29*24*time.Hour || age > 31*24*time.Hour {
		t.Errorf("default since not within 30 days: %v", age)
	}
}

// TestListScanHistory_completeStatusMappedToCompleted verifies the wire
// status string is translated from "complete" to "completed" as documented.
func TestListScanHistory_completeStatusMappedToCompleted(t *testing.T) {
	completed := time.Now().Add(-1 * time.Hour)
	fake := &fakeRepo{
		listScansRows: []repository.ScanHistoryRow{
			{
				ScanID: "s1", Repo: "myorg/myrepo", Tag: "v1",
				ManifestDigest: "sha256:abc", Scanner: "trivy",
				CompletedAt: completed, Status: "complete",
				Critical: 1, High: 2, Medium: 0, Low: 0, Negligible: 0,
				Trigger: "push",
			},
		},
		listScansNext: "next-1",
	}
	h := newHandler(fake)
	resp, err := h.ListScanHistory(context.Background(), &metadatav1.ListScanHistoryRequest{
		TenantId: "t1", Since: timestamppb.New(completed.Add(-7 * 24 * time.Hour)),
	})
	requireNoErr(t, err)
	if len(resp.Scans) != 1 {
		t.Fatalf("expected 1 scan, got %d", len(resp.Scans))
	}
	if resp.Scans[0].Status != "completed" {
		t.Errorf("status: got %q, want completed", resp.Scans[0].Status)
	}
	if resp.Scans[0].Trigger != "push" {
		t.Errorf("trigger: got %q, want push", resp.Scans[0].Trigger)
	}
	if resp.Scans[0].GetSeverityCounts().GetHigh() != 2 {
		t.Errorf("high count not wired through: got %v", resp.Scans[0].GetSeverityCounts())
	}
	if resp.NextPageToken != "next-1" {
		t.Errorf("NextPageToken: got %q, want next-1", resp.NextPageToken)
	}
}

// TestListScanHistory_pageTokenErrorBubblesAsInvalidArgument verifies a
// malformed cursor surfaces as InvalidArgument so the BFF can return 400.
func TestListScanHistory_pageTokenErrorBubblesAsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{listScansErr: errors.New("decode page_token: bad")})
	_, err := h.ListScanHistory(context.Background(), &metadatav1.ListScanHistoryRequest{
		TenantId: "t1", PageToken: "junk",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ─── FE-API-017: ListTenantRemediations ─────────────────────────────────────

// TestListTenantRemediations_missingTenant_returnsInvalidArgument verifies
// the empty-tenant guard fires before touching the repository.
func TestListTenantRemediations_missingTenant_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.ListTenantRemediations(context.Background(), &metadatav1.ListTenantRemediationsRequest{})
	requireCode(t, err, codes.InvalidArgument)
}

// TestListTenantRemediations_happyPath_mapsRowsToProto verifies one row
// rolls through with its affected list, CVEs, and cursor. Also asserts the
// handler forwarded tenant_id + page_size to the repository.
func TestListTenantRemediations_happyPath_mapsRowsToProto(t *testing.T) {
	fake := &fakeRepo{
		listRemRows: []repository.RemediationRow{
			{
				PackageName: "openssl", FromVersion: "1.0.0", ToVersion: "1.0.1",
				CVEsFixed: []string{"CVE-2024-1", "CVE-2024-2"}, CVEsFixedCount: 2,
				MaxSeverity: "CRITICAL",
				Affected: []repository.RemediationAffectedRow{
					{Repo: "acme/api", Tag: "v1", Digest: "sha256:abc"},
				},
				AffectedCount: 5,
			},
		},
		listRemNext: "cursor-2",
	}
	h := newHandler(fake)
	resp, err := h.ListTenantRemediations(context.Background(), &metadatav1.ListTenantRemediationsRequest{
		TenantId: "t1", PageSize: 25,
	})
	requireNoErr(t, err)
	if len(resp.Remediations) != 1 {
		t.Fatalf("expected 1 remediation, got %d", len(resp.Remediations))
	}
	r0 := resp.Remediations[0]
	if r0.PackageName != "openssl" || r0.FromVersion != "1.0.0" || r0.ToVersion != "1.0.1" {
		t.Errorf("unexpected upgrade: %s %s -> %s", r0.PackageName, r0.FromVersion, r0.ToVersion)
	}
	if r0.CvesFixedCount != 2 || len(r0.CvesFixed) != 2 {
		t.Errorf("CVE fields: count=%d slice=%v", r0.CvesFixedCount, r0.CvesFixed)
	}
	if r0.AffectedCount != 5 || len(r0.Affected) != 1 {
		t.Errorf("affected: count=%d slice=%d", r0.AffectedCount, len(r0.Affected))
	}
	if resp.NextPageToken != "cursor-2" {
		t.Errorf("NextPageToken: got %q, want cursor-2", resp.NextPageToken)
	}
	if len(fake.listRemCalls) != 1 || fake.listRemCalls[0].tenantID != "t1" || fake.listRemCalls[0].limit != 25 {
		t.Errorf("forwarded args: %+v", fake.listRemCalls)
	}
}

// TestListTenantRemediations_pageTokenErrorBubblesAsInvalidArgument ensures
// the repository's "decode page_token" error is mapped to InvalidArgument so
// the BFF can surface 400 instead of 500.
func TestListTenantRemediations_pageTokenErrorBubblesAsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{listRemErr: errors.New("decode page_token: bad")})
	_, err := h.ListTenantRemediations(context.Background(), &metadatav1.ListTenantRemediationsRequest{
		TenantId: "t1", PageToken: "***",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ── UpsertScanSBOM (FE-API-033) ──────────────────────────────────────────────

// TestUpsertScanSBOM_happyPath_forwardsArgsAndReturnsEmpty verifies the
// handler unwraps the request and delegates to the repository, returning the
// empty response when the upsert succeeds.
func TestUpsertScanSBOM_happyPath_forwardsArgsAndReturnsEmpty(t *testing.T) {
	fake := &fakeRepo{}
	h := newHandler(fake)
	_, err := h.UpsertScanSBOM(context.Background(), &metadatav1.UpsertScanSBOMRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:abc",
		Format:         "spdx-json",
		SbomJson:       []byte(`{"spdxVersion":"SPDX-2.3"}`),
	})
	requireNoErr(t, err)
	if len(fake.upsertSBOMCalls) != 1 {
		t.Fatalf("expected 1 upsert call, got %d", len(fake.upsertSBOMCalls))
	}
	got := fake.upsertSBOMCalls[0]
	if got.tenantID != "t1" || got.manifestDigest != "sha256:abc" || got.format != "spdx-json" || got.sbomLen == 0 {
		t.Errorf("forwarded args: %+v", got)
	}
}

// TestUpsertScanSBOM_missingTenant_returnsInvalidArgument ensures the handler
// validates required fields before touching the repository.
func TestUpsertScanSBOM_missingTenant_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.UpsertScanSBOM(context.Background(), &metadatav1.UpsertScanSBOMRequest{
		ManifestDigest: "sha256:abc",
		Format:         "spdx-json",
		SbomJson:       []byte(`{}`),
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertScanSBOM_missingDigest_returnsInvalidArgument covers the second
// required field.
func TestUpsertScanSBOM_missingDigest_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.UpsertScanSBOM(context.Background(), &metadatav1.UpsertScanSBOMRequest{
		TenantId: "t1",
		Format:   "spdx-json",
		SbomJson: []byte(`{}`),
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertScanSBOM_unknownFormat_returnsInvalidArgument blocks bogus format
// strings up front so a typo never reaches the database.
func TestUpsertScanSBOM_unknownFormat_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.UpsertScanSBOM(context.Background(), &metadatav1.UpsertScanSBOMRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:abc",
		Format:         "swid-xml", // never accepted
		SbomJson:       []byte(`{}`),
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertScanSBOM_emptyBytes_returnsInvalidArgument prevents writing an
// empty SBOM that would later 404 on read for the wrong reason.
func TestUpsertScanSBOM_emptyBytes_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.UpsertScanSBOM(context.Background(), &metadatav1.UpsertScanSBOMRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:abc",
		Format:         "spdx-json",
		SbomJson:       nil,
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestUpsertScanSBOM_repoNotFound_returnsNotFound surfaces the "no scan row
// to attach the SBOM to" case.
func TestUpsertScanSBOM_repoNotFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{upsertSBOMErr: repository.ErrNotFound})
	_, err := h.UpsertScanSBOM(context.Background(), &metadatav1.UpsertScanSBOMRequest{
		TenantId:       "t1",
		ManifestDigest: "sha256:abc",
		Format:         "spdx-json",
		SbomJson:       []byte(`{}`),
	})
	requireCode(t, err, codes.NotFound)
}

// ── GetScanSBOM (FE-API-033) ─────────────────────────────────────────────────

// TestGetScanSBOM_happyPath_returnsBytes verifies the handler maps the
// repository's SBOMResult straight onto the response message.
func TestGetScanSBOM_happyPath_returnsBytes(t *testing.T) {
	want := &repository.SBOMResult{Format: "spdx-json", SBOMJSON: []byte(`{"spdxVersion":"SPDX-2.3"}`)}
	fake := &fakeRepo{getSBOMResult: want}
	h := newHandler(fake)
	resp, err := h.GetScanSBOM(context.Background(), &metadatav1.GetScanSBOMRequest{
		TenantId: "t1", ManifestDigest: "sha256:abc",
	})
	requireNoErr(t, err)
	if resp.GetFormat() != want.Format {
		t.Errorf("format: got %q want %q", resp.GetFormat(), want.Format)
	}
	if string(resp.GetSbomJson()) != string(want.SBOMJSON) {
		t.Errorf("sbom bytes mismatch")
	}
	if len(fake.getSBOMCalls) != 1 || fake.getSBOMCalls[0].tenantID != "t1" {
		t.Errorf("forwarded args: %+v", fake.getSBOMCalls)
	}
}

// TestGetScanSBOM_notFound_returnsNotFound covers both "never scanned" and
// "scanned but no SBOM recorded" — the repository collapses them to the same
// ErrNotFound.
func TestGetScanSBOM_notFound_returnsNotFound(t *testing.T) {
	h := newHandler(&fakeRepo{getSBOMErr: repository.ErrNotFound})
	_, err := h.GetScanSBOM(context.Background(), &metadatav1.GetScanSBOMRequest{
		TenantId: "t1", ManifestDigest: "sha256:abc",
	})
	requireCode(t, err, codes.NotFound)
}

// TestGetScanSBOM_missingTenant_returnsInvalidArgument validates the request
// before any repository round-trip.
func TestGetScanSBOM_missingTenant_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetScanSBOM(context.Background(), &metadatav1.GetScanSBOMRequest{
		ManifestDigest: "sha256:abc",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetScanSBOM_missingDigest_returnsInvalidArgument mirrors the tenant
// guard for the digest field.
func TestGetScanSBOM_missingDigest_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetScanSBOM(context.Background(), &metadatav1.GetScanSBOMRequest{
		TenantId: "t1",
	})
	requireCode(t, err, codes.InvalidArgument)
}

// ── GetTenantUsage (FE-API-028) ──────────────────────────────────────────────

// TestGetTenantUsage_happyPath_returnsAggregate ensures the handler simply
// forwards the repo's response. The CTE arithmetic is tested in the
// repository layer (integration test); here we just check the wire mapping.
func TestGetTenantUsage_happyPath_returnsAggregate(t *testing.T) {
	want := &metadatav1.TenantUsage{
		StorageUsedBytes:  4096,
		StorageQuotaBytes: 10 << 30,
		RepositoryCount:   3,
		OrganizationCount: 2,
	}
	h := newHandler(&fakeRepo{tenantUsageResp: want})
	got, err := h.GetTenantUsage(context.Background(), &metadatav1.GetTenantUsageRequest{
		TenantId: "00000000-0000-0000-0000-000000000001",
	})
	requireNoErr(t, err)
	if got.GetStorageUsedBytes() != want.StorageUsedBytes ||
		got.GetStorageQuotaBytes() != want.StorageQuotaBytes ||
		got.GetRepositoryCount() != want.RepositoryCount ||
		got.GetOrganizationCount() != want.OrganizationCount {
		t.Errorf("usage mismatch: got %+v, want %+v", got, want)
	}
}

// TestGetTenantUsage_emptyTenantID_returnsInvalidArgument verifies the
// handler-level shape check fires before the repo is touched.
func TestGetTenantUsage_emptyTenantID_returnsInvalidArgument(t *testing.T) {
	h := newHandler(&fakeRepo{})
	_, err := h.GetTenantUsage(context.Background(), &metadatav1.GetTenantUsageRequest{TenantId: ""})
	requireCode(t, err, codes.InvalidArgument)
}

// TestGetTenantUsage_repoError_returnsInternal verifies the repo error path —
// we expect a non-InvalidArgument code (the exact mapping is MapDBError's
// concern; we just check the InvalidArgument did not slip through).
func TestGetTenantUsage_repoError_returnsInternal(t *testing.T) {
	h := newHandler(&fakeRepo{tenantUsageErr: errors.New("db down")})
	_, err := h.GetTenantUsage(context.Background(), &metadatav1.GetTenantUsageRequest{
		TenantId: "00000000-0000-0000-0000-000000000001",
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	st, _ := status.FromError(err)
	if st.Code() == codes.InvalidArgument {
		t.Errorf("repo error must not surface as InvalidArgument: %v", err)
	}
}

// TestGetTenantUsage_lazyMissingTenant_returnsZero verifies the documented
// behaviour for tenants without a metadata row yet — the repo returns an
// all-zero proto and the handler forwards it verbatim.
func TestGetTenantUsage_lazyMissingTenant_returnsZero(t *testing.T) {
	h := newHandler(&fakeRepo{tenantUsageResp: &metadatav1.TenantUsage{}})
	got, err := h.GetTenantUsage(context.Background(), &metadatav1.GetTenantUsageRequest{
		TenantId: "00000000-0000-0000-0000-000000000999",
	})
	requireNoErr(t, err)
	if got.GetStorageUsedBytes() != 0 || got.GetStorageQuotaBytes() != 0 ||
		got.GetRepositoryCount() != 0 || got.GetOrganizationCount() != 0 {
		t.Errorf("expected all zeros for lazy tenant, got %+v", got)
	}
}
