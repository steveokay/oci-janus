package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

const (
	bufSize        = 1 << 20 // 1 MiB
	adminToken     = "admin-token"
	writerToken    = "writer-token"
	readerToken    = "reader-token"
	testTenantID   = "00000000-0000-0000-0000-000000000001"
	testUserID     = "00000000-0000-0000-0000-000000000099"
	testRepoID     = "00000000-0000-0000-0000-000000000010"
	testOrgID      = "00000000-0000-0000-0000-000000000020"
)

// ---------------------------------------------------------------------------
// Fake gRPC servers
// ---------------------------------------------------------------------------

// fakeAuthServer returns ValidateToken based on the token value. It also
// handles GetUserPermissions with a role set keyed on the token value.
type fakeAuthServer struct {
	authv1.UnimplementedAuthServiceServer
}

func (s *fakeAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	switch req.GetToken() {
	case adminToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: testUserID}, nil
	case writerToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: "writer-user"}, nil
	case readerToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: "reader-user"}, nil
	default:
		return &authv1.ValidateTokenResponse{Valid: false}, nil
	}
}

func (s *fakeAuthServer) GetUserPermissions(_ context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	// Existing tests use "myorg" as the only org and "myorg/myrepo" as the only
	// repo. Issue org-scoped grants so scope-aware checks (PENTEST-002) succeed
	// for both org and repo URL paths within that org.
	switch req.GetUserId() {
	case testUserID:
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"admin"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "assign-admin", UserId: testUserID, Role: "admin", ScopeType: "org", ScopeValue: "myorg"},
			},
		}, nil
	case "writer-user":
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"writer"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "assign-writer", UserId: "writer-user", Role: "writer", ScopeType: "org", ScopeValue: "myorg"},
			},
		}, nil
	default:
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"reader"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "assign-reader", UserId: "reader-user", Role: "reader", ScopeType: "org", ScopeValue: "myorg"},
			},
		}, nil
	}
}

func (s *fakeAuthServer) GrantRole(_ context.Context, _ *authv1.GrantRoleRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *fakeAuthServer) RevokeRole(_ context.Context, _ *authv1.RevokeRoleRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *fakeAuthServer) ListMembers(_ context.Context, _ *authv1.ListMembersRequest) (*authv1.ListMembersResponse, error) {
	return &authv1.ListMembersResponse{
		Members: []*authv1.RoleAssignment{
			{Id: "assign-1", UserId: testUserID, Role: "admin", ScopeType: "org", ScopeValue: "myorg"},
		},
	}, nil
}

// fakeMetaServer returns canned metadata responses.
type fakeMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer
}

func (s *fakeMetaServer) GetTenantQuotaUsage(_ context.Context, _ *metadatav1.GetTenantQuotaUsageRequest) (*metadatav1.QuotaUsage, error) {
	return &metadatav1.QuotaUsage{UsedBytes: 1024, QuotaBytes: 10737418240}, nil
}

func (s *fakeMetaServer) GetTenantVulnerabilityCount(_ context.Context, _ *metadatav1.GetTenantVulnerabilityCountRequest) (*metadatav1.VulnerabilityCountResponse, error) {
	return &metadatav1.VulnerabilityCountResponse{Total: 3}, nil
}

func (s *fakeMetaServer) CountRepositories(_ context.Context, _ *metadatav1.CountRepositoriesRequest) (*metadatav1.CountRepositoriesResponse, error) {
	return &metadatav1.CountRepositoriesResponse{Count: 1}, nil
}

func (s *fakeMetaServer) ListRepositories(req *metadatav1.ListRepositoriesRequest, stream metadatav1.MetadataService_ListRepositoriesServer) error {
	_ = stream.Send(&metadatav1.Repository{
		RepoId:       testRepoID,
		OrgId:        testOrgID,
		Name:         "myorg/myrepo",
		IsPublic:     false,
		StorageUsed:  512,
		StorageQuota: 10737418240,
		CreatedAt:    timestamppb.Now(),
	})
	return nil
}

