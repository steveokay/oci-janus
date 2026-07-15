// repo_rename_test.go — BFF tests for POST /api/v1/repositories/{org}/{repo}/rename.
//
// The rename handler spans two services, so this env wires a scriptable
// metadata server (RenameRepository + GetRepositoryByName, which findRepo
// needs) and an auth server that embeds the shared fakeAuthServer (for
// ValidateToken + GetUserPermissions) and adds a scriptable
// RewriteRepoRoleScopes so the best-effort RBAC-migration path can be forced
// to succeed or fail per case.
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

// renameMetaServer scripts the two metadata RPCs the rename handler touches.
type renameMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer

	// renameFunc overrides RenameRepository; nil returns a canned renamed repo.
	renameFunc func(ctx context.Context, req *metadatav1.RenameRepositoryRequest) (*metadatav1.Repository, error)

	renameCalls []*metadatav1.RenameRepositoryRequest
	mu          sync.Mutex
}

func (s *renameMetaServer) GetRepositoryByName(_ context.Context, req *metadatav1.GetRepositoryByNameRequest) (*metadatav1.Repository, error) {
	// findRepo resolves "org/repo" to a repo_id. Echo a stable id back.
	return &metadatav1.Repository{
		RepoId: "repo-123",
		Org:    strings.SplitN(req.GetName(), "/", 2)[0],
		Name:   req.GetName(),
	}, nil
}

func (s *renameMetaServer) RenameRepository(ctx context.Context, req *metadatav1.RenameRepositoryRequest) (*metadatav1.Repository, error) {
	s.mu.Lock()
	s.renameCalls = append(s.renameCalls, req)
	s.mu.Unlock()
	if s.renameFunc != nil {
		return s.renameFunc(ctx, req)
	}
	return &metadatav1.Repository{
		RepoId: req.GetRepoId(),
		Org:    "myorg",
		Name:   req.GetNewName(),
	}, nil
}

// renameAuthServer embeds the shared fakeAuthServer (ValidateToken +
// GetUserPermissions) and adds a scriptable RewriteRepoRoleScopes.
type renameAuthServer struct {
	fakeAuthServer

	// rewriteFunc overrides RewriteRepoRoleScopes; nil returns rewritten=2.
	rewriteFunc func(ctx context.Context, req *authv1.RewriteRepoRoleScopesRequest) (*authv1.RewriteRepoRoleScopesResponse, error)

	rewriteCalls []*authv1.RewriteRepoRoleScopesRequest
	mu           sync.Mutex
}

func (s *renameAuthServer) RewriteRepoRoleScopes(ctx context.Context, req *authv1.RewriteRepoRoleScopesRequest) (*authv1.RewriteRepoRoleScopesResponse, error) {
	s.mu.Lock()
	s.rewriteCalls = append(s.rewriteCalls, req)
	s.mu.Unlock()
	if s.rewriteFunc != nil {
		return s.rewriteFunc(ctx, req)
	}
	return &authv1.RewriteRepoRoleScopesResponse{Rewritten: 2}, nil
}

type renameTestEnv struct {
	srv  *httptest.Server
	meta *renameMetaServer
	auth *renameAuthServer
}

func newRenameTestEnv(t *testing.T) *renameTestEnv {
	t.Helper()

	auth := &renameAuthServer{}
	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, auth)
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	meta := &renameMetaServer{}
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
	return &renameTestEnv{srv: srv, meta: meta, auth: auth}
}

