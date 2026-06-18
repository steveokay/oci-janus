// Package handler contains the gRPC server implementation for registry-storage.
package handler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	"github.com/steveokay/oci-janus/services/storage/internal/driver"
)

const chunkSize = 256 * 1024 // 256 KiB per streaming chunk

// StorageHandler implements storagev1.StorageServiceServer.
type StorageHandler struct {
	storagev1.UnimplementedStorageServiceServer
	drv driver.Driver
}

// New returns a StorageHandler backed by drv.
func New(drv driver.Driver) *StorageHandler {
	return &StorageHandler{drv: drv}
}

// mapErr converts known errors to gRPC status errors.
//
// PENTEST-021: never put raw driver errors on the wire. Storage drivers
// (MinIO, S3, GCS, Azure, filesystem) often include bucket names, full
// paths, IAM principals, or signed-URL fragments in their error messages.
// Surfacing that to a caller — even an mTLS-restricted internal one —
// gives an attacker who reaches the service a lateral-movement aid.
// We log the full error server-side (with the context so trace_id and
// tenant_id are preserved by the slog handler) and return a generic
// message.
func mapErrCtx(ctx context.Context, op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return status.Error(codes.NotFound, "blob not found")
	}
	slog.ErrorContext(ctx, "storage op failed", "op", op, "error", err)
	return status.Error(codes.Internal, "internal error")
}

// keyRoots are the three top-level prefixes documented in CLAUDE.md §8.
// Any storage key must begin with one of these followed by "/<tenant_id>/".
var keyRoots = []string{"blobs/", "manifests/", "uploads/"}

// validateTenantKey enforces PENTEST-026 — every storage RPC must include a
// non-empty tenant_id, and the supplied key must live under that tenant's
// portion of the keyspace per the documented storage layout:
//
//	blobs/<tenant_id>/sha256/<first2>/<digest>
//	manifests/<tenant_id>/<repo_encoded>/<reference>
//	uploads/<tenant_id>/<upload_uuid>/parts/<part_num>
//
// The previous defense-in-depth gap was that the handler accepted any caller-
// supplied key. mTLS still bounded who could reach the gRPC port, but a buggy
// internal service that constructed a wrong-tenant key would have been served
// silently. With this check a tenant mismatch returns PermissionDenied and
// the operation is logged for triage.
func validateTenantKey(ctx context.Context, op, tenantID, key string) error {
	if tenantID == "" {
		return status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if key == "" {
		return status.Error(codes.InvalidArgument, "key is required")
	}
	for _, root := range keyRoots {
		if strings.HasPrefix(key, root+tenantID+"/") {
			return nil
		}
	}
	slog.WarnContext(ctx, "storage: tenant/key prefix mismatch — possible misconfigured caller (PENTEST-026)",
		"op", op,
		"tenant_id", tenantID,
		"key", key,
	)
	return status.Error(codes.PermissionDenied, "key does not belong to tenant")
}

// validateTenantPrefix is the ListBlobs equivalent of validateTenantKey:
// the caller must constrain the prefix to their own portion of the keyspace.
// Empty prefix is rejected (the previous "default to blobs/" behaviour would
// have leaked every tenant's blob keys if the handler ever exposed itself).
func validateTenantPrefix(ctx context.Context, op, tenantID, prefix string) error {
	if tenantID == "" {
		return status.Error(codes.InvalidArgument, "tenant_id is required")
	}
	if prefix == "" {
		return status.Error(codes.InvalidArgument, "prefix is required")
	}
	for _, root := range keyRoots {
		if prefix == root+tenantID || strings.HasPrefix(prefix, root+tenantID+"/") {
			return nil
		}
	}
	slog.WarnContext(ctx, "storage: tenant/prefix mismatch — possible misconfigured caller (PENTEST-026)",
		"op", op,
		"tenant_id", tenantID,
		"prefix", prefix,
	)
	return status.Error(codes.PermissionDenied, "prefix does not belong to tenant")
}

// PutBlob receives a client-streaming upload: first message is meta, subsequent are chunks.
func (h *StorageHandler) PutBlob(stream storagev1.StorageService_PutBlobServer) error {
	first, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "expected first message")
	}
	meta := first.GetMeta()
	if meta == nil {
		return status.Error(codes.InvalidArgument, "first message must contain meta")
	}
	// PENTEST-026: verify the supplied key lives under the caller's tenant prefix.
	if err := validateTenantKey(stream.Context(), "PutBlob", meta.TenantId, meta.Key); err != nil {
		return err
	}

	pr, pw := io.Pipe()
	recvErrCh := make(chan error, 1)

	go func() {
		defer pw.Close()
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				recvErrCh <- nil
				return
			}
			if err != nil {
				pw.CloseWithError(err)
				recvErrCh <- err
				return
			}
			chunk := msg.GetChunk()
			if len(chunk) == 0 {
				continue
			}
			if _, werr := pw.Write(chunk); werr != nil {
				recvErrCh <- werr
				return
			}
		}
	}()

	putErr := h.drv.PutBlob(stream.Context(), meta.Key, pr, meta.Size, meta.ContentType)
	pr.CloseWithError(putErr) // unblock goroutine if driver returned early
	if recvErr := <-recvErrCh; recvErr != nil && putErr == nil {
		putErr = recvErr
	}
	if putErr != nil {
		return mapErrCtx(stream.Context(), "PutBlob", putErr)
	}

	return stream.SendAndClose(&storagev1.PutBlobResponse{
		Key:          meta.Key,
		BytesWritten: meta.Size,
	})
}

