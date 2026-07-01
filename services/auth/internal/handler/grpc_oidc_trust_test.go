// Package handler — grpc_oidc_trust_test.go covers the FUT-001 RPC
// handlers. The service-layer tests (oidc_trust_test.go) already
// exercise every validation branch + the 7 reject reasons against in-
// memory fakes; here we focus on the handler-specific guard:
//
//   - Every OIDC RPC returns codes.Unimplemented when the handler was
//     built without an OIDCTrustService (operator hasn't set
//     OIDC_ALLOWED_ISSUERS).
//
// The InvalidArgument and happy-path branches require a real
// OIDCTrustService wired into the handler. Those paths are equivalently
// exercised at the service layer + via integration tests on the
// BFF/handler chain in a follow-up; duplicating them here adds little.

package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

// TestOIDCRPCs_UnimplementedWhenNotConfigured asserts every OIDC RPC
// returns codes.Unimplemented when the handler was built without an
// OIDCTrustService. This is the safe default — operators who haven't
// set OIDC_ALLOWED_ISSUERS see a clear "feature off" signal rather
// than a generic 5xx.
func TestOIDCRPCs_UnimplementedWhenNotConfigured(t *testing.T) {
	h := &GRPCHandler{} // no oidc svc wired
	ctx := context.Background()

	cases := []struct {
		name string
		call func() error
	}{
		{"ListOIDCTrusts", func() error {
			_, err := h.ListOIDCTrusts(ctx, &authv1.ListOIDCTrustsRequest{TenantId: uuid.New().String()})
			return err
		}},
		{"CreateOIDCTrust", func() error {
			_, err := h.CreateOIDCTrust(ctx, &authv1.CreateOIDCTrustRequest{TenantId: uuid.New().String()})
			return err
		}},
		{"UpdateOIDCTrust", func() error {
			_, err := h.UpdateOIDCTrust(ctx, &authv1.UpdateOIDCTrustRequest{TenantId: uuid.New().String()})
			return err
		}},
		{"DeleteOIDCTrust", func() error {
			_, err := h.DeleteOIDCTrust(ctx, &authv1.DeleteOIDCTrustRequest{TenantId: uuid.New().String()})
			return err
		}},
		{"ExchangeWorkloadToken", func() error {
			_, err := h.ExchangeWorkloadToken(ctx, &authv1.ExchangeWorkloadTokenRequest{OidcJwt: "anything"})
			return err
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			require.Error(t, err)
			s, ok := status.FromError(err)
			require.True(t, ok)
			require.Equal(t, codes.Unimplemented, s.Code())
		})
	}
}
