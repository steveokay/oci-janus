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
)
