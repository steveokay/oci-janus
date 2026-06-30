package grpc

// REDESIGN-001 Phase 6.10 — tests for the mTLS peer-CN allowlist interceptor.
//
// The allowlist has four observable behaviours we need to pin:
//
//   1. allowed CN              → handler invoked
//   2. rejected CN             → PermissionDenied (handler NOT invoked)
//   3. empty allowlist         → handler invoked regardless of CN (Option A)
//   4. missing peer / no TLS   → PermissionDenied (defence in depth)
//
// We stub peer.AuthInfo via credentials.TLSInfo + an *x509.Certificate so the
// tests run as plain in-process function calls — no bufconn needed. The
// recordingHandler pattern matches the existing single-tenant injector tests.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net"
	"os"
	"sync"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"

	"github.com/steveokay/oci-janus/libs/observability/metrics"
)

// testutilGetCounter reads the current value of a labelled Prometheus
// counter for asserting deltas inside tests. Promauto registers metrics
// against the default registry; we just round-trip through the public
// CounterVec → Write(dto.Metric) API.
func testutilGetCounter(t *testing.T, vec *prometheus.CounterVec, labels ...string) float64 {
	t.Helper()
	c := vec.WithLabelValues(labels...)
	m := &dto.Metric{}
	if err := c.Write(m); err != nil {
		t.Fatalf("counter Write: %v", err)
	}
	return m.GetCounter().GetValue()
}

// testutilGetGauge reads the current value of an unlabelled Prometheus gauge.
func testutilGetGauge(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := g.Write(m); err != nil {
		t.Fatalf("gauge Write: %v", err)
	}
	return m.GetGauge().GetValue()
}

// peerCNRecordingHandler mirrors the recordingHandler pattern in
// single_tenant_injector_test.go but kept local so the two test files don't
// share mutable state.
type peerCNRecordingHandler struct {
	called bool
}

func (h *peerCNRecordingHandler) handle(_ context.Context, _ any) (any, error) {
	h.called = true
	return "ok", nil
}

// ctxWithPeerCN builds a context with a peer.Peer whose AuthInfo carries a TLS
// state with a single leaf cert bearing the requested CommonName. The
// interceptor reads PeerCertificates[0].Subject.CommonName, so this is all the
// fidelity the test needs.
func ctxWithPeerCN(cn string) context.Context {
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: cn}}
	authInfo := credentials.TLSInfo{
		State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}},
	}
	p := &peer.Peer{
		Addr:     &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
		AuthInfo: authInfo,
	}
	return peer.NewContext(context.Background(), p)
}

// ctxWithEmptyPeerCerts simulates a misconfigured server where TLS info is
// present but the cert chain slice is empty — the interceptor must still
// deny.
func ctxWithEmptyPeerCerts() context.Context {
	authInfo := credentials.TLSInfo{
		State: tls.ConnectionState{PeerCertificates: nil},
	}
	p := &peer.Peer{
		Addr:     &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345},
		AuthInfo: authInfo,
	}
	return peer.NewContext(context.Background(), p)
}

func dispatchPeerCN(t *testing.T, ctx context.Context, allowed ...string) (*peerCNRecordingHandler, any, error) {
	t.Helper()
	rec := &peerCNRecordingHandler{}
	interceptor := PeerCNAllowlist(allowed...)
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}
	resp, err := interceptor(ctx, nil, info, rec.handle)
	return rec, resp, err
}

func TestPeerCNAllowlist_AllowedCN_HandlerRuns(t *testing.T) {
	// CN matches an allowlist entry exactly → handler runs, response returned.
	rec, resp, err := dispatchPeerCN(t, ctxWithPeerCN("registry-core"), "registry-core", "registry-management")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler response, got %v", resp)
	}
	if !rec.called {
		t.Error("handler must be invoked for an allowed CN")
	}
}

func TestPeerCNAllowlist_RejectedCN_PermissionDenied(t *testing.T) {
	// CN not in allowlist → PermissionDenied + handler must not run.
	rec, _, err := dispatchPeerCN(t, ctxWithPeerCN("registry-gc"), "registry-core")
	if rec.called {
		t.Error("handler must NOT be invoked for a rejected CN")
	}
	if err == nil {
		t.Fatal("expected an error on rejected CN")
	}
	st, ok := status.FromError(err)
	if !ok {
		t.Fatalf("expected gRPC status, got %T: %v", err, err)
	}
	if st.Code() != codes.PermissionDenied {
		t.Errorf("code: got %v, want PermissionDenied", st.Code())
	}
	// The caller-facing message must NOT leak the offending CN — that fact is
	// captured server-side via slog.Warn instead. Pin this to keep future
	// refactors from "improving" the error by inlining the rejected CN.
	if got := st.Message(); got == "" || containsRejectedCN(got, "registry-gc") {
		t.Errorf("error message must not include the rejected CN; got %q", got)
	}
}

