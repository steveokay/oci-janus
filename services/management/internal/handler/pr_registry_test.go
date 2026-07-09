// Tests for FUT-023 Phase 1 PR-registry BFF routes (pr_registry.go).
//
// Mirrors the newEmailEnv bufconn pattern: stand up fakes for the management
// handler's metadata + auth + audit gRPC clients, drive HTTP requests through
// the real mux, and assert the response.
//
// The metadata fake here (fakePRMetaServer) overrides only the PR-registry
// RPCs; every other metadata RPC stays unimplemented (unused by these tests).
// It is driven by package-level override vars so a single case can simulate an
// edge condition (PermissionDenied, FailedPrecondition, a specific outcome)
// without rebuilding the bufconn stack.
//
// Coverage:
//   - config GET/PUT admin gate: reader → 403, SA bearer → 403, admin → 200
//   - config GET: has_secret masking + webhook_url present/empty
//   - config PUT: happy path (updated_by forced from JWT) + 409 on KEK-unset
//   - namespaces GET: rows + empty state
//   - receiver: ping → 204, bad/blank signature → 401, disabled → 404,
//     provisioned → 200 {outcome, org}
//   - receiver is reachable WITHOUT a JWT; a no-token config GET is 401
package handler_test

import (
	"bytes"
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
	// prPublicBaseURL is the public base URL wired into the handler under
	// test; the config route should render <base>/webhooks/scm/github/pr.
	prPublicBaseURL = "https://registry.example.com"
	prWebhookURL    = prPublicBaseURL + "/webhooks/scm/github/pr"
)

// ---------------------------------------------------------------------------
// Fake metadata server — PR-registry RPCs, driven by the package-level vars.
// ---------------------------------------------------------------------------

type fakePRMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer
}

var (
	// GetPRRegistryConfig.
	prGetConfigReturn *metadatav1.PRRegistryConfig
	prGetConfigErr    error

	// PutPRRegistryConfig.
	prPutReturn  *metadatav1.PRRegistryConfig
	prPutErr     error
	prLastPutReq *metadatav1.PutPRRegistryConfigRequest

	// ListPRNamespaces.
	prListReturn *metadatav1.ListPRNamespacesResponse
	prListErr    error
	prLastListReq *metadatav1.ListPRNamespacesRequest

	// HandlePREvent.
	prEventReturn  *metadatav1.HandlePREventResponse
	prEventErr     error
	prLastEventReq *metadatav1.HandlePREventRequest

	// prFakeMu guards the recorded-request pointers so the -race detector
	// stays quiet across the bufconn goroutine boundary.
	prFakeMu sync.Mutex
)

func (s *fakePRMetaServer) GetPRRegistryConfig(_ context.Context, _ *metadatav1.GetPRRegistryConfigRequest) (*metadatav1.PRRegistryConfig, error) {
	if prGetConfigErr != nil {
		return nil, prGetConfigErr
	}
	if prGetConfigReturn != nil {
		return prGetConfigReturn, nil
	}
	return &metadatav1.PRRegistryConfig{}, nil
}

func (s *fakePRMetaServer) PutPRRegistryConfig(_ context.Context, req *metadatav1.PutPRRegistryConfigRequest) (*metadatav1.PRRegistryConfig, error) {
	prFakeMu.Lock()
	prLastPutReq = req
	prFakeMu.Unlock()
	if prPutErr != nil {
		return nil, prPutErr
	}
	if prPutReturn != nil {
		return prPutReturn, nil
	}
	// Echo the request back as a masked config so the round-trip test has
	// fields to inspect — has_secret is set from whether a secret was supplied.
	return &metadatav1.PRRegistryConfig{
		Enabled:          req.GetEnabled(),
		HasSecret:        req.GetWebhookSecret() != "",
		PromoteTargetOrg: req.GetPromoteTargetOrg(),
	}, nil
}

func (s *fakePRMetaServer) ListPRNamespaces(_ context.Context, req *metadatav1.ListPRNamespacesRequest) (*metadatav1.ListPRNamespacesResponse, error) {
	prFakeMu.Lock()
	prLastListReq = req
	prFakeMu.Unlock()
	if prListErr != nil {
		return nil, prListErr
	}
	if prListReturn != nil {
		return prListReturn, nil
	}
	return &metadatav1.ListPRNamespacesResponse{
		Namespaces: []*metadatav1.PRNamespace{
			{Provider: "github", SourceRepo: "acme/api", PrNumber: 42, OrgName: "pr-42", Status: "active", CreatedAt: timestamppb.Now()},
		},
	}, nil
}

