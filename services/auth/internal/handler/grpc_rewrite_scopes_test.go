// grpc_rewrite_scopes_test.go — gRPC handler tests for RewriteRepoRoleScopes
// (repo rename / transfer RBAC-scope migration). Reuses the in-memory
// handlerFakeUserRepo (which records the (old,new) args + returns a stubbed
// count) from http_test.go so no real PostgreSQL is required.
package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

func TestRewriteRepoRoleScopes_passesThroughAndReturnsCount(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	tc.users.rewriteScopesResult = 3

	h := NewGRPCHandler(tc.svc, nil)
	resp, err := h.RewriteRepoRoleScopes(context.Background(), &authv1.RewriteRepoRoleScopesRequest{
		TenantId: uuid.NewString(),
		OldScope: "dev/old",
		NewScope: "dev/new",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetRewritten() != 3 {
		t.Errorf("rewritten = %d, want 3", resp.GetRewritten())
	}
	if len(tc.users.rewriteScopesCalls) != 1 || tc.users.rewriteScopesCalls[0] != [2]string{"dev/old", "dev/new"} {
		t.Errorf("repo call args = %v, want one (dev/old, dev/new)", tc.users.rewriteScopesCalls)
	}
}

func TestRewriteRepoRoleScopes_invalidTenant(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	h := NewGRPCHandler(tc.svc, nil)
	_, err := h.RewriteRepoRoleScopes(context.Background(), &authv1.RewriteRepoRoleScopesRequest{
		TenantId: "not-a-uuid", OldScope: "a/b", NewScope: "a/c",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestRewriteRepoRoleScopes_emptyScopes(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	h := NewGRPCHandler(tc.svc, nil)
	_, err := h.RewriteRepoRoleScopes(context.Background(), &authv1.RewriteRepoRoleScopesRequest{
		TenantId: uuid.NewString(), OldScope: "", NewScope: "a/c",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestRewriteRepoRoleScopes_equalScopesRejected(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	h := NewGRPCHandler(tc.svc, nil)
	_, err := h.RewriteRepoRoleScopes(context.Background(), &authv1.RewriteRepoRoleScopesRequest{
		TenantId: uuid.NewString(), OldScope: "a/b", NewScope: "a/b",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestRewriteRepoRoleScopes_repoErrorMapsToInternal(t *testing.T) {
	tc, cleanup := buildTestService(t)
	t.Cleanup(cleanup)
	tc.users.rewriteScopesErr = errors.New("boom")
	h := NewGRPCHandler(tc.svc, nil)
	_, err := h.RewriteRepoRoleScopes(context.Background(), &authv1.RewriteRepoRoleScopesRequest{
		TenantId: uuid.NewString(), OldScope: "a/b", NewScope: "a/c",
	})
	if status.Code(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", status.Code(err))
	}
}
