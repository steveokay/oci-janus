// Package blobfetcher implements the scanner plugin.BlobFetcher interface
// by streaming blobs from the registry-storage gRPC service.
package blobfetcher

import (
	"context"
	"fmt"
	"io"
	"strings"

	"google.golang.org/grpc"

	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
)

// Fetcher retrieves blobs from registry-storage via gRPC streaming.
type Fetcher struct {
	client   storagev1.StorageServiceClient
	tenantID string
}

// New creates a Fetcher scoped to a specific tenant.
func New(conn *grpc.ClientConn, tenantID string) *Fetcher {
	return &Fetcher{
		client:   storagev1.NewStorageServiceClient(conn),
		tenantID: tenantID,
	}
}

// FetchBlob streams a blob from storage and returns an io.ReadCloser.
// The caller must close the returned reader.
func (f *Fetcher) FetchBlob(ctx context.Context, digest string) (io.ReadCloser, error) {
	key := blobKey(f.tenantID, digest)
	stream, err := f.client.GetBlob(ctx, &storagev1.GetBlobRequest{
		Key:      key,
		TenantId: f.tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("GetBlob %s: %w", digest, err)
	}
	return &streamReader{stream: stream}, nil
}

// blobKey builds the storage key for a blob.
// Format: blobs/<tenant_id>/sha256/<first2>/<full_sha256_hex>
func blobKey(tenantID, digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	if len(hex) < 2 {
		return fmt.Sprintf("blobs/%s/sha256/%s", tenantID, hex)
	}
	return fmt.Sprintf("blobs/%s/sha256/%s/%s", tenantID, hex[:2], hex)
}

// streamReader wraps a GetBlob gRPC stream as an io.ReadCloser.
type streamReader struct {
	stream storagev1.StorageService_GetBlobClient
	buf    []byte
	pos    int
}

func (r *streamReader) Read(p []byte) (int, error) {
	for r.pos >= len(r.buf) {
		msg, err := r.stream.Recv()
		if err != nil {
			return 0, err // io.EOF or error
		}
		r.buf = msg.Chunk
		r.pos = 0
	}
	n := copy(p, r.buf[r.pos:])
	r.pos += n
	return n, nil
}

func (r *streamReader) Close() error { return nil }
