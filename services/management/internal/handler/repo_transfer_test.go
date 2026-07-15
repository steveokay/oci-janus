// repo_transfer_test.go — BFF tests for POST /api/v1/repositories/{org}/{repo}/transfer.
//
// Transfer has a stricter gate than rename: admin on BOTH the source repo and
// the destination org. The auth server here overrides GetUserPermissions so a
// test can grant/withhold the destination-org admin grant independently of the
// source grant, and scripts RewriteRepoRoleScopes so the partial-success path
// is reachable. The metadata server scripts TransferRepository (NotFound /
// AlreadyExists) plus the GetRepositoryByName that findRepo needs.
package handler_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// transferMetaServer scripts the two metadata RPCs the transfer handler uses.
type transferMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer

	transferFunc  func(ctx context.Context, req *metadatav1.TransferRepositoryRequest) (*metadatav1.Repository, error)
	transferCalls []*metadatav1.TransferRepositoryRequest
	mu            sync.Mutex
}

func (s *transferMetaServer) GetRepositoryByName(_ context.Context, req *metadatav1.GetRepositoryByNameRequest) (*metadatav1.Repository, error) {
	return &metadatav1.Repository{
		RepoId: "repo-123",
		Org:    strings.SplitN(req.GetName(), "/", 2)[0],
		Name:   req.GetName(),
	}, nil
}

func (s *transferMetaServer) TransferRepository(ctx context.Context, req *metadatav1.TransferRepositoryRequest) (*metadatav1.Repository, error) {
	s.mu.Lock()
	s.transferCalls = append(s.transferCalls, req)
	s.mu.Unlock()
	if s.transferFunc != nil {
		return s.transferFunc(ctx, req)
	}
	return &metadatav1.Repository{
		RepoId: req.GetRepoId(),
		Org:    req.GetDestOrg(),
		Name:   req.GetDestOrg() + "/myrepo",
	}, nil
}

// transferAuthServer embeds the shared fakeAuthServer for ValidateToken and
// overrides GetUserPermissions so the destination-org admin grant can be
// toggled per test. RewriteRepoRoleScopes is scriptable.
type transferAuthServer struct {
	fakeAuthServer

	// grantDestAdmin controls whether the admin caller (testUserID) also holds
	// an admin grant on "destorg". Source-org admin ("myorg") is always granted.
	grantDestAdmin bool

	rewriteFunc  func(ctx context.Context, req *authv1.RewriteRepoRoleScopesRequest) (*authv1.RewriteRepoRoleScopesResponse, error)
	rewriteCalls []*authv1.RewriteRepoRoleScopesRequest
	mu           sync.Mutex
}

func (s *transferAuthServer) GetUserPermissions(_ context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	if req.GetUserId() == testUserID {
		assigns := []*authv1.RoleAssignment{
			{Id: "src-admin", UserId: testUserID, Role: "admin", ScopeType: "org", ScopeValue: "myorg"},
		}
		if s.grantDestAdmin {
			assigns = append(assigns, &authv1.RoleAssignment{
				Id: "dest-admin", UserId: testUserID, Role: "admin", ScopeType: "org", ScopeValue: "destorg",
			})
		}
		return &authv1.GetUserPermissionsResponse{Roles: []string{"admin"}, RoleAssignments: assigns}, nil
	}
	// Everyone else is a reader on myorg only.
	return &authv1.GetUserPermissionsResponse{
		Roles: []string{"reader"},
		RoleAssignments: []*authv1.RoleAssignment{
			{Id: "rd", UserId: "reader-user", Role: "reader", ScopeType: "org", ScopeValue: "myorg"},
		},
	}, nil
}

func (s *transferAuthServer) RewriteRepoRoleScopes(ctx context.Context, req *authv1.RewriteRepoRoleScopesRequest) (*authv1.RewriteRepoRoleScopesResponse, error) {
	s.mu.Lock()
	s.rewriteCalls = append(s.rewriteCalls, req)
	s.mu.Unlock()
	if s.rewriteFunc != nil {
		return s.rewriteFunc(ctx, req)
	}
	return &authv1.RewriteRepoRoleScopesResponse{Rewritten: 4}, nil
}

type transferTestEnv struct {
	srv  *httptest.Server
	meta *transferMetaServer
	auth *transferAuthServer
}

// newTransferTestEnv wires the fakes. destAdmin seeds whether the admin caller
// also holds admin on the destination org.
func newTransferTestEnv(t *testing.T, destAdmin bool) *transferTestEnv {
	t.Helper()

	auth := &transferAuthServer{grantDestAdmin: destAdmin}
	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, auth)
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	meta := &transferMetaServer{}
	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, meta)
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	dial := func(lis *bufconn.Listener) *grpc.ClientConn {
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

	h := handler.New(
		authv1.NewAuthServiceClient(dial(authLis)),
		metadatav1.NewMetadataServiceClient(dial(metaLis)),
		auditv1.NewAuditServiceClient(dial(auditLis)),
		nil,
		"",
	)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &transferTestEnv{srv: srv, meta: meta, auth: auth}
}