func (s *fakeMetaServer) GetRepositoryByName(_ context.Context, req *metadatav1.GetRepositoryByNameRequest) (*metadatav1.Repository, error) {
	if req.GetName() == "myorg/myrepo" {
		return &metadatav1.Repository{
			RepoId:       testRepoID,
			OrgId:        testOrgID,
			Name:         "myorg/myrepo",
			IsPublic:     false,
			StorageUsed:  512,
			StorageQuota: 10737418240,
			CreatedAt:    timestamppb.Now(),
		}, nil
	}
	return nil, grpc.ErrServerStopped // any error → 404
}

func (s *fakeMetaServer) CreateRepository(_ context.Context, req *metadatav1.CreateRepositoryRequest) (*metadatav1.Repository, error) {
	return &metadatav1.Repository{
		RepoId:       testRepoID,
		OrgId:        testOrgID,
		Name:         req.GetName(),
		IsPublic:     req.GetIsPublic(),
		StorageUsed:  0,
		StorageQuota: req.GetStorageQuota(),
		CreatedAt:    timestamppb.Now(),
	}, nil
}

func (s *fakeMetaServer) DeleteRepository(_ context.Context, _ *metadatav1.DeleteRepositoryRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *fakeMetaServer) ListTags(req *metadatav1.ListTagsRequest, stream metadatav1.MetadataService_ListTagsServer) error {
	_ = stream.Send(&metadatav1.Tag{
		Name:           "v1.0",
		ManifestDigest: "sha256:abc123",
		UpdatedAt:      timestamppb.Now(),
		CreatedAt:      timestamppb.Now(),
	})
	return nil
}

func (s *fakeMetaServer) GetTag(_ context.Context, req *metadatav1.GetTagRequest) (*metadatav1.Tag, error) {
	return &metadatav1.Tag{
		Name:           req.GetName(),
		ManifestDigest: "sha256:abc123",
		CreatedAt:      timestamppb.Now(),
		UpdatedAt:      timestamppb.Now(),
	}, nil
}

func (s *fakeMetaServer) DeleteTag(_ context.Context, _ *metadatav1.DeleteTagRequest) (*emptypb.Empty, error) {
	return &emptypb.Empty{}, nil
}

func (s *fakeMetaServer) GetScanResult(_ context.Context, _ *metadatav1.GetScanResultRequest) (*metadatav1.ScanResult, error) {
	return &metadatav1.ScanResult{
		ScanId:      "scan-1",
		Status:      "complete",
		ScannerName: "trivy",
	}, nil
}

// securityOverrides lets individual tests swap in a custom SecurityOverview
// payload without redefining the whole fakeMetaServer. The pointer is set via
// the package-level test variable `securityOverviewOverride` below.
func (s *fakeMetaServer) GetSecurityOverview(_ context.Context, _ *metadatav1.GetSecurityOverviewRequest) (*metadatav1.SecurityOverview, error) {
	if securityOverviewOverride != nil {
		return securityOverviewOverride, nil
	}
	// Default: mixed-severity populated payload covering the FE-API-020 happy
	// path. Mirrors the dashboard's "scanned, partial coverage" state.
	return &metadatav1.SecurityOverview{
		OpenVulnerabilitiesTotal: 12,
		SeverityCounts: &metadatav1.SecurityCounts{
			Critical: 2, High: 3, Medium: 4, Low: 2, Negligible: 1,
		},
		ScanCoverage: &metadatav1.ScanCoverage{
			TagsTotal: 4, TagsScanned: 3, Percent: 75.0,
		},
		RecentScans_24H:   5,
		DaysSinceLastScan: 2,
	}, nil
}

// securityOverviewOverride is consulted by fakeMetaServer.GetSecurityOverview
// when non-nil. Tests reset it via t.Cleanup so cases stay isolated.
var securityOverviewOverride *metadatav1.SecurityOverview

// fakeAuditServer handles build history and daily pull count.
type fakeAuditServer struct {
	auditv1.UnimplementedAuditServiceServer
}

func (s *fakeAuditServer) GetBuildHistory(_ context.Context, _ *auditv1.GetBuildHistoryRequest) (*auditv1.GetBuildHistoryResponse, error) {
	return &auditv1.GetBuildHistoryResponse{
		Builds: []*auditv1.BuildRecord{
			{BuildId: "build-1", Status: "success", TriggeredBy: "ci-bot"},
		},
		Total: 1,
	}, nil
}