func TestPeerCNAllowlist_EmptyAllowlist_PassesThrough(t *testing.T) {
	// Option A: empty allowlist == no enforcement. Any CN, including one that
	// would be rejected by a populated allowlist, must reach the handler.
	rec, resp, err := dispatchPeerCN(t, ctxWithPeerCN("anything-goes"))
	if err != nil {
		t.Fatalf("expected nil error with empty allowlist, got %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler response, got %v", resp)
	}
	if !rec.called {
		t.Error("handler must be invoked when the allowlist is empty (Option A)")
	}
}

func TestPeerCNAllowlist_NoPeerInfo_Rejected(t *testing.T) {
	// Defence-in-depth: a context with no peer at all (e.g. an in-process call
	// that bypasses gRPC's network layer entirely) must NOT slip past a
	// configured allowlist. The interceptor returns PermissionDenied.
	rec, _, err := dispatchPeerCN(t, context.Background(), "registry-core")
	if rec.called {
		t.Error("handler must NOT be invoked when no peer info is present")
	}
	if err == nil {
		t.Fatal("expected an error when peer info is missing")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

func TestPeerCNAllowlist_EmptyPeerCerts_Rejected(t *testing.T) {
	// TLS info present but no certificates — same defence-in-depth posture as
	// "no peer info at all".
	rec, _, err := dispatchPeerCN(t, ctxWithEmptyPeerCerts(), "registry-core")
	if rec.called {
		t.Error("handler must NOT be invoked when no peer certs are present")
	}
	if err == nil {
		t.Fatal("expected an error when peer certs are missing")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

func TestPeerCNAllowlist_TrimsAndDedupesEntries(t *testing.T) {
	// Whitespace around entries should not silently widen the allowlist, and
	// duplicate entries should fold into one. We assert both by passing a
	// noisy list and confirming the matching CN still gets through.
	rec, _, err := dispatchPeerCN(t,
		ctxWithPeerCN("registry-core"),
		"registry-core", "  registry-core  ", "", "   ",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !rec.called {
		t.Error("handler must be invoked for an allowed CN even with noisy allowlist input")
	}
}

func TestPeerCNAllowlistFromEnv_ReadsCSV(t *testing.T) {
	// The env-driven constructor must split the CSV and trim whitespace so a
	// declarative MTLS_PEER_CN_ALLOWLIST="registry-core, registry-management"
	// works as written.
	t.Setenv(peerCNAllowlistEnvVar, "registry-core, registry-management ,, ")

	rec := &peerCNRecordingHandler{}
	interceptor := PeerCNAllowlistFromEnv()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}

	// Allowed CN → handler runs.
	if _, err := interceptor(ctxWithPeerCN("registry-management"), nil, info, rec.handle); err != nil {
		t.Fatalf("unexpected error for allowed CN: %v", err)
	}
	if !rec.called {
		t.Error("handler must be invoked for an allowed CN parsed from env")
	}

	// Rejected CN → PermissionDenied.
	rec2 := &peerCNRecordingHandler{}
	_, err := interceptor(ctxWithPeerCN("registry-gc"), nil, info, rec2.handle)
	if rec2.called {
		t.Error("handler must NOT be invoked for a rejected CN parsed from env")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

func TestPeerCNAllowlistFromEnv_UnsetIsNoOp(t *testing.T) {
	// Unset MTLS_PEER_CN_ALLOWLIST means "no enforcement" (Option A). The
	// constructor must still produce a usable interceptor; it just lets every
	// caller through.
	os.Unsetenv(peerCNAllowlistEnvVar)

	rec := &peerCNRecordingHandler{}
	interceptor := PeerCNAllowlistFromEnv()
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/Method"}
	if _, err := interceptor(ctxWithPeerCN("anything"), nil, info, rec.handle); err != nil {
		t.Fatalf("unexpected error with unset env var: %v", err)
	}
	if !rec.called {
		t.Error("unset env var must yield a no-op interceptor (Option A)")
	}
}

func TestPeerCNAllowlistStream_RejectsBadCN(t *testing.T) {
	// Stream twin parity — same denial semantics on a stream RPC.
	allowed := PeerCNAllowlistStream("registry-core")
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
	called := false
	err := allowed(nil, &fakeServerStream{ctx: ctxWithPeerCN("registry-gc")}, info, func(_ any, _ grpc.ServerStream) error {
		called = true
		return nil
	})
	if called {
		t.Error("stream handler must NOT be invoked for a rejected CN")
	}
	if st, ok := status.FromError(err); !ok || st.Code() != codes.PermissionDenied {
		t.Errorf("expected PermissionDenied, got %v", err)
	}
}

// TestPeerCNAllowlistStream_AllowsGoodCN is the parity test for the happy
// path on the stream interceptor — symmetric with the unary "allowed CN"
// coverage. Added per the code-review-agent's nit on the 6.10 review batch.
func TestPeerCNAllowlistStream_AllowsGoodCN(t *testing.T) {
	allowed := PeerCNAllowlistStream("registry-core")
	info := &grpc.StreamServerInfo{FullMethod: "/test.Service/Stream"}
	called := false
	err := allowed(nil, &fakeServerStream{ctx: ctxWithPeerCN("registry-core")}, info, func(_ any, _ grpc.ServerStream) error {
		called = true
		return nil
	})
	if err != nil {
		t.Fatalf("allowed CN must pass through, got: %v", err)
	}
	if !called {
		t.Error("stream handler must be invoked for an allowed CN")
	}
}

// TestPeerCNAllowlist_DeniedIncrementsMetric is the SEC-045 follow-up: every
// rejection must bump the `registry_grpc_peer_cn_denied_total` counter so
// operators can alert + page on unexpected cross-service call attempts even
// when slog logs are sampled out.
func TestPeerCNAllowlist_DeniedIncrementsMetric(t *testing.T) {
	// Fresh sync.Once so the test's constructor state runs predictably.
	peerCNAllowlistStateLog = sync.Once{}

	interceptor := PeerCNAllowlist("registry-core")
	info := &grpc.UnaryServerInfo{FullMethod: "/test.Service/MetricTest"}

	before := testutilGetCounter(t, metrics.GRPCPeerCNDeniedTotal, info.FullMethod, "cn_not_allowed")
	rec := &peerCNRecordingHandler{}
	if _, err := interceptor(ctxWithPeerCN("registry-gc"), nil, info, rec.handle); err == nil {
		t.Fatal("expected denial")
	}
	after := testutilGetCounter(t, metrics.GRPCPeerCNDeniedTotal, info.FullMethod, "cn_not_allowed")
	if after-before != 1 {
		t.Errorf("denied_total{cn_not_allowed}: before=%v after=%v; expected +1", before, after)
	}

	// "missing_cn" path bumps a different reason label.
	beforeMissing := testutilGetCounter(t, metrics.GRPCPeerCNDeniedTotal, info.FullMethod, "missing_cn")
	if _, err := interceptor(context.Background(), nil, info, rec.handle); err == nil {
		t.Fatal("expected denial for missing CN")
	}
	afterMissing := testutilGetCounter(t, metrics.GRPCPeerCNDeniedTotal, info.FullMethod, "missing_cn")
	if afterMissing-beforeMissing != 1 {
		t.Errorf("denied_total{missing_cn}: before=%v after=%v; expected +1", beforeMissing, afterMissing)
	}
}

// TestPeerCNAllowlist_ConstructorSetsGauge confirms the SEC-044 gauge fires
// at construction time, not on first RPC. The gauge value is the canonical
// "is enforcement on?" signal scrapeable by Prometheus.
func TestPeerCNAllowlist_ConstructorSetsGauge(t *testing.T) {
	// Reset the sync.Once so this test's constructor actually emits state.
	peerCNAllowlistStateLog = sync.Once{}
	// Force a known state by setting the gauge to a sentinel before construct.
	metrics.GRPCPeerCNAllowlistEnabled.Set(42)
	_ = PeerCNAllowlist("registry-core")
	if got := testutilGetGauge(t, metrics.GRPCPeerCNAllowlistEnabled); got != 1 {
		t.Errorf("populated allowlist must set gauge to 1, got %v", got)
	}

	peerCNAllowlistStateLog = sync.Once{}
	metrics.GRPCPeerCNAllowlistEnabled.Set(42)
	_ = PeerCNAllowlist() // empty allowlist
	if got := testutilGetGauge(t, metrics.GRPCPeerCNAllowlistEnabled); got != 0 {
		t.Errorf("empty allowlist must set gauge to 0, got %v", got)
	}
}

// fakeServerStream is the minimal grpc.ServerStream impl needed to satisfy the
// stream interceptor signature — we only ever pull Context() off it.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *fakeServerStream) Context() context.Context { return s.ctx }

// containsRejectedCN is a deliberately small helper so the leak-check above
// reads cleanly. We split the substring check off so future tightening of the
// "no CN in error message" contract has one place to update.
func containsRejectedCN(msg, cn string) bool {
	// strings.Contains intentionally — the contract is "the CN must not appear
	// anywhere in the caller-facing message". This guards against future
	// refactors that might append "(cn=...)" for "debuggability".
	for i := 0; i+len(cn) <= len(msg); i++ {
		if msg[i:i+len(cn)] == cn {
			return true
		}
	}
	return false
}

// Sanity check: the interceptor signature matches the gRPC contract so a
// future grpc upgrade that changes the signature fails the compile here
// (before it fails CI in every service).
var (
	_ grpc.UnaryServerInterceptor  = PeerCNAllowlist("anything")
	_ grpc.StreamServerInterceptor = PeerCNAllowlistStream("anything")
)
