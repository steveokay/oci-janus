// Package codes_test verifies the gRPC → HTTP status code mapping table.
// All expected mappings are tested, including the default fallback.
package codes

import (
	"net/http"
	"testing"

	"google.golang.org/grpc/codes"
)

// TestGRPCToHTTP_tabledriven exercises every branch of GRPCToHTTP to ensure
// the mapping table matches the standard RFC/gRPC conventions.
func TestGRPCToHTTP_tabledriven(t *testing.T) {
	tests := []struct {
		name     string
		grpc     codes.Code
		wantHTTP int
	}{
		{name: "OK → 200", grpc: codes.OK, wantHTTP: http.StatusOK},
		{name: "InvalidArgument → 400", grpc: codes.InvalidArgument, wantHTTP: http.StatusBadRequest},
		{name: "NotFound → 404", grpc: codes.NotFound, wantHTTP: http.StatusNotFound},
		{name: "AlreadyExists → 409", grpc: codes.AlreadyExists, wantHTTP: http.StatusConflict},
		{name: "PermissionDenied → 403", grpc: codes.PermissionDenied, wantHTTP: http.StatusForbidden},
		{name: "Unauthenticated → 401", grpc: codes.Unauthenticated, wantHTTP: http.StatusUnauthorized},
		{name: "ResourceExhausted → 429", grpc: codes.ResourceExhausted, wantHTTP: http.StatusTooManyRequests},
		{name: "Unimplemented → 501", grpc: codes.Unimplemented, wantHTTP: http.StatusNotImplemented},
		{name: "Unavailable → 503", grpc: codes.Unavailable, wantHTTP: http.StatusServiceUnavailable},
		// Default fallback for any unmapped code.
		{name: "Internal → 500 (default)", grpc: codes.Internal, wantHTTP: http.StatusInternalServerError},
		{name: "DeadlineExceeded → 500 (default)", grpc: codes.DeadlineExceeded, wantHTTP: http.StatusInternalServerError},
		{name: "Canceled → 500 (default)", grpc: codes.Canceled, wantHTTP: http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := GRPCToHTTP(tc.grpc)
			if got != tc.wantHTTP {
				t.Errorf("GRPCToHTTP(%v) = %d, want %d", tc.grpc, got, tc.wantHTTP)
			}
		})
	}
}