func (s *fakeAuditServer) GetDailyPullCount(_ context.Context, _ *auditv1.GetDailyPullCountRequest) (*auditv1.GetDailyPullCountResponse, error) {
	return &auditv1.GetDailyPullCountResponse{Count: 7}, nil
}

// repoActivityCall captures the parameters passed to one GetRepoActivity invocation
// against the fake audit server. Test cases read activityCalls to assert the
// handler forwarded the expected fields (event_types, page_token, etc.).
type repoActivityCall struct {
	tenantID       string
	repositoryName string
	limit          int32
	pageToken      string
	eventTypes     []string
	sinceUnix      int64
}

// lastActivityCall is set on every GetRepoActivity invocation. Tests inspect it
// to verify the handler forwarded the right CSV / cursor / etc. to the audit
// service. Reset by t.Cleanup in tests that read it so cases stay isolated.
var lastActivityCall *repoActivityCall

// GetRepoActivity returns a small canned activity feed plus an opaque next
// page token so the management handler tests can assert wire mapping without
// the full keyset cursor dance.
func (s *fakeAuditServer) GetRepoActivity(_ context.Context, req *auditv1.GetRepoActivityRequest) (*auditv1.GetRepoActivityResponse, error) {
	c := &repoActivityCall{
		tenantID:       req.GetTenantId(),
		repositoryName: req.GetRepositoryName(),
		limit:          req.GetLimit(),
		pageToken:      req.GetPageToken(),
		eventTypes:     append([]string(nil), req.GetEventTypes()...),
	}
	if ts := req.GetSince(); ts != nil {
		c.sinceUnix = ts.AsTime().Unix()
	}
	lastActivityCall = c

	// If the caller passed page_token, return an empty page so the test sees
	// the cursor was forwarded.
	if req.GetPageToken() != "" {
		return &auditv1.GetRepoActivityResponse{Events: nil, NextPageToken: ""}, nil
	}
	return &auditv1.GetRepoActivityResponse{
		Events: []*auditv1.RepoActivityEvent{
			{
				EventId:       "ev-1",
				EventType:     "push.image",
				ActorId:       testUserID,
				ActorUsername: "alice",
				Tag:           "v1.0",
				Digest:        "sha256:abc",
				Outcome:       "success",
				Summary:       "Pushed myorg/myrepo:v1.0",
				OccurredAt:    timestamppb.Now(),
			},
		},
		NextPageToken: "next-cursor",
	}, nil
}

// fakeHealthServer always returns SERVING.
type fakeHealthServer struct {
	healthpb.UnimplementedHealthServer
}

func (s *fakeHealthServer) Check(_ context.Context, _ *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	return &healthpb.HealthCheckResponse{Status: healthpb.HealthCheckResponse_SERVING}, nil
}

// ---------------------------------------------------------------------------
// Test harness
// ---------------------------------------------------------------------------

// testEnv holds all fakes and the HTTP server for a test.
type testEnv struct {
	srv *httptest.Server
}

// newTestEnv spins up in-process gRPC fakes via bufconn and wires them into
// a management Handler. Returns a testEnv with a running httptest.Server.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()

	// Auth bufconn
	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &fakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	// Metadata bufconn
	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &fakeMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	// Audit bufconn
	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	// dialBufconn returns a grpc.ClientConn for a bufconn listener.
	dialBufconn := func(lis *bufconn.Listener) *grpc.ClientConn {
		conn, err := grpc.NewClient("passthrough:///bufnet",
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return lis.DialContext(ctx)
			}),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
		)
		if err != nil {
			t.Fatalf("dial bufconn: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}

	authConn := dialBufconn(authLis)
	metaConn := dialBufconn(metaLis)
	auditConn := dialBufconn(auditLis)

	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil, // publisher — trigger-scan happy path not tested here
		"",  // platformAdminTenantID — set to disable the cross-tenant quota route in tests
		healthpb.NewHealthClient(authConn),
		healthpb.NewHealthClient(metaConn),
		healthpb.NewHealthClient(auditConn),
	)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}
}

