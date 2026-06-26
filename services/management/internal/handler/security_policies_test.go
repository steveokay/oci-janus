// Tests for the FE-API-018 scan policy routes. We wire an in-process
// bufconn ScannerService fake into the existing testEnv via
// handler.WithScannerClient and drive the routes through the real
// HTTP→gRPC→proto path.
package handler_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	scannerv1 "github.com/steveokay/oci-janus/proto/gen/go/scanner/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// fakeScannerServer captures the last UpdateScanPolicy request so tests can
// assert the BFF forwarded the right fields. Defaults cover the FE-API-018
// happy path; individual tests override fields via the package vars below.
type fakeScannerServer struct {
	scannerv1.UnimplementedScannerServiceServer

	policyOverride *scannerv1.ScanPolicy
	updateCall     *scannerv1.UpdateScanPolicyRequest

	// Compliance report fakes are exercised by security_reports_test.go;
	// share the same fake server so wiring stays in one place.
	getReportReturn  *scannerv1.ComplianceReport
	generateReportID string
	listReports      []*scannerv1.ComplianceReport
}

func (s *fakeScannerServer) GetScanPolicy(_ context.Context, req *scannerv1.GetScanPolicyRequest) (*scannerv1.ScanPolicy, error) {
	if s.policyOverride != nil {
		return s.policyOverride, nil
	}
	return &scannerv1.ScanPolicy{
		TenantId:          req.GetTenantId(),
		AutoScanOnPush:    true,
		BlockOnSeverity:   "",
		ExemptCves:        []string{},
		ScannerPlugin:     "trivy",
		ScannerVersionPin: "",
		UpdatedAt:         timestamppb.Now(),
	}, nil
}

func (s *fakeScannerServer) UpdateScanPolicy(_ context.Context, req *scannerv1.UpdateScanPolicyRequest) (*scannerv1.ScanPolicy, error) {
	s.updateCall = req
	return &scannerv1.ScanPolicy{
		TenantId:          req.GetTenantId(),
		AutoScanOnPush:    req.GetAutoScanOnPush(),
		BlockOnSeverity:   req.GetBlockOnSeverity(),
		ExemptCves:        req.GetExemptCves(),
		ScannerPlugin:     req.GetScannerPlugin(),
		ScannerVersionPin: req.GetScannerVersionPin(),
		UpdatedAt:         timestamppb.Now(),
		UpdatedBy:         req.GetUpdatedBy(),
	}, nil
}

func (s *fakeScannerServer) GenerateComplianceReport(_ context.Context, _ *scannerv1.GenerateComplianceReportRequest) (*scannerv1.GenerateComplianceReportResponse, error) {
	id := s.generateReportID
	if id == "" {
		id = "11111111-1111-1111-1111-111111111111"
	}
	return &scannerv1.GenerateComplianceReportResponse{ReportId: id, Status: "pending"}, nil
}

// getComplianceReportOverride is an optional hook installed by individual
// tests (see security_reports_test.go) to inject a NOT_FOUND error or
// other gRPC status without spinning up a new fake server. When set and
// the function returns a non-nil error, the fake propagates it.
var getComplianceReportOverride func(req *scannerv1.GetComplianceReportRequest) (*scannerv1.ComplianceReport, error)

func (s *fakeScannerServer) GetComplianceReport(_ context.Context, req *scannerv1.GetComplianceReportRequest) (*scannerv1.ComplianceReport, error) {
	if getComplianceReportOverride != nil {
		if rec, err := getComplianceReportOverride(req); err != nil || rec != nil {
			return rec, err
		}
	}
	if s.getReportReturn != nil {
		return s.getReportReturn, nil
	}
	return &scannerv1.ComplianceReport{
		ReportId:    req.GetReportId(),
		TenantId:    req.GetTenantId(),
		Status:      "pending",
		RequestedAt: timestamppb.Now(),
	}, nil
}

func (s *fakeScannerServer) ListComplianceReports(_ context.Context, _ *scannerv1.ListComplianceReportsRequest) (*scannerv1.ListComplianceReportsResponse, error) {
	return &scannerv1.ListComplianceReportsResponse{Reports: s.listReports}, nil
}

// downloadComplianceReportOverride lets a test fully control the stream
// (multi-chunk fanout, mid-stream errors) without spinning up another
// fake. When set, it short-circuits the file-read path below.
var downloadComplianceReportOverride func(*scannerv1.DownloadComplianceReportRequest, scannerv1.ScannerService_DownloadComplianceReportServer) error

