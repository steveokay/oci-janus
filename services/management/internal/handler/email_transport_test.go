// Tests for FUT-019 Phase 3 email transport BFF routes (email_transport.go).
//
// Mirrors the newGCEnv bufconn pattern: stand up fakes for the
// management handler's audit + auth + metadata gRPC clients, drive HTTP
// requests through the real mux, and assert the response.
//
// The fakes here (fakeEmailAuditServer / emailFakeAuthServer) are kept
// private to this file and driven by package-level override vars so a
// single test can simulate an edge condition (FailedPrecondition, no
// resolvable email) without rebuilding the bufconn stack.
//
// Coverage:
//   - GET as non-admin (reader)        → 403
//   - GET as service-account principal → 403 (kind deny)
//   - GET as admin                     → 200 masked config (no secrets)
//   - PUT as admin                     → 200 + fake records the request
//   - test-send resolves caller email  → 200 {ok:true}, addressed to self
//   - test-send with no email          → 400
//   - audit FailedPrecondition on GET  → 409
//   - delivery-log forces the JWT user id (a client-supplied id is ignored)
package handler_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

const (
	// emailAdminToken is recognised by emailFakeAuthServer and maps to a
	// user with IsGlobalAdmin=true so requireEmailAdmin lets it through.
	emailAdminToken = "email-admin-token"
	emailAdminUser  = "00000000-0000-0000-0000-0000000000e1"
	// emailAdminEmail is the address emailFakeAuthServer resolves for the
	// admin user (used by the test-send happy path).
	emailAdminEmail = "admin@example.com"
)

// ---------------------------------------------------------------------------
// Fake audit server — email RPCs. Driven by the package-level vars below.
// ---------------------------------------------------------------------------

type fakeEmailAuditServer struct {
	auditv1.UnimplementedAuditServiceServer
}

var (
	// GET config.
	emailGetConfigReturn *auditv1.EmailTransportConfig
	emailGetConfigErr    error

	// PUT config.
	emailPutReturn  *auditv1.EmailTransportConfig
	emailPutErr     error
	emailLastPutReq *auditv1.PutEmailTransportConfigRequest

	// test-send.
	emailTestReturn  *auditv1.SendTestEmailResponse
	emailTestErr     error
	emailLastTestReq *auditv1.SendTestEmailRequest

	// delivery log.
	emailListReturn  *auditv1.ListEmailDeliveriesResponse
	emailListErr     error
	emailLastListReq *auditv1.ListEmailDeliveriesRequest

	// emailFakeMu guards the recorded-request pointers so the -race
	// detector stays quiet across the bufconn goroutine boundary.
	emailFakeMu sync.Mutex
)

func (s *fakeEmailAuditServer) GetEmailTransportConfig(_ context.Context, _ *auditv1.GetEmailTransportConfigRequest) (*auditv1.EmailTransportConfig, error) {
	if emailGetConfigErr != nil {
		return nil, emailGetConfigErr
	}
	if emailGetConfigReturn != nil {
		return emailGetConfigReturn, nil
	}
	return &auditv1.EmailTransportConfig{}, nil
}

func (s *fakeEmailAuditServer) PutEmailTransportConfig(_ context.Context, req *auditv1.PutEmailTransportConfigRequest) (*auditv1.EmailTransportConfig, error) {
	emailFakeMu.Lock()
	emailLastPutReq = req
	emailFakeMu.Unlock()
	if emailPutErr != nil {
		return nil, emailPutErr
	}
	if emailPutReturn != nil {
		return emailPutReturn, nil
	}
	// Echo the request back as a masked config (has_* set from whether a
	// secret was supplied) so the round-trip test has fields to inspect.
	return &auditv1.EmailTransportConfig{
		Provider:        req.GetProvider(),
		Enabled:         req.GetEnabled(),
		FromAddress:     req.GetFromAddress(),
		FromName:        req.GetFromName(),
		SmtpHost:        req.GetSmtpHost(),
		SmtpPort:        req.GetSmtpPort(),
		SmtpUsername:    req.GetSmtpUsername(),
		SmtpTlsMode:     req.GetSmtpTlsMode(),
		HasResendKey:    req.GetResendApiKey() != "",
		HasSmtpPassword: req.GetSmtpPassword() != "",
	}, nil
}

