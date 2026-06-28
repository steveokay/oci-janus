package grpc

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// REDESIGN-001 Phase 3.3 — middleware behaviour matrix is tightly
// specified, so the tests pin every quadrant:
//
//   1. multi mode (bootstrap == "")        → pass through, no md mutation
//   2. single mode + md absent             → inject bootstrap id
//   3. single mode + md == bootstrap       → pass through unchanged
//   4. single mode + md != bootstrap       → reject InvalidArgument
//   5. single mode + md empty string       → treated as "absent"
//
// Every case asserts BOTH the handler return path AND what the handler
// saw on the context, so a future refactor that flips one without the
// other is caught here.

const (
	testBootstrapID = "bootstrap-uuid"
	otherTenantID   = "some-other-uuid"
)

// recordingHandler captures the metadata it sees so each test can
// assert on the post-interceptor context shape.
type recordingHandler struct {
	called   bool
	sawValue string // value of x-tenant-id at handler call time
	mdFound  bool   // was there any incoming metadata at all
}

func (h *recordingHandler) handle(ctx context.Context, _ any) (any, error) {
	h.called = true
	md, ok := metadata.FromIncomingContext(ctx)
	h.mdFound = ok
	if ok {
		if vals := md.Get(tenantIDMetadataKey); len(vals) > 0 {
			h.sawValue = vals[0]
		}
	}
	return "ok", nil
}

// dispatch is a tiny harness that wires a fresh ctx + the interceptor
// under test + a recording handler. Returns the handler's record so the
// test can assert on what the downstream code saw.
func dispatch(t *testing.T, bootstrap string, mdPairs ...string) (*recordingHandler, any, error) {
	t.Helper()
	rec := &recordingHandler{}
	interceptor := SingleTenantInjector(bootstrap)

	ctx := context.Background()
	if len(mdPairs) > 0 {
		ctx = metadata.NewIncomingContext(ctx, metadata.Pairs(mdPairs...))
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}
	resp, err := interceptor(ctx, nil, info, rec.handle)
	return rec, resp, err
}

func TestSingleTenantInjector_MultiMode_PassesThrough(t *testing.T) {
	// bootstrap == "" → middleware must be a true no-op even when no
	// metadata is supplied. The handler runs and sees whatever the caller
	// sent (empty, in this case).
	rec, resp, err := dispatch(t, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("unexpected response: %v", resp)
	}
	if !rec.called {
		t.Error("handler must be invoked in multi mode")
	}
	if rec.sawValue != "" {
		t.Errorf("multi mode must not inject anything; handler saw %q", rec.sawValue)
	}
}

func TestSingleTenantInjector_MultiMode_LeavesExistingMDAlone(t *testing.T) {
	// Even in multi mode, if the caller DID send a tenant id, the
	// middleware must not touch it — multi mode means "trust the caller".
	rec, _, err := dispatch(t, "", tenantIDMetadataKey, "multi-tenant-real-uuid")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rec.sawValue != "multi-tenant-real-uuid" {
		t.Errorf("multi mode must pass metadata through unchanged; handler saw %q", rec.sawValue)
	}
}

func TestSingleTenantInjector_SingleMode_AbsentMetadata_InjectsBootstrap(t *testing.T) {
	// No metadata at all on the incoming ctx. The interceptor must
	// fabricate metadata + inject the bootstrap id so downstream handlers
	// see a populated value.
	rec, _, err := dispatch(t, testBootstrapID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rec.mdFound {
		t.Error("interceptor must attach metadata even when none was supplied")
	}
	if rec.sawValue != testBootstrapID {
		t.Errorf("handler saw %q, want bootstrap %q", rec.sawValue, testBootstrapID)
	}
}

func TestSingleTenantInjector_SingleMode_EmptyStringMD_TreatedAsAbsent(t *testing.T) {
	// metadata.Pairs("x-tenant-id", "") yields a single empty value —
	// the middleware must treat this the same as "absent" rather than
	// flagging it as a mismatch against the bootstrap id.
	rec, _, err := dispatch(t, testBootstrapID, tenantIDMetadataKey, "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rec.sawValue != testBootstrapID {
		t.Errorf("empty string md must be treated as absent; handler saw %q", rec.sawValue)
	}
}

func TestSingleTenantInjector_SingleMode_MatchingMD_PassesThrough(t *testing.T) {
	rec, _, err := dispatch(t, testBootstrapID, tenantIDMetadataKey, testBootstrapID)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if rec.sawValue != testBootstrapID {
		t.Errorf("handler saw %q, want %q", rec.sawValue, testBootstrapID)
	}
}

func TestSingleTenantInjector_SingleMode_MultiValueMD_FirstWins(t *testing.T) {
	// gRPC metadata is map[string][]string — a misconfigured caller could
	// send the same key twice. The interceptor reads md.Get(...)[0] so the
	// first value wins. Pin that contract so a future refactor that swaps
	// to "last value" doesn't silently shift behaviour.
	// Construct metadata directly so both values land under the same key.
	ctx := metadata.NewIncomingContext(context.Background(), metadata.MD{
		tenantIDMetadataKey: []string{testBootstrapID, otherTenantID},
	})
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}
	rec := &recordingHandler{}
	resp, err := SingleTenantInjector(testBootstrapID)(ctx, nil, info, rec.handle)

	if err != nil {
		t.Fatalf("first-value-match must pass through, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler response, got %v", resp)
	}
	if rec.sawValue != testBootstrapID {
		t.Errorf("handler saw %q, want first value %q (md.Get returns first)",
			rec.sawValue, testBootstrapID)
	}
}

func TestSingleTenantInjector_SingleMode_MismatchedMD_RejectsInvalidArgument(t *testing.T) {
	rec, _, err := dispatch(t, testBootstrapID, tenantIDMetadataKey, otherTenantID)
	if rec.called {
		t.Error("handler must NOT be invoked on a mismatched tenant id")
	}

	if err == nil {
		t.Fatal("expected an error on mismatched tenant id")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %T: %v", err, err)
	}
	if st.Code() != codes.InvalidArgument {
		t.Errorf("code: got %v, want InvalidArgument", st.Code())
	}
	// The error message should include the mismatched value so an
	// operator chasing the ticket can see it. This is not a secret —
	// every authenticated caller can read their own tenant id.
	if !errorContains(err, otherTenantID) {
		t.Errorf("error message must include the rejected value; got %q", err.Error())
	}
}

func errorContains(err error, s string) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), s)
}

// Sanity check: the interceptor signature matches the gRPC contract so a
// future grpc upgrade that changes the signature fails the compile here
// (before it fails CI in every service).
var _ grpc.UnaryServerInterceptor = SingleTenantInjector("anything")
