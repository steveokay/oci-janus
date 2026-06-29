// RED-FU-011 — smoke tests for buildGRPCOptions, the Phase 3.4 helper
// that threads the SingleTenantInjector through the shared interceptor
// chain. libs/tenant/bootstrap already has bufconn-based coverage for
// the post-dial FetchTenantID call itself; this file covers the
// per-service signature added in Phase 3.4 #1 (PR #162).
//
// What we verify:
//   - nil extraUnary returns the baseline option set with no error
//   - non-nil extraUnary returns the same option count (interceptor goes
//     INTO the unary chain, not as a new ServerOption — common foot-gun)
//   - bad mTLS cert paths return an error rather than crashing
//
// We deliberately do NOT spin up a bufconn server here. That would
// duplicate the interceptor-ordering coverage already living in
// libs/middleware/grpc and add no per-service value.

package server

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc"

	"github.com/steveokay/oci-janus/services/signer/internal/config"
)

func TestBuildGRPCOptions_NilExtraUnary(t *testing.T) {
	opts, err := buildGRPCOptions(&config.Config{}, nil)
	if err != nil {
		t.Fatalf("buildGRPCOptions(nil): unexpected err: %v", err)
	}
	// OTEL stats handler + ChainUnaryInterceptor + ChainStreamInterceptor.
	// mTLS is absent (paths empty) so no grpc.Creds is appended.
	if len(opts) < 3 {
		t.Errorf("expected at least 3 server options without mTLS, got %d", len(opts))
	}
}

func TestBuildGRPCOptions_WithExtraUnary(t *testing.T) {
	// Sentinel interceptor — we don't invoke it, we just verify it does
	// not change the returned option count. It must be folded into the
	// ChainUnaryInterceptor option, not appended as a new ServerOption.
	noop := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return handler(ctx, req)
	}

	withNil, err := buildGRPCOptions(&config.Config{}, nil)
	if err != nil {
		t.Fatalf("buildGRPCOptions(nil): %v", err)
	}
	withExtra, err := buildGRPCOptions(&config.Config{}, noop)
	if err != nil {
		t.Fatalf("buildGRPCOptions(extra): %v", err)
	}

	if len(withExtra) != len(withNil) {
		t.Errorf("extraUnary must thread INTO the unary chain (no new ServerOption); "+
			"got %d opts with nil vs %d with non-nil", len(withNil), len(withExtra))
	}
}

func TestBuildGRPCOptions_BadMTLSPaths(t *testing.T) {
	cfg := &config.Config{}
	cfg.MTLSCACertPath = "/nonexistent/ca.crt"
	cfg.MTLSCertPath = "/nonexistent/cert.crt"
	cfg.MTLSKeyPath = "/nonexistent/key.pem"

	if _, err := buildGRPCOptions(cfg, nil); err == nil {
		t.Fatal("buildGRPCOptions with bogus mTLS paths must return an error, got nil")
	} else if !strings.Contains(err.Error(), "mTLS") && !strings.Contains(err.Error(), "cert") {
		// Loose check: the error must mention the cert/mTLS failure so
		// the operator can see what went wrong. Exact wording differs
		// across services.
		t.Errorf("error should mention mTLS / cert failure, got: %v", err)
	}
}
