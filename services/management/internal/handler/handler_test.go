package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	switch req.GetUserId() {
	case testUserID:
		return &authv1.GetUserPermissionsResponse{Roles: []string{"admin"}}, nil
	case "writer-user":
		return &authv1.GetUserPermissionsResponse{Roles: []string{"writer"}}, nil
	default:
		return &authv1.GetUserPermissionsResponse{Roles: []string{"reader"}}, nil
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