// get sends a GET with the given Bearer token and returns the response.
func (e *testEnv) get(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// post sends a POST with a JSON body and returns the response.
func (e *testEnv) post(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// del sends a DELETE request.
func (e *testEnv) del(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, e.srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

// ---------------------------------------------------------------------------
// /healthz
// ---------------------------------------------------------------------------

func TestHealthz_noAuth_returns200(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/healthz", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Auth middleware
// ---------------------------------------------------------------------------

func TestRequireAuth_missingToken_returns401(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestRequireAuth_invalidToken_returns401(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats", "bad-token")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/stats
// ---------------------------------------------------------------------------

func TestStats_adminToken_returnsStatsJSON(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.StatsResponse
	decodeJSON(t, resp, &body)

	if body.TotalRepos != 1 {
		t.Errorf("expected TotalRepos=1, got %d", body.TotalRepos)
	}
	if body.DailyPulls != 7 {
		t.Errorf("expected DailyPulls=7, got %d", body.DailyPulls)
	}
	if body.VulnerabilityCount != 3 {
		t.Errorf("expected VulnerabilityCount=3, got %d", body.VulnerabilityCount)
	}
	if body.SystemHealthPct != 100.0 {
		t.Errorf("expected SystemHealthPct=100, got %f", body.SystemHealthPct)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories
// ---------------------------------------------------------------------------

func TestListRepositories_adminToken_returnsList(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)

	repos, ok := body["repositories"].([]any)
	if !ok || len(repos) != 1 {
		t.Errorf("expected 1 repo, got %v", body["repositories"])
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/repositories
// ---------------------------------------------------------------------------

func TestCreateRepository_adminToken_returns201(t *testing.T) {
	env := newTestEnv(t)
	body := `{"org":"myorg","name":"newrepo","is_public":false}`
	resp := env.post(t, "/api/v1/repositories", adminToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("expected 201, got %d", resp.StatusCode)
	}
}

func TestCreateRepository_readerToken_returns403(t *testing.T) {
	env := newTestEnv(t)
	body := `{"org":"myorg","name":"newrepo"}`
	resp := env.post(t, "/api/v1/repositories", readerToken, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestCreateRepository_invalidOrgName_returns400(t *testing.T) {
	env := newTestEnv(t)
	body := `{"org":"INVALID_ORG","name":"repo"}`
	resp := env.post(t, "/api/v1/repositories", adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestCreateRepository_invalidRepoName_returns400(t *testing.T) {
	env := newTestEnv(t)
	body := `{"org":"myorg","name":"INVALID REPO"}`
	resp := env.post(t, "/api/v1/repositories", adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestCreateRepository_malformedJSON_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.post(t, "/api/v1/repositories", adminToken, `{not json}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}
// ---------------------------------------------------------------------------

func TestGetRepository_knownRepo_returns200(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.RepoResponse
	decodeJSON(t, resp, &body)
	if body.Name != "myorg/myrepo" {
		t.Errorf("expected name 'myorg/myrepo', got %q", body.Name)
	}
}

func TestGetRepository_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/unknown", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetRepository_invalidOrgName_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/BAD_ORG/repo", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/repositories/{org}/{repo}
// ---------------------------------------------------------------------------

func TestDeleteRepository_adminToken_returns204(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/repositories/myorg/myrepo", adminToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestDeleteRepository_writerToken_returns403(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/repositories/myorg/myrepo", writerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestDeleteRepository_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/repositories/myorg/missing", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/tags
// ---------------------------------------------------------------------------

func TestListTags_knownRepo_returnsTags(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)
	tags, _ := body["tags"].([]any)
	if len(tags) != 1 {
		t.Errorf("expected 1 tag, got %d", len(tags))
	}
}

func TestListTags_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/unknown/tags", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/repositories/{org}/{repo}/tags/{tag}
// ---------------------------------------------------------------------------

func TestDeleteTag_writerToken_returns204(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0", writerToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestDeleteTag_readerToken_returns403(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestDeleteTag_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/repositories/myorg/badrepo/tags/v1.0", writerToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/tags/{tag}/scan
// ---------------------------------------------------------------------------

func TestGetScan_knownRepoAndTag_returnsScan(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/scan", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.ScanResponse
	decodeJSON(t, resp, &body)
	if body.Status != "complete" {
		t.Errorf("expected status='complete', got %q", body.Status)
	}
}

func TestGetScan_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/missing/tags/v1.0/scan", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// POST /api/v1/repositories/{org}/{repo}/tags/{tag}/scan (validation paths only)
// ---------------------------------------------------------------------------

func TestTriggerScan_invalidTagName_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/bad tag!/scan", adminToken, "")
	// URL-encoded path causes a mismatch; the tag value captured by PathValue
	// will be "bad tag!" which fails validateTagName.
	if resp.StatusCode != http.StatusBadRequest && resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 400 or 404, got %d", resp.StatusCode)
	}
}

func TestTriggerScan_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.post(t, "/api/v1/repositories/myorg/norepo/tags/v1.0/scan", adminToken, "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/tags/{tag}/builds
// ---------------------------------------------------------------------------

func TestListBuilds_knownRepo_returnsBuilds(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/builds", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)
	builds, _ := body["builds"].([]any)
	if len(builds) != 1 {
		t.Errorf("expected 1 build, got %d", len(builds))
	}
}

func TestListBuilds_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/nosuchthing/tags/v1.0/builds", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// RBAC — GET /api/v1/orgs/{org}/members
// ---------------------------------------------------------------------------

func TestListOrgMembers_adminToken_returnsMembers(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/orgs/myorg/members", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var body map[string]any
	decodeJSON(t, resp, &body)
	members, _ := body["members"].([]any)
	if len(members) != 1 {
		t.Errorf("expected 1 member, got %d", len(members))
	}
}

// ---------------------------------------------------------------------------
// RBAC — POST /api/v1/orgs/{org}/members
// ---------------------------------------------------------------------------

func TestGrantOrgMember_adminToken_returns204(t *testing.T) {
	env := newTestEnv(t)
	body := `{"user_id":"some-user","role":"writer"}`
	resp := env.post(t, "/api/v1/orgs/myorg/members", adminToken, body)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestGrantOrgMember_writerToken_returns403(t *testing.T) {
	env := newTestEnv(t)
	body := `{"user_id":"some-user","role":"reader"}`
	resp := env.post(t, "/api/v1/orgs/myorg/members", writerToken, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestGrantOrgMember_missingFields_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.post(t, "/api/v1/orgs/myorg/members", adminToken, `{"role":"writer"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// RBAC — DELETE /api/v1/orgs/{org}/members/{assignmentID}
// ---------------------------------------------------------------------------

func TestRevokeOrgMember_adminToken_returns204(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/orgs/myorg/members/assign-1", adminToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestRevokeOrgMember_readerToken_returns403(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/orgs/myorg/members/assign-1", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// RBAC — GET /api/v1/repositories/{org}/{repo}/members
// ---------------------------------------------------------------------------

func TestListRepoMembers_adminToken_returnsMembers(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/members", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// RBAC — POST /api/v1/repositories/{org}/{repo}/members
// ---------------------------------------------------------------------------

func TestGrantRepoMember_adminToken_returns204(t *testing.T) {
	env := newTestEnv(t)
	body := `{"user_id":"some-user","role":"writer"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/members", adminToken, body)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

func TestGrantRepoMember_writerToken_returns403(t *testing.T) {
	env := newTestEnv(t)
	body := `{"user_id":"some-user","role":"reader"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/members", writerToken, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// RBAC — DELETE /api/v1/repositories/{org}/{repo}/members/{assignmentID}
// ---------------------------------------------------------------------------

func TestRevokeRepoMember_adminToken_returns204(t *testing.T) {
	env := newTestEnv(t)
	resp := env.del(t, "/api/v1/repositories/myorg/myrepo/members/assign-1", adminToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/stats — FE-API-016 nested severity_counts
// ---------------------------------------------------------------------------

// TestStats_severityCountsNested_returnsNestedObject verifies that the FE-API-016
// dashboard mini-bar receives a fully populated nested object even when the
// upstream proto carries the same data as flat *_count fields.
func TestStats_severityCountsNested_returnsNestedObject(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.StatsResponse
	decodeJSON(t, resp, &body)
	// The fakeMetaServer returns Total=3 with all *_count fields zero. The
	// nested object should therefore also be all-zero — but exist.
	if body.SeverityCounts.Critical != 0 ||
		body.SeverityCounts.High != 0 ||
		body.SeverityCounts.Medium != 0 ||
		body.SeverityCounts.Low != 0 ||
		body.SeverityCounts.Negligible != 0 {
		t.Errorf("severity_counts: expected all zeros, got %+v", body.SeverityCounts)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/security/overview — FE-API-020
// ---------------------------------------------------------------------------

// TestSecurityOverview_unauthenticated_returns401 ensures the route is gated
// by RequireAuth even though it surfaces non-sensitive aggregate counts.
func TestSecurityOverview_unauthenticated_returns401(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/overview", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestSecurityOverview_populated_returnsAggregatedJSON exercises the happy
// path: mixed-severity findings, partial scan coverage, recent activity.
func TestSecurityOverview_populated_returnsAggregatedJSON(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/overview", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.SecurityOverviewResponse
	decodeJSON(t, resp, &body)
	if body.OpenVulnerabilitiesTotal != 12 {
		t.Errorf("OpenVulnerabilitiesTotal: got %d, want 12", body.OpenVulnerabilitiesTotal)
	}
	if body.SeverityCounts.Critical != 2 || body.SeverityCounts.High != 3 {
		t.Errorf("severity: got %+v, want C=2 H=3", body.SeverityCounts)
	}
	if body.ScanCoverage.TagsTotal != 4 || body.ScanCoverage.TagsScanned != 3 {
		t.Errorf("coverage: got %+v, want 3/4", body.ScanCoverage)
	}
	if body.ScanCoverage.Percent != 75.0 {
		t.Errorf("percent: got %f, want 75.0", body.ScanCoverage.Percent)
	}
	if body.RecentScans24h != 5 {
		t.Errorf("RecentScans24h: got %d, want 5", body.RecentScans24h)
	}
}

// TestSecurityOverview_emptyTenant_returnsZeros covers a fresh tenant where
// no tags have been pushed and no scans have run. The route must still
// succeed and emit a fully zero-valued payload (no nulls).
func TestSecurityOverview_emptyTenant_returnsZeros(t *testing.T) {
	securityOverviewOverride = &metadatav1.SecurityOverview{
		SeverityCounts: &metadatav1.SecurityCounts{},
		ScanCoverage:   &metadatav1.ScanCoverage{},
	}
	t.Cleanup(func() { securityOverviewOverride = nil })

	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/overview", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body handler.SecurityOverviewResponse
	decodeJSON(t, resp, &body)
	if body.OpenVulnerabilitiesTotal != 0 ||
		body.ScanCoverage.TagsTotal != 0 ||
		body.ScanCoverage.TagsScanned != 0 ||
		body.RecentScans24h != 0 {
		t.Errorf("expected all-zero overview, got %+v", body)
	}
}

// TestSecurityOverview_partialCoverage_returnsCorrectPercent verifies the
// scan_coverage.percent field is whatever the metadata RPC reports. Computing
// it again in management would risk drift; the SQL CTE owns the calculation.
func TestSecurityOverview_partialCoverage_returnsCorrectPercent(t *testing.T) {
	securityOverviewOverride = &metadatav1.SecurityOverview{
		OpenVulnerabilitiesTotal: 1,
		SeverityCounts:           &metadatav1.SecurityCounts{Critical: 1},
		ScanCoverage: &metadatav1.ScanCoverage{
			TagsTotal: 8, TagsScanned: 2, Percent: 25.0,
		},
		RecentScans_24H:   1,
		DaysSinceLastScan: 0,
	}
	t.Cleanup(func() { securityOverviewOverride = nil })

	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/overview", adminToken)
	var body handler.SecurityOverviewResponse
	decodeJSON(t, resp, &body)

	if body.ScanCoverage.Percent != 25.0 {
		t.Errorf("percent: got %f, want 25.0", body.ScanCoverage.Percent)
	}
	if body.SeverityCounts.Critical != 1 {
		t.Errorf("Critical: got %d, want 1", body.SeverityCounts.Critical)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/repositories/{org}/{repo}/activity   (FE-API-004)
// ---------------------------------------------------------------------------

// resetActivityCall clears lastActivityCall before/after a test that reads it
// so cases don't observe a stale value from a previous run.
func resetActivityCall(t *testing.T) {
	t.Helper()
	lastActivityCall = nil
	t.Cleanup(func() { lastActivityCall = nil })
}

func TestRepoActivity_adminToken_returnsEvents(t *testing.T) {
	resetActivityCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/activity", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.ActivityResponse
	decodeJSON(t, resp, &body)
	if len(body.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(body.Events))
	}
	ev := body.Events[0]
	if ev.EventType != "push.image" {
		t.Errorf("EventType: got %q, want push.image", ev.EventType)
	}
	if ev.Tag != "v1.0" {
		t.Errorf("Tag: got %q, want v1.0", ev.Tag)
	}
	if ev.Digest != "sha256:abc" {
		t.Errorf("Digest: got %q, want sha256:abc", ev.Digest)
	}
	if ev.ActorUsername != "alice" {
		t.Errorf("ActorUsername: got %q, want alice", ev.ActorUsername)
	}
	if ev.Summary == "" {
		t.Errorf("Summary: expected non-empty")
	}
	if body.NextPageToken != "next-cursor" {
		t.Errorf("NextPageToken: got %q, want next-cursor", body.NextPageToken)
	}
	// payload_summary should hold the same curated fields. It should never
	// have keys for actor_ip / raw / etc.
	if _, ok := ev.PayloadSummary["tag"]; !ok {
		t.Errorf("PayloadSummary missing tag")
	}
	for badKey := range map[string]struct{}{"actor_ip": {}, "raw": {}, "metadata": {}} {
		if _, leaked := ev.PayloadSummary[badKey]; leaked {
			t.Errorf("PayloadSummary leaked forbidden key %q", badKey)
		}
	}
}

func TestRepoActivity_unknownRepo_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/unknown/activity", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRepoActivity_nonMember_returns404(t *testing.T) {
	// "wrong-org" sits outside the role assignments seeded by fakeAuthServer,
	// so even the admin token has no grant. The handler must respond 404 (not
	// 403) so non-members can't enumerate other orgs.
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/wrong-org/myrepo/activity", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestRepoActivity_invalidOrgName_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/INVALID/myrepo/activity", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRepoActivity_unknownEventType_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/activity?event_types=push.image,shenanigans", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRepoActivity_eventTypesFilter_forwarded(t *testing.T) {
	resetActivityCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/activity?event_types=push.image,scan.completed", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if lastActivityCall == nil {
		t.Fatal("expected GetRepoActivity to be called")
	}
	want := []string{"push.image", "scan.completed"}
	if len(lastActivityCall.eventTypes) != len(want) {
		t.Fatalf("eventTypes len: got %v, want %v", lastActivityCall.eventTypes, want)
	}
	for i, et := range want {
		if lastActivityCall.eventTypes[i] != et {
			t.Errorf("eventTypes[%d]: got %q, want %q", i, lastActivityCall.eventTypes[i], et)
		}
	}
}

func TestRepoActivity_pageTokenForwarded_returnsEmptyPage(t *testing.T) {
	resetActivityCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/activity?page_token=opaqueCursor123", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if lastActivityCall == nil || lastActivityCall.pageToken != "opaqueCursor123" {
		t.Errorf("expected page_token forwarded; lastCall=%+v", lastActivityCall)
	}
	var body handler.ActivityResponse
	decodeJSON(t, resp, &body)
	if len(body.Events) != 0 {
		t.Errorf("expected empty events on page 2, got %d", len(body.Events))
	}
	if body.NextPageToken != "" {
		t.Errorf("expected empty next_page_token on terminal page, got %q", body.NextPageToken)
	}
}

func TestRepoActivity_limitOutOfRange_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/activity?limit=99999", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRepoActivity_futureSince_returns400(t *testing.T) {
	env := newTestEnv(t)
	future := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/activity?since="+future, adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestRepoActivity_invalidSince_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/activity?since=not-a-time", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}
