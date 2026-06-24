// Package codes_test verifies the gRPC → HTTP status code mapping table.
// All expected mappings are tested, including the default fallback.
package codes

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMapDBError covers the REM-016 SQLSTATE-aware paths. Each PG code
// gets a synthetic *pgconn.PgError so we don't need a live database.
func TestMapDBError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantSub  string // substring of the gRPC error message
	}{
		{
			name:     "context.DeadlineExceeded → ResourceExhausted",
			err:      context.DeadlineExceeded,
			wantCode: codes.ResourceExhausted,
			wantSub:  "connection pool",
		},
		{
			name:     "23503 FK violation → NotFound with constraint name",
			err:      &pgconn.PgError{Code: "23503", ConstraintName: "fk_repo_org"},
			wantCode: codes.NotFound,
			wantSub:  "fk_repo_org",
		},
		{
			name:     "23505 unique violation → AlreadyExists with constraint name",
			err:      &pgconn.PgError{Code: "23505", ConstraintName: "uq_tenant_domain"},
			wantCode: codes.AlreadyExists,
			wantSub:  "uq_tenant_domain",
		},
		{
			name:     "23514 check violation → InvalidArgument with constraint name",
			err:      &pgconn.PgError{Code: "23514", ConstraintName: "format_allowlist"},
			wantCode: codes.InvalidArgument,
			wantSub:  "format_allowlist",
		},
		{
			name:     "23502 not-null violation → InvalidArgument with column name",
			err:      &pgconn.PgError{Code: "23502", ColumnName: "tenant_id"},
			wantCode: codes.InvalidArgument,
			wantSub:  "tenant_id",
		},
		{
			name:     "unknown PG code → Internal with fallback",
			err:      &pgconn.PgError{Code: "42P01"}, // undefined_table
			wantCode: codes.Internal,
			wantSub:  "fallback hit",
		},
		{
			name:     "non-PG error → Internal with fallback",
			err:      errors.New("plain old error"),
			wantCode: codes.Internal,
			wantSub:  "fallback hit",
		},
		{
			name:     "wrapped FK violation still detected via errors.As",
			err:      fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23503", ConstraintName: "fk_repo_org"}),
			wantCode: codes.NotFound,
			wantSub:  "fk_repo_org",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := MapDBError(tc.err, "fallback hit")
			st, ok := status.FromError(got)
			if !ok {
				t.Fatalf("MapDBError returned non-status error: %v", got)
			}
			if st.Code() != tc.wantCode {
				t.Errorf("code = %v, want %v", st.Code(), tc.wantCode)
			}
			if tc.wantSub != "" && !contains(st.Message(), tc.wantSub) {
				t.Errorf("message = %q, want substring %q", st.Message(), tc.wantSub)
			}
		})
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

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
