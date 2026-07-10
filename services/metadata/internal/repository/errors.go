package repository

import "errors"

var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	// ErrImmutableTag is returned when a write would move a tag protected
	// by the repo-wide immutable_tags flag OR the per-tag immutable pin.
	// FUT-020's PromoteTag maps this to gRPC FailedPrecondition; other
	// callers (services/core.checkTagImmutable) may map it differently.
	ErrImmutableTag = errors.New("tag immutable")
	// ErrInvalidPageToken is returned when a caller-supplied keyset
	// page_token cannot be decoded (bad base64, wrong shape, or an
	// unparseable cursor field). It is caller error, not a server fault,
	// so handlers map it to codes.InvalidArgument rather than letting it
	// fall through MapDBError into codes.Internal (FUT-023 PR #293 review).
	ErrInvalidPageToken = errors.New("invalid page_token")
)

func isUniqueViolation(err error) bool {
	type pgErr interface{ SQLState() string }
	var pe pgErr
	if errors.As(err, &pe) {
		return pe.SQLState() == "23505"
	}
	return false
}