func (s *fakePRMetaServer) HandlePREvent(_ context.Context, req *metadatav1.HandlePREventRequest) (*metadatav1.HandlePREventResponse, error) {
	prFakeMu.Lock()
	prLastEventReq = req
	prFakeMu.Unlock()
	if prEventErr != nil {
		return nil, prEventErr
	}
	if prEventReturn != nil {
		return prEventReturn, nil
	}
	// Default: ping / unhandled → IGNORED.
	return &metadatav1.HandlePREventResponse{Outcome: metadatav1.HandlePREventResponse_OUTCOME_IGNORED}, nil
}

// newPREnv stands up a bufconn stack wired with the PR metadata fake + the
// email auth fake (which already recognises the admin / reader / SA tokens and
// resolves is_global_admin), and resets all override vars via t.Cleanup so
// cases stay isolated.
func newPREnv(t *testing.T) *testEnv {
	t.Helper()

	prGetConfigReturn, prGetConfigErr = nil, nil
	prPutReturn, prPutErr, prLastPutReq = nil, nil, nil
	prListReturn, prListErr, prLastListReq = nil, nil, nil
	prEventReturn, prEventErr, prLastEventReq = nil, nil, nil
	t.Cleanup(func() {
		prGetConfigReturn, prGetConfigErr = nil, nil
		prPutReturn, prPutErr, prLastPutReq = nil, nil, nil
		prListReturn, prListErr, prLastListReq = nil, nil, nil
		prEventReturn, prEventErr, prLastEventReq = nil, nil, nil
	})

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &emailFakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &fakePRMetaServer{})
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
	).WithPublicBaseURL(prPublicBaseURL)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &testEnv{srv: srv}
}

// postWebhook posts a raw body to the receiver with the given GitHub headers
// and NO Authorization header (the receiver is unauthenticated). event or
// signature may be empty to exercise the missing-header paths.
func (e *testEnv) postWebhook(t *testing.T, event, signature, delivery string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/webhooks/scm/github/pr", bytes.NewReader(body))
	if event != "" {
		req.Header.Set("X-GitHub-Event", event)
	}
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	if delivery != "" {
		req.Header.Set("X-GitHub-Delivery", delivery)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST webhook: %v", err)
	}
	return resp
}

// ─── config GET admin gate ──────────────────────────────────────────────────