func (s *fakeEmailAuditServer) SendTestEmail(_ context.Context, req *auditv1.SendTestEmailRequest) (*auditv1.SendTestEmailResponse, error) {
	emailFakeMu.Lock()
	emailLastTestReq = req
	emailFakeMu.Unlock()
	if emailTestErr != nil {
		return nil, emailTestErr
	}
	if emailTestReturn != nil {
		return emailTestReturn, nil
	}
	return &auditv1.SendTestEmailResponse{Ok: true}, nil
}

func (s *fakeEmailAuditServer) ListEmailDeliveries(_ context.Context, req *auditv1.ListEmailDeliveriesRequest) (*auditv1.ListEmailDeliveriesResponse, error) {
	emailFakeMu.Lock()
	emailLastListReq = req
	emailFakeMu.Unlock()
	if emailListErr != nil {
		return nil, emailListErr
	}
	if emailListReturn != nil {
		return emailListReturn, nil
	}
	return &auditv1.ListEmailDeliveriesResponse{
		Deliveries: []*auditv1.EmailDelivery{
			{Id: "d1", Category: "scanner_freshness", Subject: "Test", ToAddress: emailAdminEmail, Status: "sent", CreatedAt: timestamppb.Now(), SentAt: timestamppb.Now()},
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Fake auth server — recognises the email test tokens + resolves emails.
// ---------------------------------------------------------------------------

type emailFakeAuthServer struct {
	authv1.UnimplementedAuthServiceServer
}

// emailResolveReturn / emailResolveErr drive ResolveUserEmails. When both
// are nil the fake resolves the admin user to emailAdminEmail and every
// other user to an empty address (no email on file).
var (
	emailResolveReturn *authv1.ResolveUserEmailsResponse
	emailResolveErr    error
)

func (s *emailFakeAuthServer) ValidateToken(_ context.Context, req *authv1.ValidateTokenRequest) (*authv1.ValidateTokenResponse, error) {
	switch req.GetToken() {
	case emailAdminToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: emailAdminUser}, nil
	case readerToken:
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: "reader-user"}, nil
	case saBearerToken:
		// JWT-shaped token — the management middleware tags the request as
		// a service-account principal from the principal_kind claim.
		return &authv1.ValidateTokenResponse{Valid: true, TenantId: testTenantID, UserId: saAdminUserID}, nil
	default:
		return &authv1.ValidateTokenResponse{Valid: false}, nil
	}
}

func (s *emailFakeAuthServer) GetUserPermissions(_ context.Context, req *authv1.GetUserPermissionsRequest) (*authv1.GetUserPermissionsResponse, error) {
	switch req.GetUserId() {
	case emailAdminUser:
		return &authv1.GetUserPermissionsResponse{IsGlobalAdmin: true, Roles: []string{"admin"}}, nil
	case saAdminUserID:
		// SA shadow user inherits global-admin so the ONLY thing keeping
		// the gate closed is the principal-kind deny in requireEmailAdmin.
		return &authv1.GetUserPermissionsResponse{IsGlobalAdmin: true, Roles: []string{"admin"}}, nil
	default:
		return &authv1.GetUserPermissionsResponse{
			Roles: []string{"reader"},
			RoleAssignments: []*authv1.RoleAssignment{
				{Id: "r", UserId: "reader-user", Role: "reader", ScopeType: "org", ScopeValue: "myorg"},
			},
		}, nil
	}
}

func (s *emailFakeAuthServer) ResolveUserEmails(_ context.Context, req *authv1.ResolveUserEmailsRequest) (*authv1.ResolveUserEmailsResponse, error) {
	if emailResolveErr != nil {
		return nil, emailResolveErr
	}
	if emailResolveReturn != nil {
		return emailResolveReturn, nil
	}
	out := &authv1.ResolveUserEmailsResponse{}
	for _, uid := range req.GetUserIds() {
		email := ""
		if uid == emailAdminUser {
			email = emailAdminEmail
		}
		out.Emails = append(out.Emails, &authv1.ResolvedEmail{UserId: uid, Email: email, EmailVerified: true})
	}
	return out, nil
}

// newEmailEnv stands up a bufconn stack wired with the email audit + auth
// fakes and resets all override vars via t.Cleanup so cases stay isolated.
func newEmailEnv(t *testing.T) *testEnv {
	t.Helper()

	// Reset per-test state so ordering never leaks between cases.
	emailGetConfigReturn, emailGetConfigErr = nil, nil
	emailPutReturn, emailPutErr, emailLastPutReq = nil, nil, nil
	emailTestReturn, emailTestErr, emailLastTestReq = nil, nil, nil
	emailListReturn, emailListErr, emailLastListReq = nil, nil, nil
	emailResolveReturn, emailResolveErr = nil, nil
	t.Cleanup(func() {
		emailGetConfigReturn, emailGetConfigErr = nil, nil
		emailPutReturn, emailPutErr, emailLastPutReq = nil, nil, nil
		emailTestReturn, emailTestErr, emailLastTestReq = nil, nil, nil
		emailListReturn, emailListErr, emailLastListReq = nil, nil, nil
		emailResolveReturn, emailResolveErr = nil, nil
	})

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &emailFakeAuthServer{})
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
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeEmailAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	dial := func(lis *bufconn.Listener) *grpc.ClientConn {
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

	h := handler.New(
		authv1.NewAuthServiceClient(dial(authLis)),
		metadatav1.NewMetadataServiceClient(dial(metaLis)),
		auditv1.NewAuditServiceClient(dial(auditLis)),
		nil, // publisher not exercised
		"",
		healthpb.NewHealthClient(dial(authLis)),
	)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}
}

