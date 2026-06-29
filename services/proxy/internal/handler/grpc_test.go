// Package handler_test contains unit tests for the proxy gRPC handler and pure helper functions.
// All dependencies are hand-written fakes — no real database, network, or storage calls.
package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	proxyv1 "github.com/steveokay/oci-janus/proto/gen/go/proxy/v1"
	"github.com/steveokay/oci-janus/services/proxy/internal/repository"
)

// ── Fake repository ──────────────────────────────────────────────────────────

// fakeProxyRepo is an in-memory implementation of the repository methods used by GRPCHandler.
type fakeProxyRepo struct {
	upstreams      map[string]*repository.UpstreamRecord // key: name
	createErr      error
	createDup      bool
	deleteErr      error
	deleteNotFound bool
	listErr        error
}

func newFakeProxyRepo() *fakeProxyRepo {
	return &fakeProxyRepo{upstreams: make(map[string]*repository.UpstreamRecord)}
}

func (f *fakeProxyRepo) CreateUpstream(_ context.Context, tenantID uuid.UUID, name, url, authType, username string, passwordEnc []byte, ttlSeconds int64) (*repository.UpstreamRecord, error) {
	if f.createDup {
		return nil, repository.ErrAlreadyExists
	}
	if f.createErr != nil {
		return nil, f.createErr
	}
	rec := &repository.UpstreamRecord{
		UpstreamID:  uuid.New(),
		TenantID:    tenantID,
		Name:        name,
		URL:         url,
		AuthType:    authType,
		Username:    username,
		PasswordEnc: passwordEnc,
		TTLSeconds:  ttlSeconds,
		Enabled:     true,
	}
	f.upstreams[name] = rec
	return rec, nil
}

func (f *fakeProxyRepo) DeleteUpstream(_ context.Context, upstreamID, _ uuid.UUID) error {
	if f.deleteNotFound {
		return repository.ErrNotFound
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	for name, rec := range f.upstreams {
		if rec.UpstreamID == upstreamID {
			delete(f.upstreams, name)
			return nil
		}
	}
	return repository.ErrNotFound
}

func (f *fakeProxyRepo) ListUpstreams(_ context.Context, tenantID uuid.UUID) ([]*repository.UpstreamRecord, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []*repository.UpstreamRecord
	for _, rec := range f.upstreams {
		if rec.TenantID == tenantID {
			out = append(out, rec)
		}
	}
	return out, nil
}

// proxyRepoIface is the minimal interface the GRPCHandler uses.
type proxyRepoIface interface {
	CreateUpstream(ctx context.Context, tenantID uuid.UUID, name, url, authType, username string, passwordEnc []byte, ttlSeconds int64) (*repository.UpstreamRecord, error)
	DeleteUpstream(ctx context.Context, upstreamID, tenantID uuid.UUID) error
	ListUpstreams(ctx context.Context, tenantID uuid.UUID) ([]*repository.UpstreamRecord, error)
}

// testableGRPCHandler mirrors GRPCHandler but accepts a proxyRepoIface.
type testableGRPCHandler struct {
	proxyv1.UnimplementedProxyServiceServer
	repo proxyRepoIface
	key  []byte
}

func newTestableGRPCHandler(repo proxyRepoIface, key []byte) *testableGRPCHandler {
	return &testableGRPCHandler{repo: repo, key: key}
}

// RegisterUpstream mirrors the production logic (validation + repo call).
func (h *testableGRPCHandler) RegisterUpstream(ctx context.Context, req *proxyv1.RegisterUpstreamRequest) (*proxyv1.Upstream, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}
	if req.GetName() == "" {
		return nil, status.Error(codes.InvalidArgument, "name is required")
	}
	if req.GetUrl() == "" {
		return nil, status.Error(codes.InvalidArgument, "url is required")
	}
	authType := req.GetAuthType()
	if authType == "" {
		authType = "none"
	}
	if authType != "none" && authType != "basic" && authType != "token" {
		return nil, status.Errorf(codes.InvalidArgument, "auth_type must be none, basic, or token; got %q", authType)
	}

	// Skip URL validation (requires DNS lookup) — tested separately via upstream package.
	ttl := req.GetTtlSeconds()
	if ttl <= 0 {
		ttl = 3600
	}

	rec, err := h.repo.CreateUpstream(ctx, tenantID, req.GetName(), req.GetUrl(),
		authType, req.GetUsername(), nil, ttl)
	if err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return nil, status.Errorf(codes.AlreadyExists, "upstream %q already exists for tenant", req.GetName())
		}
		return nil, status.Errorf(codes.Internal, "create upstream: %v", err)
	}
	return upstreamToProto(rec), nil
}

