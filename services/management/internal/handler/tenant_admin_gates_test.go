// Package handler_test — Phase 5.2 scope-aware tenant-admin gate HTTP tests.
//
// These tests verify that the Review §A1 Top-5 #2 fix is correctly wired end-to-end
// through the HTTP→auth→gate path. Each gate (scan policy, webhook, domain,
// proxy-cache, audit-export) is tested with three callers:
//
//   - adminToken        — (admin, org, "myorg")      → MUST return 403
//   - "tenant-admin-token" — (admin, tenant, <id>)   → MUST NOT return 403
//   - platformAdminToken   — (admin, org, "*")        → MUST NOT return 403
//
// Unit tests for effectiveTenantAdmin itself live in rbac_test.go (package
// handler, white-box) because effectiveTenantAdmin is unexported.
package handler_test

import (
	"net/http"
	"testing"
)

// ─── requireScanPolicyAdmin HTTP gate tests ────────────────────────────────

// TestRequireScanPolicyAdmin_OrgAdminOnly_Denied verifies that an org-A admin
// cannot configure tenant-wide scan policies (Review §A1, Top-5 #2 fix).
// Previously the gate returned true for any org admin in the tenant; the fix
// scopes the check to tenant-admin or platform-admin only.
func TestRequireScanPolicyAdmin_OrgAdminOnly_Denied(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"","exempt_cves":[],"scanner_plugin":"trivy","scanner_version_pin":""}`
	// adminToken carries (admin, org, "myorg") — must be denied after Phase 5.2.
	resp := env.putBody(t, "/api/v1/security/policies", adminToken, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-scoped admin must be denied scan policy PUT: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireScanPolicyAdmin_TenantAdmin_Allowed verifies that a tenant-scoped
// admin (migration 20260625000001) can configure scan policies.
func TestRequireScanPolicyAdmin_TenantAdmin_Allowed(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"","exempt_cves":[],"scanner_plugin":"trivy","scanner_version_pin":""}`
	// "tenant-admin-token" carries (admin, tenant, testTenantID).
	resp := env.putBody(t, "/api/v1/security/policies", "tenant-admin-token", body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("tenant-admin must be allowed scan policy PUT: got %d, want 200", resp.StatusCode)
	}
}

