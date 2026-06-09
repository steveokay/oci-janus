// Package handler contains the gRPC server implementation for registry-storage.
package handler

import (
	"context"
	"errors"
	"io"
	"os"

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
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return status.Error(codes.NotFound, "blob not found")
	}
	return status.Error(codes.Internal, err.Error())
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
		return mapErr(putErr)
	}

	return stream.SendAndClose(&storagev1.PutBlobResponse{
		Key:          meta.Key,
		BytesWritten: meta.Size,
	})
}

// GetBlob streams a blob to the client in 256 KiB chunks.
func (h *StorageHandler) GetBlob(req *storagev1.GetBlobRequest, stream storagev1.StorageService_GetBlobServer) error {
	rc, _, err := h.drv.GetBlob(stream.Context(), req.Key)
	if err != nil {
		return mapErr(err)
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
			return mapErr(readErr)
		}
	}
}

// StatBlob returns metadata for a stored blob.
func (h *StorageHandler) StatBlob(ctx context.Context, req *storagev1.StatBlobRequest) (*storagev1.StatBlobResponse, error) {
	info, err := h.drv.StatBlob(ctx, req.Key)
	if err != nil {
		return nil, mapErr(err)
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
	if err := h.drv.DeleteBlob(ctx, req.Key); err != nil {
		return nil, mapErr(err)
	}
	return &storagev1.DeleteBlobResponse{}, nil
}

// BlobExists checks whether a key exists.
func (h *StorageHandler) BlobExists(ctx context.Context, req *storagev1.BlobExistsRequest) (*storagev1.BlobExistsResponse, error) {
	exists, err := h.drv.BlobExists(ctx, req.Key)
	if err != nil {
		return nil, mapErr(err)
	}
	return &storagev1.BlobExistsResponse{Exists: exists}, nil
}

// ListBlobs streams all blob keys under the given prefix.
func (h *StorageHandler) ListBlobs(req *storagev1.ListBlobsRequest, stream storagev1.StorageService_ListBlobsServer) error {
	prefix := req.Prefix
	if prefix == "" {
		prefix = "blobs/"
	}
	keys, err := h.drv.ListBlobs(stream.Context(), prefix)
	if err != nil {
		return mapErr(err)
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
	uploadID, err := h.drv.InitiateMultipart(ctx, req.Key)
	if err != nil {
		return nil, mapErr(err)
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
		return mapErr(putErr)
	}

	return stream.SendAndClose(&storagev1.UploadPartResponse{Etag: etag, PartNum: meta.PartNum})
}

// CompleteMultipart finalises a multipart upload.
func (h *StorageHandler) CompleteMultipart(ctx context.Context, req *storagev1.CompleteMultipartRequest) (*storagev1.CompleteMultipartResponse, error) {
	parts := make([]driver.CompletedPart, len(req.Parts))
	for i, p := range req.Parts {
		parts[i] = driver.CompletedPart{PartNum: p.PartNum, ETag: p.Etag}
	}
	if err := h.drv.CompleteMultipart(ctx, req.Key, req.UploadId, parts); err != nil {
		return nil, mapErr(err)
	}
	return &storagev1.CompleteMultipartResponse{Key: req.Key}, nil
}

// AbortMultipart cancels an in-progress multipart upload.
func (h *StorageHandler) AbortMultipart(ctx context.Context, req *storagev1.AbortMultipartRequest) (*storagev1.AbortMultipartResponse, error) {
	if err := h.drv.AbortMultipart(ctx, req.Key, req.UploadId); err != nil {
		return nil, mapErr(err)
	}
	return &storagev1.AbortMultipartResponse{}, nil
}
