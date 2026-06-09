// Package repository contains all database access for registry-auth.
// No SQL appears outside this package.
package repository

import "errors"

// Sentinel errors returned by repository methods. Callers should use errors.Is
// rather than string comparison so the service layer can map them to gRPC codes.
var (
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
)
