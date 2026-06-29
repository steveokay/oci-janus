// REDESIGN-001 Phase 5.4 / Decision #24 — admin gates must deny
// service-account principals up front, regardless of the role
// assignments on their shadow user. The shadow user inherits the
// human owner's roles, so a naïve role lookup would accidentally
// clear admin gates that an API key should never clear.
//
// These tests exercise every management `require*Admin` helper with
// a JWT-shaped bearer whose principal_kind="service_account". The
// underlying GetUserPermissions returns full admin grants
// (is_global_admin=true plus tenant + org=* assignments), so the
// gate failing closed proves the principal-kind deny is firing
// before the role lookup.
package handler_test

import (
	"net/http"
	"testing"

	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
)

// TestRequirePlatformAdmin_SAPrincipal_Denied covers admin_tenants.go.
// The fake auth tier hands the request a full is_global_admin=true grant,
// so absent the Phase 5.4 deny the route would return 200. A 403 proves
// the principal-kind check fires before effectiveGlobalAdmin runs.
func TestRequirePlatformAdmin_SAPrincipal_Denied(t *testing.T) {
	env, _ := newAdminEnv(t)
	// /api/v1/admin/tenants/{id} routes through requirePlatformAdmin.
	resp := env.get(t, "/api/v1/admin/tenants/"+detailTenantID, saBearerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("SA bearer at requirePlatformAdmin: got %d, want 403", resp.StatusCode)
	}
}

// TestRequirePlatformAdmin_HumanAdmin_Allowed is the positive control:
// a human admin with the same role bundle MUST NOT be denied. The only
// difference vs. the SA case above is the principal_kind claim on the
// bearer — this guards against an over-broad deny accidentally locking
// out humans. Any non-403 status proves the gate accepted the caller
// (downstream gRPC fakes return errors for some sub-RPCs which is fine).
func TestRequirePlatformAdmin_HumanAdmin_Allowed(t *testing.T) {
	env, _ := newAdminEnv(t)
	// platformAdminToken resolves to platformAdminUser — a human caller
	// whose adminFakeAuthServer entry sets IsGlobalAdmin=true.
	resp := env.get(t, "/api/v1/admin/tenants/"+detailTenantID, platformAdminToken)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("human platform-admin at requirePlatformAdmin: got 403, want non-403 (gate must pass)")
	}
}

// TestRequireScanPolicyAdmin_SAPrincipal_Denied covers security_policies.go.
// PUT /api/v1/security/policies hits requireScanPolicyAdmin → 403 expected.
func TestRequireScanPolicyAdmin_SAPrincipal_Denied(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"","exempt_cves":[],"scanner_plugin":"trivy","scanner_version_pin":""}`
	resp := env.putBody(t, "/api/v1/security/policies", saBearerToken, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("SA bearer at requireScanPolicyAdmin: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireScanPolicyAdmin_HumanAdmin_Allowed mirrors the positive
// control for the scan-policy gate. A human tenant-admin with the same
// shadow-equivalent grants must pass.
func TestRequireScanPolicyAdmin_HumanAdmin_Allowed(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"","exempt_cves":[],"scanner_plugin":"trivy","scanner_version_pin":""}`
	resp := env.putBody(t, "/api/v1/security/policies", "tenant-admin-token", body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("human tenant-admin at requireScanPolicyAdmin: got %d, want 200", resp.StatusCode)
	}
}

// TestRequireWebhookAdmin_SAPrincipal_Denied covers webhooks.go.
// GET /api/v1/webhooks is the simplest entry-point exercising the gate.
// The errWebhookServer (returning a backend error) is sufficient because
// the gate fires before any gRPC dispatch.
func TestRequireWebhookAdmin_SAPrincipal_Denied(t *testing.T) {
	env := newWebhookTestEnv(t, &errWebhookServer{})
	resp := env.get(t, "/api/v1/webhooks", saBearerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("SA bearer at requireWebhookAdmin: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireWebhookAdmin_HumanAdmin_Allowed is the positive control. A
// non-403 status (e.g. 500 from errWebhookServer) proves the gate passed
// the request through to the backend client.
func TestRequireWebhookAdmin_HumanAdmin_Allowed(t *testing.T) {
	env := newWebhookTestEnv(t, &errWebhookServer{})
	resp := env.get(t, "/api/v1/webhooks", "tenant-admin-token")
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("human tenant-admin at requireWebhookAdmin: got 403, want non-403 (gate must pass)")
	}
}

// TestRequireGCAdmin_SAPrincipal_Denied covers admin_gc.go.
// GET /api/v1/admin/gc/status hits requireGCAdmin → 403 expected.
// Triggering GC destroys blobs, so an SA bearer must never clear this gate.
func TestRequireGCAdmin_SAPrincipal_Denied(t *testing.T) {
	env, _ := newGCEnv(t)
	resp := env.get(t, "/api/v1/admin/gc/status", saBearerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("SA bearer at requireGCAdmin: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireScannerAdmin_SAPrincipal_Denied covers admin_scanners.go.
// GET /api/v1/admin/scanners routes through requireScannerAdmin → 403
// expected. Swapping the active scanner adapter is platform-wide
// configuration that SA bearers must not clear.
func TestRequireScannerAdmin_SAPrincipal_Denied(t *testing.T) {
	env, _ := newScannerEnv(t)
	resp := env.get(t, "/api/v1/admin/scanners", saBearerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("SA bearer at requireScannerAdmin: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireDomainAdmin_SAPrincipal_Denied covers handler.go's
// requireDomainAdmin via the audit-export route. The gate also fronts
// proxy-cache, so a single deny here proves the helper is locked down.
// Proxy-cache config and audit-export sinks are both data-egress channels
// — an SA bearer must never reconfigure them.
func TestRequireDomainAdmin_SAPrincipal_Denied(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/workspace/me/audit-export", saBearerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("SA bearer at requireDomainAdmin: got %d, want 403", resp.StatusCode)
	}
}

// _ keeps the webhookv1 import alive in case future test extensions need
// to inspect dispatched messages without re-importing.
var _ webhookv1.WebhookServiceServer = (*errWebhookServer)(nil)
