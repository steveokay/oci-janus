package service

import "errors"

var (
	ErrUnauthorized   = errors.New("unauthorized")
	ErrForbidden      = errors.New("forbidden")
	ErrNotFound       = errors.New("not found")
	ErrDigestMismatch = errors.New("digest mismatch")
	ErrQuotaExceeded  = errors.New("quota exceeded")
	ErrInvalidName    = errors.New("invalid repository name")
	ErrInvalidDigest  = errors.New("invalid digest format")
	ErrUploadNotFound = errors.New("upload not found")
	// ErrTagImmutable is returned when a PutManifest would move an
	// existing tag whose parent repository has `immutable_tags=true`
	// OR the tag itself has `immutable=true`. The HTTP layer maps this
	// to MANIFEST_INVALID per the OCI Distribution Spec § 4.2.2.
	// Futures.md Tier 1 #2.
	ErrTagImmutable = errors.New("tag is immutable")
)
