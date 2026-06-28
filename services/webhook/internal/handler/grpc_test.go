// Package handler_test exercises the WebhookService gRPC handler using hand-written
// fake implementations of webhookRepo. No PostgreSQL, no network connections required.
package handler

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	webhookv1 "github.com/steveokay/oci-janus/proto/gen/go/webhook/v1"
	"github.com/steveokay/oci-janus/services/webhook/internal/repository"
)

// validKeyHex is a 32-byte (64 hex chars) AES key used in tests.
const validKeyHex = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

// fakeRepo implements webhookRepo with configurable responses.
// All fields are optional; unset fields return zero-value / nil.
type fakeRepo struct {
	// CreateEndpoint result
	createEndpointRec *repository.EndpointRecord
	createEndpointErr error

	// DeleteEndpoint result
	deleteEndpointErr error

	// ListEndpoints result
	listEndpointsRecs []*repository.EndpointRecord
	listEndpointsErr  error

	// GetEndpointForTenant / Update / Rotate / ListDeliveries — added with
	// the FE-API-021..024 routes. Each method returns its `*Rec`/`*Err` pair.
	getEndpointRec *repository.EndpointRecord
	getEndpointErr error

	updateEndpointRec *repository.EndpointRecord
	updateEndpointErr error

	rotateSecretErr error

	listDeliveriesRecs []*repository.DeliveryRecord
	listDeliveriesErr  error

	// GetDelivery (FE-API-035) — single-row variant.
	getDeliveryRec *repository.DeliveryRecord
	getDeliveryErr error

	// Recorded arguments for assertion
	lastCreateURL    string
	lastCreateEvents []string
	lastDeleteID     uuid.UUID
}

func (f *fakeRepo) CreateEndpoint(_ context.Context, tenantID uuid.UUID, url string, events []string, secretEnc string) (*repository.EndpointRecord, error) {
	f.lastCreateURL = url
	f.lastCreateEvents = events
	return f.createEndpointRec, f.createEndpointErr
}

func (f *fakeRepo) DeleteEndpoint(_ context.Context, endpointID, _ uuid.UUID) error {
	f.lastDeleteID = endpointID
	return f.deleteEndpointErr
}

func (f *fakeRepo) ListEndpoints(_ context.Context, _ uuid.UUID) ([]*repository.EndpointRecord, error) {
	return f.listEndpointsRecs, f.listEndpointsErr
}

func (f *fakeRepo) GetEndpointForTenant(_ context.Context, _ uuid.UUID, _ uuid.UUID) (*repository.EndpointRecord, error) {
	return f.getEndpointRec, f.getEndpointErr
}

func (f *fakeRepo) UpdateEndpoint(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ *string, _ []string, _ *bool) (*repository.EndpointRecord, error) {
	return f.updateEndpointRec, f.updateEndpointErr
}

func (f *fakeRepo) RotateSecret(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ string) error {
	return f.rotateSecretErr
}

func (f *fakeRepo) ListDeliveries(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ time.Time, _ int) ([]*repository.DeliveryRecord, error) {
	return f.listDeliveriesRecs, f.listDeliveriesErr
}

func (f *fakeRepo) GetDelivery(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ uuid.UUID) (*repository.DeliveryRecord, error) {
	return f.getDeliveryRec, f.getDeliveryErr
}

// fakeDispatcher implements testDispatcher with a configurable result.
type fakeDispatcher struct {
	code  int
	durMs int64
	err   error
}

func (d *fakeDispatcher) DeliverWithResult(_ context.Context, _ string, _ []byte, _ []byte) (int, int64, error) {
	return d.code, d.durMs, d.err
}

// makeHandler creates a handler with the given fake repo and the validKeyHex credential key.
// dispatcher is nil here — none of the existing test cases exercise TestDispatch.
func makeHandler(t *testing.T, repo webhookRepo) *GRPCHandler {
	t.Helper()
	h, err := newWithRepo(repo, &fakeDispatcher{}, validKeyHex)
	if err != nil {
		t.Fatalf("newWithRepo: %v", err)
	}
	return h
}