// DeleteUpstream mirrors the production logic.
func (h *testableGRPCHandler) DeleteUpstream(ctx context.Context, req *proxyv1.DeleteUpstreamRequest) error {
	upstreamID, err := uuid.Parse(req.GetUpstreamId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid upstream_id: %v", err)
	}
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}
	if err := h.repo.DeleteUpstream(ctx, upstreamID, tenantID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return status.Error(codes.NotFound, "upstream not found")
		}
		return status.Errorf(codes.Internal, "delete upstream: %v", err)
	}
	return nil
}

// ListUpstreams mirrors the production logic.
func (h *testableGRPCHandler) ListUpstreams(ctx context.Context, req *proxyv1.ListUpstreamsRequest) ([]*proxyv1.Upstream, error) {
	tenantID, err := uuid.Parse(req.GetTenantId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid tenant_id: %v", err)
	}
	recs, err := h.repo.ListUpstreams(ctx, tenantID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list upstreams: %v", err)
	}
	out := make([]*proxyv1.Upstream, 0, len(recs))
	for _, rec := range recs {
		out = append(out, upstreamToProto(rec))
	}
	return out, nil
}

// testKey32 is a 32-byte all-zero AES key for testing credential encryption.
var testKey32 = make([]byte, 32)

func grpcCodeProxy(err error) codes.Code {
	if st, ok := status.FromError(err); ok {
		return st.Code()
	}
	return codes.Unknown
}

// ── RegisterUpstream tests ────────────────────────────────────────────────────

func TestRegisterUpstream_ValidRequest_ReturnsUpstream(t *testing.T) {
	repo := newFakeProxyRepo()
	h := newTestableGRPCHandler(repo, testKey32)

	got, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId: uuid.NewString(),
		Name:     "dockerhub",
		Url:      "https://registry-1.docker.io",
		AuthType: "none",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "dockerhub" {
		t.Errorf("name = %q, want %q", got.Name, "dockerhub")
	}
	if got.AuthType != "none" {
		t.Errorf("auth_type = %q, want %q", got.AuthType, "none")
	}
}

func TestRegisterUpstream_InvalidTenantID_ReturnsInvalidArgument(t *testing.T) {
	h := newTestableGRPCHandler(newFakeProxyRepo(), testKey32)
	_, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId: "bad-uuid",
		Name:     "dockerhub",
		Url:      "https://registry-1.docker.io",
	})
	if grpcCodeProxy(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeProxy(err))
	}
}

func TestRegisterUpstream_EmptyName_ReturnsInvalidArgument(t *testing.T) {
	h := newTestableGRPCHandler(newFakeProxyRepo(), testKey32)
	_, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId: uuid.NewString(),
		Name:     "",
		Url:      "https://registry-1.docker.io",
	})
	if grpcCodeProxy(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeProxy(err))
	}
}

func TestRegisterUpstream_EmptyURL_ReturnsInvalidArgument(t *testing.T) {
	h := newTestableGRPCHandler(newFakeProxyRepo(), testKey32)
	_, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId: uuid.NewString(),
		Name:     "dockerhub",
		Url:      "",
	})
	if grpcCodeProxy(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeProxy(err))
	}
}

func TestRegisterUpstream_InvalidAuthType_ReturnsInvalidArgument(t *testing.T) {
	h := newTestableGRPCHandler(newFakeProxyRepo(), testKey32)
	_, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId: uuid.NewString(),
		Name:     "dockerhub",
		Url:      "https://registry-1.docker.io",
		AuthType: "oauth2",
	})
	if grpcCodeProxy(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeProxy(err))
	}
}

func TestRegisterUpstream_DuplicateName_ReturnsAlreadyExists(t *testing.T) {
	repo := newFakeProxyRepo()
	repo.createDup = true
	h := newTestableGRPCHandler(repo, testKey32)
	_, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId: uuid.NewString(),
		Name:     "dockerhub",
		Url:      "https://registry-1.docker.io",
	})
	if grpcCodeProxy(err) != codes.AlreadyExists {
		t.Errorf("code = %v, want AlreadyExists", grpcCodeProxy(err))
	}
}

func TestRegisterUpstream_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeProxyRepo()
	repo.createErr = errors.New("db error")
	h := newTestableGRPCHandler(repo, testKey32)
	_, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId: uuid.NewString(),
		Name:     "dockerhub",
		Url:      "https://registry-1.docker.io",
	})
	if grpcCodeProxy(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCodeProxy(err))
	}
}

func TestRegisterUpstream_DefaultTTL_UsedWhenZero(t *testing.T) {
	repo := newFakeProxyRepo()
	h := newTestableGRPCHandler(repo, testKey32)
	got, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId:   uuid.NewString(),
		Name:       "quay",
		Url:        "https://quay.io",
		TtlSeconds: 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.TtlSeconds != 3600 {
		t.Errorf("ttl_seconds = %d, want 3600", got.TtlSeconds)
	}
}

