// Tests for FE-API-039 — per-org default retention routes + the inheritance
// fallback on the per-repo GET. We exercise the full HTTP → middleware →
// gRPC bufconn → fake metadata path so role gating, JSON shape, gRPC error
// mapping, AND the inherited_from labelling are all validated together.
package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

const (
	orgRetentionPath  = "/api/v1/orgs/myorg/policies/retention"
	repoRetentionPath = "/api/v1/repositories/myorg/myrepo/policies/retention"
)

// resetOrgRetentionFakes wipes both the org-default fakes AND the per-repo
// fakes used by the inheritance tests, so cases stay isolated regardless of
// run order. (The per-repo fakes are reset via resetRetentionFakes; this
// wrapper covers FE-API-039's added state in one place.)
func resetOrgRetentionFakes(t *testing.T) {
	t.Helper()
	resetRetentionFakes(t)
	t.Cleanup(func() {
		getOrgRetentionOverride = nil
		getOrgRetentionErr = nil
		upsertOrgRetentionCall = nil
		upsertOrgRetentionErr = nil
		deleteOrgRetentionCall = nil
		deleteOrgRetentionErr = nil
		effectiveRetentionOverride = nil
		effectiveRetentionErr = nil
		lookupOrgIDByNameErr = nil
	})
}

// ── GET /api/v1/orgs/{org}/policies/retention ────────────────────────────

// TestOrgRetentionGet_reader_returns200 verifies reader is sufficient — GET
// is purely informational so we don't gate it behind admin.
func TestOrgRetentionGet_reader_returns200(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.get(t, orgRetentionPath, readerToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for reader, got %d", resp.StatusCode)
	}
	var body handler.RetentionPolicyResponse
	decodeJSON(t, resp, &body)
	if body.OrgID == "" {
		t.Error("expected org_id on the response")
	}
	if body.InheritedFrom != "org" {
		t.Errorf("expected inherited_from=\"org\" on the org GET, got %q", body.InheritedFrom)
	}
}

