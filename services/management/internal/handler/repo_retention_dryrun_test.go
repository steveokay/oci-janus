// Tests for FE-API-038 dry-run + preview routes. Same harness pattern as
// repo_retention_test.go — bufconn'd metadata fake, exercise the full
// HTTP → middleware → gRPC path so role gating, JSON shape, and gRPC
// error mapping are validated together.
package handler_test

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

const (
	dryRunPath  = "/api/v1/repositories/myorg/myrepo/policies/retention/dry-run"
	previewPath = "/api/v1/repositories/myorg/myrepo/policies/retention/preview"
)

// resetDryRunFakes wipes the package-level fake state so each test runs in
// isolation. Mirrors resetRetentionFakes; called via t.Cleanup.
func resetDryRunFakes(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		evalRetentionCall = nil
		evalRetentionResp = nil
		evalRetentionErr = nil
		// Also reset the GET fakes — the preview route reads through
		// GetRepoRetentionPolicy before calling EvaluateRetention.
		getRetentionOverride = nil
		getRetentionErr = nil
	})
}

// validDryRunBody is the happy-path POST body — tests tweak this as needed.
const validDryRunBody = `{
    "enabled": true,
    "rules": [{"kind": "max_age_days", "value": 90}, {"kind": "max_count", "value": 50}],
    "protected_tag_patterns": ["latest", "stable"]
}`

// ── POST dry-run ───────────────────────────────────────────────────────────

// TestRetentionDryRun_admin_returns200WithPayload verifies the happy path:
// admin → 200 → canned response includes would_delete + protected_skipped.
func TestRetentionDryRun_admin_returns200WithPayload(t *testing.T) {
	resetDryRunFakes(t)
	env := newTestEnv(t)
	resp := env.post(t, dryRunPath, adminToken, validDryRunBody)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if evalRetentionCall == nil {
		t.Fatal("expected metadata EvaluateRetention to be called")
	}
	if !evalRetentionCall.GetCandidate().GetEnabled() ||
		len(evalRetentionCall.GetCandidate().GetRules()) != 2 {
		t.Errorf("BFF did not forward candidate fields: %+v", evalRetentionCall.GetCandidate())
	}

	var body handler.DryRunResponse
	decodeJSON(t, resp, &body)
	if len(body.WouldDelete) == 0 {
		t.Error("expected at least one would_delete entry from canned response")
	}
	if body.TotalCount != 47 {
		t.Errorf("expected canned total_count=47, got %d", body.TotalCount)
	}
}

// TestRetentionDryRun_owner_returns200 verifies an owner clears the admin
// gate (owner > admin in the hierarchy).
func TestRetentionDryRun_owner_returns200(t *testing.T) {
	resetDryRunFakes(t)
	env := newTestEnv(t)
	resp := env.post(t, dryRunPath, ownerToken, validDryRunBody)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for owner, got %d", resp.StatusCode)
	}
}

// TestRetentionDryRun_writer_returns403 verifies writer is NOT enough.
// Dry-run leaks selection state that would otherwise be admin-only —
// keeping the gate aligned with PUT is the load-bearing security property.
func TestRetentionDryRun_writer_returns403(t *testing.T) {
	resetDryRunFakes(t)
	env := newTestEnv(t)
	resp := env.post(t, dryRunPath, writerToken, validDryRunBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestRetentionDryRun_reader_returns403.
func TestRetentionDryRun_reader_returns403(t *testing.T) {
	resetDryRunFakes(t)
	env := newTestEnv(t)
	resp := env.post(t, dryRunPath, readerToken, validDryRunBody)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestRetentionDryRun_malformedJSON_returns400.
func TestRetentionDryRun_malformedJSON_returns400(t *testing.T) {
	resetDryRunFakes(t)
	env := newTestEnv(t)
	resp := env.post(t, dryRunPath, adminToken, "not json {")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestRetentionDryRun_invalidCandidate_returns400 verifies that
// InvalidArgument from the metadata gRPC layer surfaces as HTTP 400 —
// the metadata handler validates rule kinds, regex compilability, etc.
func TestRetentionDryRun_invalidCandidate_returns400(t *testing.T) {
	resetDryRunFakes(t)
	evalRetentionErr = status.Error(codes.InvalidArgument, "unknown retention rule kind")
	env := newTestEnv(t)
	body := `{
        "enabled": true,
        "rules": [{"kind": "shred_with_lasers", "value": 1}],
        "protected_tag_patterns": []
    }`
	resp := env.post(t, dryRunPath, adminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestRetentionDryRun_repoNotFound_returns404 verifies the NotFound branch
// (repo deleted between findRepo and EvaluateRetention).
func TestRetentionDryRun_repoNotFound_returns404(t *testing.T) {
	resetDryRunFakes(t)
	evalRetentionErr = status.Error(codes.NotFound, "repository not found")
	env := newTestEnv(t)
	resp := env.post(t, dryRunPath, adminToken, validDryRunBody)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// ── GET preview ────────────────────────────────────────────────────────────

// TestRetentionPreview_admin_returns200WithState verifies the happy path:
// the saved policy is loaded via the canned GetRepoRetentionPolicy fake,
// then fed back into EvaluateRetention for live totals. in_preview_window
// is true when preview_until is in the future (canned policy fixture sets
// it to NOW+24h).
func TestRetentionPreview_admin_returns200WithState(t *testing.T) {
	resetDryRunFakes(t)
	env := newTestEnv(t)
	resp := env.get(t, previewPath, adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.PreviewStateResponse
	decodeJSON(t, resp, &body)
	if !body.Enabled {
		t.Error("expected enabled=true from canned policy")
	}
	if !body.InPreviewWindow {
		t.Error("expected in_preview_window=true (canned policy has preview_until in future)")
	}
	if body.WouldDeleteCount == 0 {
		t.Error("expected non-zero would_delete_count from canned evaluator")
	}
	if body.PreviewUntil == "" {
		t.Error("expected preview_until on response")
	}
}

// TestRetentionPreview_reader_returns200 verifies reader is sufficient —
// preview is informational, same disclosure surface as the tags list.
func TestRetentionPreview_reader_returns200(t *testing.T) {
	resetDryRunFakes(t)
	env := newTestEnv(t)
	resp := env.get(t, previewPath, readerToken)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200 for reader, got %d", resp.StatusCode)
	}
}

// TestRetentionPreview_noPolicy_returns404WithCode verifies the typed-404
// branch when GetRepoRetentionPolicy returns NotFound — same shape as the
// FE-API-037 GET so the dashboard's banner state machine has one branch.
func TestRetentionPreview_noPolicy_returns404WithCode(t *testing.T) {
	resetDryRunFakes(t)
	getRetentionErr = status.Error(codes.NotFound, "not found")
	env := newTestEnv(t)
	resp := env.get(t, previewPath, adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
	var body map[string]string
	decodeJSON(t, resp, &body)
	if body["code"] != "no-policy" {
		t.Errorf("expected code=no-policy, got %q", body["code"])
	}
}
