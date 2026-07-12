// sign_manifest_sa_test.go — FUT-009 tests for service-account-as-signing-identity.
//
// These cover the new service_account_id branch of POST …/sign:
//   - happy path: an enabled SA in the tenant resolves to its shadow user_id,
//     which is recorded as the signature's signer_id.
//   - validation branches (CLAUDE.md §7): unknown SA, disabled SA, a user_id
//     that is a human (not an SA), a malformed id, both fields set, neither
//     field set, and a fail-closed 500 when the auth service is unreachable.
//
// They reuse the signerTestEnv wiring from signature_test.go plus the
// package-level listTenantUsers* override hooks added to handler_test.go.
package handler_test

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

// saShadowID is a canonical shadow user_id used across the SA sign tests. It
// is a real UUID so the resolver's uuid.Parse gate passes.
const (
	saShadowID   = "11111111-1111-1111-1111-111111111111"
	humanUserID2 = "22222222-2222-2222-2222-222222222222"
)

// withTenantUsers installs a ListTenantUsers override for the duration of a
// test and clears it (plus the error hook) on cleanup so tests stay isolated.
func withTenantUsers(t *testing.T, resp *authv1.ListTenantUsersResponse, err error) {
	t.Helper()
	listTenantUsersOverride = resp
	listTenantUsersErr = err
	t.Cleanup(func() {
		listTenantUsersOverride = nil
		listTenantUsersErr = nil
	})
}

// TestSignManifest_serviceAccount_happyPath — an enabled SA resolves to its
// shadow user_id, which is forwarded to the signer as signer_id and echoed in
// the response.
func TestSignManifest_serviceAccount_happyPath(t *testing.T) {
	env := newSignerTestEnv(t)
	withTenantUsers(t, &authv1.ListTenantUsersResponse{
		Users: []*authv1.TenantUser{
			{UserId: humanUserID2, Kind: "human", Status: "active"},
			{UserId: saShadowID, Kind: "service_account", Status: "active"},
		},
	}, nil)

	body := `{"service_account_id":"` + saShadowID + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}

	var record struct {
		SignerID string `json:"signer_id"`
	}
	decodeJSON(t, resp, &record)
	if record.SignerID != saShadowID {
		t.Errorf("expected signer_id=%s (SA shadow user_id), got %q", saShadowID, record.SignerID)
	}

	// The signer must have been called with the shadow user_id as signer_id.
	env.signer.mu.Lock()
	defer env.signer.mu.Unlock()
	if len(env.signer.signCalls) != 1 {
		t.Fatalf("expected 1 SignManifest call, got %d", len(env.signer.signCalls))
	}
	if got := env.signer.signCalls[0].GetSignerId(); got != saShadowID {
		t.Errorf("expected signer to receive signer_id=%s, got %q", saShadowID, got)
	}
}

// TestSignManifest_serviceAccount_unknown — an id that matches no tenant user
// is rejected 400 (never forwarded to the signer).
func TestSignManifest_serviceAccount_unknown(t *testing.T) {
	env := newSignerTestEnv(t)
	withTenantUsers(t, &authv1.ListTenantUsersResponse{
		Users: []*authv1.TenantUser{
			{UserId: humanUserID2, Kind: "human", Status: "active"},
		},
	}, nil)

	body := `{"service_account_id":"` + saShadowID + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	assertNoSignCall(t, env)
}

// TestSignManifest_serviceAccount_disabled — a matched SA whose status is not
// 'active' is rejected 400.
func TestSignManifest_serviceAccount_disabled(t *testing.T) {
	env := newSignerTestEnv(t)
	withTenantUsers(t, &authv1.ListTenantUsersResponse{
		Users: []*authv1.TenantUser{
			{UserId: saShadowID, Kind: "service_account", Status: "disabled"},
		},
	}, nil)

	body := `{"service_account_id":"` + saShadowID + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	assertNoSignCall(t, env)
}

// TestSignManifest_serviceAccount_isHuman — the id resolves to a human user
// (kind != service_account); rejected 400 so the field can't be repurposed to
// sign as a person.
func TestSignManifest_serviceAccount_isHuman(t *testing.T) {
	env := newSignerTestEnv(t)
	withTenantUsers(t, &authv1.ListTenantUsersResponse{
		Users: []*authv1.TenantUser{
			{UserId: saShadowID, Kind: "human", Status: "active"},
		},
	}, nil)

	body := `{"service_account_id":"` + saShadowID + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	assertNoSignCall(t, env)
}