// sampleEndpoint returns a realistic EndpointRecord for use in fake responses.
func sampleEndpoint() *repository.EndpointRecord {
	return &repository.EndpointRecord{
		ID:        uuid.MustParse("11111111-1111-1111-1111-111111111111"),
		TenantID:  uuid.MustParse("22222222-2222-2222-2222-222222222222"),
		URL:       "https://hooks.example.com/push",
		Events:    []string{"push.completed"},
		SecretEnc: hex.EncodeToString([]byte("encrypted-secret")),
		Active:    true,
		CreatedAt: time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC),
	}
}

// TestNew_invalidKeyTooShort verifies that New() returns an error when the
// credential key is shorter than 64 hex characters (< 32 bytes).
func TestNew_invalidKeyTooShort(t *testing.T) {
	_, err := newWithRepo(&fakeRepo{}, &fakeDispatcher{}, "deadbeef")
	if err == nil {
		t.Fatal("expected error for key shorter than 32 bytes")
	}
}

// TestNew_invalidKeyNotHex verifies that New() returns an error for non-hex input.
func TestNew_invalidKeyNotHex(t *testing.T) {
	_, err := newWithRepo(&fakeRepo{}, &fakeDispatcher{}, "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if err == nil {
		t.Fatal("expected error for non-hex credential key")
	}
}

// TestNew_validKey verifies that New() succeeds with a 64-hex-char key.
func TestNew_validKey(t *testing.T) {
	h, err := newWithRepo(&fakeRepo{}, &fakeDispatcher{}, validKeyHex)
	if err != nil {
		t.Fatalf("expected success with valid key, got: %v", err)
	}
	if h == nil {
		t.Fatal("handler should not be nil")
	}
	if len(h.credentialKey) != 32 {
		t.Errorf("credentialKey length: got %d, want 32", len(h.credentialKey))
	}
}