// TestRequireScanPolicyAdmin_PlatformAdmin_Allowed verifies the legacy
// platform-admin marker (admin, org, "*") still passes the scan policy gate.
func TestRequireScanPolicyAdmin_PlatformAdmin_Allowed(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"","exempt_cves":[],"scanner_plugin":"trivy","scanner_version_pin":""}`
	resp := env.putBody(t, "/api/v1/security/policies", platformAdminToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("platform-admin must be allowed scan policy PUT: got %d, want 200", resp.StatusCode)
	}
}

// ─── requireWebhookAdmin HTTP gate tests ──────────────────────────────────

// TestRequireWebhookAdmin_OrgAdminOnly_Denied verifies that an org-scoped admin
// cannot list webhooks (which expose URLs that may carry auth tokens in the
// query string). Previously the gate admitted any org-level admin; the fix
// requires tenant-admin or platform-admin (Review §A1, Top-5 #2 fix).
func TestRequireWebhookAdmin_OrgAdminOnly_Denied(t *testing.T) {
	env := newWebhookTestEnv(t, &errWebhookServer{})
	// adminToken carries (admin, org, "myorg") — must be denied.
	resp := env.get(t, "/api/v1/webhooks", adminToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-scoped admin must be denied webhook list: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireWebhookAdmin_TenantAdmin_Allowed verifies that a tenant-admin
// (migration 20260625000001) can reach the webhook list. The errWebhookServer
// returns an error, so we'll see a 5xx — any non-403 proves the gate passed.
func TestRequireWebhookAdmin_TenantAdmin_Allowed(t *testing.T) {
	env := newWebhookTestEnv(t, &errWebhookServer{})
	// "tenant-admin-token" carries (admin, tenant, testTenantID).
	resp := env.get(t, "/api/v1/webhooks", "tenant-admin-token")
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("tenant-admin must NOT be denied webhook list: got 403")
	}
}

// TestRequireWebhookAdmin_PlatformAdmin_Allowed verifies the legacy platform-admin
// marker (admin, org, "*") still passes the webhook gate.
func TestRequireWebhookAdmin_PlatformAdmin_Allowed(t *testing.T) {
	env := newWebhookTestEnv(t, &errWebhookServer{})
	resp := env.get(t, "/api/v1/webhooks", platformAdminToken)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("platform-admin must NOT be denied webhook list: got 403")
	}
}

// ─── requireDomainAdmin HTTP gate tests (domains route) ───────────────────

// TestRequireDomainAdmin_OrgAdminOnly_Denied verifies that an org-scoped admin
// cannot list custom domains. Domain list exposes verification tokens and
// notification timestamps that are tenant-admin-only.
// (Review §A1, Top-5 #2 fix — domains gate.)
func TestRequireDomainAdmin_OrgAdminOnly_Denied(t *testing.T) {
	env := newDomainsEnv(t)
	// adminToken carries (admin, org, "myorg") — must be denied.
	resp := env.get(t, "/api/v1/workspace/me/domains", adminToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-scoped admin must be denied domain list: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireDomainAdmin_TenantAdmin_Allowed verifies that a tenant-admin
// can list domains.
func TestRequireDomainAdmin_TenantAdmin_Allowed(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.get(t, "/api/v1/workspace/me/domains", "tenant-admin-token")
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("tenant-admin must NOT be denied domain list: got 403")
	}
}

// TestRequireDomainAdmin_PlatformAdmin_Allowed verifies the platform-admin
// marker (admin, org, "*") still passes the domain gate.
func TestRequireDomainAdmin_PlatformAdmin_Allowed(t *testing.T) {
	env := newDomainsEnv(t)
	resp := env.get(t, "/api/v1/workspace/me/domains", platformAdminToken)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("platform-admin must NOT be denied domain list: got 403")
	}
}

// ─── requireDomainAdmin HTTP gate tests (proxy-cache route) ───────────────

// TestRequireProxyCacheAdmin_OrgAdminOnly_Denied verifies that an org-scoped
// admin cannot view proxy-cache stats (proxy cache is a tenant-wide resource).
// This exercises the proxy_cache.go leg of requireDomainAdmin (Review §A1 #3).
func TestRequireProxyCacheAdmin_OrgAdminOnly_Denied(t *testing.T) {
	env, _ := newProxyEnv(t)
	// adminToken carries (admin, org, "myorg") — must be denied.
	resp := env.get(t, "/api/v1/proxy/cache/stats", adminToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-scoped admin must be denied proxy cache stats: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireProxyCacheAdmin_TenantAdmin_Allowed verifies the proxy-cache gate
// accepts a tenant-admin (migration 20260625000001).
func TestRequireProxyCacheAdmin_TenantAdmin_Allowed(t *testing.T) {
	env, _ := newProxyEnv(t)
	resp := env.get(t, "/api/v1/proxy/cache/stats", "tenant-admin-token")
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("tenant-admin must NOT be denied proxy cache stats: got 403")
	}
}

// TestRequireProxyCacheAdmin_PlatformAdmin_Allowed verifies the platform-admin
// marker still passes the proxy-cache gate.
func TestRequireProxyCacheAdmin_PlatformAdmin_Allowed(t *testing.T) {
	env, _ := newProxyEnv(t)
	resp := env.get(t, "/api/v1/proxy/cache/stats", platformAdminToken)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("platform-admin must NOT be denied proxy cache stats: got 403")
	}
}

// ─── requireDomainAdmin HTTP gate tests (audit-export route) ──────────────

// TestRequireAuditExportAdmin_OrgAdminOnly_Denied exercises the audit-export
// GET route which uses requireDomainAdmin as its gate. An org-scoped admin
// must not be able to retrieve (or configure) the SIEM export sink that
// receives the entire tenant's audit trail (Review §A1, Top-5 #2 fix).
func TestRequireAuditExportAdmin_OrgAdminOnly_Denied(t *testing.T) {
	// newTestEnv wires the audit client (needed for build-history / activity).
	// The gate runs before the gRPC call, so a 403 proves the gate fired.
	env := newTestEnv(t)
	// adminToken carries (admin, org, "myorg") — must be denied.
	resp := env.get(t, "/api/v1/workspace/me/audit-export", adminToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("org-scoped admin must be denied audit-export GET: got %d, want 403", resp.StatusCode)
	}
}

// TestRequireAuditExportAdmin_TenantAdmin_Allowed verifies that a tenant-admin
// passes the audit-export gate. The underlying gRPC call may return an error
// (no stub), but that surfaces as non-403 — proving the gate admitted the caller.
func TestRequireAuditExportAdmin_TenantAdmin_Allowed(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/workspace/me/audit-export", "tenant-admin-token")
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("tenant-admin must NOT be denied audit-export GET: got 403")
	}
}

// TestRequireAuditExportAdmin_PlatformAdmin_Allowed verifies the legacy
// platform-admin marker passes the audit-export gate.
func TestRequireAuditExportAdmin_PlatformAdmin_Allowed(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/workspace/me/audit-export", platformAdminToken)
	if resp.StatusCode == http.StatusForbidden {
		t.Errorf("platform-admin must NOT be denied audit-export GET: got 403")
	}
}