// TestOrgRetentionGet_admin_returns200 — admin is also fine (sanity check
// against an overly tight gate).
func TestOrgRetentionGet_admin_returns200(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.get(t, orgRetentionPath, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

// TestOrgRetentionGet_noDefault_returns404WithCode verifies the typed 404
// body so the UI can distinguish "no org default" from other 404s.
func TestOrgRetentionGet_noDefault_returns404WithCode(t *testing.T) {
	resetOrgRetentionFakes(t)
	getOrgRetentionErr = status.Error(codes.NotFound, "not found")
	env := newTestEnv(t)
	resp := env.get(t, orgRetentionPath, adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	var body map[string]string
	decodeJSON(t, resp, &body)
	if body["code"] != "no-org-default" {
		t.Errorf("expected code=no-org-default, got %q", body["code"])
	}
}

// TestOrgRetentionGet_unknownOrg_returns404 verifies the org-lookup miss
// surfaces as a plain 404 (no info disclosure).
func TestOrgRetentionGet_unknownOrg_returns404(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	// Even an admin token has no "org" scope on "ghost", so the gate
	// short-circuits to 404 before any lookup. Use the same expectation
	// here so a future relaxation of the gate doesn't pass silently.
	resp := env.get(t, "/api/v1/orgs/ghost/policies/retention", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ── PUT /api/v1/orgs/{org}/policies/retention ────────────────────────────

const validOrgPutBody = `{
    "enabled": true,
    "rules": [{"kind": "max_age_days", "value": 90}],
    "protected_tag_patterns": ["latest", "stable"]
}`

// TestOrgRetentionPut_admin_returns200 verifies admin succeeds and updated_by
// is taken from the JWT.
func TestOrgRetentionPut_admin_returns200(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, orgRetentionPath, adminToken, validOrgPutBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if upsertOrgRetentionCall == nil {
		t.Fatal("expected metadata UpsertOrgRetentionPolicy to be called")
	}
	if upsertOrgRetentionCall.GetUpdatedBy() != testUserID {
		t.Errorf("updated_by: got %q, want %q (from JWT)", upsertOrgRetentionCall.GetUpdatedBy(), testUserID)
	}
	if !upsertOrgRetentionCall.GetEnabled() || len(upsertOrgRetentionCall.GetRules()) != 1 {
		t.Errorf("BFF did not forward fields: %+v", upsertOrgRetentionCall)
	}
	var body handler.RetentionPolicyResponse
	decodeJSON(t, resp, &body)
	if body.InheritedFrom != "org" {
		t.Errorf("expected inherited_from=\"org\" on org PUT response, got %q", body.InheritedFrom)
	}
}

// TestOrgRetentionPut_owner_returns200 — owner is above admin in the role
// hierarchy and should also succeed.
func TestOrgRetentionPut_owner_returns200(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, orgRetentionPath, ownerToken, validOrgPutBody)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for owner, got %d", resp.StatusCode)
	}
}

// TestOrgRetentionPut_writer_returns403 verifies writer is NOT enough —
// retention is destructive.
func TestOrgRetentionPut_writer_returns403(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, orgRetentionPath, writerToken, validOrgPutBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for writer, got %d", resp.StatusCode)
	}
}

// TestOrgRetentionPut_reader_returns403.
func TestOrgRetentionPut_reader_returns403(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, orgRetentionPath, readerToken, validOrgPutBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for reader, got %d", resp.StatusCode)
	}
}

// TestOrgRetentionPut_invalidRuleKind_returns400 verifies the metadata
// gRPC's InvalidArgument bubbles up as HTTP 400 on the org path too.
func TestOrgRetentionPut_invalidRuleKind_returns400(t *testing.T) {
	resetOrgRetentionFakes(t)
	upsertOrgRetentionErr = status.Error(codes.InvalidArgument, "unknown retention rule kind")
	env := newTestEnv(t)
	body := `{
        "enabled": true,
        "rules": [{"kind": "shred_with_lasers", "value": 1}],
        "protected_tag_patterns": []
    }`
	resp := env.putBody(t, orgRetentionPath, adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestOrgRetentionPut_malformedJSON_returns400.
func TestOrgRetentionPut_malformedJSON_returns400(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, orgRetentionPath, adminToken, "not json {")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestOrgRetentionPut_emitsNonNilSlices verifies the wire response always
// has rules + protected_tag_patterns as JSON arrays (not null) so dashboard
// code can iterate without a null-check.
func TestOrgRetentionPut_emitsNonNilSlices(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, orgRetentionPath, adminToken, validOrgPutBody)
	defer resp.Body.Close()
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode raw: %v", err)
	}
	if string(raw["rules"]) == "null" || string(raw["protected_tag_patterns"]) == "null" {
		t.Errorf("expected non-null slices, got rules=%s patterns=%s",
			string(raw["rules"]), string(raw["protected_tag_patterns"]))
	}
}

// ── DELETE /api/v1/orgs/{org}/policies/retention ─────────────────────────

// TestOrgRetentionDelete_happyPath_returns204.
func TestOrgRetentionDelete_happyPath_returns204(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.del(t, orgRetentionPath, adminToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
	if deleteOrgRetentionCall == nil {
		t.Error("expected metadata DeleteOrgRetentionPolicy to be called")
	}
}

// TestOrgRetentionDelete_noDefault_returns404.
func TestOrgRetentionDelete_noDefault_returns404(t *testing.T) {
	resetOrgRetentionFakes(t)
	deleteOrgRetentionErr = status.Error(codes.NotFound, "not found")
	env := newTestEnv(t)
	resp := env.del(t, orgRetentionPath, adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestOrgRetentionDelete_writer_returns403 — same admin-only gate as PUT.
func TestOrgRetentionDelete_writer_returns403(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.del(t, orgRetentionPath, writerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for writer, got %d", resp.StatusCode)
	}
}

// ── FE-API-039 inheritance fallback on per-repo GET ──────────────────────

// TestRepoRetentionGet_inheritsFromOrgDefault verifies the per-repo GET
// returns the org default when no per-repo policy exists. The
// `inherited_from` field is the load-bearing signal.
func TestRepoRetentionGet_inheritsFromOrgDefault(t *testing.T) {
	resetOrgRetentionFakes(t)
	// Force the per-repo lookup to NotFound so the BFF tries the fallback.
	getRetentionErr = status.Error(codes.NotFound, "not found")
	// Prime the fallback to return an enabled org default.
	effectiveRetentionOverride = &metadatav1.EffectiveRetentionPolicy{
		Policy: &metadatav1.RetentionPolicy{
			OrgId:                testOrgID,
			TenantId:             testTenantID,
			Enabled:              true,
			Rules:                []*metadatav1.RetentionRule{{Kind: "max_age_days", Value: 365}},
			ProtectedTagPatterns: []string{"latest"},
			PreviewUntil:         timestamppb.Now(),
			CreatedAt:            timestamppb.Now(),
			UpdatedAt:            timestamppb.Now(),
		},
		InheritedFrom: "org",
		OrgId:         testOrgID,
	}

	env := newTestEnv(t)
	resp := env.get(t, repoRetentionPath, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with org-default fallback, got %d", resp.StatusCode)
	}
	var body handler.RetentionPolicyResponse
	decodeJSON(t, resp, &body)
	if body.InheritedFrom != "org" {
		t.Errorf("expected inherited_from=\"org\", got %q", body.InheritedFrom)
	}
	if body.OrgID != testOrgID {
		t.Errorf("expected org_id=%q on inherited policy, got %q", testOrgID, body.OrgID)
	}
	if len(body.Rules) == 0 || body.Rules[0].Value != 365 {
		t.Errorf("expected inherited rule value 365, got %+v", body.Rules)
	}
}

// TestRepoRetentionGet_orgDefaultDisabled_returns404 verifies the
// "disabled default doesn't propagate" rule reaches the BFF: when the
// effective lookup returns NotFound (because the org default is disabled),
// the per-repo GET still surfaces the existing "no-policy" 404 body so
// existing clients work unchanged.
func TestRepoRetentionGet_orgDefaultDisabled_returns404(t *testing.T) {
	resetOrgRetentionFakes(t)
	getRetentionErr = status.Error(codes.NotFound, "not found")
	// effectiveRetentionErr is nil ⇒ default fake returns NotFound, which
	// matches what the metadata layer would do when the org default is
	// disabled (the SQL filter is `enabled = TRUE`).
	env := newTestEnv(t)
	resp := env.get(t, repoRetentionPath, adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 when no fallback policy, got %d", resp.StatusCode)
	}
	var body map[string]string
	decodeJSON(t, resp, &body)
	if body["code"] != "no-policy" {
		t.Errorf("expected code=no-policy, got %q", body["code"])
	}
}

// TestRepoRetentionGet_perRepoWins_labelsInheritedFromRepo verifies that
// when the per-repo lookup succeeds, the response carries
// `inherited_from: "repo"` regardless of any org default state. The fake
// effective lookup is NOT called in this branch — we don't even probe it.
func TestRepoRetentionGet_perRepoWins_labelsInheritedFromRepo(t *testing.T) {
	resetOrgRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.get(t, repoRetentionPath, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.RetentionPolicyResponse
	decodeJSON(t, resp, &body)
	if body.InheritedFrom != "repo" {
		t.Errorf("expected inherited_from=\"repo\" on per-repo GET, got %q", body.InheritedFrom)
	}
}