// TestCreateEndpoint_missingTenantID verifies that an empty tenant_id returns
// codes.InvalidArgument.
func TestCreateEndpoint_missingTenantID(t *testing.T) {
	h := makeHandler(t, &fakeRepo{})
	_, err := h.CreateEndpoint(context.Background(), &webhookv1.CreateEndpointRequest{
		TenantId: "",
		Url:      "https://example.com/hook",
		Events:   []string{"push.completed"},
		Secret:   "secret",
	})
	if err == nil {
		t.Fatal("expected error for empty tenant_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestCreateEndpoint_missingURL verifies that an empty URL returns codes.InvalidArgument.
func TestCreateEndpoint_missingURL(t *testing.T) {
	h := makeHandler(t, &fakeRepo{})
	_, err := h.CreateEndpoint(context.Background(), &webhookv1.CreateEndpointRequest{
		TenantId: "22222222-2222-2222-2222-222222222222",
		Url:      "",
		Events:   []string{"push.completed"},
		Secret:   "secret",
	})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestCreateEndpoint_emptyEvents verifies that an empty events list returns
// codes.InvalidArgument.
func TestCreateEndpoint_emptyEvents(t *testing.T) {
	h := makeHandler(t, &fakeRepo{})
	_, err := h.CreateEndpoint(context.Background(), &webhookv1.CreateEndpointRequest{
		TenantId: "22222222-2222-2222-2222-222222222222",
		Url:      "https://example.com/hook",
		Events:   nil,
		Secret:   "secret",
	})
	if err == nil {
		t.Fatal("expected error for empty events")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestCreateEndpoint_httpURLRejected verifies that ValidateURL blocks http:// URLs.
func TestCreateEndpoint_httpURLRejected(t *testing.T) {
	h := makeHandler(t, &fakeRepo{})
	_, err := h.CreateEndpoint(context.Background(), &webhookv1.CreateEndpointRequest{
		TenantId: "22222222-2222-2222-2222-222222222222",
		Url:      "http://example.com/hook",
		Events:   []string{"push.completed"},
		Secret:   "secret",
	})
	if err == nil {
		t.Fatal("expected error for http:// webhook URL")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestCreateEndpoint_invalidTenantUUID verifies that a non-UUID tenant_id returns
// codes.InvalidArgument (uuid.Parse will fail).
func TestCreateEndpoint_invalidTenantUUID(t *testing.T) {
	h := makeHandler(t, &fakeRepo{})
	_, err := h.CreateEndpoint(context.Background(), &webhookv1.CreateEndpointRequest{
		TenantId: "not-a-uuid",
		Url:      "https://example.com/hook",
		Events:   []string{"push.completed"},
		Secret:   "secret",
	})
	if err == nil {
		t.Fatal("expected error for non-UUID tenant_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestCreateEndpoint_repoError verifies that a database error returns codes.Internal.
func TestCreateEndpoint_repoError(t *testing.T) {
	fake := &fakeRepo{
		createEndpointErr: errors.New("db down"),
	}
	h := makeHandler(t, fake)

	// Use an IP-literal public address to bypass DNS resolution in ValidateURL.
	// 198.51.100.x is TEST-NET-2 per RFC 5737, guaranteed non-routable in production,
	// and not in the private ranges blocked by isPrivateIP.
	_, err := h.CreateEndpoint(context.Background(), &webhookv1.CreateEndpointRequest{
		TenantId: "22222222-2222-2222-2222-222222222222",
		Url:      "https://198.51.100.1/hook",
		Events:   []string{"push.completed"},
		Secret:   "secret",
	})
	if err == nil {
		t.Fatal("expected error from repo")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("code: got %v, want %v", st.Code(), codes.Internal)
	}
}

// TestDeleteEndpoint_emptyIDs verifies that empty endpoint_id or tenant_id
// returns codes.InvalidArgument.
func TestDeleteEndpoint_emptyIDs(t *testing.T) {
	h := makeHandler(t, &fakeRepo{})

	_, err := h.DeleteEndpoint(context.Background(), &webhookv1.DeleteEndpointRequest{
		EndpointId: "",
		TenantId:   "22222222-2222-2222-2222-222222222222",
	})
	if err == nil {
		t.Fatal("expected error for empty endpoint_id")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestDeleteEndpoint_invalidUUID verifies that a non-UUID endpoint_id returns
// codes.InvalidArgument.
func TestDeleteEndpoint_invalidUUID(t *testing.T) {
	h := makeHandler(t, &fakeRepo{})
	_, err := h.DeleteEndpoint(context.Background(), &webhookv1.DeleteEndpointRequest{
		EndpointId: "not-a-uuid",
		TenantId:   "22222222-2222-2222-2222-222222222222",
	})
	if err == nil {
		t.Fatal("expected error for invalid endpoint UUID")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want %v", st.Code(), codes.InvalidArgument)
	}
}

// TestDeleteEndpoint_repoError verifies that a DB error returns codes.Internal.
func TestDeleteEndpoint_repoError(t *testing.T) {
	fake := &fakeRepo{deleteEndpointErr: errors.New("db error")}
	h := makeHandler(t, fake)

	_, err := h.DeleteEndpoint(context.Background(), &webhookv1.DeleteEndpointRequest{
		EndpointId: "11111111-1111-1111-1111-111111111111",
		TenantId:   "22222222-2222-2222-2222-222222222222",
	})
	if err == nil {
		t.Fatal("expected error from repo")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.Internal {
		t.Errorf("code: got %v, want %v", st.Code(), codes.Internal)
	}
}

// TestEndpointToProto_fieldsMapping verifies the internal endpointToProto helper
// correctly maps all fields from an EndpointRecord to the protobuf Endpoint.
func TestEndpointToProto_fieldsMapping(t *testing.T) {
	rec := sampleEndpoint()
	proto := endpointToProto(rec)

	if proto.EndpointId != rec.ID.String() {
		t.Errorf("EndpointId: got %q, want %q", proto.EndpointId, rec.ID.String())
	}
	if proto.TenantId != rec.TenantID.String() {
		t.Errorf("TenantId: got %q, want %q", proto.TenantId, rec.TenantID.String())
	}
	if proto.Url != rec.URL {
		t.Errorf("Url: got %q, want %q", proto.Url, rec.URL)
	}
	if !proto.Active {
		t.Error("Active: expected true")
	}
	if len(proto.Events) != len(rec.Events) {
		t.Errorf("Events length: got %d, want %d", len(proto.Events), len(rec.Events))
	}
	// Secret must never appear in the proto response — the Endpoint proto message
	// intentionally has no Secret field; this is enforced at the protobuf level.
	if proto.CreatedAt == nil {
		t.Error("CreatedAt should be set")
	}
}