// GetBlob streams a blob to the client in 256 KiB chunks.
func (h *StorageHandler) GetBlob(req *storagev1.GetBlobRequest, stream storagev1.StorageService_GetBlobServer) error {
	if err := validateTenantKey(stream.Context(), "GetBlob", req.TenantId, req.Key); err != nil {
		return err
	}
	rc, _, err := h.drv.GetBlob(stream.Context(), req.Key)
	if err != nil {
		return mapErrCtx(stream.Context(), "GetBlob", err)
	}
	defer rc.Close()

	buf := make([]byte, chunkSize)
	for {
		n, readErr := rc.Read(buf)
		if n > 0 {
			if sendErr := stream.Send(&storagev1.GetBlobResponse{Chunk: buf[:n]}); sendErr != nil {
				return sendErr
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return mapErrCtx(stream.Context(), "GetBlob", readErr)
		}
	}
}

// StatBlob returns metadata for a stored blob.
func (h *StorageHandler) StatBlob(ctx context.Context, req *storagev1.StatBlobRequest) (*storagev1.StatBlobResponse, error) {
	if err := validateTenantKey(ctx, "StatBlob", req.TenantId, req.Key); err != nil {
		return nil, err
	}
	info, err := h.drv.StatBlob(ctx, req.Key)
	if err != nil {
		return nil, mapErrCtx(ctx, "StatBlob", err)
	}
	return &storagev1.StatBlobResponse{
		Key:          info.Key,
		Size:         info.Size,
		ContentType:  info.ContentType,
		LastModified: timestamppb.New(info.LastModified),
	}, nil
}

// DeleteBlob removes a blob from the backend.
func (h *StorageHandler) DeleteBlob(ctx context.Context, req *storagev1.DeleteBlobRequest) (*storagev1.DeleteBlobResponse, error) {
	if err := validateTenantKey(ctx, "DeleteBlob", req.TenantId, req.Key); err != nil {
		return nil, err
	}
	if err := h.drv.DeleteBlob(ctx, req.Key); err != nil {
		return nil, mapErrCtx(ctx, "DeleteBlob", err)
	}
	return &storagev1.DeleteBlobResponse{}, nil
}

// BlobExists checks whether a key exists.
func (h *StorageHandler) BlobExists(ctx context.Context, req *storagev1.BlobExistsRequest) (*storagev1.BlobExistsResponse, error) {
	if err := validateTenantKey(ctx, "BlobExists", req.TenantId, req.Key); err != nil {
		return nil, err
	}
	exists, err := h.drv.BlobExists(ctx, req.Key)
	if err != nil {
		return nil, mapErrCtx(ctx, "BlobExists", err)
	}
	return &storagev1.BlobExistsResponse{Exists: exists}, nil
}

// ListBlobs streams all blob keys under the given prefix.
//
// PENTEST-026: prefix must be confined to the caller's tenant. The previous
// "default to blobs/" fallback was removed — it would have leaked every
// tenant's blob keys to any internal service that called with empty prefix.
func (h *StorageHandler) ListBlobs(req *storagev1.ListBlobsRequest, stream storagev1.StorageService_ListBlobsServer) error {
	if err := validateTenantPrefix(stream.Context(), "ListBlobs", req.TenantId, req.Prefix); err != nil {
		return err
	}
	keys, err := h.drv.ListBlobs(stream.Context(), req.Prefix)
	if err != nil {
		return mapErrCtx(stream.Context(), "ListBlobs", err)
	}
	for _, key := range keys {
		info, err := h.drv.StatBlob(stream.Context(), key)
		if err != nil {
			continue // blob may have been deleted concurrently
		}
		if sendErr := stream.Send(&storagev1.ListBlobsResponse{Key: key, Size: info.Size}); sendErr != nil {
			return sendErr
		}
	}
	return nil
}

// InitiateMultipart begins a multipart upload.
func (h *StorageHandler) InitiateMultipart(ctx context.Context, req *storagev1.InitiateMultipartRequest) (*storagev1.InitiateMultipartResponse, error) {
	if err := validateTenantKey(ctx, "InitiateMultipart", req.TenantId, req.Key); err != nil {
		return nil, err
	}
	uploadID, err := h.drv.InitiateMultipart(ctx, req.Key)
	if err != nil {
		return nil, mapErrCtx(ctx, "InitiateMultipart", err)
	}
	return &storagev1.InitiateMultipartResponse{UploadId: uploadID}, nil
}

// UploadPart receives a client-streaming upload for a single part.
func (h *StorageHandler) UploadPart(stream storagev1.StorageService_UploadPartServer) error {
	first, err := stream.Recv()
	if err != nil {
		return status.Error(codes.InvalidArgument, "expected first message")
	}
	meta := first.GetMeta()
	if meta == nil {
		return status.Error(codes.InvalidArgument, "first message must contain meta")
	}
	// PENTEST-026: verify the upload part belongs to the caller's tenant.
	if err := validateTenantKey(stream.Context(), "UploadPart", meta.TenantId, meta.Key); err != nil {
		return err
	}

	pr, pw := io.Pipe()
	recvErrCh := make(chan error, 1)

	go func() {
		defer pw.Close()
		for {
			msg, err := stream.Recv()
			if err == io.EOF {
				recvErrCh <- nil
				return
			}
			if err != nil {
				pw.CloseWithError(err)
				recvErrCh <- err
				return
			}
			chunk := msg.GetChunk()
			if len(chunk) > 0 {
				if _, werr := pw.Write(chunk); werr != nil {
					recvErrCh <- werr
					return
				}
			}
		}
	}()

	etag, putErr := h.drv.UploadPart(stream.Context(), meta.Key, meta.UploadId, meta.PartNum, pr, meta.Size)
	pr.CloseWithError(putErr)
	if recvErr := <-recvErrCh; recvErr != nil && putErr == nil {
		putErr = recvErr
	}
	if putErr != nil {
		return mapErrCtx(stream.Context(), "UploadPart", putErr)
	}

	return stream.SendAndClose(&storagev1.UploadPartResponse{Etag: etag, PartNum: meta.PartNum})
}

// CompleteMultipart finalises a multipart upload.
func (h *StorageHandler) CompleteMultipart(ctx context.Context, req *storagev1.CompleteMultipartRequest) (*storagev1.CompleteMultipartResponse, error) {
	if err := validateTenantKey(ctx, "CompleteMultipart", req.TenantId, req.Key); err != nil {
		return nil, err
	}
	parts := make([]driver.CompletedPart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = driver.CompletedPart{PartNum: p.PartNum, ETag: p.Etag}
	}
	if err := h.drv.CompleteMultipart(ctx, req.Key, req.UploadId, parts); err != nil {
		return nil, mapErrCtx(ctx, "CompleteMultipart", err)
	}
	return &storagev1.CompleteMultipartResponse{Key: req.Key}, nil
}

// AbortMultipart cancels an in-progress multipart upload.
func (h *StorageHandler) AbortMultipart(ctx context.Context, req *storagev1.AbortMultipartRequest) (*storagev1.AbortMultipartResponse, error) {
	if err := validateTenantKey(ctx, "AbortMultipart", req.TenantId, req.Key); err != nil {
		return nil, err
	}
	if err := h.drv.AbortMultipart(ctx, req.Key, req.UploadId); err != nil {
		return nil, mapErrCtx(ctx, "AbortMultipart", err)
	}
	return &storagev1.AbortMultipartResponse{}, nil
}
