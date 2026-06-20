package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
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

// storageBreakdownOverride lets FE-API-031 tests inject specific row sets.
var storageBreakdownOverride *metadatav1.GetTenantStorageBreakdownResponse

func (s *fakeMetaServer) GetTenantStorageBreakdown(_ context.Context, _ *metadatav1.GetTenantStorageBreakdownRequest) (*metadatav1.GetTenantStorageBreakdownResponse, error) {
	if storageBreakdownOverride != nil {
		return storageBreakdownOverride, nil
	}
	return &metadatav1.GetTenantStorageBreakdownResponse{
		TenantStorageUsedBytes: 1500,
		Repositories: []*metadatav1.RepositoryStorageEntry{
			{RepoId: "r1", Org: "acme", Name: "api", StorageUsedBytes: 1000, PercentOfTenant: 66.66666666666667},
			{RepoId: "r2", Org: "acme", Name: "web", StorageUsedBytes: 500, PercentOfTenant: 33.333333333333336},
		},
	}, nil
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

// deleteTagOverride lets bulk-delete tests inject per-call results — the
// global is keyed by tag name so a single fake instance can return success
// for some tags and a stipulated gRPC error for others.
var (
	deleteTagOverride map[string]error
	deleteTagCalls    []string // ordered record of which tags were attempted
)

func (s *fakeMetaServer) DeleteTag(_ context.Context, req *metadatav1.DeleteTagRequest) (*emptypb.Empty, error) {
	deleteTagCalls = append(deleteTagCalls, req.GetName())
	if err, ok := deleteTagOverride[req.GetName()]; ok && err != nil {
		return nil, err
	}
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

// ListTenantVulnerabilities (FE-API-014) fake — returns vulnsListOverride
// when non-nil, otherwise a small canned set covering the happy path.
// vulnsListCall captures the last request so tests can assert the BFF
// forwarded the query string verbatim.
var (
	vulnsListOverride *metadatav1.ListTenantVulnerabilitiesResponse
	vulnsListCall     *metadatav1.ListTenantVulnerabilitiesRequest
)

func (s *fakeMetaServer) ListTenantVulnerabilities(_ context.Context, req *metadatav1.ListTenantVulnerabilitiesRequest) (*metadatav1.ListTenantVulnerabilitiesResponse, error) {
	vulnsListCall = req
	if vulnsListOverride != nil {
		return vulnsListOverride, nil
	}
	return &metadatav1.ListTenantVulnerabilitiesResponse{
		Vulnerabilities: []*metadatav1.TenantVulnerability{
			{
				CveId: "CVE-2024-9999", Severity: "CRITICAL",
				PackageName: "openssl", PackageVersion: "1.0.0", FixedIn: "1.0.1",
				Affected: []*metadatav1.AffectedTag{
					{Repo: "myorg/myrepo", Tag: "v1", Digest: "sha256:abc"},
				},
				FirstSeen: timestamppb.Now(),
				LastSeen:  timestamppb.Now(),
			},
		},
		NextPageToken: "next-page",
	}, nil
}

// ListTenantRemediations (FE-API-017) fake — returns remListOverride when
// non-nil, otherwise a single canned remediation row. remListCall captures
// the last request so tests can assert query-string wiring.
var (
	remListOverride *metadatav1.ListTenantRemediationsResponse
	remListCall     *metadatav1.ListTenantRemediationsRequest
)

func (s *fakeMetaServer) ListTenantRemediations(_ context.Context, req *metadatav1.ListTenantRemediationsRequest) (*metadatav1.ListTenantRemediationsResponse, error) {
	remListCall = req
	if remListOverride != nil {
		return remListOverride, nil
	}
	return &metadatav1.ListTenantRemediationsResponse{
		Remediations: []*metadatav1.Remediation{
			{
				PackageName:    "openssl",
				FromVersion:    "1.0.0",
				ToVersion:      "1.0.1",
				CvesFixed:      []string{"CVE-2024-1", "CVE-2024-2"},
				CvesFixedCount: 2,
				MaxSeverity:    "CRITICAL",
				Affected: []*metadatav1.RemediationAffected{
					{Repo: "acme/api", Tag: "v1.2.3", Digest: "sha256:abc"},
				},
				AffectedCount: 5,
			},
		},
		NextPageToken: "next-rem",
	}, nil
}

// ListScanHistory (FE-API-015) fake — returns scanHistoryOverride when
// non-nil, otherwise a single canned scan. scanHistoryCall captures the
// last request so tests can assert query-string wiring.
var (
	scanHistoryOverride *metadatav1.ListScanHistoryResponse
	scanHistoryCall     *metadatav1.ListScanHistoryRequest
)

func (s *fakeMetaServer) ListScanHistory(_ context.Context, req *metadatav1.ListScanHistoryRequest) (*metadatav1.ListScanHistoryResponse, error) {
	scanHistoryCall = req
	if scanHistoryOverride != nil {
		return scanHistoryOverride, nil
	}
	now := timestamppb.Now()
	return &metadatav1.ListScanHistoryResponse{
		Scans: []*metadatav1.ScanHistoryEntry{
			{
				ScanId: "scan-1", Repo: "myorg/myrepo", Tag: "v1",
				ManifestDigest: "sha256:abc", Scanner: "trivy",
				StartedAt: now, CompletedAt: now,
				Status:         "completed",
				SeverityCounts: &metadatav1.SecurityCounts{Critical: 1, High: 2},
				Trigger:        "push",
			},
		},
		NextPageToken: "next-scan",
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

// notificationCall captures the parameters passed to one GetNotifications
// invocation against the fake audit server. Tests inspect lastNotificationCall
// to verify the handler forwarded the right CSV / cursor / since to the audit
// service.
type notificationCall struct {
	tenantID   string
	limit      int32
	pageToken  string
	eventTypes []string
	sinceUnix  int64
}

// lastNotificationCall is set on every GetNotifications invocation.
var lastNotificationCall *notificationCall

// GetNotifications returns a small canned tenant-wide notifications feed
// plus an opaque next page token so the handler wire-mapping tests can
// assert behaviour without the full keyset cursor dance.
func (s *fakeAuditServer) GetNotifications(_ context.Context, req *auditv1.GetNotificationsRequest) (*auditv1.GetNotificationsResponse, error) {
	c := &notificationCall{
		tenantID:   req.GetTenantId(),
		limit:      req.GetLimit(),
		pageToken:  req.GetPageToken(),
		eventTypes: append([]string(nil), req.GetEventTypes()...),
	}
	if ts := req.GetSince(); ts != nil {
		c.sinceUnix = ts.AsTime().Unix()
	}
	lastNotificationCall = c

	// Page-2 short-circuit: empty page so tests see the cursor was forwarded.
	if req.GetPageToken() != "" {
		return &auditv1.GetNotificationsResponse{}, nil
	}
	return &auditv1.GetNotificationsResponse{
		Notifications: []*auditv1.NotificationEvent{
			{
				EventId:    "notif-1",
				EventType:  "push.image",
				ActorId:    testUserID,
				Title:      "Push completed",
				Summary:    "acme/registry:3.20 pushed",
				Link:       "/repositories/acme/registry/tags/3.20",
				OccurredAt: timestamppb.Now(),
				Metadata: map[string]string{
					"repo": "acme/registry",
					"tag":  "3.20",
				},
			},
		},
		NextPageToken: "next-cursor",
		UnreadCount:   1,
	}, nil
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

// ---------------------------------------------------------------------------
// GET /api/v1/notifications   (FE-API-008)
// ---------------------------------------------------------------------------

// resetNotificationCall clears lastNotificationCall before/after tests that
// inspect it so cases don't observe a stale value from a previous run.
func resetNotificationCall(t *testing.T) {
	t.Helper()
	lastNotificationCall = nil
	t.Cleanup(func() { lastNotificationCall = nil })
}

func TestNotifications_missingToken_returns401(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestNotifications_adminToken_returnsRenderedFeed(t *testing.T) {
	resetNotificationCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.NotificationsResponse
	decodeJSON(t, resp, &body)
	if len(body.Notifications) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(body.Notifications))
	}
	n := body.Notifications[0]
	if n.Title != "Push completed" {
		t.Errorf("Title: got %q, want %q", n.Title, "Push completed")
	}
	if n.Summary != "acme/registry:3.20 pushed" {
		t.Errorf("Summary: got %q, want %q", n.Summary, "acme/registry:3.20 pushed")
	}
	if n.Link != "/repositories/acme/registry/tags/3.20" {
		t.Errorf("Link: got %q, want %q", n.Link, "/repositories/acme/registry/tags/3.20")
	}
	if body.NextPageToken != "next-cursor" {
		t.Errorf("NextPageToken: got %q, want next-cursor", body.NextPageToken)
	}
	if body.UnreadCount != 1 {
		t.Errorf("UnreadCount: got %d, want 1", body.UnreadCount)
	}
}

func TestNotifications_unknownEventType_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications?event_types=push.image,shenanigans", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestNotifications_eventTypesFilter_forwarded(t *testing.T) {
	resetNotificationCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications?event_types=push.failed,webhook.delivery_failed", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if lastNotificationCall == nil {
		t.Fatal("expected GetNotifications to be called")
	}
	want := []string{"push.failed", "webhook.delivery_failed"}
	got := lastNotificationCall.eventTypes
	if len(got) != len(want) {
		t.Fatalf("eventTypes len: got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("eventTypes[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNotifications_pageTokenForwarded_returnsEmptyPage(t *testing.T) {
	resetNotificationCall(t)
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications?page_token=opaqueCursor123", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if lastNotificationCall == nil || lastNotificationCall.pageToken != "opaqueCursor123" {
		t.Errorf("expected page_token forwarded; lastCall=%+v", lastNotificationCall)
	}
	var body handler.NotificationsResponse
	decodeJSON(t, resp, &body)
	if len(body.Notifications) != 0 {
		t.Errorf("expected empty notifications on page 2, got %d", len(body.Notifications))
	}
	if body.NextPageToken != "" {
		t.Errorf("expected empty next_page_token on terminal page, got %q", body.NextPageToken)
	}
}

func TestNotifications_limitOutOfRange_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications?limit=99999", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestNotifications_invalidLimitFormat_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications?limit=abc", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestNotifications_futureSince_returns400(t *testing.T) {
	env := newTestEnv(t)
	future := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	resp := env.get(t, "/api/v1/notifications?since="+future, adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestNotifications_invalidSince_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications?since=not-a-time", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestNotifications_invalidUnreadOnly_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/notifications?unread_only=maybe", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestNotifications_sinceForwardedToAudit(t *testing.T) {
	resetNotificationCall(t)
	env := newTestEnv(t)
	since := time.Now().Add(-24 * time.Hour).Truncate(time.Second).UTC()
	resp := env.get(t, "/api/v1/notifications?since="+since.Format(time.RFC3339), adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if lastNotificationCall == nil {
		t.Fatal("expected GetNotifications to be called")
	}
	if lastNotificationCall.sinceUnix != since.Unix() {
		t.Errorf("since: got unix=%d, want unix=%d", lastNotificationCall.sinceUnix, since.Unix())
	}
}


// ---------------------------------------------------------------------------
// GET /api/v1/security/vulnerabilities   (FE-API-014)
// ---------------------------------------------------------------------------

// TestListVulnerabilities_adminToken_returnsList exercises the happy path
// against the canned fakeMetaServer response.
func TestListVulnerabilities_adminToken_returnsList(t *testing.T) {
	t.Cleanup(func() { vulnsListCall = nil; vulnsListOverride = nil })
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/vulnerabilities?severity=critical&limit=10", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.VulnerabilityListResponse
	decodeJSON(t, resp, &body)
	if len(body.Vulnerabilities) != 1 || body.Vulnerabilities[0].CVEID != "CVE-2024-9999" {
		t.Errorf("unexpected vulnerabilities: %+v", body.Vulnerabilities)
	}
	if body.NextPageToken != "next-page" {
		t.Errorf("NextPageToken: got %q, want next-page", body.NextPageToken)
	}
	// Severity is uppercased before forwarding to metadata.
	if vulnsListCall == nil || vulnsListCall.GetSeverity() != "CRITICAL" {
		t.Errorf("severity forwarded: got %v", vulnsListCall)
	}
}

// TestListVulnerabilities_invalidSeverity_returns400 verifies a bad
// severity value short-circuits before any gRPC call.
func TestListVulnerabilities_invalidSeverity_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/vulnerabilities?severity=SEVERE", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestListVulnerabilities_invalidPageToken_returns400 verifies an unsafe
// page_token (chars outside the base64url alphabet) is rejected.
func TestListVulnerabilities_invalidPageToken_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/vulnerabilities?page_token=!!!", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestListVulnerabilities_noAuth_returns401 verifies the route is behind
// the standard auth middleware.
func TestListVulnerabilities_noAuth_returns401(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/vulnerabilities", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestListVulnerabilities_limitOver200_clampedTo200 verifies the over-the-
// top limit is capped server-side so a caller can't request millions of
// rows in one shot.
func TestListVulnerabilities_limitOver200_clampedTo200(t *testing.T) {
	t.Cleanup(func() { vulnsListCall = nil })
	env := newTestEnv(t)
	_ = env.get(t, "/api/v1/security/vulnerabilities?limit=10000", adminToken)
	if vulnsListCall == nil || vulnsListCall.GetPageSize() != 200 {
		t.Errorf("page_size not clamped: %v", vulnsListCall)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/security/scans   (FE-API-015)
// ---------------------------------------------------------------------------

// TestListScanHistory_adminToken_returnsList exercises the happy path
// against the canned fakeMetaServer response.
func TestListScanHistory_adminToken_returnsList(t *testing.T) {
	t.Cleanup(func() { scanHistoryCall = nil; scanHistoryOverride = nil })
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/scans?limit=25", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.ScanHistoryListResponse
	decodeJSON(t, resp, &body)
	if len(body.Scans) != 1 || body.Scans[0].ScanID != "scan-1" {
		t.Errorf("unexpected scans: %+v", body.Scans)
	}
	if body.NextPageToken != "next-scan" {
		t.Errorf("NextPageToken: got %q, want next-scan", body.NextPageToken)
	}
	if body.Scans[0].Trigger != "push" {
		t.Errorf("Trigger: got %q, want push", body.Scans[0].Trigger)
	}
	if body.Scans[0].SeverityCounts.High != 2 {
		t.Errorf("severity_counts.high: got %d, want 2", body.Scans[0].SeverityCounts.High)
	}
}

// TestListScanHistory_sinceParsesRFC3339 verifies the BFF forwards a parsed
// timestamp to the metadata service rather than passing the raw string.
func TestListScanHistory_sinceParsesRFC3339(t *testing.T) {
	t.Cleanup(func() { scanHistoryCall = nil })
	env := newTestEnv(t)
	since := time.Now().Add(-72 * time.Hour).UTC().Format(time.RFC3339)
	_ = env.get(t, "/api/v1/security/scans?since="+since, adminToken)
	if scanHistoryCall == nil || scanHistoryCall.GetSince() == nil {
		t.Fatalf("since not forwarded: %+v", scanHistoryCall)
	}
}

// TestListScanHistory_invalidSince_returns400 verifies a malformed since
// value short-circuits before any gRPC call.
func TestListScanHistory_invalidSince_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/scans?since=not-a-time", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestListScanHistory_invalidPageToken_returns400 verifies an unsafe
// page_token (chars outside the base64url alphabet) is rejected.
func TestListScanHistory_invalidPageToken_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/scans?page_token=!!!", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestListScanHistory_noAuth_returns401 verifies the route is behind the
// standard auth middleware.
func TestListScanHistory_noAuth_returns401(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/scans", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/security/remediation   (FE-API-017)
// ---------------------------------------------------------------------------

// TestListRemediations_adminToken_returnsList exercises the happy path
// against the canned fakeMetaServer response and verifies the BFF maps the
// proto fields onto the public JSON shape verbatim.
func TestListRemediations_adminToken_returnsList(t *testing.T) {
	t.Cleanup(func() { remListCall = nil; remListOverride = nil })
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/remediation?limit=10", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.RemediationListResponse
	decodeJSON(t, resp, &body)
	if len(body.Remediations) != 1 {
		t.Fatalf("unexpected remediations: %+v", body.Remediations)
	}
	r0 := body.Remediations[0]
	if r0.PackageName != "openssl" || r0.FromVersion != "1.0.0" || r0.ToVersion != "1.0.1" {
		t.Errorf("upgrade tuple: got %s %s -> %s", r0.PackageName, r0.FromVersion, r0.ToVersion)
	}
	if r0.CVEsFixedCount != 2 || len(r0.CVEsFixed) != 2 {
		t.Errorf("CVE fields: count=%d slice=%v", r0.CVEsFixedCount, r0.CVEsFixed)
	}
	if r0.AffectedCount != 5 || len(r0.Affected) != 1 {
		t.Errorf("affected: count=%d slice=%d", r0.AffectedCount, len(r0.Affected))
	}
	if body.NextPageToken != "next-rem" {
		t.Errorf("NextPageToken: got %q, want next-rem", body.NextPageToken)
	}
}

// TestListRemediations_pageTokenForwarded ensures a valid base64-safe page
// token is passed through to the metadata service (the BFF must not mangle
// the cursor between calls).
func TestListRemediations_pageTokenForwarded(t *testing.T) {
	t.Cleanup(func() { remListCall = nil })
	env := newTestEnv(t)
	_ = env.get(t, "/api/v1/security/remediation?page_token=abc-_123", adminToken)
	if remListCall == nil || remListCall.GetPageToken() != "abc-_123" {
		t.Errorf("page_token forwarded: %+v", remListCall)
	}
}

// TestListRemediations_invalidPageToken_returns400 verifies an unsafe page
// token (chars outside base64url) is rejected before the gRPC call.
func TestListRemediations_invalidPageToken_returns400(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/remediation?page_token=!!!", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestListRemediations_limitOver200_clampedTo200 verifies the over-the-top
// limit is capped server-side so callers can't request millions of rows.
func TestListRemediations_limitOver200_clampedTo200(t *testing.T) {
	t.Cleanup(func() { remListCall = nil })
	env := newTestEnv(t)
	_ = env.get(t, "/api/v1/security/remediation?limit=10000", adminToken)
	if remListCall == nil || remListCall.GetPageSize() != 200 {
		t.Errorf("page_size not clamped: %v", remListCall)
	}
}

// TestListRemediations_noAuth_returns401 verifies the route is behind the
// standard auth middleware.
func TestListRemediations_noAuth_returns401(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/remediation", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// GET /api/v1/stats/storage   (FE-API-031)
// ---------------------------------------------------------------------------

func TestStorageBreakdown_adminToken_returns200(t *testing.T) {
	t.Cleanup(func() { storageBreakdownOverride = nil })
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats/storage", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.StorageBreakdownResponse
	decodeJSON(t, resp, &body)
	if body.TenantStorageUsedBytes != 1500 {
		t.Errorf("tenant total: got %d, want 1500", body.TenantStorageUsedBytes)
	}
	if len(body.Repositories) != 2 {
		t.Fatalf("repositories len: got %d, want 2", len(body.Repositories))
	}
	if body.Repositories[0].Name != "api" || body.Repositories[0].StorageUsedBytes != 1000 {
		t.Errorf("repo[0]: got %+v", body.Repositories[0])
	}
	if body.Repositories[0].PercentOfTenant < 66.66 || body.Repositories[0].PercentOfTenant > 66.67 {
		t.Errorf("percent[0]: got %v, want ~66.667", body.Repositories[0].PercentOfTenant)
	}
}

func TestStorageBreakdown_zeroRepos_returnsEmptyArray(t *testing.T) {
	t.Cleanup(func() { storageBreakdownOverride = nil })
	storageBreakdownOverride = &metadatav1.GetTenantStorageBreakdownResponse{
		TenantStorageUsedBytes: 0,
		Repositories:           nil,
	}
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats/storage", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.StorageBreakdownResponse
	decodeJSON(t, resp, &body)
	if body.TenantStorageUsedBytes != 0 {
		t.Errorf("tenant total: got %d, want 0", body.TenantStorageUsedBytes)
	}
	if body.Repositories == nil {
		t.Error("repositories must be empty array, not null (stable JSON shape)")
	}
	if len(body.Repositories) != 0 {
		t.Errorf("expected 0 repositories, got %d", len(body.Repositories))
	}
}

func TestStorageBreakdown_noAuth_returns401(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/stats/storage", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// DELETE /api/v1/repositories/{org}/{repo}/tags   (FE-API-036)
// ---------------------------------------------------------------------------

// delBody sends a DELETE with a JSON body and returns the response.
func (e *testEnv) delBody(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE %s: %v", path, err)
	}
	return resp
}

func resetBulkDeleteFakes(t *testing.T) {
	t.Helper()
	deleteTagOverride = nil
	deleteTagCalls = nil
	t.Cleanup(func() {
		deleteTagOverride = nil
		deleteTagCalls = nil
	})
}

func TestBulkDeleteTags_writer_mixedResults_returns200(t *testing.T) {
	resetBulkDeleteFakes(t)
	deleteTagOverride = map[string]error{
		"v1.1": status.Error(codes.NotFound, "tag not found"),
	}
	env := newTestEnv(t)
	resp := env.delBody(t, "/api/v1/repositories/myorg/myrepo/tags", writerToken,
		`{"tag_names":["v1.0","v1.1","v1.2"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Results []struct {
			TagName string `json:"tag_name"`
			Deleted bool   `json:"deleted"`
			Reason  string `json:"reason,omitempty"`
		} `json:"results"`
	}
	decodeJSON(t, resp, &body)
	if len(body.Results) != 3 {
		t.Fatalf("results len: got %d, want 3", len(body.Results))
	}
	if !body.Results[0].Deleted || body.Results[0].TagName != "v1.0" {
		t.Errorf("result[0]: %+v", body.Results[0])
	}
	if body.Results[1].Deleted || body.Results[1].Reason != "tag not found" {
		t.Errorf("result[1]: %+v", body.Results[1])
	}
	if !body.Results[2].Deleted {
		t.Errorf("result[2]: %+v", body.Results[2])
	}
	if len(deleteTagCalls) != 3 {
		t.Errorf("expected 3 DeleteTag calls, got %d", len(deleteTagCalls))
	}
}

func TestBulkDeleteTags_emptyArray_returns400(t *testing.T) {
	resetBulkDeleteFakes(t)
	env := newTestEnv(t)
	resp := env.delBody(t, "/api/v1/repositories/myorg/myrepo/tags", writerToken, `{"tag_names":[]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestBulkDeleteTags_over100_returns400(t *testing.T) {
	resetBulkDeleteFakes(t)
	names := make([]string, 0, 101)
	for i := 0; i < 101; i++ {
		names = append(names, "tag"+strconv.Itoa(i))
	}
	body := `{"tag_names":["` + strings.Join(names, `","`) + `"]}`
	env := newTestEnv(t)
	resp := env.delBody(t, "/api/v1/repositories/myorg/myrepo/tags", writerToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestBulkDeleteTags_duplicatesDeduped(t *testing.T) {
	resetBulkDeleteFakes(t)
	env := newTestEnv(t)
	resp := env.delBody(t, "/api/v1/repositories/myorg/myrepo/tags", writerToken,
		`{"tag_names":["v1.0","v1.0","v1.0"]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if len(deleteTagCalls) != 1 {
		t.Errorf("dedupe: expected 1 call, got %d", len(deleteTagCalls))
	}
}

func TestBulkDeleteTags_invalidTagName_returns400(t *testing.T) {
	resetBulkDeleteFakes(t)
	env := newTestEnv(t)
	resp := env.delBody(t, "/api/v1/repositories/myorg/myrepo/tags", writerToken,
		`{"tag_names":["v1.0","!! bad name"]}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	if len(deleteTagCalls) != 0 {
		t.Errorf("no DeleteTag calls should fire on validation failure, got %d", len(deleteTagCalls))
	}
}

func TestBulkDeleteTags_reader_returns403(t *testing.T) {
	resetBulkDeleteFakes(t)
	env := newTestEnv(t)
	resp := env.delBody(t, "/api/v1/repositories/myorg/myrepo/tags", readerToken,
		`{"tag_names":["v1.0"]}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}