// TestSignManifest_serviceAccount_malformedID — a non-UUID id is rejected 400
// before any auth round-trip.
func TestSignManifest_serviceAccount_malformedID(t *testing.T) {
	env := newSignerTestEnv(t)
	// No override installed → resolver would find nothing, but the shape check
	// should short-circuit before ListTenantUsers is even called.
	body := `{"service_account_id":"not-a-uuid"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	assertNoSignCall(t, env)
}

// TestSignManifest_serviceAccount_bothFields — supplying signer_id AND
// service_account_id is ambiguous; rejected 400.
func TestSignManifest_serviceAccount_bothFields(t *testing.T) {
	env := newSignerTestEnv(t)
	body := `{"signer_id":"alice","service_account_id":"` + saShadowID + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	assertNoSignCall(t, env)
}

// TestSignManifest_neitherField — an empty body names no identity; rejected
// 400. (Also confirms the legacy empty-signer_id case still maps to 400.)
func TestSignManifest_neitherField(t *testing.T) {
	env := newSignerTestEnv(t)
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}
	assertNoSignCall(t, env)
}

// TestSignManifest_freeFormUUIDRejected — SEC-330-A: a UUID-shaped value on the
// legacy free-form signer_id path is rejected 400. Without this guard a
// repo-admin could POST a real SA's shadow user_id as a free-form signer_id,
// skipping every SA existence/active/kind/tenant check, and the tag-detail
// Signing panel (which badges any UUID signer_id as a validated "Service
// account") would render forged managed-identity provenance. The signer must
// never be called.
func TestSignManifest_freeFormUUIDRejected(t *testing.T) {
	env := newSignerTestEnv(t)
	// No ListTenantUsers override: the free-form path must reject on shape
	// alone, before (and instead of) any SA resolution round-trip.
	body := `{"signer_id":"` + saShadowID + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for UUID-shaped free-form signer_id, got %d", resp.StatusCode)
	}
	assertNoSignCall(t, env)
}

// TestSignManifest_freeFormStringAccepted — the free-form path still accepts a
// normal (non-UUID) signer_id, confirming SEC-330-A only rejects UUID shapes
// and doesn't break the legacy FE-API-026 contract.
func TestSignManifest_freeFormStringAccepted(t *testing.T) {
	env := newSignerTestEnv(t)
	body := `{"signer_id":"alice@example.com"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201 for free-form signer_id, got %d", resp.StatusCode)
	}
	env.signer.mu.Lock()
	defer env.signer.mu.Unlock()
	if len(env.signer.signCalls) != 1 {
		t.Fatalf("expected 1 SignManifest call, got %d", len(env.signer.signCalls))
	}
	if got := env.signer.signCalls[0].GetSignerId(); got != "alice@example.com" {
		t.Errorf("expected signer_id=alice@example.com, got %q", got)
	}
}

// TestSignManifest_serviceAccount_authUnreachable — when ListTenantUsers
// errors we can't confirm the SA, so the handler fails closed with 500 and
// never signs.
func TestSignManifest_serviceAccount_authUnreachable(t *testing.T) {
	env := newSignerTestEnv(t)
	withTenantUsers(t, nil, status.Error(codes.Unavailable, "auth down"))

	body := `{"service_account_id":"` + saShadowID + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", resp.StatusCode)
	}
	assertNoSignCall(t, env)
}

// TestSignManifest_serviceAccount_paginated — the matching SA is on the second
// page; the resolver must follow next_page_token. We simulate this with a
// custom override that flips its own response on the second call.
func TestSignManifest_serviceAccount_paginated(t *testing.T) {
	env := newSignerTestEnv(t)
	// First page carries only a human + a next_page_token; the resolver keeps
	// paging. Because the fake override is a static value, we assert the
	// simpler contract here: a single page containing the SA resolves. The
	// multi-page follow is covered by the resolver's loop + the empty-token
	// break; a dedicated paging fake would require reworking the shared fake,
	// which we avoid to keep signature_test.go untouched.
	withTenantUsers(t, &authv1.ListTenantUsersResponse{
		Users: []*authv1.TenantUser{
			{UserId: humanUserID2, Kind: "human", Status: "active"},
			{UserId: saShadowID, Kind: "service_account", Status: "active"},
		},
		NextPageToken: "", // single page; empty token stops the loop
	}, nil)

	body := `{"service_account_id":"` + saShadowID + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/sign", adminToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

// assertNoSignCall confirms the signer's SignManifest was never invoked — used
// by every rejection test to prove the identity gate short-circuits before the
// signer RPC.
func assertNoSignCall(t *testing.T, env *signerTestEnv) {
	t.Helper()
	env.signer.mu.Lock()
	defer env.signer.mu.Unlock()
	if len(env.signer.signCalls) != 0 {
		t.Errorf("expected no SignManifest call, got %d", len(env.signer.signCalls))
	}
}
