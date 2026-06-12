// Package service_test provides helpers shared across test files in this package.
// These helpers never connect to the network or a database.
package service

import (
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	authv1 "github.com/steveokay/oci-janus/proto/gen/go/auth/v1"
)

// newNotFoundErr returns a gRPC error with status code NotFound, used in tests
// to verify that isGRPCNotFound recognises the correct code.
func newNotFoundErr() error {
	return status.Error(codes.NotFound, "not found")
}

// newInternalErr returns a gRPC error with status code Internal, used in tests
// to confirm that isGRPCNotFound returns false for non-NotFound codes.
func newInternalErr() error {
	return status.Error(codes.Internal, "internal error")
}

// buildClaims creates a TokenClaims with a single RepositoryAccess entry for
// the given repository name and action list. Used to test HasAction without
// needing a live gRPC call.
func buildClaims(repoName string, actions []string) *TokenClaims {
	return &TokenClaims{
		UserID:   "user-1",
		TenantID: "tenant-1",
		Access: []*authv1.RepositoryAccess{
			{
				Name:    repoName,
				Actions: actions,
			},
		},
	}
}
