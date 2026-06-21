// Tests for FE-API-037 per-repo retention CRUD routes. We exercise the full
// HTTP → middleware → gRPC bufconn → fake metadata path so role gating,
// JSON shape, and gRPC error mapping are all validated together.
package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

const retentionPath = "/api/v1/repositories/myorg/myrepo/policies/retention"

// resetRetentionFakes wipes the package-level fake state set by previous
// tests. Called via t.Cleanup so cases stay isolated regardless of order.
func resetRetentionFakes(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		getRetentionOverride = nil
		getRetentionErr = nil
		upsertRetentionCall = nil
		upsertRetentionErr = nil
		deleteRetentionCall = nil
		deleteRetentionErr = nil
	})
}

// ── GET ────────────────────────────────────────────────────────────────────

// TestRetentionGet_happyPath returns the canned policy + 200.
func TestRetentionGet_happyPath(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.get(t, retentionPath, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.RetentionPolicyResponse
	decodeJSON(t, resp, &body)
	if !body.Enabled {
		t.Error("expected enabled=true on canned policy")
	}
	if len(body.Rules) == 0 {
		t.Error("expected at least one rule in canned policy")
	}
	if body.PreviewUntil == "" {
		t.Error("expected preview_until on canned policy")
	}
}

// TestRetentionGet_noPolicy_returns404WithCode verifies the NotFound branch
// surfaces the "no-policy" code so the dashboard can render the inherited
// state cleanly.
func TestRetentionGet_noPolicy_returns404WithCode(t *testing.T) {
	resetRetentionFakes(t)
	getRetentionErr = status.Error(codes.NotFound, "not found")
	env := newTestEnv(t)
	resp := env.get(t, retentionPath, adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	var body map[string]string
	decodeJSON(t, resp, &body)
	if body["code"] != "no-policy" {
		t.Errorf("expected code=no-policy, got %q", body["code"])
	}
}

// ── PUT ────────────────────────────────────────────────────────────────────

// validPutBody is the happy-path PUT shape — used by multiple tests as the
// baseline they tweak.
const validPutBody = `{
    "enabled": true,
    "rules": [{"kind": "max_age_days", "value": 90}],
    "protected_tag_patterns": ["latest", "stable"]
}`

// TestRetentionPut_happyPath_returns200WithPreview verifies a valid PUT
// reaches the metadata service and the response includes preview_until.
func TestRetentionPut_happyPath_returns200WithPreview(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, retentionPath, adminToken, validPutBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if upsertRetentionCall == nil {
		t.Fatal("expected metadata to be called")
	}
	if !upsertRetentionCall.GetEnabled() || len(upsertRetentionCall.GetRules()) != 1 {
		t.Errorf("BFF did not forward fields: %+v", upsertRetentionCall)
	}
	// updated_by comes from the JWT, not the body; the fake auth maps
	// adminToken → testUserID.
	if upsertRetentionCall.GetUpdatedBy() != testUserID {
		t.Errorf("updated_by: got %q, want %q (from JWT)", upsertRetentionCall.GetUpdatedBy(), testUserID)
	}

	var body handler.RetentionPolicyResponse
	decodeJSON(t, resp, &body)
	if body.PreviewUntil == "" {
		t.Error("expected preview_until on response")
	}
}

// TestRetentionPut_owner_returns200 verifies an owner role passes the admin
// gate (owner > admin in the role hierarchy).
func TestRetentionPut_owner_returns200(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, retentionPath, ownerToken, validPutBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for owner, got %d", resp.StatusCode)
	}
}

// TestRetentionPut_writer_returns403 verifies writer is NOT enough.
func TestRetentionPut_writer_returns403(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, retentionPath, writerToken, validPutBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestRetentionPut_reader_returns403.
func TestRetentionPut_reader_returns403(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, retentionPath, readerToken, validPutBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestRetentionPut_invalidRuleKind_returns400 verifies the metadata gRPC's
// InvalidArgument bubbles up as HTTP 400. We force the metadata fake to
// return InvalidArgument since the BFF defers validation to the gRPC layer.
func TestRetentionPut_invalidRuleKind_returns400(t *testing.T) {
	resetRetentionFakes(t)
	upsertRetentionErr = status.Error(codes.InvalidArgument, "unknown retention rule kind")
	env := newTestEnv(t)
	body := `{
        "enabled": true,
        "rules": [{"kind": "shred_with_lasers", "value": 1}],
        "protected_tag_patterns": []
    }`
	resp := env.putBody(t, retentionPath, adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestRetentionPut_invalidRegex_returns400 verifies a malformed regex
// pattern (forced as InvalidArgument from the gRPC fake) becomes a 400.
func TestRetentionPut_invalidRegex_returns400(t *testing.T) {
	resetRetentionFakes(t)
	upsertRetentionErr = status.Error(codes.InvalidArgument, "protected_tag_pattern is not a valid regex")
	env := newTestEnv(t)
	body := `{
        "enabled": true,
        "rules": [{"kind": "max_age_days", "value": 30}],
        "protected_tag_patterns": ["(unclosed"]
    }`
	resp := env.putBody(t, retentionPath, adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestRetentionPut_malformedJSON_returns400.
func TestRetentionPut_malformedJSON_returns400(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, retentionPath, adminToken, "not json {")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestRetentionPut_emitsNonNilSlices verifies the wire response always has
// rules / protected_tag_patterns as JSON arrays (not null) so the dashboard
// can iterate without a null-check. A bit of belt-and-braces — the BFF
// guarantee complements the metadata-side validation.
func TestRetentionPut_emitsNonNilSlices(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.putBody(t, retentionPath, adminToken, validPutBody)
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

// ── DELETE ─────────────────────────────────────────────────────────────────

// TestRetentionDelete_happyPath_returns204.
func TestRetentionDelete_happyPath_returns204(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.del(t, retentionPath, adminToken)
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204, got %d", resp.StatusCode)
	}
	if deleteRetentionCall == nil {
		t.Error("expected metadata DeleteRetentionPolicy to be called")
	}
}

// TestRetentionDelete_noPolicy_returns404.
func TestRetentionDelete_noPolicy_returns404(t *testing.T) {
	resetRetentionFakes(t)
	deleteRetentionErr = status.Error(codes.NotFound, "not found")
	env := newTestEnv(t)
	resp := env.del(t, retentionPath, adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestRetentionDelete_writer_returns403.
func TestRetentionDelete_writer_returns403(t *testing.T) {
	resetRetentionFakes(t)
	env := newTestEnv(t)
	resp := env.del(t, retentionPath, writerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// Static check ensures the metadatav1 alias stays referenced even if a
// future refactor pulls the explicit reference out of the test bodies.
var _ = (*metadatav1.RetentionPolicy)(nil)