// DownloadComplianceReport (REM-012) — streaming fake. Replicates the
// real scanner's behaviour:
//   - Resolve the report row the same way GetComplianceReport does so the
//     existing getReportReturn / getComplianceReportOverride fixtures
//     drive both surfaces uniformly.
//   - 404 / 409 / 400 mapping mirrors the production handler.
//   - On success, read the file at the matching path field and stream it
//     in two chunks: first chunk carries content_type only (matches the
//     production "headers BEFORE bytes" contract), second chunk carries
//     the full body. Multi-chunk fanout isn't needed for the basic
//     fixtures; tests exercising the chunk loop use the override hook
//     above.
func (s *fakeScannerServer) DownloadComplianceReport(
	req *scannerv1.DownloadComplianceReportRequest,
	stream scannerv1.ScannerService_DownloadComplianceReportServer,
) error {
	if downloadComplianceReportOverride != nil {
		return downloadComplianceReportOverride(req, stream)
	}
	// Resolve report via the same path as GetComplianceReport so tests
	// installing scannerGetErrorOverride / getReportReturn cover this
	// surface too. The override returning (nil, err) lets a test force
	// NotFound; (rec, nil) returns an explicit row.
	var rec *scannerv1.ComplianceReport
	if getComplianceReportOverride != nil {
		r, err := getComplianceReportOverride(&scannerv1.GetComplianceReportRequest{
			TenantId: req.GetTenantId(),
			ReportId: req.GetReportId(),
		})
		if err != nil {
			return err
		}
		rec = r
	}
	if rec == nil && s.getReportReturn != nil {
		rec = s.getReportReturn
	}
	if rec == nil {
		// Default fake — pending. Production returns FailedPrecondition.
		return status.Errorf(codes.FailedPrecondition, "report not ready (status=pending)")
	}
	if rec.GetStatus() != "succeeded" {
		return status.Errorf(codes.FailedPrecondition,
			"report not ready (status=%s)", rec.GetStatus())
	}

	var path, contentType string
	switch req.GetFormat() {
	case "pdf":
		path = rec.GetDownloadPdfUrl()
		contentType = "application/pdf"
	case "sbom":
		path = rec.GetDownloadSbomUrl()
		contentType = "application/json"
	default:
		return status.Errorf(codes.InvalidArgument, "format must be one of pdf|sbom")
	}
	if path == "" {
		return status.Errorf(codes.Internal, "report artifact missing for format %q", req.GetFormat())
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return status.Errorf(codes.Internal, "open report artifact: %v", err)
	}

	// First chunk: content_type with empty body — matches the production
	// "commit headers before bytes" sequence.
	if err := stream.Send(&scannerv1.ReportChunk{ContentType: contentType}); err != nil {
		return err
	}
	// Second chunk: the body in one shot. Tests don't care about
	// streaming granularity; the BFF's Recv loop handles either.
	if len(body) > 0 {
		if err := stream.Send(&scannerv1.ReportChunk{Data: body}); err != nil {
			return err
		}
	}
	return nil
}

// newScannerEnv wires the same set of fakes as newTestEnv but additionally
// attaches a ScannerService client. The returned fake is mutable so a single
// test can override response data without spinning up a new env.
func newScannerEnv(t *testing.T) (*testEnv, *fakeScannerServer) {
	t.Helper()

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &fakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &fakeMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	fakeScanner := &fakeScannerServer{}
	scannerLis := bufconn.Listen(bufSize)
	scannerGRPC := grpc.NewServer()
	scannerv1.RegisterScannerServiceServer(scannerGRPC, fakeScanner)
	healthpb.RegisterHealthServer(scannerGRPC, &fakeHealthServer{})
	go func() { _ = scannerGRPC.Serve(scannerLis) }()
	t.Cleanup(scannerGRPC.Stop)

	dialBufconn := func(lis *bufconn.Listener) *grpc.ClientConn {
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

	authConn := dialBufconn(authLis)
	metaConn := dialBufconn(metaLis)
	auditConn := dialBufconn(auditLis)
	scannerConn := dialBufconn(scannerLis)

	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil,
		"",
		healthpb.NewHealthClient(authConn),
	).WithScannerClient(scannerv1.NewScannerServiceClient(scannerConn))

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}, fakeScanner
}

