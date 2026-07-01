// promote_tag_test.go — FUT-020 image promotion BFF handler tests.
//
// Uses its own test environment (promoteTestEnv) because the promotion tests
// need a fake publisher AND a fake metadata server whose PromoteTag /
// ListPromotions can be scripted per-case. The shared testEnv in
// handler_test.go doesn't expose the metadata surface, and signerTestEnv
// doesn't expose the promotion surface, so a fresh scaffold keeps the
// concerns cleanly separated.
package handler_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	auditv1 "github.com/steveokay/oci-janus/proto/gen/go/audit/v1"
	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	"github.com/steveokay/oci-janus/services/management/internal/handler"
)

// promoteMetaServer is a hand-scripted metadata server for the promotion
// tests. Both PromoteTag + ListPromotions can be swapped per test to
// force success or a specific gRPC status error. Every call is captured
// so tests can assert the BFF forwarded the tenant + actor as expected.
type promoteMetaServer struct {
	metadatav1.UnimplementedMetadataServiceServer

	// promoteFunc overrides the PromoteTag handler; nil returns the canned
	// success below.
	promoteFunc func(ctx context.Context, req *metadatav1.PromoteTagRequest) (*metadatav1.Promotion, error)
	// listFunc overrides ListPromotions.
	listFunc func(ctx context.Context, req *metadatav1.ListPromotionsRequest) (*metadatav1.ListPromotionsResponse, error)

	promoteCalls []*metadatav1.PromoteTagRequest
	listCalls    []*metadatav1.ListPromotionsRequest
	mu           sync.Mutex
}

func (s *promoteMetaServer) PromoteTag(ctx context.Context, req *metadatav1.PromoteTagRequest) (*metadatav1.Promotion, error) {
	s.mu.Lock()
	s.promoteCalls = append(s.promoteCalls, req)
	s.mu.Unlock()
	if s.promoteFunc != nil {
		return s.promoteFunc(ctx, req)
	}
	return &metadatav1.Promotion{
		Id:          "11111111-1111-1111-1111-111111111111",
		TenantId:    req.GetTenantId(),
		SrcOrg:      req.GetSrcOrg(),
		SrcRepo:     req.GetSrcRepo(),
		SrcTag:      req.GetSrcTag(),
		SrcDigest:   "sha256:aaa",
		DstOrg:      req.GetDstOrg(),
		DstRepo:     req.GetDstRepo(),
		DstTag:      req.GetDstTag(),
		DstDigest:   "sha256:aaa",
		ActorUserId: req.GetActorUserId(),
		Note:        req.GetNote(),
		PromotedAt:  timestamppb.Now(),
	}, nil
}

func (s *promoteMetaServer) ListPromotions(ctx context.Context, req *metadatav1.ListPromotionsRequest) (*metadatav1.ListPromotionsResponse, error) {
	s.mu.Lock()
	s.listCalls = append(s.listCalls, req)
	s.mu.Unlock()
	if s.listFunc != nil {
		return s.listFunc(ctx, req)
	}
	return &metadatav1.ListPromotionsResponse{}, nil
}

// promoteTestEnv wraps the httptest.Server + fakes each test can mutate.
type promoteTestEnv struct {
	srv  *httptest.Server
	meta *promoteMetaServer
	pub  *fakePublisher
}

// newPromoteTestEnv wires the standard fakes and returns handles the tests
// need to configure per case. Same shape as newSignerTestEnv but keyed on
// the promotion routes.
func newPromoteTestEnv(t *testing.T) *promoteTestEnv {
	t.Helper()

	authLis := bufconn.Listen(bufSize)
	authGRPC := grpc.NewServer()
	authv1.RegisterAuthServiceServer(authGRPC, &fakeAuthServer{})
	healthpb.RegisterHealthServer(authGRPC, &fakeHealthServer{})
	go func() { _ = authGRPC.Serve(authLis) }()
	t.Cleanup(authGRPC.Stop)

	meta := &promoteMetaServer{}
	metaLis := bufconn.Listen(bufSize)
	metaGRPC := grpc.NewServer()
	metadatav1.RegisterMetadataServiceServer(metaGRPC, meta)
	healthpb.RegisterHealthServer(metaGRPC, &fakeHealthServer{})
	go func() { _ = metaGRPC.Serve(metaLis) }()
	t.Cleanup(metaGRPC.Stop)

	auditLis := bufconn.Listen(bufSize)
	auditGRPC := grpc.NewServer()
	auditv1.RegisterAuditServiceServer(auditGRPC, &fakeAuditServer{})
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

	authConn := dial(authLis)
	metaConn := dial(metaLis)
	auditConn := dial(auditLis)

	pub := &fakePublisher{}
	h := handler.New(
		authv1.NewAuthServiceClient(authConn),
		metadatav1.NewMetadataServiceClient(metaConn),
		auditv1.NewAuditServiceClient(auditConn),
		nil,
		"",
	)
	h = h.WithPublisher(pub)

	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &promoteTestEnv{srv: srv, meta: meta, pub: pub}
}