func TestPRConfigGet_ReaderDenied_returns403(t *testing.T) {
	env := newPREnv(t)
	resp := env.get(t, "/api/v1/pr-registry/config", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

func TestPRConfigGet_ServiceAccountDenied_returns403(t *testing.T) {
	env := newPREnv(t)
	resp := env.get(t, "/api/v1/pr-registry/config", saBearerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403 for SA principal, got %d", resp.StatusCode)
	}
}

func TestPRConfigGet_NoToken_returns401(t *testing.T) {
	env := newPREnv(t)
	resp := env.get(t, "/api/v1/pr-registry/config", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401 for no-token config GET, got %d", resp.StatusCode)
	}
}

// TestPRConfigGet_Admin_returnsConfigWithWebhookURL verifies the config is
// served to an admin, the secret is masked to has_secret, and the derived
// webhook_url is rendered from the configured public base URL.
func TestPRConfigGet_Admin_returnsConfigWithWebhookURL(t *testing.T) {
	env := newPREnv(t)
	prGetConfigReturn = &metadatav1.PRRegistryConfig{
		Enabled:          true,
		HasSecret:        true,
		PromoteTargetOrg: "prod",
	}
	resp := env.get(t, "/api/v1/pr-registry/config", emailAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var raw map[string]any
	decodeJSON(t, resp, &raw)
	if raw["enabled"] != true || raw["has_secret"] != true || raw["promote_target_org"] != "prod" {
		t.Errorf("config fields: got %+v", raw)
	}
	if raw["webhook_url"] != prWebhookURL {
		t.Errorf("webhook_url: got %v, want %q", raw["webhook_url"], prWebhookURL)
	}
	// The raw secret must never be serialised, under any key spelling.
	for _, k := range []string{"webhook_secret", "webhookSecret", "secret"} {
		if _, present := raw[k]; present {
			t.Errorf("secret key %q leaked in GET response", k)
		}
	}
}

// TestPRConfigGet_NoPublicBaseURL_emptyWebhookURL verifies webhook_url renders
// empty (rather than a guessed URL) when PUBLIC_BASE_URL is unset.
func TestPRConfigGet_NoPublicBaseURL_emptyWebhookURL(t *testing.T) {
	// Build an env whose handler has NO public base URL wired.
	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &emailFakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, &fakePRMetaServer{})
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeEmailAuditServer{})
	healthpb.RegisterHealthServer(auditGRPC, &fakeHealthServer{})
	go func() { _ = auditGRPC.Serve(auditLis) }()
	t.Cleanup(auditGRPC.Stop)

	// Reset override vars for isolation.
	prGetConfigReturn, prGetConfigErr = nil, nil
	t.Cleanup(func() { prGetConfigReturn, prGetConfigErr = nil, nil })

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
		nil, "",
		healthpb.NewHealthClient(dial(authLis)),
	) // no WithPublicBaseURL
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	env := &testEnv{srv: srv}

	resp := env.get(t, "/api/v1/pr-registry/config", emailAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var raw map[string]any
	decodeJSON(t, resp, &raw)
	if raw["webhook_url"] != "" {
		t.Errorf("webhook_url should be empty when PUBLIC_BASE_URL unset, got %v", raw["webhook_url"])
	}
}

// ─── config PUT ─────────────────────────────────────────────────────────────

// TestPRConfigPut_Admin_roundTrips verifies the body maps onto the proto
// request (secret + updated_by from the JWT) and the masked config comes back.
func TestPRConfigPut_Admin_roundTrips(t *testing.T) {
	env := newPREnv(t)
	body := `{"enabled":true,"webhook_secret":"s3cret","promote_target_org":"prod"}`
	resp := env.put(t, "/api/v1/pr-registry/config", emailAdminToken, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	prFakeMu.Lock()
	req := prLastPutReq
	prFakeMu.Unlock()
	if req == nil {
		t.Fatal("PutPRRegistryConfig was not called")
	}
	if !req.GetEnabled() || req.GetPromoteTargetOrg() != "prod" {
		t.Errorf("forwarded fields: got enabled=%v org=%q", req.GetEnabled(), req.GetPromoteTargetOrg())
	}
	if req.GetWebhookSecret() != "s3cret" {
		t.Errorf("webhook_secret: got %q, want s3cret", req.GetWebhookSecret())
	}
	// updated_by must be the JWT user, never from the body.
	if req.GetUpdatedBy() != emailAdminUser {
		t.Errorf("updated_by: got %q, want %q", req.GetUpdatedBy(), emailAdminUser)
	}
	var raw map[string]any
	decodeJSON(t, resp, &raw)
	if raw["has_secret"] != true {
		t.Errorf("has_secret: got %v, want true", raw["has_secret"])
	}
	if _, present := raw["webhook_secret"]; present {
		t.Error("webhook_secret leaked in PUT response")
	}
}

func TestPRConfigPut_ReaderDenied_returns403(t *testing.T) {
	env := newPREnv(t)
	resp := env.put(t, "/api/v1/pr-registry/config", readerToken, `{"enabled":true}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// TestPRConfigPut_KEKUnset_returns409 verifies a FailedPrecondition (the KEK is
// unset so the secret can't be sealed) surfaces as a 409, not a 500.
func TestPRConfigPut_KEKUnset_returns409(t *testing.T) {
	env := newPREnv(t)
	prPutErr = status.Error(codes.FailedPrecondition, "notify email KEK not configured")
	resp := env.put(t, "/api/v1/pr-registry/config", emailAdminToken, `{"enabled":true,"webhook_secret":"s3cret"}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
}

// ─── namespaces ─────────────────────────────────────────────────────────────

func TestPRNamespaces_Admin_returnsRows(t *testing.T) {
	env := newPREnv(t)
	resp := env.get(t, "/api/v1/pr-registry/namespaces", emailAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var raw map[string]any
	decodeJSON(t, resp, &raw)
	rows, ok := raw["namespaces"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("expected 1 namespace row, got %v", raw["namespaces"])
	}
	// Default status filter is "active".
	prFakeMu.Lock()
	req := prLastListReq
	prFakeMu.Unlock()
	if req == nil || req.GetStatus() != "active" {
		got := ""
		if req != nil {
			got = req.GetStatus()
		}
		t.Errorf("default status filter: got %q, want active", got)
	}
}

func TestPRNamespaces_EmptyState_returnsEmptyList(t *testing.T) {
	env := newPREnv(t)
	prListReturn = &metadatav1.ListPRNamespacesResponse{}
	resp := env.get(t, "/api/v1/pr-registry/namespaces", emailAdminToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var raw map[string]any
	decodeJSON(t, resp, &raw)
	rows, ok := raw["namespaces"].([]any)
	if !ok || len(rows) != 0 {
		t.Fatalf("expected empty (non-null) namespaces, got %v", raw["namespaces"])
	}
}

func TestPRNamespaces_ReaderDenied_returns403(t *testing.T) {
	env := newPREnv(t)
	resp := env.get(t, "/api/v1/pr-registry/namespaces", readerToken)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expected 403, got %d", resp.StatusCode)
	}
}

// ─── receiver ───────────────────────────────────────────────────────────────

// TestPRWebhook_Ping_returns204 verifies a ping (metadata → IGNORED) is a bland
// 204 and the raw body + signature + event are forwarded verbatim.
func TestPRWebhook_Ping_returns204(t *testing.T) {
	env := newPREnv(t)
	body := []byte(`{"zen":"ping"}`)
	resp := env.postWebhook(t, "ping", "sha256=deadbeef", "delivery-1", body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	prFakeMu.Lock()
	req := prLastEventReq
	prFakeMu.Unlock()
	if req == nil {
		t.Fatal("HandlePREvent was not called")
	}
	if req.GetProvider() != "github" || req.GetEvent() != "ping" || req.GetSignature() != "sha256=deadbeef" {
		t.Errorf("forwarded headers: provider=%q event=%q sig=%q", req.GetProvider(), req.GetEvent(), req.GetSignature())
	}
	if !bytes.Equal(req.GetRawBody(), body) {
		t.Errorf("raw body not forwarded verbatim: got %q", req.GetRawBody())
	}
	// Tenant is resolved downstream: management passes an empty tenant id.
	if req.GetTenantId() != "" {
		t.Errorf("tenant_id should be empty (resolved downstream), got %q", req.GetTenantId())
	}
}

// TestPRWebhook_BadSignature_returns401 verifies a PermissionDenied (bad HMAC)
// maps to a bland 401.
func TestPRWebhook_BadSignature_returns401(t *testing.T) {
	env := newPREnv(t)
	prEventErr = status.Error(codes.PermissionDenied, "signature mismatch")
	resp := env.postWebhook(t, "pull_request", "sha256=bad", "delivery-2", []byte(`{}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

// TestPRWebhook_BlankSignature_returns401 verifies a missing signature header
// (metadata rejects it as PermissionDenied) also maps to 401.
func TestPRWebhook_BlankSignature_returns401(t *testing.T) {
	env := newPREnv(t)
	prEventErr = status.Error(codes.PermissionDenied, "missing signature")
	resp := env.postWebhook(t, "pull_request", "", "delivery-3", []byte(`{}`))
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
	// Confirm a blank signature is what was forwarded.
	prFakeMu.Lock()
	req := prLastEventReq
	prFakeMu.Unlock()
	if req == nil || req.GetSignature() != "" {
		t.Errorf("expected blank signature forwarded, got %+v", req)
	}
}

// TestPRWebhook_Disabled_returns404 verifies OUTCOME_DISABLED renders a bland
// 404 (the endpoint must not be a probe oracle for whether the feature is on).
func TestPRWebhook_Disabled_returns404(t *testing.T) {
	env := newPREnv(t)
	prEventReturn = &metadatav1.HandlePREventResponse{Outcome: metadatav1.HandlePREventResponse_OUTCOME_DISABLED}
	resp := env.postWebhook(t, "pull_request", "sha256=abc", "delivery-4", []byte(`{}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

// TestPRWebhook_Provisioned_returns200 verifies a provisioning outcome returns
// 200 with the {outcome, org} body.
func TestPRWebhook_Provisioned_returns200(t *testing.T) {
	env := newPREnv(t)
	prEventReturn = &metadatav1.HandlePREventResponse{
		Outcome: metadatav1.HandlePREventResponse_OUTCOME_PROVISIONED,
		OrgName: "pr-99",
	}
	resp := env.postWebhook(t, "pull_request", "sha256=abc", "delivery-5", []byte(`{"action":"opened"}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var body struct {
		Outcome string `json:"outcome"`
		Org     string `json:"org"`
	}
	decodeJSON(t, resp, &body)
	if body.Outcome != "provisioned" || body.Org != "pr-99" {
		t.Errorf("body: got %+v, want provisioned/pr-99", body)
	}
}

// TestPRWebhook_InvalidArgument_returns400 verifies an InvalidArgument from
// metadata maps to 400.
func TestPRWebhook_InvalidArgument_returns400(t *testing.T) {
	env := newPREnv(t)
	prEventErr = status.Error(codes.InvalidArgument, "unparseable body")
	resp := env.postWebhook(t, "pull_request", "sha256=abc", "delivery-6", []byte(`not-json`))
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

// TestPRWebhook_NoAuthRequired verifies the receiver is reachable WITHOUT any
// Authorization header (the postWebhook helper never sets one) — a 204 here
// proves the route bypasses authMW.
func TestPRWebhook_NoAuthRequired(t *testing.T) {
	env := newPREnv(t)
	resp := env.postWebhook(t, "ping", "sha256=x", "delivery-7", []byte(`{}`))
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("receiver must not require a JWT; got 401")
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("expected 204 for ping, got %d", resp.StatusCode)
	}
}
