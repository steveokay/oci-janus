package driver

import (
	"context"
	"io"
	"time"
)

// Driver is the interface all storage backends must implement.
// No service writes blobs directly — all I/O goes through registry-storage,
// which uses this interface internally.
type Driver interface {
	PutBlob(ctx context.Context, key string, r io.Reader, size int64, contentType string) error
	GetBlob(ctx context.Context, key string) (io.ReadCloser, int64, error)
	StatBlob(ctx context.Context, key string) (BlobInfo, error)
	DeleteBlob(ctx context.Context, key string) error
	BlobExists(ctx context.Context, key string) (bool, error)

	InitiateMultipart(ctx context.Context, key string) (uploadID string, err error)
	UploadPart(ctx context.Context, key, uploadID string, partNum int, r io.Reader, size int64) (etag string, err error)
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []CompletedPart) error
	AbortMultipart(ctx context.Context, key, uploadID string) error

	ListBlobs(ctx context.Context, prefix string) ([]string, error)

	Ping(ctx context.Context) error
}

type BlobInfo struct {
	Key         string
	Size        int64
	ContentType string
	LastModified time.Time
}

type CompletedPart struct {
	PartNum int
	ETag    string
}