func (e *renameTestEnv) post(t *testing.T, path, token, body string) *http.Response {
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

// TestRenameRepository_HappyPath: an admin renames the repo, gets 200 with the
// new name, the metadata RenameRepository ran with the resolved repo_id + new
// name, and the RBAC scope rewrite ran with the correct old/new scope strings.
func TestRenameRepository_HappyPath(t *testing.T) {
	env := newRenameTestEnv(t)

	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/rename", adminToken, `{"new_name":"newrepo"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}

	var got struct {
		Name           string `json:"name"`
		RolesRewritten int64  `json:"roles_rewritten"`
		RBACWarning    string `json:"rbac_warning"`
	}
	decodeJSON(t, resp, &got)
	if got.Name != "newrepo" {
		t.Errorf("name = %q, want %q", got.Name, "newrepo")
	}
	if got.RolesRewritten != 2 {
		t.Errorf("roles_rewritten = %d, want 2", got.RolesRewritten)
	}
	if got.RBACWarning != "" {
		t.Errorf("unexpected rbac_warning: %q", got.RBACWarning)
	}

	env.meta.mu.Lock()
	if len(env.meta.renameCalls) != 1 {
		t.Fatalf("want 1 RenameRepository call, got %d", len(env.meta.renameCalls))
	}
	rc := env.meta.renameCalls[0]
	env.meta.mu.Unlock()
	if rc.GetRepoId() != "repo-123" || rc.GetNewName() != "newrepo" {
		t.Errorf("rename call args = %+v, want repo_id=repo-123 new_name=newrepo", rc)
	}

	env.auth.mu.Lock()
	if len(env.auth.rewriteCalls) != 1 {
		t.Fatalf("want 1 RewriteRepoRoleScopes call, got %d", len(env.auth.rewriteCalls))
	}
	sc := env.auth.rewriteCalls[0]
	env.auth.mu.Unlock()
	if sc.GetOldScope() != "myorg/myrepo" || sc.GetNewScope() != "myorg/newrepo" {
		t.Errorf("scope rewrite = %q→%q, want myorg/myrepo→myorg/newrepo", sc.GetOldScope(), sc.GetNewScope())
	}
}

// TestRenameRepository_Forbidden: a reader (no admin) gets 403 and neither
// downstream RPC fires.
func TestRenameRepository_Forbidden(t *testing.T) {
	env := newRenameTestEnv(t)

	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/rename", readerToken, `{"new_name":"newrepo"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
	env.meta.mu.Lock()
	defer env.meta.mu.Unlock()
	if len(env.meta.renameCalls) != 0 {
		t.Errorf("rename should not run for a non-admin; got %d calls", len(env.meta.renameCalls))
	}
}

// TestRenameRepository_InvalidNewName: a name that fails the allowlist is a
// 400 before any RPC.
func TestRenameRepository_InvalidNewName(t *testing.T) {
	env := newRenameTestEnv(t)

	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/rename", adminToken, `{"new_name":"Bad Name"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	env.meta.mu.Lock()
	defer env.meta.mu.Unlock()
	if len(env.meta.renameCalls) != 0 {
		t.Errorf("rename should not run for an invalid name; got %d calls", len(env.meta.renameCalls))
	}
}

// TestRenameRepository_SameName: renaming to the current name is a 400 no-op.
func TestRenameRepository_SameName(t *testing.T) {
	env := newRenameTestEnv(t)

	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/rename", adminToken, `{"new_name":"myrepo"}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestRenameRepository_Collision: a metadata AlreadyExists maps to 409 and the
// RBAC rewrite never runs (the durable rename didn't happen).
func TestRenameRepository_Collision(t *testing.T) {
	env := newRenameTestEnv(t)
	env.meta.renameFunc = func(_ context.Context, _ *metadatav1.RenameRepositoryRequest) (*metadatav1.Repository, error) {
		return nil, status.Error(codes.AlreadyExists, "already exists")
	}

	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/rename", adminToken, `{"new_name":"taken"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
	env.auth.mu.Lock()
	defer env.auth.mu.Unlock()
	if len(env.auth.rewriteCalls) != 0 {
		t.Errorf("scope rewrite should not run when the rename fails; got %d calls", len(env.auth.rewriteCalls))
	}
}

// TestRenameRepository_RBACWarning: when the durable rename succeeds but the
// follow-up scope rewrite fails, the caller still gets 200 with the renamed
// repo AND a non-empty rbac_warning (partial success).
func TestRenameRepository_RBACWarning(t *testing.T) {
	env := newRenameTestEnv(t)
	env.auth.rewriteFunc = func(_ context.Context, _ *authv1.RewriteRepoRoleScopesRequest) (*authv1.RewriteRepoRoleScopesResponse, error) {
		return nil, status.Error(codes.Unavailable, "auth down")
	}

	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/rename", adminToken, `{"new_name":"newrepo"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200 (rename is durable), got %d", resp.StatusCode)
	}
	var got struct {
		Name           string `json:"name"`
		RolesRewritten int64  `json:"roles_rewritten"`
		RBACWarning    string `json:"rbac_warning"`
	}
	decodeJSON(t, resp, &got)
	if got.Name != "newrepo" {
		t.Errorf("name = %q, want newrepo", got.Name)
	}
	if got.RBACWarning == "" {
		t.Error("expected a non-empty rbac_warning on partial success")
	}
	if got.RolesRewritten != 0 {
		t.Errorf("roles_rewritten = %d, want 0 on rewrite failure", got.RolesRewritten)
	}
}
