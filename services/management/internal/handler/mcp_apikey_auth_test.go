// mcp_apikey_auth_test.go — the management BFF must accept API-key Bearer
// tokens (`key.<uuid>.<secret>`), not just JWTs.
//
// registry-mcp (and CLI/Terraform clients) authenticate to the BFF with a
// long-lived API key — a JWT (300s TTL) is unusable for a persistent client.
// Before this, RequireAuth only called auth.ValidateToken (JWT-only), so every
// API-key-bearing request 401'd. This test drives a real route with a `key.`
// Bearer and asserts the BFF dispatches it to auth.ValidateAPIKey. Mirrors the
// FUT-006 dispatch registry-auth's own HTTP middleware already does.
package handler_test

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

// apiKeyBearer is a well-formed `key.<uuid>.<secret>` token. The uuid segment
// is the key id; the fakeAuthServer.ValidateAPIKey below accepts exactly this
// id and returns the standard test identity.
// apiKeySecret is a deliberately non-credential-shaped placeholder — the fake
// ValidateAPIKey below never inspects the secret (it matches on key id only),
// and parseAPIKeyBearer only requires a non-empty segment after the second
// dot. Kept obviously-fake so the secret scanner doesn't flag a 64-hex string.
const (
	apiKeyID     = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	apiKeySecret = "placeholder-not-a-real-secret"
	apiKeyBearer = "key." + apiKeyID + "." + apiKeySecret
)

func TestRequireAuth_APIKeyBearer_Accepted(t *testing.T) {
	env := newTestEnv(t)
	// A route that only needs authentication + tenant scope — no role gate —
	// so a clean 200 proves the API-key path authenticated and injected the
	// tenant context. GET /api/v1/service-accounts is exactly that (FUT-082).
	resp := env.get(t, "/api/v1/service-accounts", apiKeyBearer)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("API-key Bearer got HTTP %d, want 200 — BFF did not accept the key form", resp.StatusCode)
	}
}

func TestRequireAuth_APIKeyBearer_BadKeyRejected(t *testing.T) {
	env := newTestEnv(t)
	// A key-shaped token whose id the fake does not recognise → auth
	// ValidateAPIKey returns Unauthenticated → BFF must 401 (fail closed),
	// never fall through to a JWT parse that also fails silently.
	bad := "key.99999999-9999-9999-9999-999999999999." + apiKeySecret
	resp := env.get(t, "/api/v1/service-accounts", bad)
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("unknown API key got HTTP %d, want 401", resp.StatusCode)
	}
}

// ValidateAPIKey accepts the single canned key id and returns the standard
// test identity; any other id is Unauthenticated. Declared here so the
// FUT-006-style BFF dispatch has a fake to call. The shared fakeAuthServer
// type lives in handler_test.go.
func (s *fakeAuthServer) ValidateAPIKey(_ context.Context, req *authv1.ValidateAPIKeyRequest) (*authv1.ValidateAPIKeyResponse, error) {
	if req.GetKeyId() != apiKeyID {
		return nil, status.Error(codes.Unauthenticated, "unknown api key")
	}
	return &authv1.ValidateAPIKeyResponse{
		Valid:    true,
		UserId:   testUserID,
		TenantId: testTenantID,
	}, nil
}
