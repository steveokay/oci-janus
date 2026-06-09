// Package driver defines the storage backend interface and shared types
// used by all driver implementations (MinIO, S3, GCS, Azure, filesystem).
package driver

import (
	"context"
	"io"
	"time"
)

// BlobInfo contains metadata returned by StatBlob.
type BlobInfo struct {
	Key         string
	Size        int64
	ContentType string
	LastModified time.Time
}

// CompletedPart represents an uploaded multipart part.
type CompletedPart struct {
	PartNum int32
	ETag    string
}

// Driver is the storage backend interface all drivers must implement.
// No driver is the "default" — STORAGE_DRIVER must be set explicitly.
type Driver interface {
	// Blob operations
	PutBlob(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	GetBlob(ctx context.Context, key string) (io.ReadCloser, int64, error)
	StatBlob(ctx context.Context, key string) (BlobInfo, error)
	DeleteBlob(ctx context.Context, key string) error
	BlobExists(ctx context.Context, key string) (bool, error)

	// Multipart uploads (for large blobs)
	InitiateMultipart(ctx context.Context, key string) (uploadID string, err error)
	UploadPart(ctx context.Context, key, uploadID string, partNum int32, r io.Reader, size int64) (etag string, err error)
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []CompletedPart) error
	AbortMultipart(ctx context.Context, key, uploadID string) error

	// Listing (for GC)
	ListBlobs(ctx context.Context, prefix string) ([]string, error)

	// Health
	Ping(ctx context.Context) error
}
