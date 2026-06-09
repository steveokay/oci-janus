// Package codes maps gRPC status codes to HTTP status codes for use at service
// boundaries where a gRPC error must be translated into an HTTP response.
package codes

import (
	"net/http"

	"google.golang.org/grpc/codes"
)

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
