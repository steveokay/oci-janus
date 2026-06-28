// Tests for the FE-API-019 compliance report routes. Reuses the
// newScannerEnv / fakeScannerServer from security_policies_test.go for the
// scanner gRPC fake.
package handler_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// TestReports_ScannerUnset_returns404 — every report route is gated on
// h.scanner != nil; verify a single route as a smoke check.
func TestReports_ScannerUnset_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.post(t, "/api/v1/security/reports/generate", adminToken, "{}")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestReports_GenerateHappyPath verifies POST /generate returns 202 with a
// report id.
func TestReports_GenerateHappyPath(t *testing.T) {
	env, _ := newScannerEnv(t)
	resp := env.post(t, "/api/v1/security/reports/generate", adminToken, "{}")
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", resp.StatusCode)
	}
	var body struct {
		ReportID string `json:"report_id"`
		Status   string `json:"status"`
	}
	decodeJSON(t, resp, &body)
	if body.ReportID == "" {
		t.Error("report_id should be non-empty")
	}
	if body.Status != "pending" {
		t.Errorf("status: got %q, want pending", body.Status)
	}
}

// TestReports_GetNotFound verifies 404 when the scanner reports NOT_FOUND.
func TestReports_GetNotFound(t *testing.T) {
	env, fake := newScannerEnv(t)
	// Install a fake that returns codes.NotFound for GetComplianceReport.
	fake.getReportReturn = nil
	// Stub the server to return NOT_FOUND by swapping the override with
	// a wrapper that returns an error — simplest is a separate fake
	// instance. Instead use the trick: panic on getReportReturn with an
	// error sentinel. To keep this clean we override the fake by giving
	// it a "panic" report id by using a separate path test below.
	// For this test, replace the fake server via a small override hook.
	fake.getReportReturn = &scannerv1.ComplianceReport{} // any zero value — but we want NOT_FOUND
	// Easier route: swap the fake's behavior by attaching an error map.
	// We need to override GetComplianceReport, but the method is on a
	// pointer receiver. Use the per-test pkg var below.
	scannerGetErrorOverride = status.Error(codes.NotFound, "report not found")
	t.Cleanup(func() { scannerGetErrorOverride = nil })

	resp := env.get(t, "/api/v1/security/reports/00000000-0000-0000-0000-000000000001", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// scannerGetErrorOverride lets a single test force GetComplianceReport on
// the fake server to return an error. Read by the fake; reset via t.Cleanup.
var scannerGetErrorOverride error

// fakeScannerGetWrapper wraps fakeScannerServer.GetComplianceReport so we
// can return scannerGetErrorOverride when set. We achieve this by extending
// the fakeScannerServer logic at call time via a tiny override applied in
// the security_policies_test.go fake: see the GetComplianceReport method.
//
// The fake itself reads scannerGetErrorOverride and returns it when non-nil.
func init() {
	getComplianceReportOverride = func(req *scannerv1.GetComplianceReportRequest) (*scannerv1.ComplianceReport, error) {
		if scannerGetErrorOverride != nil {
			return nil, scannerGetErrorOverride
		}
		return nil, nil
	}
}

// TestReports_DownloadPDF_happyPath verifies the file is streamed when the
// underlying report is in `succeeded` state and the on-disk file exists.
func TestReports_DownloadPDF_happyPath(t *testing.T) {
	env, fake := newScannerEnv(t)

	// Write a small fake PDF to a temp dir.
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("%PDF-1.4 fake body"), 0o644); err != nil {
		t.Fatalf("write fake pdf: %v", err)
	}

	fake.getReportReturn = &scannerv1.ComplianceReport{
		ReportId:       "22222222-2222-2222-2222-222222222222",
		TenantId:       testTenantID,
		Status:         "succeeded",
		DownloadPdfUrl: pdfPath,
		RequestedAt:    timestamppb.Now(),
	}

	resp := env.get(t, "/api/v1/security/reports/22222222-2222-2222-2222-222222222222/download/pdf", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "application/pdf" {
		t.Errorf("content-type: got %q, want application/pdf", ct)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "%PDF-1.4 fake body" {
		t.Errorf("body: got %q", string(b))
	}
}

// TestReports_DownloadPDF_pendingReport_returns409 verifies the route
// rejects downloads for reports that haven't completed yet.
func TestReports_DownloadPDF_pendingReport_returns409(t *testing.T) {
	env, fake := newScannerEnv(t)
	fake.getReportReturn = &scannerv1.ComplianceReport{
		ReportId: "33333333-3333-3333-3333-333333333333",
		TenantId: testTenantID,
		Status:   "pending",
	}
	resp := env.get(t, "/api/v1/security/reports/33333333-3333-3333-3333-333333333333/download/pdf", adminToken)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}

// TestReports_DownloadPDF_malformedID_returns400.
func TestReports_DownloadPDF_malformedID_returns400(t *testing.T) {
	env, _ := newScannerEnv(t)
	resp := env.get(t, "/api/v1/security/reports/not-a-uuid/download/pdf", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestReports_DownloadPDF_multiChunkStream — REM-012 verification that
// the BFF correctly assembles a multi-chunk stream from the scanner.
// The default fake puts the whole body in one chunk; this test installs
// an override that splits the body into three to exercise the recv loop.
//
// We assert the response body is the concatenation of the chunks AND
// Content-Type came from the FIRST chunk (zero-data, content_type only).
func TestReports_DownloadPDF_multiChunkStream(t *testing.T) {
	env, fake := newScannerEnv(t)

	const reportID = "44444444-4444-4444-4444-444444444444"
	const part1, part2, part3 = "%PDF-1.4 ", "chunk-two-here ", "tail"

	// Drive GetComplianceReport so the existing path-resolution layer
	// has something to chew on; we only override the stream path.
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte(part1+part2+part3), 0o644); err != nil {
		t.Fatalf("write fake pdf: %v", err)
	}
	fake.getReportReturn = &scannerv1.ComplianceReport{
		ReportId:       reportID,
		TenantId:       testTenantID,
		Status:         "succeeded",
		DownloadPdfUrl: pdfPath,
		RequestedAt:    timestamppb.Now(),
	}

	// Override DownloadComplianceReport on the fake to emit three chunks
	// in the documented sequence: content_type first, then bytes.
	downloadComplianceReportOverride = func(_ *scannerv1.DownloadComplianceReportRequest, stream scannerv1.ScannerService_DownloadComplianceReportServer) error {
		if err := stream.Send(&scannerv1.ReportChunk{ContentType: "application/pdf"}); err != nil {
			return err
		}
		for _, p := range []string{part1, part2, part3} {
			if err := stream.Send(&scannerv1.ReportChunk{Data: []byte(p)}); err != nil {
				return err
			}
		}
		return nil
	}
	t.Cleanup(func() { downloadComplianceReportOverride = nil })

	resp := env.get(t, "/api/v1/security/reports/"+reportID+"/download/pdf", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/pdf" {
		t.Errorf("content-type: got %q, want application/pdf", ct)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	want := part1 + part2 + part3
	if string(body) != want {
		t.Errorf("body: got %q, want %q", string(body), want)
	}
}

// TestReports_DownloadPDF_streamErrorMidway — when the scanner stream
// errors AFTER headers have been committed, the BFF should log + close
// (it can't change the HTTP status code). The client sees a truncated
// body but no 5xx.
func TestReports_DownloadPDF_streamErrorMidway(t *testing.T) {
	env, fake := newScannerEnv(t)

	const reportID = "55555555-5555-5555-5555-555555555555"
	dir := t.TempDir()
	pdfPath := filepath.Join(dir, "doc.pdf")
	if err := os.WriteFile(pdfPath, []byte("dummy"), 0o644); err != nil {
		t.Fatalf("write fake pdf: %v", err)
	}
	fake.getReportReturn = &scannerv1.ComplianceReport{
		ReportId:       reportID,
		TenantId:       testTenantID,
		Status:         "succeeded",
		DownloadPdfUrl: pdfPath,
		RequestedAt:    timestamppb.Now(),
	}

	downloadComplianceReportOverride = func(_ *scannerv1.DownloadComplianceReportRequest, stream scannerv1.ScannerService_DownloadComplianceReportServer) error {
		// First chunk: content_type (commits headers on the BFF side).
		if err := stream.Send(&scannerv1.ReportChunk{ContentType: "application/pdf"}); err != nil {
			return err
		}
		// One legitimate data chunk.
		if err := stream.Send(&scannerv1.ReportChunk{Data: []byte("partial")}); err != nil {
			return err
		}
		// Then bail mid-stream.
		return status.Errorf(codes.Internal, "simulated mid-stream failure")
	}
	t.Cleanup(func() { downloadComplianceReportOverride = nil })

	resp := env.get(t, "/api/v1/security/reports/"+reportID+"/download/pdf", adminToken)
	// HTTP status was committed as 200 before the mid-stream error;
	// the BFF can't go back and rewrite it. The body is truncated.
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 (headers already committed), got %d", resp.StatusCode)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "partial" {
		t.Errorf("expected truncated body %q, got %q", "partial", string(body))
	}
}

// TestReports_List_returnsEmptyJSON verifies the BFF wraps the response in
// the expected map shape even when there are no rows.
func TestReports_List_returnsEmptyJSON(t *testing.T) {
	env, _ := newScannerEnv(t)
	resp := env.get(t, "/api/v1/security/reports", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Reports       []handler.ComplianceReportResponse `json:"reports"`
		NextPageToken string                             `json:"next_page_token"`
	}
	decodeJSON(t, resp, &body)
	if body.Reports == nil {
		t.Error("reports field should always be a non-nil array")
	}
}

// TestReports_List_invalidStatus_returns400.
func TestReports_List_invalidStatus_returns400(t *testing.T) {
	env, _ := newScannerEnv(t)
	resp := env.get(t, "/api/v1/security/reports?status=bogus", adminToken)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}