func (e *transferTestEnv) post(t *testing.T, path, token, body string) *http.Response {
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

// TestTransferRepository_HappyPath: admin on both orgs → 200, TransferRepository
// ran with the dest org, and the scope rewrite migrated "myorg/myrepo" →
// "destorg/myrepo".
func TestTransferRepository_HappyPath(t *testing.T) {
	env := newTransferTestEnv(t, true)

	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/transfer", adminToken, `{"dest_org":"destorg"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var got struct {
		Org            string `json:"org"`
		RolesRewritten int64  `json:"roles_rewritten"`
		RBACWarning    string `json:"rbac_warning"`
	}
	decodeJSON(t, resp, &got)
	if got.Org != "destorg" {
		t.Errorf("org = %q, want destorg", got.Org)
	}
	if got.RolesRewritten != 4 {
		t.Errorf("roles_rewritten = %d, want 4", got.RolesRewritten)
	}
	if got.RBACWarning != "" {
		t.Errorf("unexpected rbac_warning: %q", got.RBACWarning)
	}

	env.meta.mu.Lock()
	if len(env.meta.transferCalls) != 1 {
		t.Fatalf("want 1 TransferRepository call, got %d", len(env.meta.transferCalls))
	}
	tc := env.meta.transferCalls[0]
	env.meta.mu.Unlock()
	if tc.GetRepoId() != "repo-123" || tc.GetDestOrg() != "destorg" {
		t.Errorf("transfer call = %+v, want repo_id=repo-123 dest_org=destorg", tc)
	}

	env.auth.mu.Lock()
	sc := env.auth.rewriteCalls[0]
	env.auth.mu.Unlock()
	if sc.GetOldScope() != "myorg/myrepo" || sc.GetNewScope() != "destorg/myrepo" {
		t.Errorf("scope rewrite = %q→%q, want myorg/myrepo→destorg/myrepo", sc.GetOldScope(), sc.GetNewScope())
	}
}

// TestTransferRepository_ForbiddenSource: a reader on the source repo → 403.
func TestTransferRepository_ForbiddenSource(t *testing.T) {
	env := newTransferTestEnv(t, true)
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/transfer", readerToken, `{"dest_org":"destorg"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
}

// TestTransferRepository_ForbiddenDest: admin on the source but NOT the
// destination org → 403 and the transfer never runs.
func TestTransferRepository_ForbiddenDest(t *testing.T) {
	env := newTransferTestEnv(t, false) // no dest-org admin grant
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/transfer", adminToken, `{"dest_org":"destorg"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
	env.meta.mu.Lock()
	defer env.meta.mu.Unlock()
	if len(env.meta.transferCalls) != 0 {
		t.Errorf("transfer should not run without dest-org admin; got %d calls", len(env.meta.transferCalls))
	}
}

// TestTransferRepository_SameOrg: dest == source org → 400.
func TestTransferRepository_SameOrg(t *testing.T) {
	env := newTransferTestEnv(t, true)
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/transfer", adminToken, `{"dest_org":"myorg"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestTransferRepository_InvalidDestOrg: a malformed dest org → 400.
func TestTransferRepository_InvalidDestOrg(t *testing.T) {
	env := newTransferTestEnv(t, true)
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/transfer", adminToken, `{"dest_org":"Bad Org"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestTransferRepository_DestOrgMissing: metadata NotFound → 404.
func TestTransferRepository_DestOrgMissing(t *testing.T) {
	env := newTransferTestEnv(t, true)
	env.meta.transferFunc = func(_ context.Context, _ *metadatav1.TransferRepositoryRequest) (*metadatav1.Repository, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/transfer", adminToken, `{"dest_org":"destorg"}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// TestTransferRepository_Collision: metadata AlreadyExists → 409, no rewrite.
func TestTransferRepository_Collision(t *testing.T) {
	env := newTransferTestEnv(t, true)
	env.meta.transferFunc = func(_ context.Context, _ *metadatav1.TransferRepositoryRequest) (*metadatav1.Repository, error) {
		return nil, status.Error(codes.AlreadyExists, "already exists")
	}
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/transfer", adminToken, `{"dest_org":"destorg"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
	env.auth.mu.Lock()
	defer env.auth.mu.Unlock()
	if len(env.auth.rewriteCalls) != 0 {
		t.Errorf("scope rewrite should not run when the transfer fails; got %d", len(env.auth.rewriteCalls))
	}
}

// TestTransferRepository_RBACWarning: durable transfer succeeds but the scope
// rewrite fails → 200 with a non-empty rbac_warning.
func TestTransferRepository_RBACWarning(t *testing.T) {
	env := newTransferTestEnv(t, true)
	env.auth.rewriteFunc = func(_ context.Context, _ *authv1.RewriteRepoRoleScopesRequest) (*authv1.RewriteRepoRoleScopesResponse, error) {
		return nil, status.Error(codes.Unavailable, "auth down")
	}
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/transfer", adminToken, `{"dest_org":"destorg"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 (transfer is durable), got %d", resp.StatusCode)
	}
	var got struct {
		Org            string `json:"org"`
		RolesRewritten int64  `json:"roles_rewritten"`
		RBACWarning    string `json:"rbac_warning"`
	}
	decodeJSON(t, resp, &got)
	if got.RBACWarning == "" {
		t.Error("expected a non-empty rbac_warning on partial success")
	}
	if got.RolesRewritten != 0 {
		t.Errorf("roles_rewritten = %d, want 0 on rewrite failure", got.RolesRewritten)
	}
}