// ─── GET config ───────────────────────────────────────────────────────────

func TestEmailTransportGet_ReaderDenied_returns403(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.get(t, "/api/v1/notifications/email-transport", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestEmailTransportGet_ServiceAccountDenied_returns403(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.get(t, "/api/v1/notifications/email-transport", saBearerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for SA principal, got %d", resp.StatusCode)
	}
}

// TestEmailTransportGet_Admin_returnsMaskedConfig verifies the config is
// served to an admin AND that no secret ever crosses the wire — only the
// has_* markers survive.
func TestEmailTransportGet_Admin_returnsMaskedConfig(t *testing.T) {
	env := newEmailEnv(t)
	emailGetConfigReturn = &auditv1.EmailTransportConfig{
		Provider:        "resend",
		Enabled:         true,
		FromAddress:     "noreply@example.com",
		FromName:        "Registry",
		HasResendKey:    true,
		HasSmtpPassword: false,
	}
	resp := env.get(t, "/api/v1/notifications/email-transport", emailAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// Decode into a loose map so we can assert secret keys are absent.
	var raw map[string]any
	decodeJSON(t, resp, &raw)
	if raw["provider"] != "resend" || raw["from_address"] != "noreply@example.com" {
		t.Errorf("config fields: got %+v", raw)
	}
	if raw["has_resend_key"] != true {
		t.Errorf("has_resend_key: got %v, want true", raw["has_resend_key"])
	}
	// Secrets must never be serialised, under any key spelling.
	for _, k := range []string{"resend_api_key", "smtp_password", "resendApiKey", "smtpPassword"} {
		if _, present := raw[k]; present {
			t.Errorf("secret key %q leaked in GET response", k)
		}
	}
}

// TestEmailTransportGet_AuditFailedPrecondition_returns409 verifies the
// "not configured" state surfaces as a 409, not a 500.
func TestEmailTransportGet_AuditFailedPrecondition_returns409(t *testing.T) {
	env := newEmailEnv(t)
	emailGetConfigErr = status.Error(codes.FailedPrecondition, "email transport not configured")
	resp := env.get(t, "/api/v1/notifications/email-transport", emailAdminToken)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}

// ─── PUT config ───────────────────────────────────────────────────────────

// TestEmailTransportPut_Admin_roundTrips verifies the body maps onto the
// proto request (including the secret fields) and the masked config comes
// back.
func TestEmailTransportPut_Admin_roundTrips(t *testing.T) {
	env := newEmailEnv(t)
	body := `{"provider":"smtp","enabled":true,"from_address":"ops@example.com","from_name":"Ops",` +
		`"smtp_host":"smtp.example.com","smtp_port":587,"smtp_username":"ops","smtp_tls_mode":"starttls",` +
		`"resend_api_key":"","smtp_password":"s3cret"}`
	resp := env.put(t, "/api/v1/notifications/email-transport", emailAdminToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	emailFakeMu.Lock()
	req := emailLastPutReq
	emailFakeMu.Unlock()
	if req == nil {
		t.Fatal("PutEmailTransportConfig was not called")
	}
	if req.GetProvider() != "smtp" || req.GetSmtpHost() != "smtp.example.com" || req.GetSmtpPort() != 587 {
		t.Errorf("forwarded fields: got provider=%q host=%q port=%d", req.GetProvider(), req.GetSmtpHost(), req.GetSmtpPort())
	}
	// Empty resend key = keep existing; non-empty smtp password = replace.
	if req.GetResendApiKey() != "" {
		t.Errorf("resend_api_key should be empty (keep existing), got %q", req.GetResendApiKey())
	}
	if req.GetSmtpPassword() != "s3cret" {
		t.Errorf("smtp_password: got %q, want s3cret", req.GetSmtpPassword())
	}
	// updated_by must be the JWT user, never from the body.
	if req.GetUpdatedBy() != emailAdminUser {
		t.Errorf("updated_by: got %q, want %q", req.GetUpdatedBy(), emailAdminUser)
	}
	// Response is the masked config — smtp password set means has_smtp_password.
	var raw map[string]any
	decodeJSON(t, resp, &raw)
	if raw["has_smtp_password"] != true {
		t.Errorf("has_smtp_password: got %v, want true", raw["has_smtp_password"])
	}
	if _, present := raw["smtp_password"]; present {
		t.Error("smtp_password leaked in PUT response")
	}
}

func TestEmailTransportPut_ReaderDenied_returns403(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.put(t, "/api/v1/notifications/email-transport", readerToken, `{"provider":"resend"}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ─── test-send ────────────────────────────────────────────────────────────

// TestEmailTransportTest_Admin_resolvesCallerEmail verifies the test send
// is addressed to the caller's OWN resolved email and returns {ok:true}.
func TestEmailTransportTest_Admin_resolvesCallerEmail(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.post(t, "/api/v1/notifications/email-transport/test", emailAdminToken, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	decodeJSON(t, resp, &body)
	if !body.OK {
		t.Errorf("expected ok:true, got %+v", body)
	}
	emailFakeMu.Lock()
	req := emailLastTestReq
	emailFakeMu.Unlock()
	if req == nil || req.GetToAddress() != emailAdminEmail {
		got := ""
		if req != nil {
			got = req.GetToAddress()
		}
		t.Errorf("test send recipient: got %q, want caller's own %q", got, emailAdminEmail)
	}
}

// TestEmailTransportTest_NoEmail_returns400 verifies a caller with no
// resolvable address gets a clean 400 (and no send is attempted).
func TestEmailTransportTest_NoEmail_returns400(t *testing.T) {
	env := newEmailEnv(t)
	// Resolve returns an entry with an empty email.
	emailResolveReturn = &authv1.ResolveUserEmailsResponse{
		Emails: []*authv1.ResolvedEmail{{UserId: emailAdminUser, Email: ""}},
	}
	resp := env.post(t, "/api/v1/notifications/email-transport/test", emailAdminToken, "")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	emailFakeMu.Lock()
	called := emailLastTestReq != nil
	emailFakeMu.Unlock()
	if called {
		t.Error("SendTestEmail should not be called when the caller has no email")
	}
}

// ─── delivery log ─────────────────────────────────────────────────────────

// TestEmailDeliveries_Admin_returnsRows verifies the happy path shape.
func TestEmailDeliveries_Admin_returnsRows(t *testing.T) {
	env := newEmailEnv(t)
	resp := env.get(t, "/api/v1/notifications/email-deliveries", emailAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	// The wire struct is unexported, so assert on a loose map instead.
	var raw map[string]any
	decodeJSON(t, resp, &raw)
	rows, ok := raw["deliveries"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 delivery row, got %v", raw["deliveries"])
	}
}

// TestEmailDeliveries_ForcesJWTUserID verifies the user_id sent to the
// audit service is always the JWT subject — a client-supplied user_id in
// the query string must be ignored so a caller can't read another user's
// delivery rows.
func TestEmailDeliveries_ForcesJWTUserID(t *testing.T) {
	env := newEmailEnv(t)
	// Attempt to spoof another user's id via the query string.
	resp := env.get(t, "/api/v1/notifications/email-deliveries?user_id=victim-user-id", emailAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	emailFakeMu.Lock()
	req := emailLastListReq
	emailFakeMu.Unlock()
	if req == nil {
		t.Fatal("ListEmailDeliveries was not called")
	}
	if req.GetUserId() != emailAdminUser {
		t.Errorf("user_id must be forced from the JWT: got %q, want %q", req.GetUserId(), emailAdminUser)
	}
	if req.GetTenantId() != testTenantID {
		t.Errorf("tenant_id: got %q, want %q", req.GetTenantId(), testTenantID)
	}
}
