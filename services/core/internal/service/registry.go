package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	metadatav1 "github.com/steveokay/oci-janus/proto/gen/go/metadata/v1"
	storagev1 "github.com/steveokay/oci-janus/proto/gen/go/storage/v1"
	"github.com/steveokay/oci-janus/libs/rabbitmq/events"
	"github.com/steveokay/oci-janus/libs/rabbitmq/publisher"
)

var (
	repoNameRE = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*/[a-z0-9]+([._-][a-z0-9]+)*$`)
	digestRE   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Registry is the core OCI registry service.
type Registry struct {
	metadata  metadatav1.MetadataServiceClient
	storage   storagev1.StorageServiceClient
	uploads   *UploadStore
	publisher *publisher.Publisher
}

// NewRegistry constructs a Registry.
func NewRegistry(
	metaConn *grpc.ClientConn,
	storageConn *grpc.ClientConn,
	uploads *UploadStore,
	pub *publisher.Publisher,
) *Registry {
	return &Registry{
		metadata:  metadatav1.NewMetadataServiceClient(metaConn),
		storage:   storagev1.NewStorageServiceClient(storageConn),
		uploads:   uploads,
		publisher: pub,
	}
}

// ValidateName ensures the name is org/repo format and matches allowed characters.
func ValidateName(name string) error {
	if !repoNameRE.MatchString(name) {
		return ErrInvalidName
	}
	return nil
}

// ValidateDigest ensures the digest is sha256:<hex64>.
func ValidateDigest(digest string) error {
	if !digestRE.MatchString(digest) {
		return ErrInvalidDigest
	}
	return nil
}

// blobKey returns the storage key for a blob.
func blobKey(tenantID, digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	return fmt.Sprintf("blobs/%s/sha256/%s/%s", tenantID, hex[:2], hex)
}

// --- Blob operations ---

// BlobExists checks whether a blob exists in storage.
func (r *Registry) BlobExists(ctx context.Context, tenantID, digest string) (bool, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := r.storage.BlobExists(ctx, &storagev1.BlobExistsRequest{
		Key:      blobKey(tenantID, digest),
		TenantId: tenantID,
	})
	if err != nil {
		return false, 0, fmt.Errorf("blob exists rpc: %w", err)
	}
	if !resp.GetExists() {
		return false, 0, nil
	}
	stat, err := r.storage.StatBlob(ctx, &storagev1.StatBlobRequest{
		Key:      blobKey(tenantID, digest),
		TenantId: tenantID,
	})
	if err != nil {
		return true, 0, nil
	}
	return true, stat.GetSize(), nil
}

// GetBlob streams a blob from storage into w. Returns (size, contentType, error).
func (r *Registry) GetBlob(ctx context.Context, tenantID, digest string, w io.Writer) (int64, error) {
	stream, err := r.storage.GetBlob(ctx, &storagev1.GetBlobRequest{
		Key:      blobKey(tenantID, digest),
		TenantId: tenantID,
	})
	if err != nil {
		if isGRPCNotFound(err) {
			return 0, ErrNotFound
		}
		return 0, fmt.Errorf("get blob rpc: %w", err)
	}

	var total int64
	for {
		chunk, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return total, fmt.Errorf("recv blob chunk: %w", err)
		}
		n, werr := w.Write(chunk.GetChunk())
		total += int64(n)
		if werr != nil {
			return total, fmt.Errorf("write blob chunk: %w", werr)
		}
	}
	return total, nil
}

// DeleteBlob removes a blob from storage and unlinks it from the repository.
func (r *Registry) DeleteBlob(ctx context.Context, tenantID, repoID, digest string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if _, err := r.storage.DeleteBlob(ctx, &storagev1.DeleteBlobRequest{
		Key:      blobKey(tenantID, digest),
		TenantId: tenantID,
	}); err != nil {
		if !isGRPCNotFound(err) {
			return fmt.Errorf("delete blob rpc: %w", err)
		}
	}

	if _, err := r.metadata.UnlinkBlob(ctx, &metadatav1.UnlinkBlobRequest{
		RepoId:     repoID,
		BlobDigest: digest,
	}); err != nil {
		return fmt.Errorf("unlink blob rpc: %w", err)
	}
	return nil
}

// --- Upload (chunked blob push) ---

// InitiateUpload creates a new upload session and returns the upload UUID.
func (r *Registry) InitiateUpload(ctx context.Context, tenantID, repoName string) (string, error) {
	id := uuid.New().String()
	if err := r.uploads.Create(ctx, UploadState{
		UUID:     id,
		TenantID: tenantID,
		RepoName: repoName,
		Offset:   0,
	}); err != nil {
		return "", fmt.Errorf("create upload state: %w", err)
	}
	return id, nil
}

// GetUpload returns current upload state.
func (r *Registry) GetUpload(ctx context.Context, uploadUUID string) (*UploadState, error) {
	return r.uploads.Get(ctx, uploadUUID)
}

// AppendChunk streams a chunk to storage and advances the offset.
// The caller is responsible for verifying Content-Range against the current offset.
func (r *Registry) AppendChunk(ctx context.Context, uploadUUID string, chunk io.Reader, chunkSize int64) (int64, error) {
	st, err := r.uploads.Get(ctx, uploadUUID)
	if err != nil {
		return 0, err
	}

	// stream the chunk to storage using multipart (one "part" per PATCH)
	stream, err := r.storage.UploadPart(ctx)
	if err != nil {
		return 0, fmt.Errorf("open upload-part stream: %w", err)
	}

	// first message: metadata
	if err := stream.Send(&storagev1.UploadPartRequest{
		Data: &storagev1.UploadPartRequest_Meta{
			Meta: &storagev1.UploadPartMeta{
				Key:      fmt.Sprintf("uploads/%s/%s/data", st.TenantID, uploadUUID),
				UploadId: uploadUUID,
				PartNum:  int32(st.Offset/chunkSize) + 1, // approximate part number
				TenantId: st.TenantID,
			},
		},
	}); err != nil {
		return 0, fmt.Errorf("send upload part meta: %w", err)
	}

	// stream data
	buf := make([]byte, 64*1024)
	var written int64
	for {
		n, rerr := chunk.Read(buf)
		if n > 0 {
			if err := stream.Send(&storagev1.UploadPartRequest{
				Data: &storagev1.UploadPartRequest_Chunk{Chunk: buf[:n]},
			}); err != nil {
				return written, fmt.Errorf("send chunk: %w", err)
			}
			written += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return written, fmt.Errorf("read chunk: %w", rerr)
		}
	}

	if _, err := stream.CloseAndRecv(); err != nil {
		return written, fmt.Errorf("close upload part stream: %w", err)
	}

	st.Offset += written
	if err := r.uploads.Update(ctx, st); err != nil {
		return written, fmt.Errorf("update upload state: %w", err)
	}
	return written, nil
}

// CompleteUpload finalises an upload, verifies the digest, stores the blob, and returns the digest.
func (r *Registry) CompleteUpload(ctx context.Context, uploadUUID, expectedDigest string, finalChunk io.Reader, chunkSize int64) (string, int64, error) {
	st, err := r.uploads.Get(ctx, uploadUUID)
	if err != nil {
		return "", 0, err
	}

	// If there's a final chunk (monolithic upload or last PATCH+PUT), stream it directly.
	// For simplicity we stream everything via a single PutBlob to storage.
	// In production the multipart assembly would happen here or in the storage service.

	stream, err := r.storage.PutBlob(ctx)
	if err != nil {
		return "", 0, fmt.Errorf("open put-blob stream: %w", err)
	}

	key := blobKey(st.TenantID, expectedDigest)
	if err := stream.Send(&storagev1.PutBlobRequest{
		Data: &storagev1.PutBlobRequest_Meta{
			Meta: &storagev1.PutBlobMeta{
				Key:         key,
				ContentType: "application/octet-stream",
				TenantId:    st.TenantID,
			},
		},
	}); err != nil {
		return "", 0, fmt.Errorf("send put-blob meta: %w", err)
	}

	hash := sha256.New()
	buf := make([]byte, 64*1024)
	var totalBytes int64

	for {
		n, rerr := finalChunk.Read(buf)
		if n > 0 {
			hash.Write(buf[:n])
			if err := stream.Send(&storagev1.PutBlobRequest{
				Data: &storagev1.PutBlobRequest_Chunk{Chunk: buf[:n]},
			}); err != nil {
				return "", 0, fmt.Errorf("send blob chunk: %w", err)
			}
			totalBytes += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", 0, fmt.Errorf("read blob: %w", rerr)
		}
	}

	resp, err := stream.CloseAndRecv()
	if err != nil {
		return "", 0, fmt.Errorf("close put-blob stream: %w", err)
	}

	actualDigest := fmt.Sprintf("sha256:%x", hash.Sum(nil))
	if expectedDigest != "" && actualDigest != expectedDigest {
		// delete what we stored since digest is wrong
		ctx2, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = r.storage.DeleteBlob(ctx2, &storagev1.DeleteBlobRequest{Key: key, TenantId: st.TenantID})
		return "", 0, ErrDigestMismatch
	}

	_ = resp
	_ = r.uploads.Delete(ctx, uploadUUID)
	return actualDigest, totalBytes, nil
}

// CancelUpload removes upload state.
func (r *Registry) CancelUpload(ctx context.Context, uploadUUID string) error {
	return r.uploads.Delete(ctx, uploadUUID)
}

// --- Manifest operations ---

// PutManifest stores a manifest, creates/updates the tag if reference is a tag, and publishes push.completed.
func (r *Registry) PutManifest(ctx context.Context, tenantID, repoID, repoName, reference, mediaType string, rawJSON []byte, pushedBy string) (string, error) {
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(rawJSON))

	ctx5, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, err := r.metadata.PutManifest(ctx5, &metadatav1.PutManifestRequest{
		RepoId:    repoID,
		TenantId:  tenantID,
		Digest:    digest,
		MediaType: mediaType,
		RawJson:   rawJSON,
		SizeBytes: int64(len(rawJSON)),
	})
	if err != nil {
		return "", fmt.Errorf("put manifest rpc: %w", err)
	}

	// if reference is a tag name, also upsert the tag
	if !digestRE.MatchString(reference) {
		if _, err := r.metadata.PutTag(ctx5, &metadatav1.PutTagRequest{
			RepoId:         repoID,
			TenantId:       tenantID,
			Name:           reference,
			ManifestDigest: digest,
		}); err != nil {
			return "", fmt.Errorf("put tag rpc: %w", err)
		}
	}

	// publish push.completed event — best-effort, don't fail the push
	payload, _ := json.Marshal(events.PushCompletedPayload{
		RepositoryName: repoName,
		Tag:            reference,
		ManifestDigest: digest,
		PushedBy:       pushedBy,
		SizeBytes:      int64(len(rawJSON)),
	})
	evt := events.Event{
		ID:         uuid.New().String(),
		Type:       events.RoutingPushCompleted,
		TenantID:   tenantID,
		OccurredAt: time.Now().UTC(),
		Version:    "1.0",
		Payload:    payload,
	}
	pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pubCancel()
	if err := r.publisher.Publish(pubCtx, events.RoutingPushCompleted, evt); err != nil {
		// log but don't fail — the push itself succeeded
		fmt.Printf("warn: publish push.completed: %v\n", err)
	}

	return digest, nil
}

// GetManifest retrieves a manifest by digest or tag reference.
func (r *Registry) GetManifest(ctx context.Context, tenantID, repoID, reference string) (*metadatav1.Manifest, error) {
	ctx5, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// resolve tag → digest if needed
	if !digestRE.MatchString(reference) {
		tag, err := r.metadata.GetTag(ctx5, &metadatav1.GetTagRequest{
			RepoId:   repoID,
			TenantId: tenantID,
			Name:     reference,
		})
		if err != nil {
			if isGRPCNotFound(err) {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("get tag rpc: %w", err)
		}
		reference = tag.GetManifestDigest()
	}

	m, err := r.metadata.GetManifest(ctx5, &metadatav1.GetManifestRequest{
		RepoId:    repoID,
		TenantId:  tenantID,
		Reference: reference,
	})
	if err != nil {
		if isGRPCNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get manifest rpc: %w", err)
	}
	return m, nil
}

// DeleteManifest removes a manifest and its tag (if any).
func (r *Registry) DeleteManifest(ctx context.Context, tenantID, repoID, reference string) error {
	ctx5, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	digest := reference
	if !digestRE.MatchString(reference) {
		tag, err := r.metadata.GetTag(ctx5, &metadatav1.GetTagRequest{
			RepoId:   repoID,
			TenantId: tenantID,
			Name:     reference,
		})
		if err != nil {
			if isGRPCNotFound(err) {
				return ErrNotFound
			}
			return fmt.Errorf("get tag rpc: %w", err)
		}
		digest = tag.GetManifestDigest()
		if _, err := r.metadata.DeleteTag(ctx5, &metadatav1.DeleteTagRequest{
			RepoId:   repoID,
			TenantId: tenantID,
			Name:     reference,
		}); err != nil {
			return fmt.Errorf("delete tag rpc: %w", err)
		}
	}

	if _, err := r.metadata.DeleteManifest(ctx5, &metadatav1.DeleteManifestRequest{
		RepoId:   repoID,
		TenantId: tenantID,
		Digest:   digest,
	}); err != nil {
		if isGRPCNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("delete manifest rpc: %w", err)
	}
	return nil
}

// --- Tags ---

// ListTags returns all tag names for the repository, with pagination.
func (r *Registry) ListTags(ctx context.Context, tenantID, repoID string, n int32, last string) ([]string, error) {
	stream, err := r.metadata.ListTags(ctx, &metadatav1.ListTagsRequest{
		RepoId:    repoID,
		TenantId:  tenantID,
		PageSize:  n,
		PageToken: last,
	})
	if err != nil {
		return nil, fmt.Errorf("list tags rpc: %w", err)
	}

	var tags []string
	for {
		tag, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("recv tag: %w", err)
		}
		tags = append(tags, tag.GetName())
	}
	return tags, nil
}

// --- Repository ---

// GetOrCreateRepository returns the repo from metadata, creating it if it doesn't exist.
func (r *Registry) GetOrCreateRepository(ctx context.Context, tenantID, repoName string) (*metadatav1.Repository, error) {
	ctx5, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// try to get by name — the metadata service uses repo_id, so we create if absent
	// In a full implementation we'd have GetRepositoryByName; for now we always create
	// and rely on the metadata service's unique constraint to return the existing record.
	repo, err := r.metadata.CreateRepository(ctx5, &metadatav1.CreateRepositoryRequest{
		TenantId: tenantID,
		Name:     repoName,
		IsPublic: false,
	})
	if err != nil {
		// if already exists, the metadata service should return the existing one;
		// if it returns AlreadyExists we should fetch it — but we don't have GetByName.
		// Treat any error here as internal for now.
		return nil, fmt.Errorf("create repository rpc: %w", err)
	}
	return repo, nil
}

// CheckQuota verifies the tenant has enough remaining quota for uploadSize bytes.
func (r *Registry) CheckQuota(ctx context.Context, tenantID string, uploadSize int64) error {
	ctx5, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	usage, err := r.metadata.GetTenantQuotaUsage(ctx5, &metadatav1.GetTenantQuotaUsageRequest{
		TenantId: tenantID,
	})
	if err != nil {
		// fail open if quota service unreachable — don't block pushes
		return nil
	}
	if usage.GetUsedBytes()+uploadSize > usage.GetQuotaBytes() {
		return ErrQuotaExceeded
	}
	return nil
}

// isGRPCNotFound returns true if the gRPC error is a NotFound status.
func isGRPCNotFound(err error) bool {
	if st, ok := status.FromError(err); ok {
		return st.Code() == codes.NotFound
	}
	return false
}
