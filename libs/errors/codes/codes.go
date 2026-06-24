// Package codes maps gRPC status codes to HTTP status codes for use at service
// boundaries where a gRPC error must be translated into an HTTP response.
package codes

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PostgreSQL SQLSTATE codes (subset). See
// https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	pgForeignKeyViolation = "23503"
	pgUniqueViolation     = "23505"
	pgCheckViolation      = "23514"
	pgNotNullViolation    = "23502"
)

// MapDBError maps a repository/database error to a gRPC status error so
// service handlers don't collapse every PG failure to codes.Internal
// (REM-016).
//
// Recognised cases:
//
//   - context.DeadlineExceeded (pool exhaustion from pgxpool.Acquire) →
//     ResourceExhausted so callers back off instead of retrying immediately.
//   - PG 23503 foreign-key violation → NotFound, with the ConstraintName
//     so the caller knows which parent row is missing.
//   - PG 23505 unique violation → AlreadyExists, with the ConstraintName.
//   - PG 23514 check violation → InvalidArgument, with the ConstraintName.
//   - PG 23502 not-null violation → InvalidArgument, naming the column.
//   - everything else → Internal with the supplied fallback message.
//
// The fallback message is preserved for the Internal path so existing
// log-and-grep workflows keep working. Specific codes produce more
// actionable messages because the constraint / column names are
// non-secret and tell the caller what to fix.
func MapDBError(err error, fallbackMsg string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.ResourceExhausted, "database connection pool exhausted")
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case pgForeignKeyViolation:
			return status.Errorf(codes.NotFound, "foreign key violation on constraint %q: parent row not found", pgErr.ConstraintName)
		case pgUniqueViolation:
			return status.Errorf(codes.AlreadyExists, "unique constraint %q violated", pgErr.ConstraintName)
		case pgCheckViolation:
			return status.Errorf(codes.InvalidArgument, "check constraint %q violated", pgErr.ConstraintName)
		case pgNotNullViolation:
			return status.Errorf(codes.InvalidArgument, "column %q is not nullable", pgErr.ColumnName)
		}
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