// TestScanPolicy_ScannerUnset_returns404 verifies the route stays disabled
// when management isn't wired to registry-scanner.
func TestScanPolicy_ScannerUnset_returns404(t *testing.T) {
	env := newTestEnv(t)
	resp := env.get(t, "/api/v1/security/policies", adminToken)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestScanPolicy_GetHappyPath verifies the default policy flows through
// untouched.
func TestScanPolicy_GetHappyPath(t *testing.T) {
	env, _ := newScannerEnv(t)
	resp := env.get(t, "/api/v1/security/policies", adminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body handler.ScanPolicyResponse
	decodeJSON(t, resp, &body)
	if body.ScannerPlugin != "trivy" {
		t.Errorf("scanner_plugin: got %q, want trivy", body.ScannerPlugin)
	}
	if !body.AutoScanOnPush {
		t.Error("auto_scan_on_push: got false, want true")
	}
	if body.ExemptCVEs == nil {
		t.Error("exempt_cves should be non-nil even when empty")
	}
}

// TestScanPolicy_UpdateForbidden_forReader verifies the PUT route enforces
// the requireScanPolicyAdmin gate.
func TestScanPolicy_UpdateForbidden_forReader(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"","exempt_cves":[],"scanner_plugin":"trivy","scanner_version_pin":""}`
	resp := env.putBody(t, "/api/v1/security/policies", readerToken, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestScanPolicy_UpdateInvalidSeverity_returns400.
// Uses platformAdminToken (Phase 5.2): validation fires after the auth gate,
// so the caller must pass requireScanPolicyAdmin first.
func TestScanPolicy_UpdateInvalidSeverity_returns400(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"EXTREME","exempt_cves":[],"scanner_plugin":"trivy","scanner_version_pin":""}`
	resp := env.putBody(t, "/api/v1/security/policies", platformAdminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestScanPolicy_UpdateInvalidCVE_returns400 verifies the CVE entry shape
// gate fires before the gRPC call.
// Uses platformAdminToken (Phase 5.2): validation fires after the auth gate.
func TestScanPolicy_UpdateInvalidCVE_returns400(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"","exempt_cves":["NOT-A-CVE"],"scanner_plugin":"trivy","scanner_version_pin":""}`
	resp := env.putBody(t, "/api/v1/security/policies", platformAdminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestScanPolicy_UpdateInvalidScannerPlugin_returns400.
// Uses platformAdminToken (Phase 5.2): validation fires after the auth gate.
func TestScanPolicy_UpdateInvalidScannerPlugin_returns400(t *testing.T) {
	env, _ := newScannerEnv(t)
	body := `{"auto_scan_on_push":true,"block_on_severity":"","exempt_cves":[],"scanner_plugin":"snyk","scanner_version_pin":""}`
	resp := env.putBody(t, "/api/v1/security/policies", platformAdminToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestScanPolicy_UpdateHappyPath_forwardsFields verifies a valid PUT
// reaches the scanner with the request body's values preserved.
// Uses platformAdminToken (Phase 5.2): tenant-wide policy requires
// platform-admin or tenant-admin, not merely any org-scoped admin.
func TestScanPolicy_UpdateHappyPath_forwardsFields(t *testing.T) {
	env, fake := newScannerEnv(t)
	body := `{"auto_scan_on_push":false,"block_on_severity":"HIGH","exempt_cves":["CVE-2024-1234","CVE-2025-99999"],"scanner_plugin":"grype","scanner_version_pin":"v0.74.0"}`
	resp := env.putBody(t, "/api/v1/security/policies", platformAdminToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if fake.updateCall == nil {
		t.Fatal("expected updateCall to be captured")
	}
	if fake.updateCall.GetBlockOnSeverity() != "HIGH" {
		t.Errorf("block_on_severity: got %q, want HIGH", fake.updateCall.GetBlockOnSeverity())
	}
	if fake.updateCall.GetScannerPlugin() != "grype" {
		t.Errorf("scanner_plugin: got %q, want grype", fake.updateCall.GetScannerPlugin())
	}
	if len(fake.updateCall.GetExemptCves()) != 2 {
		t.Errorf("exempt_cves len: got %d, want 2", len(fake.updateCall.GetExemptCves()))
	}
}

// putBody is a tiny helper for PUT requests with a JSON body — mirrors
// testEnv.post but for PUT, kept inline to avoid bloating handler_test.go.
func (e *testEnv) putBody(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT %s: %v", path, err)
	}
	return resp
}