// post fires a POST against the test server with the given bearer token.
func (e *promoteTestEnv) post(t *testing.T, path, token, body string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

// get fires a GET against the test server.
func (e *promoteTestEnv) get(t *testing.T, path, token string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// TestPromoteTag_HappyPath asserts a writer promoting within the same org
// gets 201 back, the metadata RPC was called with the right args, and an
// image.promoted event was published exactly once.
func TestPromoteTag_HappyPath(t *testing.T) {
	env := newPromoteTestEnv(t)

	body := `{"dst_org":"myorg","dst_repo":"myrepo","dst_tag":"v2","note":"promoted from src"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/promote", writerToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201, got %d", resp.StatusCode)
	}

	var got struct {
		ID          string `json:"id"`
		DstOrg      string `json:"dst_org"`
		DstTag      string `json:"dst_tag"`
		Note        string `json:"note"`
		ActorUserID string `json:"actor_user_id"`
	}
	decodeJSON(t, resp, &got)
	if got.ID == "" {
		t.Error("empty id in response")
	}
	if got.DstOrg != "myorg" || got.DstTag != "v2" {
		t.Errorf("dst fields dropped: %+v", got)
	}
	if got.Note != "promoted from src" {
		t.Errorf("note dropped: %q", got.Note)
	}

	// The metadata surface must have received the parsed request.
	env.meta.mu.Lock()
	defer env.meta.mu.Unlock()
	if len(env.meta.promoteCalls) != 1 {
		t.Fatalf("want 1 metadata call, got %d", len(env.meta.promoteCalls))
	}
	call := env.meta.promoteCalls[0]
	if call.GetSrcOrg() != "myorg" || call.GetSrcRepo() != "myrepo" || call.GetSrcTag() != "v1.0" {
		t.Errorf("src fields dropped from grpc call: %+v", call)
	}
	if call.GetDstOrg() != "myorg" || call.GetDstRepo() != "myrepo" || call.GetDstTag() != "v2" {
		t.Errorf("dst fields dropped from grpc call: %+v", call)
	}
	if call.GetActorUserId() == "" {
		t.Error("actor_user_id missing from grpc call — JWT sub should have been forwarded")
	}

	// image.promoted was published.
	if atomic.LoadInt64(&env.pub.count) != 1 {
		t.Errorf("want 1 publish, got %d", env.pub.count)
	}
	env.pub.mu.Lock()
	defer env.pub.mu.Unlock()
	if len(env.pub.calls) != 1 {
		t.Fatalf("want 1 captured publish, got %d", len(env.pub.calls))
	}
	if env.pub.calls[0].routingKey != events.RoutingImagePromoted {
		t.Errorf("want routing key %q, got %q", events.RoutingImagePromoted, env.pub.calls[0].routingKey)
	}
}

// TestPromoteTag_ReaderTokenRejected — the reader token has no writer role
// anywhere. Must return 403 without touching metadata.
func TestPromoteTag_ReaderTokenRejected(t *testing.T) {
	env := newPromoteTestEnv(t)
	body := `{"dst_org":"myorg","dst_repo":"myrepo","dst_tag":"v2"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/promote", readerToken, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
	if len(env.meta.promoteCalls) != 0 {
		t.Errorf("metadata called despite forbidden: %d calls", len(env.meta.promoteCalls))
	}
}

// TestPromoteTag_NoWriteRoleOnDest — the writer token has writer on "myorg"
// only. Promoting into "otherorg/prod" must be forbidden because the caller
// does not have write access to the destination. This is the load-bearing
// security assertion — someone with pull-only access on prod/* cannot
// promote INTO it via a laundering source they DO control.
func TestPromoteTag_NoWriteRoleOnDest(t *testing.T) {
	env := newPromoteTestEnv(t)
	body := `{"dst_org":"otherorg","dst_repo":"prod","dst_tag":"v2"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/promote", writerToken, body)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("want 403, got %d", resp.StatusCode)
	}
	if len(env.meta.promoteCalls) != 0 {
		t.Errorf("metadata called despite forbidden: %d calls", len(env.meta.promoteCalls))
	}
	// No event should have been published on a forbidden path.
	if atomic.LoadInt64(&env.pub.count) != 0 {
		t.Errorf("publisher called on forbidden path: %d", env.pub.count)
	}
}

// TestPromoteTag_ImmutableConflict — the metadata surface returns
// FailedPrecondition when the destination tag is immutable at a different
// digest. BFF must surface as 409 Conflict so the FE can render a clear
// "tag is immutable" toast.
func TestPromoteTag_ImmutableConflict(t *testing.T) {
	env := newPromoteTestEnv(t)
	env.meta.promoteFunc = func(_ context.Context, _ *metadatav1.PromoteTagRequest) (*metadatav1.Promotion, error) {
		return nil, status.Error(codes.FailedPrecondition, "destination tag is immutable")
	}
	body := `{"dst_org":"myorg","dst_repo":"myrepo","dst_tag":"v2"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/promote", writerToken, body)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", resp.StatusCode)
	}
}

// TestPromoteTag_NotFound — metadata NotFound surfaces as 404 (source tag
// or destination repo missing).
func TestPromoteTag_NotFound(t *testing.T) {
	env := newPromoteTestEnv(t)
	env.meta.promoteFunc = func(_ context.Context, _ *metadatav1.PromoteTagRequest) (*metadatav1.Promotion, error) {
		return nil, status.Error(codes.NotFound, "not found")
	}
	body := `{"dst_org":"myorg","dst_repo":"myrepo","dst_tag":"v2"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/promote", writerToken, body)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}

// TestPromoteTag_InvalidBody — a body missing dst_org is rejected before
// the metadata call.
func TestPromoteTag_InvalidBody(t *testing.T) {
	env := newPromoteTestEnv(t)
	body := `{"dst_repo":"myrepo","dst_tag":"v2"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/promote", writerToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
	if len(env.meta.promoteCalls) != 0 {
		t.Errorf("metadata called despite bad body: %d calls", len(env.meta.promoteCalls))
	}
}

// TestPromoteTag_NoteTooLong — a note over 256 chars is rejected as 400.
func TestPromoteTag_NoteTooLong(t *testing.T) {
	env := newPromoteTestEnv(t)
	long := strings.Repeat("x", 257)
	body := `{"dst_org":"myorg","dst_repo":"myrepo","dst_tag":"v2","note":"` + long + `"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/promote", writerToken, body)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", resp.StatusCode)
	}
}

// TestPromoteTag_PublishFailureDoesNotFailRequest — if the publisher
// returns an error, the request must still return 201 (the promotion is
// already durable in the DB; audit can be replayed from the promotions
// table). Regression guard against a future change that switches to
// hard-failing on publish errors.
func TestPromoteTag_PublishFailureDoesNotFailRequest(t *testing.T) {
	env := newPromoteTestEnv(t)
	env.pub.publishErr = errBoom{}
	body := `{"dst_org":"myorg","dst_repo":"myrepo","dst_tag":"v2"}`
	resp := env.post(t, "/api/v1/repositories/myorg/myrepo/tags/v1.0/promote", writerToken, body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("want 201 despite publish failure, got %d", resp.StatusCode)
	}
}

// TestListPromotions_HappyPath — reader can view history; response envelope
// wraps rows under `promotions` key.
func TestListPromotions_HappyPath(t *testing.T) {
	env := newPromoteTestEnv(t)
	env.meta.listFunc = func(_ context.Context, _ *metadatav1.ListPromotionsRequest) (*metadatav1.ListPromotionsResponse, error) {
		return &metadatav1.ListPromotionsResponse{
			Promotions: []*metadatav1.Promotion{
				{Id: "a", DstOrg: "myorg", DstRepo: "myrepo", DstTag: "v1", PromotedAt: timestamppb.Now()},
				{Id: "b", DstOrg: "myorg", DstRepo: "myrepo", DstTag: "v2", PromotedAt: timestamppb.Now()},
			},
		}, nil
	}
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/promotions", readerToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var body struct {
		Promotions []struct {
			ID     string `json:"id"`
			DstTag string `json:"dst_tag"`
		} `json:"promotions"`
	}
	decodeJSON(t, resp, &body)
	if len(body.Promotions) != 2 {
		t.Fatalf("want 2 rows, got %d", len(body.Promotions))
	}
}

// TestListPromotions_EmptyRendersEmptyArray — the FE differentiates empty
// state (array of zero) from an error (missing / null field). Regression
// guard for the make([]…, 0, …) allocation in the handler.
func TestListPromotions_EmptyRendersEmptyArray(t *testing.T) {
	env := newPromoteTestEnv(t)
	resp := env.get(t, "/api/v1/repositories/myorg/myrepo/promotions", readerToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	// Decode into a raw map so we can assert the field is [] not null.
	var raw map[string]json.RawMessage
	decodeJSON(t, resp, &raw)
	got := string(raw["promotions"])
	if got != "[]" {
		t.Fatalf("want empty array literal, got %q", got)
	}
}

// errBoom is a sentinel error used by the publish-failure regression test.
type errBoom struct{}

func (errBoom) Error() string { return "boom" }