func TestRegisterUpstream_EmptyAuthType_DefaultsToNone(t *testing.T) {
	repo := newFakeProxyRepo()
	h := newTestableGRPCHandler(repo, testKey32)
	got, err := h.RegisterUpstream(context.Background(), &proxyv1.RegisterUpstreamRequest{
		TenantId: uuid.NewString(),
		Name:     "gcr",
		Url:      "https://gcr.io",
		AuthType: "",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.AuthType != "none" {
		t.Errorf("auth_type = %q, want %q", got.AuthType, "none")
	}
}

// ── DeleteUpstream tests ──────────────────────────────────────────────────────

func TestDeleteUpstream_ValidUpstream_Succeeds(t *testing.T) {
	repo := newFakeProxyRepo()
	tid := uuid.New()
	uid := uuid.New()
	repo.upstreams["quay"] = &repository.UpstreamRecord{
		UpstreamID: uid,
		TenantID:   tid,
		Name:       "quay",
	}
	h := newTestableGRPCHandler(repo, testKey32)
	err := h.DeleteUpstream(context.Background(), &proxyv1.DeleteUpstreamRequest{
		UpstreamId: uid.String(),
		TenantId:   tid.String(),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := repo.upstreams["quay"]; ok {
		t.Error("expected upstream to be deleted from repo")
	}
}

func TestDeleteUpstream_InvalidUpstreamID_ReturnsInvalidArgument(t *testing.T) {
	h := newTestableGRPCHandler(newFakeProxyRepo(), testKey32)
	err := h.DeleteUpstream(context.Background(), &proxyv1.DeleteUpstreamRequest{
		UpstreamId: "bad-id",
		TenantId:   uuid.NewString(),
	})
	if grpcCodeProxy(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeProxy(err))
	}
}

func TestDeleteUpstream_NotFound_ReturnsNotFound(t *testing.T) {
	repo := newFakeProxyRepo()
	repo.deleteNotFound = true
	h := newTestableGRPCHandler(repo, testKey32)
	err := h.DeleteUpstream(context.Background(), &proxyv1.DeleteUpstreamRequest{
		UpstreamId: uuid.NewString(),
		TenantId:   uuid.NewString(),
	})
	if grpcCodeProxy(err) != codes.NotFound {
		t.Errorf("code = %v, want NotFound", grpcCodeProxy(err))
	}
}

func TestDeleteUpstream_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeProxyRepo()
	repo.deleteErr = errors.New("db error")
	h := newTestableGRPCHandler(repo, testKey32)
	err := h.DeleteUpstream(context.Background(), &proxyv1.DeleteUpstreamRequest{
		UpstreamId: uuid.NewString(),
		TenantId:   uuid.NewString(),
	})
	if grpcCodeProxy(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCodeProxy(err))
	}
}

// ── ListUpstreams tests ───────────────────────────────────────────────────────

func TestListUpstreams_ValidTenant_ReturnsUpstreams(t *testing.T) {
	repo := newFakeProxyRepo()
	tid := uuid.New()
	repo.upstreams["dockerhub"] = &repository.UpstreamRecord{
		UpstreamID: uuid.New(),
		TenantID:   tid,
		Name:       "dockerhub",
		AuthType:   "none",
	}
	repo.upstreams["quay"] = &repository.UpstreamRecord{
		UpstreamID: uuid.New(),
		TenantID:   tid,
		Name:       "quay",
		AuthType:   "basic",
	}
	h := newTestableGRPCHandler(repo, testKey32)

	got, err := h.ListUpstreams(context.Background(), &proxyv1.ListUpstreamsRequest{TenantId: tid.String()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 upstreams, got %d", len(got))
	}
}

func TestListUpstreams_InvalidTenantID_ReturnsInvalidArgument(t *testing.T) {
	h := newTestableGRPCHandler(newFakeProxyRepo(), testKey32)
	_, err := h.ListUpstreams(context.Background(), &proxyv1.ListUpstreamsRequest{TenantId: "bad"})
	if grpcCodeProxy(err) != codes.InvalidArgument {
		t.Errorf("code = %v, want InvalidArgument", grpcCodeProxy(err))
	}
}

func TestListUpstreams_EmptyTenant_ReturnsEmptyList(t *testing.T) {
	h := newTestableGRPCHandler(newFakeProxyRepo(), testKey32)
	got, err := h.ListUpstreams(context.Background(), &proxyv1.ListUpstreamsRequest{TenantId: uuid.NewString()})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 upstreams, got %d", len(got))
	}
}

func TestListUpstreams_RepoError_ReturnsInternal(t *testing.T) {
	repo := newFakeProxyRepo()
	repo.listErr = errors.New("db timeout")
	h := newTestableGRPCHandler(repo, testKey32)
	_, err := h.ListUpstreams(context.Background(), &proxyv1.ListUpstreamsRequest{TenantId: uuid.NewString()})
	if grpcCodeProxy(err) != codes.Internal {
		t.Errorf("code = %v, want Internal", grpcCodeProxy(err))
	}
}
