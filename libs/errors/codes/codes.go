// Package codes maps gRPC status codes to HTTP status codes for use at service
// boundaries where a gRPC error must be translated into an HTTP response.
package codes

import (
	"context"
	"errors"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MapDBError maps a repository/database error to a gRPC status error.
// context.DeadlineExceeded from pgxpool.Acquire (pool exhaustion) is mapped
// to ResourceExhausted so callers back off rather than retrying immediately.
// All other errors become Internal with the supplied fallback message.
func MapDBError(err error, fallbackMsg string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.ResourceExhausted, "database connection pool exhausted")
	}
	return status.Error(codes.Internal, fallbackMsg)
}

// GRPCToHTTP maps a gRPC status code to an HTTP status code.
func GRPCToHTTP(c codes.Code) int {
	switch c {
	case codes.OK:
		return http.StatusOK
	case codes.InvalidArgument:
		return http.StatusBadRequest
	case codes.NotFound:
		return http.StatusNotFound
	case codes.AlreadyExists:
		return http.StatusConflict
	case codes.PermissionDenied:
		return http.StatusForbidden
	case codes.Unauthenticated:
		return http.StatusUnauthorized
	case codes.ResourceExhausted:
		return http.StatusTooManyRequests
	case codes.Unimplemented:
		return http.StatusNotImplemented
	case codes.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}
